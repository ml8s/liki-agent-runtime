package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentruntime "github.com/ml8s/liki-agent-runtime/internal/agent"
	"github.com/ml8s/liki-agent-runtime/internal/audit/sqlite"
	"github.com/ml8s/liki-agent-runtime/internal/notify"
	"github.com/ml8s/liki-agent-runtime/internal/observability/otel"
	"github.com/ml8s/liki-agent-runtime/internal/observability/prometheus"
	"github.com/ml8s/liki-agent-runtime/internal/platform"
	"github.com/ml8s/liki-agent-runtime/internal/platform/buildinfo"
	"github.com/ml8s/liki-agent-runtime/internal/platform/config"
	"github.com/ml8s/liki-agent-runtime/internal/platform/tlsconf"
	a2aadapter "github.com/ml8s/liki-agent-runtime/internal/protocol/a2a"
	aguiadapter "github.com/ml8s/liki-agent-runtime/internal/protocol/agui"
	"github.com/ml8s/liki-agent-runtime/internal/transport"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		if err := validate(os.Args[2:]); err != nil {
			slog.Error("liki-agent-runtime deployment validation failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			slog.Error("liki-agent-runtime healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("liki-agent-runtime exited", "error", err)
		os.Exit(1)
	}
}

func run() (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)
	if cfg.CallbackURL != "" && cfg.CallbackSecret == "" {
		logger.Warn("run_callback_disabled",
			"reason", "LIKI_AGENT_RUNTIME_CALLBACK_SECRET is empty; run-completion callbacks stay disabled")
	}

	tracerProvider, err := otel.NewTracerProvider(
		context.Background(),
		"liki-agent-runtime",
		buildinfo.Version,
		cfg.Env,
	)
	if err != nil {
		return err
	}
	otel.InstallGlobalPropagator()

	store, err := sqlite.Open(cfg.AuditDBPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close audit database: %w", closeErr)
		}
	}()
	deployment, err := agentruntime.LoadAgentDeployment(cfg.DeploymentFile)
	if err != nil {
		return err
	}
	metricsCollector := prometheus.New()
	runtime, err := agentruntime.NewRuntime(agentruntime.Config{
		AppName:           "liki-agent-runtime",
		Model:             cfg.LLMModel,
		ModelBaseURL:      cfg.LLMBaseURL,
		ModelAPIKey:       cfg.LLMAPIKey,
		ModelTimeout:      cfg.LLMTimeout,
		Temperature:       cfg.LLMTemperature,
		AuditRecorder:     sqlite.NewAuditEventRepository(store.GORM()),
		Metrics:           metricsCollector,
		TracerProvider:    tracerProvider.Provider(),
		Provider:          cfg.LLMProvider,
		StructuredOutput:  cfg.LLMStructuredOutput,
		MaxOutputTokens:   cfg.LLMMaxOutputTokens,
		Deployment:        deployment,
		MCPTimeout:        cfg.MCPTimeout,
		MCPHealthTimeout:  cfg.MCPHealthTimeout,
		MaxConcurrentRuns: cfg.MaxConcurrentRuns,
	})
	if err != nil {
		return err
	}

	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return err
	}
	a2aServer, err := a2aadapter.New(runtime, a2aadapter.Config{
		PublicURL:  publicURL,
		RunTimeout: cfg.RunTimeout,
		Logger:     logger,
	})
	if err != nil {
		return err
	}
	// 运行完成回调（direct-connect 控制面）：仅当配置了 webhook secret 时启用。
	var completionNotifier *notify.Notifier
	if cfg.CallbackSecret != "" {
		profile := deployment.Metadata.Name
		completionNotifier, err = notify.New(notify.Config{
			URL:     cfg.CallbackURL,
			Secret:  cfg.CallbackSecret,
			Source:  "spiffe://liki/runtime/" + profile,
			Profile: profile,
			Logger:  logger,
		})
		if err != nil {
			return fmt.Errorf("init run callback notifier: %w", err)
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
			defer cancel()
			if err := completionNotifier.Close(closeCtx); err != nil {
				logger.Warn("run callback drain incomplete", "error", err)
			}
		}()
	}
	aguiConfig := aguiadapter.Config{RunTimeout: cfg.RunTimeout, Metrics: metricsCollector}
	if completionNotifier != nil {
		aguiConfig.Notifier = completionNotifier
	}
	aguiHandler, err := aguiadapter.New(runtime, aguiConfig)
	if err != nil {
		return err
	}
	healthChecks := []platform.HealthChecker{store}
	healthChecks = append(healthChecks, runtime.MCPHealthChecks()...)
	protocolServer, err := transport.New(transport.Services{
		AgentCard:     a2aServer.AgentCardHandler(),
		A2A:           a2aServer.EndpointHandler(),
		AGUI:          aguiHandler,
		HealthChecks:  healthChecks,
		Metrics:       metricsCollector,
		Dependencies:  metricsCollector,
		MetricsTarget: metricsCollector.Handler(),
		InternalToken: cfg.InternalToken,
		Logger:        logger,
	})
	if err != nil {
		return err
	}

	httpHandler := otelhttp.NewHandler(protocolServer, "liki-agent-runtime",
		otelhttp.WithTracerProvider(tracerProvider.Provider()),
		otelhttp.WithPropagators(otel.Propagator()),
	)

	httpServer := &http.Server{
		Addr:              cfg.ProtocolAddr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// 运维面独立监听：协议面的 mTLS 不波及健康检查与指标抓取。
	opsServer := &http.Server{
		Addr:              cfg.OpsAddr,
		Handler:           protocolServer.OpsHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	var tlsServer *http.Server
	if cfg.TLSAddr != "" {
		tlsConfig, err := tlsconf.ServerConfig(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSClientCAFile)
		if err != nil {
			return fmt.Errorf("build mutual TLS config: %w", err)
		}
		tlsServer = &http.Server{
			Addr:              cfg.TLSAddr,
			Handler:           httpHandler,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := tracerProvider.Shutdown(shutdownCtx); err != nil {
			logger.Warn("otel_tracer_provider_shutdown_failed", "error", err)
		}
	}()
	servers := []*http.Server{httpServer, opsServer}
	if tlsServer != nil {
		servers = append(servers, tlsServer)
	}
	errCh := make(chan error, len(servers))
	go func() {
		logger.Info("liki-agent-runtime listening",
			"addr", cfg.ProtocolAddr,
			"ops_addr", cfg.OpsAddr,
			"public_url", cfg.PublicURL,
			"protocols", "A2A,AG-UI,MCP",
			"env", cfg.Env,
			"mcp_servers", mcpServerNames(deployment),
		)
		errCh <- httpServer.ListenAndServe()
	}()
	go func() {
		errCh <- opsServer.ListenAndServe()
	}()
	if tlsServer != nil {
		go func() {
			logger.Info("liki-agent-runtime mutual TLS listening", "addr", cfg.TLSAddr)
			// 证书由 TLSConfig 提供（RequireAndVerifyClientCert）。
			errCh <- tlsServer.ListenAndServeTLS("", "")
		}()
	}

	select {
	case <-ctx.Done():
		logger.Info("liki-agent-runtime draining", "shutdown_timeout", cfg.ShutdownTimeout)
		protocolServer.Drain()
		drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		var firstErr error
		for _, server := range servers {
			if err := server.Shutdown(drainCtx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func mcpServerNames(deployment *agentruntime.Deployment) []string {
	names := make([]string, 0, len(deployment.Spec.MCPServers))
	for _, server := range deployment.Spec.MCPServers {
		names = append(names, server.Name)
	}
	return names
}

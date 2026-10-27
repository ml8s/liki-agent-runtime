package main

import "github.com/ml8s/liki-agent-runtime/internal/deployment"

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

	a2apush "github.com/a2aproject/a2a-go/v2/a2asrv/push"
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
	protocolstate "github.com/ml8s/liki-agent-runtime/internal/protocolstate/sqlite"
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

// newRunCallbackNotifier 装配运行完成回调通知器：secret 为空即禁用
// （URL 已配置时输出 run_callback_disabled 告警——静默禁用在此显式化），
// 返回 nil 通知器与 nil 关闭函数；装配失败返回错误。
func newRunCallbackNotifier(cfg config.Config, logger *slog.Logger, profile string) (*notify.Notifier, func(), error) {
	if cfg.CallbackSecret == "" {
		if cfg.CallbackURL != "" {
			logger.Warn("run_callback_disabled",
				"reason", "LIKI_AGENT_RUNTIME_CALLBACK_URL is set but LIKI_AGENT_RUNTIME_CALLBACK_SECRET is empty; run-completion callbacks stay disabled")
		}
		return nil, nil, nil
	}
	notifier, err := notify.New(notify.Config{
		URL:     cfg.CallbackURL,
		Secret:  cfg.CallbackSecret,
		Source:  "spiffe://liki/runtime/" + profile,
		Profile: profile,
		Logger:  logger,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("init run callback notifier: %w", err)
	}
	drain := func(shutdownTimeout time.Duration) func() {
		return func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer cancel()
			if err := notifier.Close(closeCtx); err != nil {
				logger.Warn("run callback drain incomplete", "error", err)
			}
		}
	}
	return notifier, drain(cfg.ShutdownTimeout), nil
}

func run() (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

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

	sessionDB, err := agentruntime.OpenSQLiteSessionService(cfg.ADKSessionDBPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := sessionDB.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close ADK session database: %w", closeErr)
		}
	}()

	store, err := sqlite.Open(cfg.AuditDBPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close audit database: %w", closeErr)
		}
	}()
	protocolDB, err := protocolstate.Open(cfg.ProtocolStateDBPath)
	if err != nil {
		return err
	}
	recovered, err := protocolDB.RecoverAbandonedTasks(context.Background(), time.Now().UTC())
	if err != nil {
		return err
	}
	if recovered > 0 {
		logger.Warn("a2a_abandoned_tasks_recovered", "count", recovered)
	}
	defer func() {
		if closeErr := protocolDB.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close protocol state database: %w", closeErr)
		}
	}()
	metricsCollector := prometheus.New()
	auditRecorder, err := sqlite.NewAuditRecorder(store, sqlite.WithRecorderMetrics(metricsCollector))
	if err != nil {
		return fmt.Errorf("init audit recorder: %w", err)
	}
	// Draining precedes the store close deferred above (defers run LIFO).
	defer func() {
		if closeErr := auditRecorder.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("drain audit recorder: %w", closeErr)
		}
	}()
	deployment, err := deployment.LoadAgentDeployment(cfg.DeploymentFile)
	if err != nil {
		return err
	}
	runtime, err := agentruntime.NewRuntime(agentruntime.Config{
		AppName:           "liki-agent-runtime",
		Model:             cfg.LLMModel,
		ModelBaseURL:      cfg.LLMBaseURL,
		ModelAPIKey:       cfg.LLMAPIKey,
		ModelTimeout:      cfg.LLMTimeout,
		Temperature:       cfg.LLMTemperature,
		AuditRecorder:     auditRecorder,
		Metrics:           metricsCollector,
		TracerProvider:    tracerProvider.Provider(),
		Provider:          cfg.LLMProvider,
		StructuredOutput:  cfg.LLMStructuredOutput,
		MaxOutputTokens:   cfg.LLMMaxOutputTokens,
		AgentDeployment:   deployment,
		MCPTimeout:        cfg.MCPTimeout,
		MCPHealthTimeout:  cfg.MCPHealthTimeout,
		MaxConcurrentRuns: cfg.MaxConcurrentRuns,
		SessionService:    sessionDB.SessionService(),
	})
	if err != nil {
		return err
	}

	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil {
		return err
	}
	a2aConfig := a2aadapter.Config{
		PublicURL:        publicURL,
		RunTimeout:       cfg.RunTimeout,
		Logger:           logger,
		Metrics:          metricsCollector,
		ServicePrincipal: "liki-a2a",
		Security: a2aadapter.SecurityConfig{
			Bearer:    cfg.InternalToken != "",
			MutualTLS: cfg.TLSAddr != "",
		},
		TaskStore: protocolDB.NewTaskStore(protocolstate.TaskStoreConfig{}),
	}
	if cfg.A2APushEnabled {
		a2aConfig.Push = a2aadapter.PushConfig{
			Store: protocolDB.NewPushConfigStore(protocolstate.PushConfigStoreConfig{}),
			Sender: a2apush.NewHTTPPushSender(&a2apush.HTTPSenderConfig{
				Timeout:              cfg.A2APushTimeout,
				AllowPrivateNetworks: cfg.A2APushAllowPrivateNetworks,
			}),
		}
	}
	a2aServer, err := a2aadapter.New(runtime, a2aConfig)
	if err != nil {
		return err
	}
	// 运行完成回调（direct-connect 控制面）：仅当配置了 webhook secret 时启用。
	completionNotifier, closeCompletionNotifier, err := newRunCallbackNotifier(cfg, logger, deployment.Metadata.Name)
	if err != nil {
		return err
	}
	if completionNotifier != nil {
		defer closeCompletionNotifier()
	}
	aguiConfig := aguiadapter.Config{RunTimeout: cfg.RunTimeout, Metrics: metricsCollector}
	if completionNotifier != nil {
		aguiConfig.Notifier = completionNotifier
	}
	aguiHandler, err := aguiadapter.New(runtime, aguiConfig)
	if err != nil {
		return err
	}
	healthChecks := []platform.HealthChecker{store, protocolDB, sessionDB}
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
		A2APrincipal:  "liki-a2a",
		A2ATenant:     cfg.A2ATenant,
		Logger:        logger,
	})
	if err != nil {
		return err
	}

	httpHandler := otelhttp.NewHandler(protocolServer, "liki-agent-runtime",
		otelhttp.WithTracerProvider(tracerProvider.Provider()),
		otelhttp.WithPropagators(otel.Propagator()),
	)

	servers, err := newHTTPServers(cfg, protocolServer, httpHandler)
	if err != nil {
		return err
	}
	httpServer, opsServer := servers[0], servers[1]
	var tlsServer *http.Server
	if len(servers) > 2 {
		tlsServer = servers[2]
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
		return shutdownServers(servers, protocolServer, cfg.ShutdownTimeout)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// newHTTPServers 组装协议、运维与可选 mTLS 三个监听；TLS 四项配置已由
// platform 校验保证成对。
func newHTTPServers(cfg config.Config, protocolServer *transport.Server, protocolHandler http.Handler) ([]*http.Server, error) {
	httpServer := &http.Server{
		Addr:              cfg.ProtocolAddr,
		Handler:           protocolHandler,
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
	servers := []*http.Server{httpServer, opsServer}
	if cfg.TLSAddr != "" {
		tlsConfig, err := tlsconf.ServerConfig(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("build mutual TLS config: %w", err)
		}
		servers = append(servers, &http.Server{
			Addr:              cfg.TLSAddr,
			Handler:           protocolServer,
			TLSConfig:         tlsConfig,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       120 * time.Second,
		})
	}
	return servers, nil
}

// shutdownServers 排水（readiness 置为 draining）并按超时逐一 Shutdown，
// 返回首个退出错误。
func shutdownServers(servers []*http.Server, protocolServer *transport.Server, shutdownTimeout time.Duration) error {
	protocolServer.Drain()
	drainCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var firstErr error
	for _, server := range servers {
		if err := server.Shutdown(drainCtx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func mcpServerNames(deployment *deployment.AgentDeployment) []string {
	names := make([]string, 0, len(deployment.Spec.MCPServers))
	for _, server := range deployment.Spec.MCPServers {
		names = append(names, server.Name)
	}
	return names
}

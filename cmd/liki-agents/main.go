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

	agentruntime "github.com/ml8s/liki-agents/internal/agent"
	"github.com/ml8s/liki-agents/internal/audit/sqlite"
	"github.com/ml8s/liki-agents/internal/observability/otel"
	"github.com/ml8s/liki-agents/internal/observability/prometheus"
	"github.com/ml8s/liki-agents/internal/platform"
	"github.com/ml8s/liki-agents/internal/platform/buildinfo"
	"github.com/ml8s/liki-agents/internal/platform/config"
	a2aadapter "github.com/ml8s/liki-agents/internal/protocol/a2a"
	aguiadapter "github.com/ml8s/liki-agents/internal/protocol/agui"
	"github.com/ml8s/liki-agents/internal/transport"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "validate" {
		if err := validate(os.Args[2:]); err != nil {
			slog.Error("liki-agents deployment validation failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			slog.Error("liki-agents healthcheck failed", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("liki-agents exited", "error", err)
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

	tracerProvider, err := otel.NewTracerProvider(
		context.Background(),
		"liki-agents",
		buildinfo.Version,
		cfg.Env,
	)
	if err != nil {
		return err
	}
	otel.InstallGlobalPropagator()

	store, err := sqlite.Open(cfg.DBPath)
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
		AppName:           "liki-agents",
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
		DeploymentDigest:  cfg.DeploymentDigest,
		SkillsRoot:        cfg.SkillsRoot,
		MCPTimeout:        cfg.MCPTimeout,
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
	})
	if err != nil {
		return err
	}
	aguiHandler, err := aguiadapter.New(runtime, aguiadapter.Config{RunTimeout: cfg.RunTimeout, Metrics: metricsCollector})
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

	httpHandler := otelhttp.NewHandler(protocolServer, "liki-agents",
		otelhttp.WithTracerProvider(tracerProvider.Provider()),
		otelhttp.WithPropagators(otel.Propagator()),
	)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
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
	errCh := make(chan error, 1)
	go func() {
		logger.Info("liki-agents listening",
			"addr", cfg.Addr,
			"public_url", cfg.PublicURL,
			"protocols", "A2A,AG-UI,MCP",
			"env", cfg.Env,
			"mcp_servers", mcpServerNames(deployment),
		)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("liki-agents draining", "shutdown_timeout", cfg.ShutdownTimeout)
		protocolServer.Drain()
		drainCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		return httpServer.Shutdown(drainCtx)
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

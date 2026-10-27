// Command mcp-fixture provides a deterministic, domain-neutral MCP server for
// runtime development and integration tests. Tool behavior intentionally has
// no business semantics; deployments own domain tools.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultAddr = ":8085"

type fixtureConfig struct {
	addr       string
	serverName string
	prefix     string
	count      int
}

type fixtureInput struct {
	Value string `json:"value,omitempty" jsonschema:"optional deterministic value echoed by the fixture"`
}

type fixtureOutput struct {
	Tool   string `json:"tool"`
	Server string `json:"server"`
	Value  string `json:"value"`
}

func loadConfig(getenv func(string) string) (fixtureConfig, error) {
	config := fixtureConfig{
		addr:       cmpOr(getenv("LISTEN_ADDR"), defaultAddr),
		serverName: cmpOr(getenv("MCP_FIXTURE_SERVER_NAME"), "mcp-fixture"),
		prefix:     getenv("MCP_FIXTURE_PREFIX"),
	}
	rawCount := cmpOr(getenv("MCP_FIXTURE_COUNT"), "1")
	count, err := strconv.Atoi(rawCount)
	if err != nil {
		return fixtureConfig{}, fmt.Errorf("MCP_FIXTURE_COUNT must be an integer: %w", err)
	}
	if config.prefix == "" || !validToolPrefix(config.prefix) {
		return fixtureConfig{}, fmt.Errorf("MCP_FIXTURE_PREFIX must contain only ASCII letters, digits, and underscores")
	}
	if count < 1 || count > 32 {
		return fixtureConfig{}, fmt.Errorf("MCP_FIXTURE_COUNT must be between 1 and 32")
	}
	config.count = count
	return config, nil
}

func cmpOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func validToolPrefix(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

func newHandler(config fixtureConfig) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    config.serverName,
		Version: "1.0.0",
	}, nil)
	for index := range config.count {
		name := fmt.Sprintf("%s_%d", config.prefix, index+1)
		mcp.AddTool(server, &mcp.Tool{
			Name:        name,
			Description: "Deterministic development fixture",
		}, func(_ context.Context, _ *mcp.CallToolRequest, args fixtureInput) (*mcp.CallToolResult, fixtureOutput, error) {
			value := args.Value
			if strings.TrimSpace(value) == "" {
				value = "ok"
			}
			return nil, fixtureOutput{Tool: name, Server: config.serverName, Value: value}, nil
		})
	}

	routes := http.NewServeMux()
	routes.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, &mcp.StreamableHTTPOptions{Stateless: true}))
	routes.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return routes
}

func run(ctx context.Context, config fixtureConfig) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{
		Addr:              config.addr,
		Handler:           newHandler(config),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutdown mcp fixture: %w", err)
		}
		return nil
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve mcp fixture: %w", err)
	}
}

func main() {
	config, err := loadConfig(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := run(context.Background(), config); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

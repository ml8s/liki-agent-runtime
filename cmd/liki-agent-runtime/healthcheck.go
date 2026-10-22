// Command liki-agent-runtime runs the generic multi-agent runtime and its operational
// subcommands.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ml8s/liki-agent-runtime/internal/platform/config"
)

// healthcheck is the container-native readiness probe used by Docker Compose.
// It intentionally checks the process-local /readyz endpoint without importing
// a shell into the distroless runtime image. Readiness lives on the dedicated
// ops listener, so the probe follows LIKI_AGENT_RUNTIME_OPS_ADDR.
func healthcheck() error {
	address := strings.TrimSpace(os.Getenv("LIKI_AGENT_RUNTIME_OPS_ADDR"))
	if address == "" {
		address = config.DefaultOpsAddr
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		healthcheckURL(address)+"/readyz",
		nil,
	)
	if err != nil {
		return fmt.Errorf("build readiness request: %w", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("readiness request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("readiness returned HTTP %d", response.StatusCode)
	}
	return nil
}

func healthcheckURL(address string) string {
	if strings.HasPrefix(address, ":") {
		return "http://127.0.0.1" + address
	}
	if !strings.Contains(address, "://") {
		return "http://" + address
	}
	return address
}

package config_test

import (
	"testing"

	"github.com/ml8s/agentgo/internal/platform/config"
)

func TestRuntimeEnvironmentUsesCanonicalNames(t *testing.T) {
	baseEnv(t)
	t.Setenv("AGENTGO_PROTOCOL_ADDR", ":18083")
	t.Setenv("AGENTGO_PUBLIC_URL", "http://127.0.0.1:18083")
	t.Setenv("AGENTGO_PROTOCOL_TOKEN", "token")
	t.Setenv("AGENTGO_AUDIT_DB_PATH", t.TempDir()+"/audit.db")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProtocolAddr != ":18083" {
		t.Fatalf("ProtocolAddr = %q", cfg.ProtocolAddr)
	}
}

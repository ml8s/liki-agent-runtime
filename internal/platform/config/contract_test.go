package config_test

import (
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/platform/config"
)

func TestRuntimeEnvironmentUsesCanonicalNames(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_AGENT_RUNTIME_PROTOCOL_ADDR", ":18083")
	t.Setenv("LIKI_AGENT_RUNTIME_PUBLIC_URL", "http://127.0.0.1:18083")
	t.Setenv("LIKI_AGENT_RUNTIME_INTERNAL_TOKEN", "token")
	t.Setenv("LIKI_AGENT_RUNTIME_AUDIT_DB_PATH", t.TempDir()+"/audit.db")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ProtocolAddr != ":18083" {
		t.Fatalf("ProtocolAddr = %q", cfg.ProtocolAddr)
	}
}

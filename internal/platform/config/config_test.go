package config_test

import (
	"strings"
	"testing"

	"github.com/ml8s/liki-agent-runtime/internal/platform/config"
)

func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LIKI_ENV", "development")
	t.Setenv("LIKI_AGENT_RUNTIME_PUBLIC_URL", "https://agent.internal")
	t.Setenv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", "/tmp/agent-deployment.json")
	t.Setenv("LIKI_LLM_PROVIDER", "zhipu")
	t.Setenv("LIKI_LLM_STRUCTURED_OUTPUT", "json_object")
	t.Setenv("LIKI_LLM_BASE_URL", "https://open.bigmodel.cn/api/v1")
	t.Setenv("LIKI_LLM_MODEL", "glm-5.3-flash")
}

func TestLoadValidConfiguration(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_LLM_STRUCTURED_OUTPUT", "none")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLMProvider != "zhipu" || cfg.LLMStructuredOutput != "" {
		t.Fatalf("provider/capability = %q/%q", cfg.LLMProvider, cfg.LLMStructuredOutput)
	}
	if cfg.DeploymentFile != "/tmp/agent-deployment.json" {
		t.Fatalf("definition file = %q", cfg.DeploymentFile)
	}
	if cfg.Topology != config.TopologySingle {
		t.Fatalf("topology = %q, want single", cfg.Topology)
	}
	t.Setenv("LIKI_MAX_CONCURRENT_RUNS", "7")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(max concurrent runs) error = %v", err)
	}
	if cfg.MaxConcurrentRuns != 7 {
		t.Fatalf("max concurrent runs = %d, want 7", cfg.MaxConcurrentRuns)
	}
	t.Setenv("LIKI_AGENT_RUNTIME_SKILLS_ROOT", "/tmp/dev/skills")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(skills root) error = %v", err)
	}
	if cfg.SkillsRoot != "/tmp/dev/skills" {
		t.Fatalf("skills root = %q", cfg.SkillsRoot)
	}
	// The provider vocabulary is open: the value is an audit provenance
	// label and a structured-output hint, not a protocol switch.
	t.Setenv("LIKI_LLM_PROVIDER", "anthropic")
	if _, err = config.Load(); err != nil {
		t.Fatalf("Load(open provider vocabulary) error = %v", err)
	}
}

func TestLoadRejectsInvalidSettings(t *testing.T) {
	testCases := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "missing deployment", key: "LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", value: "", wantErr: "LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE is required"},
		{name: "unsupported environment", key: "LIKI_ENV", value: "staging", wantErr: "LIKI_ENV is unsupported"},
		{name: "missing llm base url", key: "LIKI_LLM_BASE_URL", value: "", wantErr: "LIKI_LLM_BASE_URL is required"},
		{name: "missing llm model", key: "LIKI_LLM_MODEL", value: "", wantErr: "LIKI_LLM_MODEL is required"},
		{name: "multi topology is fail-closed", key: "LIKI_AGENT_RUNTIME_TOPOLOGY", value: "multi", wantErr: "LIKI_AGENT_RUNTIME_TOPOLOGY=multi is not supported yet"},
		{name: "invalid topology", key: "LIKI_AGENT_RUNTIME_TOPOLOGY", value: "cluster", wantErr: "LIKI_AGENT_RUNTIME_TOPOLOGY is unsupported"},
		{name: "missing provider", key: "LIKI_LLM_PROVIDER", value: "", wantErr: "LIKI_LLM_PROVIDER is required"},
		{name: "unsupported structured output", key: "LIKI_LLM_STRUCTURED_OUTPUT", value: "yaml", wantErr: "LIKI_LLM_STRUCTURED_OUTPUT is unsupported"},
		{name: "invalid public url", key: "LIKI_AGENT_RUNTIME_PUBLIC_URL", value: "agent.internal", wantErr: "LIKI_AGENT_RUNTIME_PUBLIC_URL must be an absolute HTTP(S) URL"},
		{name: "invalid llm url", key: "LIKI_LLM_BASE_URL", value: "api.example", wantErr: "LIKI_LLM_BASE_URL must be an absolute HTTP(S) URL"},
		{name: "invalid duration", key: "LIKI_RUN_TIMEOUT_SECONDS", value: "abc", wantErr: "LIKI_RUN_TIMEOUT_SECONDS is not a valid integer"},
		{name: "zero MCP timeout", key: "LIKI_MCP_TIMEOUT_SECONDS", value: "0", wantErr: "LIKI_MCP_TIMEOUT_SECONDS must be greater than zero"},
		{name: "zero concurrent runs", key: "LIKI_MAX_CONCURRENT_RUNS", value: "0", wantErr: "LIKI_MAX_CONCURRENT_RUNS must be greater than zero"},
		{name: "invalid concurrent runs", key: "LIKI_MAX_CONCURRENT_RUNS", value: "many", wantErr: "LIKI_MAX_CONCURRENT_RUNS is not a valid integer"},
		{name: "zero LLM timeout", key: "LIKI_LLM_TIMEOUT_SECONDS", value: "0", wantErr: "LIKI_LLM_TIMEOUT_SECONDS must be greater than zero"},
		{name: "zero max output tokens", key: "LIKI_LLM_MAX_OUTPUT_TOKENS", value: "0", wantErr: "LIKI_LLM_MAX_OUTPUT_TOKENS must be greater than zero"},
		{name: "negative max output tokens", key: "LIKI_LLM_MAX_OUTPUT_TOKENS", value: "-1", wantErr: "LIKI_LLM_MAX_OUTPUT_TOKENS must be greater than zero"},
		{name: "invalid max output tokens", key: "LIKI_LLM_MAX_OUTPUT_TOKENS", value: "many", wantErr: "LIKI_LLM_MAX_OUTPUT_TOKENS is not a valid integer"},
		{name: "negative temperature", key: "LIKI_LLM_TEMPERATURE", value: "-0.1", wantErr: "LIKI_LLM_TEMPERATURE must be between 0 and 2"},
		{name: "high temperature", key: "LIKI_LLM_TEMPERATURE", value: "2.1", wantErr: "LIKI_LLM_TEMPERATURE must be between 0 and 2"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv(testCase.key, testCase.value)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("Load() error = %v, want %q", err, testCase.wantErr)
			}
		})
	}
}

func TestProductionConfigurationHardening(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_ENV", "production")
	t.Setenv("LIKI_AGENT_RUNTIME_INTERNAL_TOKEN", "production-service-token")
	t.Setenv("LIKI_LLM_API_KEY", "production-model-key")

	_, err := config.Load()
	if err != nil {
		t.Fatalf("Load(production) error = %v", err)
	}

	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "llm endpoint must be HTTPS", key: "LIKI_LLM_BASE_URL", value: "http://127.0.0.1:8080/v1", wantErr: "LIKI_LLM_BASE_URL must use HTTPS outside development"},
		{name: "public URL credentials rejected", key: "LIKI_AGENT_RUNTIME_PUBLIC_URL", value: "https://user:secret@agent.internal", wantErr: "LIKI_AGENT_RUNTIME_PUBLIC_URL must not contain embedded credentials"},
		{name: "LLM URL credentials rejected", key: "LIKI_LLM_BASE_URL", value: "https://key:secret@model.internal/v1", wantErr: "LIKI_LLM_BASE_URL must not contain embedded credentials"},
		{name: "concurrent runs bounded", key: "LIKI_MAX_CONCURRENT_RUNS", value: "1025", wantErr: "LIKI_MAX_CONCURRENT_RUNS must not exceed 1024"},
		{name: "skills root outside development", key: "LIKI_AGENT_RUNTIME_SKILLS_ROOT", value: "/fixture/skills", wantErr: "LIKI_AGENT_RUNTIME_SKILLS_ROOT is allowed only in development"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Load() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadDerivesPublicURLFromDefaultAddress(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_AGENT_RUNTIME_PUBLIC_URL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "http://127.0.0.1" + config.DefaultAddr; cfg.PublicURL != want {
		t.Fatalf("public URL default = %q, want %q", cfg.PublicURL, want)
	}
}

func TestLoadMaxOutputTokens(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LLMMaxOutputTokens != 0 {
		t.Fatalf("unset max output tokens = %d, want 0 (no output cap)", cfg.LLMMaxOutputTokens)
	}
	t.Setenv("LIKI_LLM_MAX_OUTPUT_TOKENS", "2048")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(max output tokens) error = %v", err)
	}
	if cfg.LLMMaxOutputTokens != 2048 {
		t.Fatalf("max output tokens = %d, want 2048", cfg.LLMMaxOutputTokens)
	}
}

func TestDevelopmentAllowsLocalHTTPAndUnpinnedDeployment(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_LLM_BASE_URL", "http://127.0.0.1:8080/v1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(local HTTP development) error = %v", err)
	}
	if cfg.LLMBaseURL != "http://127.0.0.1:8080/v1" {
		t.Fatalf("LLM base URL = %q", cfg.LLMBaseURL)
	}
}

func TestLoadMTLSConfiguration(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_AGENT_RUNTIME_TLS_ADDR", ":8084")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "LIKI_AGENT_RUNTIME_TLS_CERT") {
		t.Fatalf("Load() error = %v, want a TLS certificate requirement", err)
	}
	t.Setenv("LIKI_AGENT_RUNTIME_TLS_CERT", "/pki/server.crt")
	t.Setenv("LIKI_AGENT_RUNTIME_TLS_KEY", "/pki/server.key")
	t.Setenv("LIKI_AGENT_RUNTIME_TLS_CLIENT_CA", "/pki/ca.pem")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(mTLS) error = %v", err)
	}
	if cfg.TLSAddr != ":8084" || cfg.TLSCertFile != "/pki/server.crt" ||
		cfg.TLSKeyFile != "/pki/server.key" || cfg.TLSClientCAFile != "/pki/ca.pem" {
		t.Fatalf("mTLS config = %q/%q/%q/%q", cfg.TLSAddr, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSClientCAFile)
	}

	// 证书文件没有 TLS 地址：配置错误必须显式失败（fail-closed）。
	t.Setenv("LIKI_AGENT_RUNTIME_TLS_ADDR", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "LIKI_AGENT_RUNTIME_TLS_ADDR") {
		t.Fatalf("Load(certs without addr) error = %v, want a TLS addr requirement", err)
	}
}

func TestOpsAddrDefaultsAndOverrides(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.OpsAddr != ":8085" {
		t.Fatalf("OpsAddr = %q, want :8085", cfg.OpsAddr)
	}
	t.Setenv("LIKI_AGENT_RUNTIME_OPS_ADDR", "127.0.0.1:9090")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(ops addr) error = %v", err)
	}
	if cfg.OpsAddr != "127.0.0.1:9090" {
		t.Fatalf("OpsAddr = %q, want override", cfg.OpsAddr)
	}
}

func TestLoadCallbackConfiguration(t *testing.T) {
	baseEnv(t)
	t.Setenv("LIKI_AGENT_RUNTIME_CALLBACK_SECRET", "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "LIKI_AGENT_RUNTIME_CALLBACK_URL") {
		t.Fatalf("Load() error = %v, want a callback URL requirement", err)
	}
	t.Setenv("LIKI_AGENT_RUNTIME_CALLBACK_URL", "http://web:8081/api/internal/runs/completed")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(callback) error = %v", err)
	}
	if cfg.CallbackURL == "" || cfg.CallbackSecret == "" {
		t.Fatalf("callback config = %q/%q", cfg.CallbackURL, cfg.CallbackSecret)
	}
}

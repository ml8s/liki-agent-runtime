package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ml8s/agent-runtime/internal/platform/config"
)

func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("AGENT_RUNTIME_ENV", "development")
	t.Setenv("AGENT_RUNTIME_PUBLIC_URL", "https://agent.internal")
	t.Setenv("AGENT_RUNTIME_DEPLOYMENT_FILE", "/tmp/agent-deployment.json")
	t.Setenv("AGENT_RUNTIME_LLM_PROVIDER", "zhipu")
	t.Setenv("AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT", "json_object")
	t.Setenv("AGENT_RUNTIME_LLM_BASE_URL", "https://open.bigmodel.cn/api/v1")
	t.Setenv("AGENT_RUNTIME_LLM_MODEL", "glm-5.3-flash")
}

func TestLoadValidConfiguration(t *testing.T) {
	baseEnv(t)
	t.Setenv("AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT", "none")
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
	t.Setenv("AGENT_RUNTIME_MAX_CONCURRENT_RUNS", "7")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(max concurrent runs) error = %v", err)
	}
	if cfg.MaxConcurrentRuns != 7 {
		t.Fatalf("max concurrent runs = %d, want 7", cfg.MaxConcurrentRuns)
	}
	// The provider vocabulary is open: the value is an audit provenance
	// label and a structured-output hint, not a protocol switch.
	t.Setenv("AGENT_RUNTIME_LLM_PROVIDER", "anthropic")
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
		{name: "missing deployment", key: "AGENT_RUNTIME_DEPLOYMENT_FILE", value: "", wantErr: "AGENT_RUNTIME_DEPLOYMENT_FILE is required"},
		{name: "unsupported environment", key: "AGENT_RUNTIME_ENV", value: "staging", wantErr: "AGENT_RUNTIME_ENV is unsupported"},
		{name: "missing llm base url", key: "AGENT_RUNTIME_LLM_BASE_URL", value: "", wantErr: "AGENT_RUNTIME_LLM_BASE_URL is required"},
		{name: "missing llm model", key: "AGENT_RUNTIME_LLM_MODEL", value: "", wantErr: "AGENT_RUNTIME_LLM_MODEL is required"},
		{name: "multi topology is fail-closed", key: "AGENT_RUNTIME_TOPOLOGY", value: "multi", wantErr: "AGENT_RUNTIME_TOPOLOGY=multi is not supported yet"},
		{name: "invalid topology", key: "AGENT_RUNTIME_TOPOLOGY", value: "cluster", wantErr: "AGENT_RUNTIME_TOPOLOGY is unsupported"},
		{name: "missing provider", key: "AGENT_RUNTIME_LLM_PROVIDER", value: "", wantErr: "AGENT_RUNTIME_LLM_PROVIDER is required"},
		{name: "unsupported structured output", key: "AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT", value: "yaml", wantErr: "AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT is unsupported"},
		{name: "invalid public url", key: "AGENT_RUNTIME_PUBLIC_URL", value: "agent.internal", wantErr: "AGENT_RUNTIME_PUBLIC_URL must be an absolute HTTP(S) URL"},
		{name: "invalid llm url", key: "AGENT_RUNTIME_LLM_BASE_URL", value: "api.example", wantErr: "AGENT_RUNTIME_LLM_BASE_URL must be an absolute HTTP(S) URL"},
		{name: "invalid duration", key: "AGENT_RUNTIME_RUN_TIMEOUT_SECONDS", value: "abc", wantErr: "AGENT_RUNTIME_RUN_TIMEOUT_SECONDS is not a valid integer"},
		{name: "zero MCP timeout", key: "AGENT_RUNTIME_MCP_TIMEOUT_SECONDS", value: "0", wantErr: "AGENT_RUNTIME_MCP_TIMEOUT_SECONDS must be greater than zero"},
		{name: "zero concurrent runs", key: "AGENT_RUNTIME_MAX_CONCURRENT_RUNS", value: "0", wantErr: "AGENT_RUNTIME_MAX_CONCURRENT_RUNS must be greater than zero"},
		{name: "invalid concurrent runs", key: "AGENT_RUNTIME_MAX_CONCURRENT_RUNS", value: "many", wantErr: "AGENT_RUNTIME_MAX_CONCURRENT_RUNS is not a valid integer"},
		{name: "zero LLM timeout", key: "AGENT_RUNTIME_LLM_TIMEOUT_SECONDS", value: "0", wantErr: "AGENT_RUNTIME_LLM_TIMEOUT_SECONDS must be greater than zero"},
		{name: "zero max output tokens", key: "AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS", value: "0", wantErr: "AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS must be greater than zero"},
		{name: "negative max output tokens", key: "AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS", value: "-1", wantErr: "AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS must be greater than zero"},
		{name: "invalid max output tokens", key: "AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS", value: "many", wantErr: "AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS is not a valid integer"},
		{name: "negative temperature", key: "AGENT_RUNTIME_LLM_TEMPERATURE", value: "-0.1", wantErr: "AGENT_RUNTIME_LLM_TEMPERATURE must be between 0 and 2"},
		{name: "high temperature", key: "AGENT_RUNTIME_LLM_TEMPERATURE", value: "2.1", wantErr: "AGENT_RUNTIME_LLM_TEMPERATURE must be between 0 and 2"},
		{name: "unsupported log level", key: "AGENT_RUNTIME_LOG_LEVEL", value: "trace", wantErr: "AGENT_RUNTIME_LOG_LEVEL is unsupported"},
		{name: "unsupported log format", key: "AGENT_RUNTIME_LOG_FORMAT", value: "yaml", wantErr: "AGENT_RUNTIME_LOG_FORMAT is unsupported"},
		{name: "zero health timeout", key: "AGENT_RUNTIME_MCP_HEALTH_TIMEOUT_SECONDS", value: "0", wantErr: "AGENT_RUNTIME_MCP_HEALTH_TIMEOUT_SECONDS must be greater than zero"},
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
	t.Setenv("AGENT_RUNTIME_ENV", "production")
	t.Setenv("AGENT_RUNTIME_INTERNAL_TOKEN", "production-service-token")
	t.Setenv("AGENT_RUNTIME_LLM_API_KEY", "production-model-key")

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
		{name: "llm endpoint must be HTTPS", key: "AGENT_RUNTIME_LLM_BASE_URL", value: "http://127.0.0.1:8080/v1", wantErr: "AGENT_RUNTIME_LLM_BASE_URL must use HTTPS outside development"},
		{name: "public URL credentials rejected", key: "AGENT_RUNTIME_PUBLIC_URL", value: "https://user:secret@agent.internal", wantErr: "AGENT_RUNTIME_PUBLIC_URL must not contain embedded credentials"},
		{name: "public URL loopback outside development", key: "AGENT_RUNTIME_PUBLIC_URL", value: "http://127.0.0.1:8083", wantErr: "AGENT_RUNTIME_PUBLIC_URL must be a deployment-reachable address outside development"},
		{name: "LLM URL credentials rejected", key: "AGENT_RUNTIME_LLM_BASE_URL", value: "https://key:secret@model.internal/v1", wantErr: "AGENT_RUNTIME_LLM_BASE_URL must not contain embedded credentials"},
		{name: "concurrent runs bounded", key: "AGENT_RUNTIME_MAX_CONCURRENT_RUNS", value: "1025", wantErr: "AGENT_RUNTIME_MAX_CONCURRENT_RUNS must not exceed 1024"},
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
	t.Setenv("AGENT_RUNTIME_PUBLIC_URL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "http://127.0.0.1" + config.DefaultAddr; cfg.PublicURL != want {
		t.Fatalf("public URL default = %q, want %q", cfg.PublicURL, want)
	}
}

func TestLoadA2ATaskRetention(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.A2ATaskRetention != 0 {
		t.Fatalf("unset A2A task retention = %s, want disabled", cfg.A2ATaskRetention)
	}
	t.Setenv("AGENT_RUNTIME_A2A_TASK_RETENTION_SECONDS", "86400")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(retention) error = %v", err)
	}
	if cfg.A2ATaskRetention != 24*time.Hour {
		t.Fatalf("A2A task retention = %s, want 24h", cfg.A2ATaskRetention)
	}
}

func TestLoadADKSessionRetention(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ADKSessionRetention != 0 {
		t.Fatalf("unset ADK session retention = %s, want disabled", cfg.ADKSessionRetention)
	}
	t.Setenv("AGENT_RUNTIME_ADK_SESSION_RETENTION_SECONDS", "300")
	if _, err = config.Load(); err == nil || !strings.Contains(err.Error(), "must be at least AGENT_RUNTIME_RUN_TIMEOUT_SECONDS") {
		t.Fatalf("Load(short session retention) error = %v, want run timeout protection", err)
	}
	t.Setenv("AGENT_RUNTIME_ADK_SESSION_RETENTION_SECONDS", "600")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(valid session retention) error = %v", err)
	}
	if cfg.ADKSessionRetention != 10*time.Minute {
		t.Fatalf("ADK session retention = %s, want 10m", cfg.ADKSessionRetention)
	}
}

func TestLoadIdentitySourceConfiguration(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(default) error = %v", err)
	}
	if cfg.IdentitySource != config.IdentitySourceHeader {
		t.Fatalf("default identity source = %q, want trusted_header", cfg.IdentitySource)
	}
	t.Setenv("AGENT_RUNTIME_IDENTITY_SOURCE", config.IdentitySourceOIDC)
	if _, err = config.Load(); err == nil || !strings.Contains(err.Error(), "AGENT_RUNTIME_OIDC_ISSUER is required") {
		t.Fatalf("Load(oidc without issuer) error = %v", err)
	}
	t.Setenv("AGENT_RUNTIME_OIDC_ISSUER", "https://issuer.example.com")
	if _, err = config.Load(); err == nil || !strings.Contains(err.Error(), "AGENT_RUNTIME_OIDC_AUDIENCE is required") {
		t.Fatalf("Load(oidc without audience) error = %v", err)
	}
	t.Setenv("AGENT_RUNTIME_OIDC_AUDIENCE", "agent-runtime")
	t.Setenv("AGENT_RUNTIME_INTERNAL_TOKEN", "must-not-combine")
	if _, err = config.Load(); err == nil || !strings.Contains(err.Error(), "AGENT_RUNTIME_INTERNAL_TOKEN must be empty") {
		t.Fatalf("Load(oidc with internal token) error = %v", err)
	}
	t.Setenv("AGENT_RUNTIME_INTERNAL_TOKEN", "")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(oidc) error = %v", err)
	}
	if cfg.OIDCUsernameClaim != "sub" {
		t.Fatalf("username claim = %q, want sub", cfg.OIDCUsernameClaim)
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
	t.Setenv("AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS", "2048")
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
	t.Setenv("AGENT_RUNTIME_LLM_BASE_URL", "http://127.0.0.1:8080/v1")
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
	t.Setenv("AGENT_RUNTIME_TLS_ADDR", ":8084")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "AGENT_RUNTIME_TLS_CERT") {
		t.Fatalf("Load() error = %v, want a TLS certificate requirement", err)
	}
	t.Setenv("AGENT_RUNTIME_TLS_CERT", "/pki/server.crt")
	t.Setenv("AGENT_RUNTIME_TLS_KEY", "/pki/server.key")
	t.Setenv("AGENT_RUNTIME_TLS_CLIENT_CA", "/pki/ca.pem")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load(mTLS) error = %v", err)
	}
	if cfg.TLSAddr != ":8084" || cfg.TLSCertFile != "/pki/server.crt" ||
		cfg.TLSKeyFile != "/pki/server.key" || cfg.TLSClientCAFile != "/pki/ca.pem" {
		t.Fatalf("mTLS config = %q/%q/%q/%q", cfg.TLSAddr, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSClientCAFile)
	}

	// 证书文件没有 TLS 地址：配置错误必须显式失败（fail-closed）。
	t.Setenv("AGENT_RUNTIME_TLS_ADDR", "")
	if _, err := config.Load(); err == nil || !strings.Contains(err.Error(), "AGENT_RUNTIME_TLS_ADDR") {
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
	t.Setenv("AGENT_RUNTIME_OPS_ADDR", "127.0.0.1:9090")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(ops addr) error = %v", err)
	}
	if cfg.OpsAddr != "127.0.0.1:9090" {
		t.Fatalf("OpsAddr = %q, want override", cfg.OpsAddr)
	}
}

// After the migration window closed, the pre-rename names are no longer
// read: an old name alone must not configure the runtime.
func TestLegacyConfigNamesAreIgnored(t *testing.T) {
	baseEnv(t)
	t.Setenv("LEGACY_RUN_TIMEOUT_SECONDS", "77")
	t.Setenv("LEGACY_MAX_CONCURRENT_RUNS", "9")
	t.Setenv("AGENT_RUNTIME_ADDR", ":9083")
	t.Setenv("LEGACY_LLM_MODEL", "legacy-model")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.RunTimeout != 600*time.Second {
		t.Fatalf("run timeout = %v, want the 600s default", cfg.RunTimeout)
	}
	if cfg.MaxConcurrentRuns != 32 {
		t.Fatalf("max concurrent runs = %d, want the 32 default", cfg.MaxConcurrentRuns)
	}
	if cfg.ProtocolAddr != ":8083" {
		t.Fatalf("protocol addr = %q, want the :8083 default", cfg.ProtocolAddr)
	}
	if cfg.LLMModel != "glm-5.3-flash" {
		t.Fatalf("model = %q, want the canonical-name value", cfg.LLMModel)
	}
}

func TestMCPHealthTimeoutFollowsToolBudget(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MCPHealthTimeout != 0 {
		t.Fatalf("unset health timeout = %v, want 0 (follows the tool budget)", cfg.MCPHealthTimeout)
	}
	t.Setenv("AGENT_RUNTIME_MCP_HEALTH_TIMEOUT_SECONDS", "45")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load(health timeout) error = %v", err)
	}
	if cfg.MCPHealthTimeout != 45*time.Second {
		t.Fatalf("health timeout = %v, want 45s", cfg.MCPHealthTimeout)
	}
}

func TestMTLSIdentitySourceDefaultsToCertificate(t *testing.T) {
	baseEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MTLSIdentitySource != config.MTLSIdentitySourceCertificate {
		t.Fatalf("mTLS identity source = %q, want %q", cfg.MTLSIdentitySource, config.MTLSIdentitySourceCertificate)
	}
}

func TestMTLSIdentitySourceHeaderRequiresTLSListener(t *testing.T) {
	baseEnv(t)
	t.Setenv(config.EnvMTLSIdentitySource, config.MTLSIdentitySourceHeader)
	if _, err := config.Load(); err == nil {
		t.Fatal("header identity source was accepted without a mutual-TLS listener")
	}
}

func TestMTLSIdentitySourceIsRejectedWithOIDC(t *testing.T) {
	baseEnv(t)
	t.Setenv("AGENT_RUNTIME_IDENTITY_SOURCE", config.IdentitySourceOIDC)
	t.Setenv("AGENT_RUNTIME_OIDC_ISSUER", "https://issuer.example.com")
	t.Setenv("AGENT_RUNTIME_OIDC_AUDIENCE", "agent-runtime")
	t.Setenv(config.EnvMTLSIdentitySource, config.MTLSIdentitySourceCertificate)
	if _, err := config.Load(); err == nil {
		t.Fatal("mTLS identity source was accepted while OIDC owns caller authentication")
	}
}

func TestOpsTokenMustDifferFromInternalToken(t *testing.T) {
	baseEnv(t)
	t.Setenv("AGENT_RUNTIME_INTERNAL_TOKEN", "protocol-secret")
	t.Setenv("AGENT_RUNTIME_OPS_TOKEN", "protocol-secret")
	if _, err := config.Load(); err == nil {
		t.Fatal("ops token equal to the protocol token was accepted")
	}
}

func TestProductionOIDCRequiresOpsToken(t *testing.T) {
	baseEnv(t)
	t.Setenv("AGENT_RUNTIME_ENV", "production")
	t.Setenv("AGENT_RUNTIME_IDENTITY_SOURCE", config.IdentitySourceOIDC)
	t.Setenv("AGENT_RUNTIME_OIDC_ISSUER", "https://issuer.example.com")
	t.Setenv("AGENT_RUNTIME_OIDC_AUDIENCE", "agent-runtime")
	if _, err := config.Load(); err == nil {
		t.Fatal("production OIDC without an ops token was accepted")
	}
}

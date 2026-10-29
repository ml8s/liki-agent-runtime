// Package config loads and validates the runtime's environment configuration.
package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Canonical runtime defaults. Load's fallbacks and the runtime/adapter
// second-chance defaults all reference these constants so a default can
// never drift between layers.
const (
	// IdentitySourceHeader trusts only channel-authenticated gateway headers.
	IdentitySourceHeader = "trusted_header"
	// IdentitySourceOIDC verifies a standard bearer OIDC ID token.
	IdentitySourceOIDC = "oidc"

	// MTLSIdentitySourceCertificate maps the verified client certificate to
	// the runtime identity on the mutual-TLS listener. It is the fail-closed
	// default: a certificate holder cannot claim another subject by headers.
	MTLSIdentitySourceCertificate = "certificate"
	// MTLSIdentitySourceHeader keeps the gateway relay-header contract on the
	// mutual-TLS listener. Deployments whose mTLS peer is a gateway that has
	// already authenticated the end user opt in explicitly.
	MTLSIdentitySourceHeader = "header"

	DefaultIdentitySource = IdentitySourceHeader
	// DefaultMTLSIdentitySource is fail-closed: direct mTLS callers are
	// identified by their certificate, not by self-declared headers.
	DefaultMTLSIdentitySource = MTLSIdentitySourceCertificate

	// EnvMTLSIdentitySource is read directly so validation can distinguish an
	// explicit opt-in from the fail-closed default.
	EnvMTLSIdentitySource = "AGENT_RUNTIME_MTLS_IDENTITY_SOURCE"

	DefaultMCPTimeout        = 30 * time.Second
	DefaultMaxConcurrentRuns = 32
	// MaxConcurrentRunsLimit is the process-wide safety bound shared by
	// environment validation and direct Runtime constructor validation.
	MaxConcurrentRunsLimit = 1024
	DefaultRunTimeout      = 600 * time.Second
	DefaultShutdownTimeout = 30 * time.Second
	DefaultLLMTimeout      = 120 * time.Second
	DefaultLLMTemperature  = 0.2
	DefaultPushTimeout     = 10 * time.Second

	// DefaultProtocolStateDBPath keeps A2A task and push-notification config state out of the
	// append-only audit database.
	DefaultProtocolStateDBPath = "./data/agent-runtime-protocol-state.db"

	// DefaultADKSessionDBPath persists official ADK session state separately
	// from append-only audit evidence.
	DefaultADKSessionDBPath = "./data/agent-runtime-adk-sessions.db"
)

// DefaultAddr is the default HTTP listen address. The server and the
// container healthcheck subcommand share it so the two cannot drift.
const DefaultAddr = ":8083"

// DefaultOpsAddr is the default address for the operational surface
// (/readyz, /version, /metrics). It is separate from the protocol listener so
// that mutual TLS on the protocol surface never gates healthchecks or scrapers.
const DefaultOpsAddr = ":8085"

// Config is the fully resolved runtime configuration.
type Config struct {
	Env                         string
	Topology                    Topology
	ProtocolAddr                string
	OpsAddr                     string
	TLSAddr                     string
	TLSCertFile                 string
	TLSKeyFile                  string
	TLSClientCAFile             string
	PublicURL                   string
	ProtocolToken               string
	PprofEnabled                bool
	IdentitySource              string
	MTLSIdentitySource          string
	AuditDBPath                 string
	ProtocolStateDBPath         string
	ADKSessionDBPath            string
	DeploymentFile              string
	MCPTimeout                  time.Duration
	MCPHealthTimeout            time.Duration
	MaxConcurrentRuns           int
	RunTimeout                  time.Duration
	ShutdownTimeout             time.Duration
	LLMStructuredOutput         string
	LLMBaseURL                  string
	LLMAPIKey                   string
	LLMModel                    string
	LLMProvider                 string
	LLMTimeout                  time.Duration
	OIDCIssuer                  string
	OIDCAudience                string
	OIDCUsernameClaim           string
	OIDCTenantClaim             string
	LLMMaxOutputTokens          int
	LLMTemperature              float64
	LogFormat                   string
	LogLevel                    string
	A2ATenant                   string
	A2APrincipal                string
	A2APushEnabled              bool
	A2APushTimeout              time.Duration
	A2APushAllowPrivateNetworks bool
	A2ATaskRetention            time.Duration
	ADKSessionRetention         time.Duration
}

// Topology declares the process state required by the deployment.
type Topology string

const (
	// TopologySingle is the only supported production topology. It uses local
	// SQLite protocol state and evidence with official in-memory ADK sessions.
	TopologySingle Topology = "single"

	// TopologyMulti is intentionally recognized but rejected. It is a fail-closed
	// deployment guard, not an enabled feature: shared ADK sessions, an A2A
	// task store, transactional audit ownership, and routing must be supplied
	// through official extension points before it can start.
	TopologyMulti Topology = "multi"
)

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	cfg := Config{
		Env:                 getEnv("AGENT_RUNTIME_ENV", "development"),
		Topology:            Topology(strings.ToLower(strings.TrimSpace(getEnv("AGENT_RUNTIME_TOPOLOGY", string(TopologySingle))))),
		ProtocolAddr:        getEnv("AGENT_RUNTIME_PROTOCOL_ADDR", DefaultAddr),
		OpsAddr:             getEnv("AGENT_RUNTIME_OPS_ADDR", DefaultOpsAddr),
		TLSAddr:             getEnv("AGENT_RUNTIME_TLS_ADDR", ""),
		TLSCertFile:         getEnv("AGENT_RUNTIME_TLS_CERT", ""),
		TLSKeyFile:          getEnv("AGENT_RUNTIME_TLS_KEY", ""),
		TLSClientCAFile:     getEnv("AGENT_RUNTIME_TLS_CLIENT_CA", ""),
		PublicURL:           getEnv("AGENT_RUNTIME_PUBLIC_URL", "http://127.0.0.1"+DefaultAddr),
		ProtocolToken:       getEnv("AGENT_RUNTIME_PROTOCOL_TOKEN", ""),
		PprofEnabled:        getEnv("AGENT_RUNTIME_PPROF_ENABLED", "") == "1",
		IdentitySource:      getEnv("AGENT_RUNTIME_IDENTITY_SOURCE", DefaultIdentitySource),
		MTLSIdentitySource:  getEnv(EnvMTLSIdentitySource, DefaultMTLSIdentitySource),
		AuditDBPath:         getEnv("AGENT_RUNTIME_AUDIT_DB_PATH", "./data/agent-runtime-audit.db"),
		ProtocolStateDBPath: getEnv("AGENT_RUNTIME_PROTOCOL_STATE_DB_PATH", DefaultProtocolStateDBPath),
		ADKSessionDBPath:    getEnv("AGENT_RUNTIME_ADK_SESSION_DB_PATH", DefaultADKSessionDBPath),
		A2ATenant:           getEnv("AGENT_RUNTIME_A2A_TENANT", ""),
		A2APrincipal:        getEnv("AGENT_RUNTIME_A2A_PRINCIPAL", "agent-runtime"),
		DeploymentFile:      getEnv("AGENT_RUNTIME_DEPLOYMENT_FILE", ""),
		LLMBaseURL:          getEnv("AGENT_RUNTIME_LLM_BASE_URL", ""),
		LLMAPIKey:           getEnv("AGENT_RUNTIME_LLM_API_KEY", ""),
		LLMModel:            getEnv("AGENT_RUNTIME_LLM_MODEL", ""),
		LLMProvider:         getEnv("AGENT_RUNTIME_LLM_PROVIDER", ""),
		LLMStructuredOutput: getEnv("AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT", ""),
		OIDCIssuer:          getEnv("AGENT_RUNTIME_OIDC_ISSUER", ""),
		OIDCAudience:        getEnv("AGENT_RUNTIME_OIDC_AUDIENCE", ""),
		OIDCUsernameClaim:   getEnv("AGENT_RUNTIME_OIDC_USERNAME_CLAIM", "sub"),
		OIDCTenantClaim:     getEnv("AGENT_RUNTIME_OIDC_TENANT_CLAIM", ""),
		LogFormat:           getEnv("AGENT_RUNTIME_LOG_FORMAT", "json"),
		LogLevel:            getEnv("AGENT_RUNTIME_LOG_LEVEL", "info"),
	}
	if cfg.LLMStructuredOutput == "none" {
		cfg.LLMStructuredOutput = ""
	}
	var err error
	if cfg.MCPTimeout, err = getDuration("AGENT_RUNTIME_MCP_TIMEOUT_SECONDS", DefaultMCPTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MCPHealthTimeout, err = getOptionalDuration("AGENT_RUNTIME_MCP_HEALTH_TIMEOUT_SECONDS"); err != nil {
		return Config{}, err
	}
	if cfg.A2APushEnabled, err = getBool("AGENT_RUNTIME_A2A_PUSH_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.A2APushTimeout, err = getDuration("AGENT_RUNTIME_A2A_PUSH_TIMEOUT_SECONDS", DefaultPushTimeout); err != nil {
		return Config{}, err
	}
	if cfg.A2APushAllowPrivateNetworks, err = getBool("AGENT_RUNTIME_A2A_PUSH_ALLOW_PRIVATE_NETWORKS", false); err != nil {
		return Config{}, err
	}
	if cfg.A2ATaskRetention, err = getOptionalDuration("AGENT_RUNTIME_A2A_TASK_RETENTION_SECONDS"); err != nil {
		return Config{}, err
	}
	if cfg.ADKSessionRetention, err = getOptionalDuration("AGENT_RUNTIME_ADK_SESSION_RETENTION_SECONDS"); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrentRuns, err = getInt("AGENT_RUNTIME_MAX_CONCURRENT_RUNS", DefaultMaxConcurrentRuns); err != nil {
		return Config{}, err
	}
	if cfg.RunTimeout, err = getDuration("AGENT_RUNTIME_RUN_TIMEOUT_SECONDS", DefaultRunTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = getDuration("AGENT_RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", DefaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LLMTimeout, err = getDuration("AGENT_RUNTIME_LLM_TIMEOUT_SECONDS", DefaultLLMTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LLMMaxOutputTokens, err = getOptionalPositiveInt("AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS"); err != nil {
		return Config{}, err
	}
	if cfg.LLMTemperature, err = getFloat("AGENT_RUNTIME_LLM_TEMPERATURE", DefaultLLMTemperature); err != nil {
		return Config{}, err
	}
	return validate(cfg)
}

func validate(cfg Config) (Config, error) {
	switch cfg.Env {
	case "development", "production":
	default:
		return Config{}, fmt.Errorf("AGENT_RUNTIME_ENV is unsupported: %q (expected development or production)", cfg.Env)
	}
	isDevelopment := cfg.Env == "development"

	switch cfg.IdentitySource {
	case "", IdentitySourceHeader:
		cfg.IdentitySource = IdentitySourceHeader
		if !isDevelopment && cfg.ProtocolToken == "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_PROTOCOL_TOKEN is required for trusted-header identity outside development")
		}
	case IdentitySourceOIDC:
		if cfg.ProtocolToken != "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_PROTOCOL_TOKEN must be empty when AGENT_RUNTIME_IDENTITY_SOURCE=oidc; the OIDC bearer token owns authentication")
		}
		if strings.TrimSpace(cfg.OIDCIssuer) == "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_OIDC_ISSUER is required when identity source is oidc")
		}
		if strings.TrimSpace(cfg.OIDCAudience) == "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_OIDC_AUDIENCE is required when identity source is oidc")
		}
		if strings.TrimSpace(cfg.OIDCUsernameClaim) == "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_OIDC_USERNAME_CLAIM must not be empty when identity source is oidc")
		}
		issuerURL, err := url.Parse(cfg.OIDCIssuer)
		if err != nil || issuerURL.Scheme != "http" && issuerURL.Scheme != "https" || issuerURL.Host == "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_OIDC_ISSUER must be an absolute HTTP(S) URL")
		}
		if issuerURL.User != nil {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_OIDC_ISSUER must not contain embedded credentials")
		}
		if !isDevelopment && issuerURL.Scheme != "https" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_OIDC_ISSUER must use HTTPS outside development")
		}
	default:
		return Config{}, fmt.Errorf("AGENT_RUNTIME_IDENTITY_SOURCE is unsupported: %q (expected %q or %q)", cfg.IdentitySource, IdentitySourceHeader, IdentitySourceOIDC)
	}
	// OIDC owns caller authentication on both protocol surfaces; a separate
	// mTLS identity mapping would be dead, misleading configuration.
	if os.Getenv(EnvMTLSIdentitySource) != "" && cfg.IdentitySource == IdentitySourceOIDC {
		return Config{}, fmt.Errorf("%s is only valid with %s=%s; OIDC verifies the caller credential directly", EnvMTLSIdentitySource, "AGENT_RUNTIME_IDENTITY_SOURCE", IdentitySourceHeader)
	}
	switch cfg.MTLSIdentitySource {
	case "", MTLSIdentitySourceCertificate:
		cfg.MTLSIdentitySource = MTLSIdentitySourceCertificate
	case MTLSIdentitySourceHeader:
		// The header contract trusts the mTLS peer to relay an authenticated
		// end user, so it must not be configured without that listener.
		if cfg.TLSAddr == "" {
			return Config{}, fmt.Errorf("%s=%s requires AGENT_RUNTIME_TLS_ADDR to be configured", EnvMTLSIdentitySource, MTLSIdentitySourceHeader)
		}
	default:
		return Config{}, fmt.Errorf("%s is unsupported: %q (expected %q or %q)", EnvMTLSIdentitySource, cfg.MTLSIdentitySource, MTLSIdentitySourceCertificate, MTLSIdentitySourceHeader)
	}
	if cfg.TLSAddr != "" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" || cfg.TLSClientCAFile == "" {
			return Config{}, fmt.Errorf("AGENT_RUNTIME_TLS_CERT, AGENT_RUNTIME_TLS_KEY and AGENT_RUNTIME_TLS_CLIENT_CA are required when AGENT_RUNTIME_TLS_ADDR is set")
		}
	} else if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" || cfg.TLSClientCAFile != "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_TLS_ADDR is required when TLS certificate files are configured")
	}
	if cfg.OpsAddr == cfg.ProtocolAddr || (cfg.TLSAddr != "" && (cfg.TLSAddr == cfg.ProtocolAddr || cfg.TLSAddr == cfg.OpsAddr)) {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_PROTOCOL_ADDR, AGENT_RUNTIME_OPS_ADDR and AGENT_RUNTIME_TLS_ADDR must be distinct listen addresses")
	}
	if !isDevelopment && cfg.LLMAPIKey == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_API_KEY is required outside development")
	}
	if cfg.LLMBaseURL == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_BASE_URL is required")
	}
	if cfg.LLMModel == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_MODEL is required")
	}
	if cfg.LLMProvider == "" {
		// The provider vocabulary is open: the value is the audit provenance
		// label and the structured-output mode hint, not a protocol switch.
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_PROVIDER is required")
	}
	if cfg.DeploymentFile == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_DEPLOYMENT_FILE is required")
	}
	if cfg.ProtocolStateDBPath == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_PROTOCOL_STATE_DB_PATH is required")
	}
	if cfg.ADKSessionDBPath == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_ADK_SESSION_DB_PATH is required")
	}
	if cfg.A2APrincipal == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_A2A_PRINCIPAL is required")
	}
	switch cfg.Topology {
	case TopologySingle:
	case TopologyMulti:
		return Config{}, fmt.Errorf(
			"AGENT_RUNTIME_TOPOLOGY=multi is not supported yet; it requires shared ADK session storage, a database-backed A2A task store, transactional audit/run ownership, deployment digest pinning, and explicit task routing",
		)
	case "":
		cfg.Topology = TopologySingle
	default:
		return Config{}, fmt.Errorf("AGENT_RUNTIME_TOPOLOGY is unsupported: %q", cfg.Topology)
	}
	switch strings.ToLower(cfg.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LOG_LEVEL is unsupported: %q (expected debug, info, warn, or error)", cfg.LogLevel)
	}
	switch strings.ToLower(cfg.LogFormat) {
	case "json", "text":
	default:
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LOG_FORMAT is unsupported: %q (expected json or text)", cfg.LogFormat)
	}
	switch cfg.LLMStructuredOutput {
	case "", "none", "json_schema", "json_object":
	default:
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT is unsupported: %q", cfg.LLMStructuredOutput)
	}
	if cfg.ADKSessionRetention > 0 && cfg.ADKSessionRetention < cfg.RunTimeout {
		return Config{}, fmt.Errorf(
			"AGENT_RUNTIME_ADK_SESSION_RETENTION_SECONDS must be at least AGENT_RUNTIME_RUN_TIMEOUT_SECONDS (%s), got %s",
			cfg.RunTimeout, cfg.ADKSessionRetention,
		)
	}
	if cfg.RunTimeout <= 0 {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_RUN_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.MCPTimeout <= 0 {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_MCP_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.MaxConcurrentRuns <= 0 {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_MAX_CONCURRENT_RUNS must be greater than zero")
	}
	if cfg.MaxConcurrentRuns > MaxConcurrentRunsLimit {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_MAX_CONCURRENT_RUNS must not exceed %d", MaxConcurrentRunsLimit)
	}
	if cfg.LLMTimeout <= 0 {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.LLMTemperature < 0 || cfg.LLMTemperature > 2 ||
		math.IsNaN(cfg.LLMTemperature) || math.IsInf(cfg.LLMTemperature, 0) {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_TEMPERATURE must be between 0 and 2")
	}
	if _, err := url.Parse(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_PUBLIC_URL is invalid: %w", err)
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_SHUTDOWN_TIMEOUT_SECONDS must be greater than zero")
	}
	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil || publicURL.Scheme != "http" && publicURL.Scheme != "https" || publicURL.Host == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_PUBLIC_URL must be an absolute HTTP(S) URL")
	}
	if publicURL.User != nil {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_PUBLIC_URL must not contain embedded credentials")
	}
	if !isDevelopment {
		if host := publicURL.Hostname(); host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return Config{}, fmt.Errorf(
				"AGENT_RUNTIME_PUBLIC_URL must be a deployment-reachable address outside development, got loopback %q",
				host,
			)
		}
	}
	llmURL, err := url.Parse(cfg.LLMBaseURL)
	if err != nil || llmURL.Scheme != "http" && llmURL.Scheme != "https" || llmURL.Host == "" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_BASE_URL must be an absolute HTTP(S) URL")
	}
	if llmURL.User != nil {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_BASE_URL must not contain embedded credentials")
	}
	if !isDevelopment && llmURL.Scheme != "https" {
		return Config{}, fmt.Errorf("AGENT_RUNTIME_LLM_BASE_URL must use HTTPS outside development")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid integer: %q", key, raw)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero, got %d", key, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

func getFloat(key string, fallback float64) (float64, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid number: %q", key, raw)
	}
	return value, nil
}

func getInt(key string, fallback int) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid integer: %q", key, raw)
	}
	return value, nil
}

func getBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s is not a valid boolean: %q", key, raw)
	}
	return value, nil
}

// getOptionalPositiveInt reads an optional positive integer. An unset
// variable returns 0, which downstream code reads as "no bound"; an explicit
// non-positive or malformed value fails closed.
func getOptionalPositiveInt(key string) (int, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid integer: %q", key, raw)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero, got %d", key, value)
	}
	if value > math.MaxInt32 {
		return 0, fmt.Errorf("%s exceeds the supported maximum, got %d", key, value)
	}
	return value, nil
}

// getOptionalDuration reads an optional duration in whole seconds. An unset
// variable returns 0, which callers read as "follow the related default";
// an explicit non-positive or malformed value fails closed.
func getOptionalDuration(key string) (time.Duration, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return 0, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid integer: %q", key, raw)
	}
	if seconds <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero, got %d", key, seconds)
	}
	return time.Duration(seconds) * time.Second, nil
}

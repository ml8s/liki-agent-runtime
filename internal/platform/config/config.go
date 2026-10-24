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
	DefaultMCPTimeout        = 30 * time.Second
	DefaultMaxConcurrentRuns = 32
	DefaultRunTimeout        = 600 * time.Second
	DefaultShutdownTimeout   = 30 * time.Second
	DefaultLLMTimeout        = 120 * time.Second
	DefaultLLMTemperature    = 0.2
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
	Env                 string
	Topology            Topology
	ProtocolAddr        string
	OpsAddr             string
	TLSAddr             string
	TLSCertFile         string
	TLSKeyFile          string
	TLSClientCAFile     string
	PublicURL           string
	InternalToken       string
	CallbackURL         string
	CallbackSecret      string
	AuditDBPath         string
	DeploymentFile      string
	MCPTimeout          time.Duration
	MCPHealthTimeout    time.Duration
	MaxConcurrentRuns   int
	RunTimeout          time.Duration
	ShutdownTimeout     time.Duration
	LLMStructuredOutput string
	LLMBaseURL          string
	LLMAPIKey           string
	LLMModel            string
	LLMProvider         string
	LLMTimeout          time.Duration
	LLMMaxOutputTokens  int
	LLMTemperature      float64
	LogFormat           string
	LogLevel            string
}

// Topology declares the process state required by the deployment.
type Topology string

const (
	// TopologySingle is the only supported production topology. It permits
	// official in-memory ADK sessions, the SDK's in-memory A2A task store, and
	// the local SQLite evidence database.
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
		Env:                 getEnv("LIKI_ENV", "development"),
		Topology:            Topology(strings.ToLower(strings.TrimSpace(getEnv("LIKI_AGENT_RUNTIME_TOPOLOGY", string(TopologySingle))))),
		ProtocolAddr:        getEnv("LIKI_AGENT_RUNTIME_PROTOCOL_ADDR", DefaultAddr),
		OpsAddr:             getEnv("LIKI_AGENT_RUNTIME_OPS_ADDR", DefaultOpsAddr),
		TLSAddr:             getEnv("LIKI_AGENT_RUNTIME_TLS_ADDR", ""),
		TLSCertFile:         getEnv("LIKI_AGENT_RUNTIME_TLS_CERT", ""),
		TLSKeyFile:          getEnv("LIKI_AGENT_RUNTIME_TLS_KEY", ""),
		TLSClientCAFile:     getEnv("LIKI_AGENT_RUNTIME_TLS_CLIENT_CA", ""),
		PublicURL:           getEnv("LIKI_AGENT_RUNTIME_PUBLIC_URL", "http://127.0.0.1"+DefaultAddr),
		InternalToken:       getEnv("LIKI_AGENT_RUNTIME_INTERNAL_TOKEN", ""),
		CallbackURL:         getEnv("LIKI_AGENT_RUNTIME_CALLBACK_URL", ""),
		CallbackSecret:      getEnv("LIKI_AGENT_RUNTIME_CALLBACK_SECRET", ""),
		AuditDBPath:         getEnv("LIKI_AGENT_RUNTIME_AUDIT_DB_PATH", "./data/liki-agent-runtime-audit.db"),
		DeploymentFile:      getEnv("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE", ""),
		LLMBaseURL:          getEnv("LIKI_AGENT_RUNTIME_LLM_BASE_URL", ""),
		LLMAPIKey:           getEnv("LIKI_AGENT_RUNTIME_LLM_API_KEY", ""),
		LLMModel:            getEnv("LIKI_AGENT_RUNTIME_LLM_MODEL", ""),
		LLMProvider:         getEnv("LIKI_AGENT_RUNTIME_LLM_PROVIDER", ""),
		LLMStructuredOutput: getEnv("LIKI_AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT", ""),
		LogFormat:           getEnv("LIKI_LOG_FORMAT", "json"),
		LogLevel:            getEnv("LIKI_LOG_LEVEL", "info"),
	}
	if cfg.LLMStructuredOutput == "none" {
		cfg.LLMStructuredOutput = ""
	}
	var err error
	if cfg.MCPTimeout, err = getDuration("LIKI_AGENT_RUNTIME_MCP_TIMEOUT_SECONDS", DefaultMCPTimeout); err != nil {
		return Config{}, err
	}
	if cfg.MCPHealthTimeout, err = getOptionalDuration("LIKI_AGENT_RUNTIME_MCP_HEALTH_TIMEOUT_SECONDS"); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrentRuns, err = getInt("LIKI_AGENT_RUNTIME_MAX_CONCURRENT_RUNS", DefaultMaxConcurrentRuns); err != nil {
		return Config{}, err
	}
	if cfg.RunTimeout, err = getDuration("LIKI_AGENT_RUNTIME_RUN_TIMEOUT_SECONDS", DefaultRunTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = getDuration("LIKI_AGENT_RUNTIME_SHUTDOWN_TIMEOUT_SECONDS", DefaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LLMTimeout, err = getDuration("LIKI_AGENT_RUNTIME_LLM_TIMEOUT_SECONDS", DefaultLLMTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LLMMaxOutputTokens, err = getOptionalPositiveInt("LIKI_AGENT_RUNTIME_LLM_MAX_OUTPUT_TOKENS"); err != nil {
		return Config{}, err
	}
	if cfg.LLMTemperature, err = getFloat("LIKI_AGENT_RUNTIME_LLM_TEMPERATURE", DefaultLLMTemperature); err != nil {
		return Config{}, err
	}
	return validate(cfg)
}

func validate(cfg Config) (Config, error) {
	switch cfg.Env {
	case "development", "production":
	default:
		return Config{}, fmt.Errorf("LIKI_ENV is unsupported: %q (expected development or production)", cfg.Env)
	}
	isDevelopment := cfg.Env == "development"

	if !isDevelopment && cfg.InternalToken == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_INTERNAL_TOKEN is required outside development")
	}
	if cfg.TLSAddr != "" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" || cfg.TLSClientCAFile == "" {
			return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_TLS_CERT, LIKI_AGENT_RUNTIME_TLS_KEY and LIKI_AGENT_RUNTIME_TLS_CLIENT_CA are required when LIKI_AGENT_RUNTIME_TLS_ADDR is set")
		}
	} else if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" || cfg.TLSClientCAFile != "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_TLS_ADDR is required when TLS certificate files are configured")
	}
	if cfg.CallbackSecret != "" && cfg.CallbackURL == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_CALLBACK_URL is required when LIKI_AGENT_RUNTIME_CALLBACK_SECRET is set")
	}
	if cfg.CallbackURL != "" && cfg.CallbackSecret == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_CALLBACK_SECRET is required when LIKI_AGENT_RUNTIME_CALLBACK_URL is set")
	}
	if cfg.OpsAddr == cfg.ProtocolAddr || (cfg.TLSAddr != "" && (cfg.TLSAddr == cfg.ProtocolAddr || cfg.TLSAddr == cfg.OpsAddr)) {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_PROTOCOL_ADDR, LIKI_AGENT_RUNTIME_OPS_ADDR and LIKI_AGENT_RUNTIME_TLS_ADDR must be distinct listen addresses")
	}
	if !isDevelopment && cfg.LLMAPIKey == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_API_KEY is required outside development")
	}
	if cfg.LLMBaseURL == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_BASE_URL is required")
	}
	if cfg.LLMModel == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_MODEL is required")
	}
	if cfg.LLMProvider == "" {
		// The provider vocabulary is open: the value is the audit provenance
		// label and the structured-output mode hint, not a protocol switch.
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_PROVIDER is required")
	}
	if cfg.DeploymentFile == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_DEPLOYMENT_FILE is required")
	}
	switch cfg.Topology {
	case TopologySingle:
	case TopologyMulti:
		return Config{}, fmt.Errorf(
			"LIKI_AGENT_RUNTIME_TOPOLOGY=multi is not supported yet; it requires shared ADK session storage, a database-backed A2A task store, transactional audit/run ownership, deployment digest pinning, and explicit task routing",
		)
	case "":
		cfg.Topology = TopologySingle
	default:
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_TOPOLOGY is unsupported: %q", cfg.Topology)
	}
	switch strings.ToLower(cfg.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("LIKI_LOG_LEVEL is unsupported: %q (expected debug, info, warn, or error)", cfg.LogLevel)
	}
	switch strings.ToLower(cfg.LogFormat) {
	case "json", "text":
	default:
		return Config{}, fmt.Errorf("LIKI_LOG_FORMAT is unsupported: %q (expected json or text)", cfg.LogFormat)
	}
	switch cfg.LLMStructuredOutput {
	case "", "none", "json_schema", "json_object":
	default:
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_STRUCTURED_OUTPUT is unsupported: %q", cfg.LLMStructuredOutput)
	}
	switch strings.ToLower(cfg.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return Config{}, fmt.Errorf("LIKI_LOG_LEVEL is unsupported: %q (expected debug, info, warn, or error)", cfg.LogLevel)
	}
	switch strings.ToLower(cfg.LogFormat) {
	case "json", "text":
	default:
		return Config{}, fmt.Errorf("LIKI_LOG_FORMAT is unsupported: %q (expected json or text)", cfg.LogFormat)
	}
	if cfg.RunTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_RUN_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.MCPTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_MCP_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.MaxConcurrentRuns <= 0 {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_MAX_CONCURRENT_RUNS must be greater than zero")
	}
	if cfg.MaxConcurrentRuns > 1024 {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_MAX_CONCURRENT_RUNS must not exceed 1024")
	}
	if cfg.LLMTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.LLMTemperature < 0 || cfg.LLMTemperature > 2 ||
		math.IsNaN(cfg.LLMTemperature) || math.IsInf(cfg.LLMTemperature, 0) {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_TEMPERATURE must be between 0 and 2")
	}
	if _, err := url.Parse(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_PUBLIC_URL is invalid: %w", err)
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_SHUTDOWN_TIMEOUT_SECONDS must be greater than zero")
	}
	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil || publicURL.Scheme != "http" && publicURL.Scheme != "https" || publicURL.Host == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_PUBLIC_URL must be an absolute HTTP(S) URL")
	}
	if publicURL.User != nil {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_PUBLIC_URL must not contain embedded credentials")
	}
	if !isDevelopment {
		if host := publicURL.Hostname(); host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return Config{}, fmt.Errorf(
				"LIKI_AGENT_RUNTIME_PUBLIC_URL must be a deployment-reachable address outside development, got loopback %q",
				host,
			)
		}
	}
	llmURL, err := url.Parse(cfg.LLMBaseURL)
	if err != nil || llmURL.Scheme != "http" && llmURL.Scheme != "https" || llmURL.Host == "" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_BASE_URL must be an absolute HTTP(S) URL")
	}
	if llmURL.User != nil {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_BASE_URL must not contain embedded credentials")
	}
	if !isDevelopment && llmURL.Scheme != "https" {
		return Config{}, fmt.Errorf("LIKI_AGENT_RUNTIME_LLM_BASE_URL must use HTTPS outside development")
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

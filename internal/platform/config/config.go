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

	"github.com/ml8s/liki-agents/internal/domain"
)

// DefaultAddr is the default HTTP listen address. The server and the
// container healthcheck subcommand share it so the two cannot drift.
const DefaultAddr = ":8083"

// Config is the fully resolved runtime configuration.
type Config struct {
	Env                 string
	Topology            Topology
	Addr                string
	PublicURL           string
	InternalToken       string
	DBPath              string
	DeploymentFile      string
	DeploymentDigest    string
	SkillsRoot          string
	MCPTimeout          time.Duration
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
		Topology:            Topology(strings.ToLower(strings.TrimSpace(getEnv("LIKI_AGENTS_TOPOLOGY", string(TopologySingle))))),
		Addr:                getEnv("LIKI_AGENTS_ADDR", DefaultAddr),
		PublicURL:           getEnv("LIKI_AGENTS_PUBLIC_URL", "http://127.0.0.1"+DefaultAddr),
		InternalToken:       getEnv("LIKI_AGENTS_INTERNAL_TOKEN", ""),
		DBPath:              getEnv("LIKI_AGENTS_DB_PATH", "./data/liki-agents-audit.db"),
		DeploymentFile:      getEnv("LIKI_AGENTS_DEPLOYMENT_FILE", ""),
		DeploymentDigest:    strings.TrimSpace(getEnv("LIKI_AGENTS_DEPLOYMENT_DIGEST", "")),
		SkillsRoot:          strings.TrimSpace(getEnv("LIKI_AGENTS_SKILLS_ROOT", "")),
		LLMBaseURL:          getEnv("LIKI_LLM_BASE_URL", ""),
		LLMAPIKey:           getEnv("LIKI_LLM_API_KEY", ""),
		LLMModel:            getEnv("LIKI_LLM_MODEL", ""),
		LLMProvider:         getEnv("LIKI_LLM_PROVIDER", ""),
		LLMStructuredOutput: getEnv("LIKI_LLM_STRUCTURED_OUTPUT", ""),
		LogFormat:           getEnv("LIKI_LOG_FORMAT", "json"),
		LogLevel:            getEnv("LIKI_LOG_LEVEL", "info"),
	}
	if cfg.LLMStructuredOutput == "none" {
		cfg.LLMStructuredOutput = ""
	}
	var err error
	if cfg.MCPTimeout, err = getDuration("LIKI_MCP_TIMEOUT_SECONDS", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.MaxConcurrentRuns, err = getInt("LIKI_MAX_CONCURRENT_RUNS", 32); err != nil {
		return Config{}, err
	}
	if cfg.RunTimeout, err = getDuration("LIKI_RUN_TIMEOUT_SECONDS", 600*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = getDuration("LIKI_SHUTDOWN_TIMEOUT_SECONDS", 30*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.LLMTimeout, err = getDuration("LIKI_LLM_TIMEOUT_SECONDS", 120*time.Second); err != nil {
		return Config{}, err
	}
	if cfg.LLMMaxOutputTokens, err = getOptionalPositiveInt("LIKI_LLM_MAX_OUTPUT_TOKENS"); err != nil {
		return Config{}, err
	}
	if cfg.LLMTemperature, err = getFloat("LIKI_LLM_TEMPERATURE", 0.2); err != nil {
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
		return Config{}, fmt.Errorf("LIKI_AGENTS_INTERNAL_TOKEN is required outside development")
	}
	if !isDevelopment && cfg.LLMAPIKey == "" {
		return Config{}, fmt.Errorf("LIKI_LLM_API_KEY is required outside development")
	}
	if cfg.LLMBaseURL == "" {
		return Config{}, fmt.Errorf("LIKI_LLM_BASE_URL is required")
	}
	if cfg.LLMModel == "" {
		return Config{}, fmt.Errorf("LIKI_LLM_MODEL is required")
	}
	if cfg.LLMProvider == "" {
		// The provider vocabulary is open: the value is the audit provenance
		// label and the structured-output mode hint, not a protocol switch.
		return Config{}, fmt.Errorf("LIKI_LLM_PROVIDER is required")
	}
	if cfg.DeploymentFile == "" {
		return Config{}, fmt.Errorf("LIKI_AGENTS_DEPLOYMENT_FILE is required")
	}
	if !isDevelopment && cfg.DeploymentDigest == "" {
		return Config{}, fmt.Errorf("LIKI_AGENTS_DEPLOYMENT_DIGEST is required outside development")
	}
	if cfg.DeploymentDigest != "" && !domain.IsValidSHA256Digest(cfg.DeploymentDigest) {
		return Config{}, fmt.Errorf("LIKI_AGENTS_DEPLOYMENT_DIGEST must be sha256:<64-hex>")
	}
	if cfg.SkillsRoot != "" && !isDevelopment {
		return Config{}, fmt.Errorf("LIKI_AGENTS_SKILLS_ROOT is allowed only in development")
	}
	switch cfg.Topology {
	case TopologySingle:
	case TopologyMulti:
		return Config{}, fmt.Errorf(
			"LIKI_AGENTS_TOPOLOGY=multi is not supported yet; it requires shared ADK session storage, a database-backed A2A task store, transactional audit/run ownership, deployment digest pinning, and explicit task routing",
		)
	case "":
		cfg.Topology = TopologySingle
	default:
		return Config{}, fmt.Errorf("LIKI_AGENTS_TOPOLOGY is unsupported: %q", cfg.Topology)
	}
	switch cfg.LLMStructuredOutput {
	case "", "none", "json_schema", "json_object":
	default:
		return Config{}, fmt.Errorf("LIKI_LLM_STRUCTURED_OUTPUT is unsupported: %q", cfg.LLMStructuredOutput)
	}
	if cfg.RunTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_RUN_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.MCPTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_MCP_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.MaxConcurrentRuns <= 0 {
		return Config{}, fmt.Errorf("LIKI_MAX_CONCURRENT_RUNS must be greater than zero")
	}
	if cfg.MaxConcurrentRuns > 1024 {
		return Config{}, fmt.Errorf("LIKI_MAX_CONCURRENT_RUNS must not exceed 1024")
	}
	if cfg.LLMTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_LLM_TIMEOUT_SECONDS must be greater than zero")
	}
	if cfg.LLMTemperature < 0 || cfg.LLMTemperature > 2 ||
		math.IsNaN(cfg.LLMTemperature) || math.IsInf(cfg.LLMTemperature, 0) {
		return Config{}, fmt.Errorf("LIKI_LLM_TEMPERATURE must be between 0 and 2")
	}
	if _, err := url.Parse(cfg.PublicURL); err != nil {
		return Config{}, fmt.Errorf("LIKI_AGENTS_PUBLIC_URL is invalid: %w", err)
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("LIKI_SHUTDOWN_TIMEOUT_SECONDS must be greater than zero")
	}
	publicURL, err := url.Parse(cfg.PublicURL)
	if err != nil || publicURL.Scheme != "http" && publicURL.Scheme != "https" || publicURL.Host == "" {
		return Config{}, fmt.Errorf("LIKI_AGENTS_PUBLIC_URL must be an absolute HTTP(S) URL")
	}
	if publicURL.User != nil {
		return Config{}, fmt.Errorf("LIKI_AGENTS_PUBLIC_URL must not contain embedded credentials")
	}
	llmURL, err := url.Parse(cfg.LLMBaseURL)
	if err != nil || llmURL.Scheme != "http" && llmURL.Scheme != "https" || llmURL.Host == "" {
		return Config{}, fmt.Errorf("LIKI_LLM_BASE_URL must be an absolute HTTP(S) URL")
	}
	if llmURL.User != nil {
		return Config{}, fmt.Errorf("LIKI_LLM_BASE_URL must not contain embedded credentials")
	}
	if !isDevelopment && llmURL.Scheme != "https" {
		return Config{}, fmt.Errorf("LIKI_LLM_BASE_URL must use HTTPS outside development")
	}
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
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

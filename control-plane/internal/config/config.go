package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all configuration for the AgentOven control plane.
type Config struct {
	Port      int
	Version   string
	Telemetry TelemetryConfig
	Auth      AuthConfig
}

type TelemetryConfig struct {
	Enabled      bool
	OTLPEndpoint string
	ServiceName  string
}

type AuthConfig struct {
	// For OSS: simple API key validation
	APIKeyHeader string
	// For Enterprise: OIDC/SAML configuration
	OIDCIssuer   string
	OIDCAudience string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:    envInt("AGENTOVEN_PORT", 8080),
		Version: envStr("AGENTOVEN_VERSION", "0.5.1"),
		Telemetry: TelemetryConfig{
			Enabled:      envBool("OTEL_ENABLED", false),
			OTLPEndpoint: envStr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
			ServiceName:  envStr("OTEL_SERVICE_NAME", "agentoven-control-plane"),
		},
		Auth: AuthConfig{
			APIKeyHeader: envStr("AUTH_API_KEY_HEADER", "Authorization"),
			OIDCIssuer:   envStr("AUTH_OIDC_ISSUER", ""),
			OIDCAudience: envStr("AUTH_OIDC_AUDIENCE", ""),
		},
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

// DataDir is where the control plane keeps its local files (the snapshot, keys): AGENTOVEN_DATA_DIR,
// or ~/.agentoven. It is empty only when neither is available.
func DataDir() string {
	if d := strings.TrimSpace(os.Getenv("AGENTOVEN_DATA_DIR")); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".agentoven")
	}
	return ""
}

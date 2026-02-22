package config

import (
	"os"
	"strconv"
)

// Config holds all configuration for the AgentOven control plane.
type Config struct {
	Port      int
	Version   string
	Database  DatabaseConfig
	Telemetry TelemetryConfig
	Auth      AuthConfig
}

type DatabaseConfig struct {
	URL             string
	MaxConnections  int
	MigrationsPath  string
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
		Version: envStr("AGENTOVEN_VERSION", "0.3.0"),
		Database: DatabaseConfig{
			URL:            envStr("DATABASE_URL", "postgres://agentoven:agentoven@localhost:5432/agentoven?sslmode=disable"),
			MaxConnections: envInt("DATABASE_MAX_CONNECTIONS", 25),
			MigrationsPath: envStr("DATABASE_MIGRATIONS_PATH", "internal/db/migrations"),
		},
		Telemetry: TelemetryConfig{
			Enabled:      envBool("OTEL_ENABLED", true),
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

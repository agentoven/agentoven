//! Configuration for AgentOven SDK and CLI.

use serde::{Deserialize, Serialize};

/// AgentOven configuration — typically stored at `~/.agentoven/config.toml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentOvenConfig {
    /// Control plane URL.
    #[serde(default = "default_url")]
    pub url: String,

    /// API key (or read from AGENTOVEN_API_KEY).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub api_key: Option<String>,

    /// Default kitchen (workspace).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub default_kitchen: Option<String>,

    /// Telemetry configuration.
    #[serde(default)]
    pub telemetry: TelemetryConfig,
}

impl Default for AgentOvenConfig {
    fn default() -> Self {
        Self {
            url: default_url(),
            api_key: None,
            default_kitchen: None,
            telemetry: TelemetryConfig::default(),
        }
    }
}

fn default_url() -> String {
    "http://localhost:8080".into()
}

/// Telemetry/observability configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TelemetryConfig {
    /// Whether to send telemetry.
    #[serde(default = "default_true")]
    pub enabled: bool,

    /// OTLP exporter endpoint.
    #[serde(default = "default_otlp_endpoint")]
    pub otlp_endpoint: String,
}

impl Default for TelemetryConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            otlp_endpoint: default_otlp_endpoint(),
        }
    }
}

fn default_true() -> bool {
    true
}

fn default_otlp_endpoint() -> String {
    "http://localhost:4317".into()
}

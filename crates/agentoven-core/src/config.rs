//! Configuration for AgentOven SDK and CLI.
//!
//! Config is loaded from `~/.agentoven/config.toml` with env var overrides.
//! Pro users authenticate via `agentoven login`; community users just set
//! the URL and API key via `agentoven config set-url` / `set-key`.

use serde::{Deserialize, Serialize};
use std::path::PathBuf;

/// AgentOven configuration — stored at `~/.agentoven/config.toml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AgentOvenConfig {
    /// Control plane URL.
    #[serde(default = "default_url")]
    pub url: String,

    /// API key (community auth — no login flow).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub api_key: Option<String>,

    /// Auth token (Pro login flow — JWT from server).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub token: Option<String>,

    /// Token expiration (ISO 8601).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub token_expires_at: Option<String>,

    /// Active kitchen (workspace).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub kitchen: Option<String>,

    /// Server edition discovered from /api/v1/info (cached).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub edition: Option<String>,

    /// Telemetry configuration.
    #[serde(default)]
    pub telemetry: TelemetryConfig,
}

impl Default for AgentOvenConfig {
    fn default() -> Self {
        Self {
            url: default_url(),
            api_key: None,
            token: None,
            token_expires_at: None,
            kitchen: None,
            edition: None,
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

impl AgentOvenConfig {
    /// Returns the config file path: `~/.agentoven/config.toml`.
    pub fn config_path() -> Option<PathBuf> {
        dirs_next::home_dir().map(|h| h.join(".agentoven").join("config.toml"))
    }

    /// Load config from disk, falling back to defaults.
    /// Env vars override file values:
    ///   AGENTOVEN_URL, AGENTOVEN_API_KEY, AGENTOVEN_KITCHEN
    pub fn load() -> Self {
        let mut cfg = Self::load_from_file().unwrap_or_default();

        // Env vars override file config
        if let Ok(url) = std::env::var("AGENTOVEN_URL") {
            cfg.url = url;
        }
        if let Ok(key) = std::env::var("AGENTOVEN_API_KEY") {
            cfg.api_key = Some(key);
        }
        if let Ok(kitchen) = std::env::var("AGENTOVEN_KITCHEN") {
            cfg.kitchen = Some(kitchen);
        }

        cfg
    }

    /// Load config from `~/.agentoven/config.toml`.
    pub fn load_from_file() -> Option<Self> {
        let path = Self::config_path()?;
        let content = std::fs::read_to_string(&path).ok()?;
        toml::from_str(&content).ok()
    }

    /// Save config to `~/.agentoven/config.toml`.
    pub fn save(&self) -> anyhow::Result<()> {
        let path = Self::config_path()
            .ok_or_else(|| anyhow::anyhow!("Cannot determine home directory"))?;

        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }

        let content = toml::to_string_pretty(self)?;
        std::fs::write(&path, content)?;
        Ok(())
    }

    /// Update a single field and save.
    pub fn set_url(&mut self, url: &str) -> anyhow::Result<()> {
        self.url = url.to_string();
        self.edition = None; // clear cached edition since URL changed
        self.save()
    }

    /// Update API key and save.
    pub fn set_api_key(&mut self, key: &str) -> anyhow::Result<()> {
        self.api_key = Some(key.to_string());
        self.save()
    }

    /// Update active kitchen and save.
    pub fn set_kitchen(&mut self, kitchen: &str) -> anyhow::Result<()> {
        self.kitchen = Some(kitchen.to_string());
        self.save()
    }

    /// Returns the effective auth credential: token (Pro) or API key (community).
    pub fn auth_credential(&self) -> Option<&str> {
        // Prefer token (Pro login) over API key (community)
        if let Some(ref token) = self.token {
            if !token.is_empty() {
                return Some(token);
            }
        }
        self.api_key.as_deref()
    }
}

//! Configuration for AgentOven SDK and CLI.
//!
//! Config is loaded from `~/.agentoven/config.toml` with env var overrides.
//! Pro users authenticate via `agentoven login`; community users just set
//! the URL and API key via `agentoven config set-url` / `set-key`.

use serde::{Deserialize, Serialize};
use std::path::PathBuf;

/// AgentOven configuration — stored at `~/.agentoven/config.toml`.
#[derive(Clone, Serialize, Deserialize)]
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

    /// Seconds one request may take (`AGENTOVEN_TIMEOUT` overrides); unset means the client's default.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub timeout_secs: Option<u64>,

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
            timeout_secs: None,
            edition: None,
            telemetry: TelemetryConfig::default(),
        }
    }
}

impl std::fmt::Debug for AgentOvenConfig {
    // The API key and the login token are never printed.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("AgentOvenConfig")
            .field("url", &self.url)
            .field("api_key", &self.api_key.as_ref().map(|_| "[set]"))
            .field("token", &self.token.as_ref().map(|_| "[set]"))
            .field("token_expires_at", &self.token_expires_at)
            .field("kitchen", &self.kitchen)
            .field("timeout_secs", &self.timeout_secs)
            .field("edition", &self.edition)
            .field("telemetry", &self.telemetry)
            .finish()
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
        if let Some(secs) = std::env::var("AGENTOVEN_TIMEOUT").ok().and_then(|v| v.parse().ok()) {
            cfg.timeout_secs = Some(secs);
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
        self.save_to(&path)
    }

    /// Save config to `path`. The file holds the API key and the login token, so it is written readable by
    /// its owner only (0600, and its directory 0700), including when it already existed with wider rights.
    pub fn save_to(&self, path: &std::path::Path) -> anyhow::Result<()> {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                std::fs::set_permissions(parent, std::fs::Permissions::from_mode(0o700))?;
            }
        }
        let content = toml::to_string_pretty(self)?;
        write_private(path, &content)
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

/// Write `content` to a file only its owner can read.
fn write_private(path: &std::path::Path, content: &str) -> anyhow::Result<()> {
    use std::io::Write;
    let mut opts = std::fs::OpenOptions::new();
    opts.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut file = opts.open(path)?;
    #[cfg(unix)]
    {
        // `mode` applies to a new file only: tighten one that already existed before anything is written.
        use std::os::unix::fs::PermissionsExt;
        file.set_permissions(std::fs::Permissions::from_mode(0o600))?;
    }
    file.write_all(content.as_bytes())?;
    Ok(())
}

#[cfg(test)]
mod secret_tests {
    use super::*;

    #[test]
    fn debug_output_never_shows_the_key_or_the_token() {
        let cfg = AgentOvenConfig {
            api_key: Some("sk-live-SECRET".into()),
            token: Some("jwt-SECRET".into()),
            ..Default::default()
        };
        let shown = format!("{cfg:?}");
        assert!(!shown.contains("SECRET"), "{shown}");
        assert!(shown.contains("[set]"));
    }

    #[cfg(unix)]
    #[test]
    fn the_config_file_is_private_even_when_it_existed_with_wider_rights() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("ao-cfg-{}", std::process::id()));
        let path = dir.join("nested").join("config.toml");
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(&path, "old").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();

        let cfg = AgentOvenConfig { api_key: Some("k".into()), ..Default::default() };
        cfg.save_to(&path).unwrap();
        let mode = |p: &std::path::Path| std::fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&path), 0o600);
        assert_eq!(mode(path.parent().unwrap()), 0o700);
        assert!(std::fs::read_to_string(&path).unwrap().contains("api_key"));
        std::fs::remove_dir_all(&dir).ok();
    }
}

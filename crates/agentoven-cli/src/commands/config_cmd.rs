//! `agentoven config` — manage CLI configuration.

use clap::Subcommand;
use colored::Colorize;

#[derive(Subcommand)]
pub enum ConfigCommands {
    /// Set the control plane URL.
    SetUrl(SetUrlArgs),
    /// Set the API key (for community/OSS).
    SetKey(SetKeyArgs),
    /// Set the active kitchen.
    SetKitchen(SetKitchenArgs),
    /// Show the current configuration.
    Show,
}

#[derive(clap::Args)]
pub struct SetUrlArgs {
    /// The control plane URL (e.g., http://localhost:8080).
    pub url: String,
}

#[derive(clap::Args)]
pub struct SetKeyArgs {
    /// API key value. If omitted, prompts interactively.
    pub key: Option<String>,
}

#[derive(clap::Args)]
pub struct SetKitchenArgs {
    /// Kitchen name or ID.
    pub kitchen: String,
}

pub async fn execute(cmd: ConfigCommands) -> anyhow::Result<()> {
    match cmd {
        ConfigCommands::SetUrl(args) => set_url(args).await,
        ConfigCommands::SetKey(args) => set_key(args).await,
        ConfigCommands::SetKitchen(args) => set_kitchen(args).await,
        ConfigCommands::Show => show().await,
    }
}

async fn set_url(args: SetUrlArgs) -> anyhow::Result<()> {
    let mut config = agentoven_core::AgentOvenConfig::load();
    config.set_url(&args.url)?;

    println!(
        "  {} Control plane URL set to {}",
        "✓".green().bold(),
        args.url.cyan()
    );

    // Probe the server to detect edition
    match probe_edition(&config).await {
        Ok(edition) => {
            println!(
                "  {} Server edition: {}",
                "→".dimmed(),
                edition.cyan().bold()
            );
        }
        Err(_) => {
            println!(
                "  {} Server not reachable yet (will retry when commands are run)",
                "→".dimmed(),
            );
        }
    }

    Ok(())
}

async fn set_key(args: SetKeyArgs) -> anyhow::Result<()> {
    let key = if let Some(k) = args.key {
        k
    } else {
        dialoguer::Password::new()
            .with_prompt("  Enter your API key")
            .interact()?
    };

    let mut config = agentoven_core::AgentOvenConfig::load();
    config.set_api_key(&key)?;

    println!("  {} API key saved.", "✓".green().bold());
    println!(
        "  {} Config: {}",
        "→".dimmed(),
        agentoven_core::AgentOvenConfig::config_path()
            .map(|p| p.display().to_string())
            .unwrap_or_else(|| "(unknown)".to_string())
            .dimmed()
    );
    Ok(())
}

async fn set_kitchen(args: SetKitchenArgs) -> anyhow::Result<()> {
    let mut config = agentoven_core::AgentOvenConfig::load();
    config.set_kitchen(&args.kitchen)?;

    println!(
        "  {} Active kitchen set to {}",
        "✓".green().bold(),
        args.kitchen.cyan().bold()
    );
    Ok(())
}

async fn show() -> anyhow::Result<()> {
    let config = agentoven_core::AgentOvenConfig::load();
    let config_path = agentoven_core::AgentOvenConfig::config_path()
        .map(|p| p.display().to_string())
        .unwrap_or_else(|| "(unknown)".to_string());

    println!("\n  ⚙️  AgentOven Configuration\n");
    println!("  {:<16} {}", "Config file:".bold(), config_path);
    println!("  {:<16} {}", "URL:".bold(), config.url.cyan());
    println!(
        "  {:<16} {}",
        "API Key:".bold(),
        match &config.api_key {
            Some(k) if !k.is_empty() => mask_key(k),
            _ => "(not set)".dimmed().to_string(),
        }
    );
    println!(
        "  {:<16} {}",
        "Kitchen:".bold(),
        match &config.kitchen {
            Some(k) if !k.is_empty() => k.clone(),
            _ => "default".dimmed().to_string(),
        }
    );
    println!(
        "  {:<16} {}",
        "Edition:".bold(),
        match &config.edition {
            Some(e) if !e.is_empty() => e.clone(),
            _ => "(unknown — run a command to detect)".dimmed().to_string(),
        }
    );

    if let Some(ref tok) = config.token {
        if !tok.is_empty() {
            println!("  {:<16} {}", "Auth token:".bold(), mask_key(tok));
            if let Some(ref exp) = config.token_expires_at {
                println!("  {:<16} {}", "Token expires:".bold(), exp);
            }
        }
    }

    println!();
    Ok(())
}

/// Mask a key, showing only the first 6 and last 4 chars.
fn mask_key(key: &str) -> String {
    if key.len() <= 12 {
        "****".to_string()
    } else {
        format!("{}...{}", &key[..6], &key[key.len() - 4..])
    }
}

/// Probe the server for its edition by calling GET /api/v1/info.
async fn probe_edition(config: &agentoven_core::AgentOvenConfig) -> anyhow::Result<String> {
    let client = agentoven_core::AgentOvenClient::from_config(config)?;
    let info = client.server_info().await?;
    let edition = info["edition"].as_str().unwrap_or("community").to_string();
    Ok(edition)
}

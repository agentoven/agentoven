//! `agentoven login` — authenticate with the control plane (Pro only).
//!
//! Community/OSS users should use `agentoven config set-key` instead.

use clap::Args;
use colored::Colorize;

#[derive(Args)]
pub struct LoginArgs {
    /// API key (or use interactive prompt).
    #[arg(long)]
    pub api_key: Option<String>,

    /// Control plane URL.
    #[arg(long)]
    pub url: Option<String>,
}

pub async fn execute(args: LoginArgs) -> anyhow::Result<()> {
    // Check server edition — login is Pro only
    let mut config = agentoven_core::AgentOvenConfig::load();

    // If URL was provided as a flag, apply it first
    if let Some(ref url) = args.url {
        config.set_url(url)?;
    }

    let client = agentoven_core::AgentOvenClient::from_config(&config)?;

    match client.server_info().await {
        Ok(info) => {
            let edition = info["edition"].as_str().unwrap_or("community");

            if edition == "community" {
                println!("\n  {} Login is not available for community edition.\n", "ℹ".cyan().bold());
                println!("  Community authentication uses API keys.");
                println!("  Use these commands instead:\n");
                println!("    agentoven config set-url <url>     Set the server URL");
                println!("    agentoven config set-key <key>     Set your API key");
                println!("    agentoven config show              Show current config");
                println!();
                return Ok(());
            }

            // Pro/Enterprise login flow
            println!("\n  🔑 Authenticating with AgentOven {} ...\n", edition.cyan().bold());

            let api_key = if let Some(key) = args.api_key {
                key
            } else {
                dialoguer::Password::new()
                    .with_prompt("  Enter your API key")
                    .interact()?
            };

            // Save the key
            config.set_api_key(&api_key)?;

            println!(
                "  {} Authenticated! Config saved to {}",
                "✓".green().bold(),
                agentoven_core::AgentOvenConfig::config_path()
                    .map(|p| p.display().to_string())
                    .unwrap_or_else(|| "(unknown)".to_string())
                    .cyan()
            );
            println!("  {} Connected to: {} ({})", "→".dimmed(), config.url.cyan(), edition);
        }
        Err(_) => {
            // Server unreachable — fall back to basic login (save key anyway)
            println!("\n  🔑 Authenticating with AgentOven...\n");
            println!(
                "  {} Could not reach server to detect edition. Saving credentials anyway.",
                "⚠".yellow().bold()
            );

            let api_key = if let Some(key) = args.api_key {
                key
            } else {
                dialoguer::Password::new()
                    .with_prompt("  Enter your API key")
                    .interact()?
            };

            config.set_api_key(&api_key)?;

            println!(
                "  {} Credentials saved to {}",
                "✓".green().bold(),
                agentoven_core::AgentOvenConfig::config_path()
                    .map(|p| p.display().to_string())
                    .unwrap_or_else(|| "(unknown)".to_string())
                    .cyan()
            );
            println!("  {} Server: {}", "→".dimmed(), config.url.cyan());
        }
    }
    Ok(())
}

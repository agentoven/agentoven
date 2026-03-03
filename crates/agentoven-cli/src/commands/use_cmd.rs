//! `agentoven use <kitchen>` — switch the active kitchen.

use clap::Args;
use colored::Colorize;

#[derive(Args)]
pub struct UseArgs {
    /// Kitchen name or ID to switch to.
    pub kitchen: String,
}

pub async fn execute(args: UseArgs) -> anyhow::Result<()> {
    println!("\n  🏠 Switching to kitchen: {}\n", args.kitchen.bold());

    let mut config = agentoven_core::AgentOvenConfig::load();

    // Build a client to verify the kitchen exists and we have access
    let client = agentoven_core::AgentOvenClient::from_config(&config)?;

    match client.get_kitchen(&args.kitchen).await {
        Ok(k) => {
            let name = k["name"]
                .as_str()
                .or_else(|| k["id"].as_str())
                .unwrap_or(&args.kitchen);
            let plan = k["plan"].as_str().unwrap_or("community");

            config.set_kitchen(&args.kitchen)?;

            println!(
                "  {} Now using kitchen {} (plan: {})",
                "✓".green().bold(),
                name.cyan().bold(),
                plan
            );
        }
        Err(e) => {
            let msg = e.to_string();

            if msg.contains("401") || msg.contains("403") {
                println!(
                    "  {} Access denied to kitchen {}",
                    "✗".red().bold(),
                    args.kitchen.yellow()
                );
                println!(
                    "  {} Your current credentials do not have access to this kitchen.",
                    "→".dimmed()
                );

                // Check if this is a Pro server — suggest login
                if let Ok(info) = client.server_info().await {
                    let edition = info["edition"].as_str().unwrap_or("community");
                    if edition != "community" {
                        println!(
                            "  {} Try `agentoven login` to re-authenticate.",
                            "→".dimmed()
                        );
                    }
                }
            } else if msg.contains("404") {
                println!(
                    "  {} Kitchen {} not found.",
                    "✗".red().bold(),
                    args.kitchen.yellow()
                );
                println!(
                    "  {} Use `agentoven kitchen list` to see available kitchens.",
                    "→".dimmed()
                );
                println!(
                    "  {} Use `agentoven kitchen create {}` to create it.",
                    "→".dimmed(),
                    args.kitchen
                );
            } else {
                println!(
                    "  {} Could not access kitchen: {}",
                    "⚠".yellow().bold(),
                    msg.dimmed()
                );
            }
        }
    }
    Ok(())
}

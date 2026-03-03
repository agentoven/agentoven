//! `agentoven service-account` — manage service accounts (Pro).

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;

const FEATURE_NAME: &str = "Service Accounts";
const FEATURE_KEY: &str = "service_accounts";

#[derive(Subcommand)]
pub enum ServiceAccountCommands {
    /// List service accounts.
    List,
    /// Create a service account.
    Create(CreateArgs),
    /// Delete a service account.
    Delete(DeleteArgs),
    /// Rotate a service account token.
    Rotate(RotateArgs),
}

#[derive(Args)]
pub struct CreateArgs {
    /// Service account name.
    pub name: String,
    /// Role to assign (baker, chef, admin).
    #[arg(long, default_value = "baker")]
    pub role: String,
    /// Kitchen scope (defaults to current).
    #[arg(long)]
    pub kitchen: Option<String>,
    /// Token expiry (e.g., "30d", "90d", "365d").
    #[arg(long, default_value = "90d")]
    pub expires: String,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Service account name or ID.
    pub id: String,
    /// Skip confirmation.
    #[arg(long)]
    pub yes: bool,
}

#[derive(Args)]
pub struct RotateArgs {
    /// Service account name or ID.
    pub id: String,
}

pub async fn execute(cmd: ServiceAccountCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }

    match cmd {
        ServiceAccountCommands::List => list().await,
        ServiceAccountCommands::Create(args) => create(args).await,
        ServiceAccountCommands::Delete(args) => delete(args).await,
        ServiceAccountCommands::Rotate(args) => rotate(args).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🤖 Service Accounts:\n");

    let client = pro_gate::build_client()?;
    let accounts: Vec<serde_json::Value> =
        client.raw_get("/api/v1/service-accounts").await?;

    if accounts.is_empty() {
        println!("  (no service accounts)");
    } else {
        println!(
            "  {:<24} {:<12} {:<16} {:<20}",
            "NAME".bold(),
            "ROLE".bold(),
            "KITCHEN".bold(),
            "EXPIRES".bold()
        );
        println!("  {}", "─".repeat(76).dimmed());
        for a in &accounts {
            let name = a["name"].as_str().unwrap_or("-");
            let role = a["role"].as_str().unwrap_or("-");
            let kitchen = a["kitchen"].as_str().unwrap_or("(all)");
            let expires = a["expires_at"].as_str().unwrap_or("-");
            let exp_short = if expires.len() > 10 {
                &expires[..10]
            } else {
                expires
            };
            println!("  {:<24} {:<12} {:<16} {:<20}", name, role, kitchen, exp_short);
        }
        println!("\n  {} {} account(s)", "→".dimmed(), accounts.len());
    }
    Ok(())
}

async fn create(args: CreateArgs) -> anyhow::Result<()> {
    println!(
        "\n  🤖 Creating service account: {}\n",
        args.name.bold()
    );

    let body = serde_json::json!({
        "name": args.name,
        "role": args.role,
        "kitchen": args.kitchen,
        "expires": args.expires,
    });

    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>("/api/v1/service-accounts", &body)
        .await
    {
        Ok(resp) => {
            let token = resp["token"].as_str().unwrap_or("");
            println!(
                "  {} Service account {} created.",
                "✓".green().bold(),
                args.name.cyan()
            );
            if !token.is_empty() {
                println!();
                println!(
                    "  {}",
                    "⚠ Save this token — it will not be shown again!".yellow().bold()
                );
                println!("  Token: {}", token.cyan());
            }
        }
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Delete service account {}?", args.id))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = pro_gate::build_client()?;
    match client
        .raw_delete(&format!("/api/v1/service-accounts/{}", args.id))
        .await
    {
        Ok(()) => {
            println!(
                "  {} Service account {} deleted.",
                "✓".green().bold(),
                args.id.dimmed()
            );
        }
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn rotate(args: RotateArgs) -> anyhow::Result<()> {
    println!(
        "\n  🔄 Rotating token for service account: {}\n",
        args.id.bold()
    );

    let body = serde_json::json!({});
    let client = pro_gate::build_client()?;
    match client
        .raw_post::<serde_json::Value>(
            &format!("/api/v1/service-accounts/{}/rotate", args.id),
            &body,
        )
        .await
    {
        Ok(resp) => {
            let token = resp["token"].as_str().unwrap_or("");
            println!(
                "  {} Token rotated for {}.",
                "✓".green().bold(),
                args.id.cyan()
            );
            if !token.is_empty() {
                println!();
                println!(
                    "  {}",
                    "⚠ Save this token — it will not be shown again!".yellow().bold()
                );
                println!("  Token: {}", token.cyan());
            }
        }
        Err(e) => {
            println!(
                "  {} Failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

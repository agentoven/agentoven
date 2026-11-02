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
    /// Give a service account to another person (a member of the kitchen at a rank that covers its role).
    Owner(OwnerArgs),
}

#[derive(Args)]
pub struct CreateArgs {
    /// Service account name.
    pub name: String,
    /// Role to assign: head_chef, chef, baker, taster, auditor, legal, finance or viewer. The retired
    /// `admin` is gone (it was a Head Chef inside a kitchen).
    #[arg(long, default_value = "baker")]
    pub role: String,
    /// The person the account belongs to (their e-mail; you if left out): a member of the kitchen at a
    /// rank that covers the role. Creating one for someone else is a Head Chef's act. When the owner
    /// leaves the kitchen the account stops until `service-account owner` gives it to someone.
    #[arg(long)]
    pub owner: Option<String>,
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

#[derive(Args)]
pub struct OwnerArgs {
    /// Service account ID.
    pub id: String,
    /// The new owner's e-mail: a member of the kitchen at a rank that covers the account's role.
    pub email: String,
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
        ServiceAccountCommands::Owner(args) => owner(args).await,
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

    let body = create_body(&args)?;

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

/// The body of a create: the role is checked against the kitchen roles before anything is sent.
pub fn create_body(args: &CreateArgs) -> anyhow::Result<serde_json::Value> {
    agentoven_core::check_role(&args.role, &agentoven_core::KITCHEN_ROLES, "a service account")?;
    let mut body = serde_json::json!({
        "name": args.name,
        "role": args.role,
        "kitchen": args.kitchen,
        "expires": args.expires,
    });
    if let Some(owner) = &args.owner {
        body["owner_email"] = serde_json::json!(owner);
    }
    Ok(body)
}

async fn owner(args: OwnerArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let sa = client.set_service_account_owner(&args.id, &args.email).await?;
    println!(
        "  {} Service account {} now belongs to {}.",
        "✓".green().bold(),
        args.id.cyan(),
        sa["owner_email"].as_str().unwrap_or(&args.email)
    );
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

#[cfg(test)]
mod tests {
    use super::*;

    fn args(role: &str, owner: Option<&str>) -> CreateArgs {
        CreateArgs {
            name: "ci".into(),
            role: role.into(),
            owner: owner.map(String::from),
            kitchen: None,
            expires: "90d".into(),
        }
    }

    #[test]
    fn the_retired_admin_and_unknown_roles_are_refused_before_sending() {
        let e = create_body(&args("admin", None)).unwrap_err().to_string();
        assert!(e.contains("retired") && e.contains("head_chef"), "{e}");
        assert!(create_body(&args("restaurateur", None)).is_err());
        assert!(create_body(&args("sous_chef", None)).is_err());
    }

    #[test]
    fn every_kitchen_role_is_accepted_and_the_owner_goes_in_the_body() {
        for r in agentoven_core::KITCHEN_ROLES {
            assert!(create_body(&args(r, None)).is_ok(), "{r}");
        }
        let b = create_body(&args("baker", Some("ana@acme.io"))).unwrap();
        assert_eq!(b["owner_email"], "ana@acme.io");
        assert!(create_body(&args("baker", None)).unwrap().get("owner_email").is_none());
    }

    #[test]
    fn help_names_the_real_roles() {
        use clap::CommandFactory;
        let cmd = crate::commands::Cli::command();
        let sa = cmd.find_subcommand("service-account").unwrap();
        let create = sa.find_subcommand("create").unwrap();
        let role = create.get_arguments().find(|a| a.get_id() == "role").unwrap();
        let help = role.get_help().unwrap().to_string();
        assert!(help.contains("head_chef") && help.contains("taster") && help.contains("legal"), "{help}");
        assert!(!help.contains("admin,") && !help.contains(", admin"), "{help}");
    }
}

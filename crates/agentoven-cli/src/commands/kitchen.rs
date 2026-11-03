//! `agentoven kitchen` — manage kitchens (workspaces/tenants).

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum KitchenCommands {
    /// List all kitchens.
    List,
    /// Get kitchen details.
    Get(GetArgs),
    /// Create a new kitchen.
    Create(CreateArgs),
    /// Delete a kitchen.
    Delete(DeleteArgs),
    /// View current kitchen settings.
    Settings,
    /// Update kitchen settings.
    UpdateSettings(UpdateSettingsArgs),
    /// Identity-provider group bindings of a kitchen (Pro): which group is which role.
    #[command(subcommand)]
    Bindings(BindingCommands),
    /// The kitchens you can enter and your role in each (Pro).
    Mine,
}

#[derive(Subcommand)]
pub enum BindingCommands {
    /// List a kitchen's bindings.
    Ls(BindingLsArgs),
    /// Bind an identity-provider group (or app role) to a kitchen role. Takes effect at the next login.
    Add(BindingAddArgs),
    /// Remove a binding.
    Rm(BindingRmArgs),
}

#[derive(Args)]
pub struct BindingLsArgs {
    /// Kitchen ID.
    pub kitchen: String,
}

#[derive(Args)]
pub struct BindingAddArgs {
    /// Kitchen ID.
    pub kitchen: String,
    /// The group object id or app role value the identity provider puts in the token.
    pub claim: String,
    /// The kitchen role: head_chef, chef, baker, taster, auditor, legal, finance or viewer. Binding a group
    /// as head_chef is a Restaurateur's decision; `restaurateur` is platform-wide and `admin` is retired.
    #[arg(long)]
    pub role: String,
    /// A name for people ("QA reviewers").
    #[arg(long, default_value = "")]
    pub label: String,
}

#[derive(Args)]
pub struct BindingRmArgs {
    /// Kitchen ID.
    pub kitchen: String,
    /// Binding ID.
    pub binding: String,
}

#[derive(Args)]
pub struct GetArgs {
    /// Kitchen ID.
    pub id: String,
}

#[derive(Args)]
pub struct CreateArgs {
    /// Kitchen name (must be unique).
    pub name: String,
    /// Plan (community, pro, enterprise). Defaults to community.
    #[arg(long, default_value = "community")]
    pub plan: String,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Kitchen ID to delete.
    pub id: String,
    /// Skip confirmation prompt.
    #[arg(long)]
    pub yes: bool,
}

#[derive(Args)]
pub struct UpdateSettingsArgs {
    /// Maximum template size in bytes.
    #[arg(long)]
    pub max_template_size: Option<u64>,
    /// Trace retention TTL (e.g., "7d", "90d").
    #[arg(long)]
    pub trace_ttl: Option<String>,
    /// Audit retention TTL (e.g., "30d", "400d").
    #[arg(long)]
    pub audit_ttl: Option<String>,
    /// Max items before archival.
    #[arg(long)]
    pub max_items: Option<u64>,
    /// Enable archival (true/false).
    #[arg(long)]
    pub archive: Option<bool>,
    /// Raw JSON settings (overrides other flags).
    #[arg(long)]
    pub json: Option<String>,
}

pub async fn execute(cmd: KitchenCommands) -> anyhow::Result<()> {
    match cmd {
        KitchenCommands::List => list().await,
        KitchenCommands::Get(args) => get(args).await,
        KitchenCommands::Create(args) => create(args).await,
        KitchenCommands::Delete(args) => delete(args).await,
        KitchenCommands::Settings => settings().await,
        KitchenCommands::UpdateSettings(args) => update_settings(args).await,
        KitchenCommands::Bindings(cmd) => {
            let client = agentoven_core::AgentOvenClient::from_env()?;
            print!("{}", bindings(cmd, &client).await?);
            Ok(())
        }
        KitchenCommands::Mine => {
            let client = agentoven_core::AgentOvenClient::from_env()?;
            print!("{}", mine(&client).await?);
            Ok(())
        }
    }
}

/// `kitchen bindings ...`: run it against `client` and return what it prints.
pub async fn bindings(
    cmd: BindingCommands,
    client: &agentoven_core::AgentOvenClient,
) -> anyhow::Result<String> {
    match cmd {
        BindingCommands::Ls(a) => {
            let bs = client.list_kitchen_bindings(&a.kitchen).await?;
            let mut out = String::from("\n");
            if bs.is_empty() {
                out.push_str("  (no bindings — people get the role their membership gives them)\n");
            } else {
                out.push_str(&format!(
                    "  {:<38} {:<40} {:<10} {}\n",
                    "ID".bold(),
                    "GROUP / APP ROLE".bold(),
                    "ROLE".bold(),
                    "LABEL".bold()
                ));
                for b in &bs {
                    out.push_str(&format!(
                        "  {:<38} {:<40} {:<10} {}\n",
                        b["id"].as_str().unwrap_or("-"),
                        b["claim_value"].as_str().unwrap_or("-"),
                        b["role"].as_str().unwrap_or("-"),
                        b["label"].as_str().unwrap_or("")
                    ));
                }
            }
            Ok(out)
        }
        BindingCommands::Add(a) => {
            let b = client
                .create_kitchen_binding(&a.kitchen, &a.claim, &a.role, &a.label)
                .await?;
            Ok(format!(
                "  {} Bound {} to {} (binding {}); it takes effect at each person's next login.\n",
                "✓".green().bold(),
                a.claim.cyan(),
                a.role.bold(),
                b["id"].as_str().unwrap_or("-")
            ))
        }
        BindingCommands::Rm(a) => {
            client
                .delete_kitchen_binding(&a.kitchen, &a.binding)
                .await?;
            Ok(format!(
                "  {} Removed binding {}.\n",
                "✓".green().bold(),
                a.binding
            ))
        }
    }
}

/// `kitchen mine`: the kitchens you can enter and your role in each.
pub async fn mine(client: &agentoven_core::AgentOvenClient) -> anyhow::Result<String> {
    let m = client.my_kitchens().await?;
    let mut out = String::from("\n");
    if m["enforced"].as_bool().unwrap_or(false) {
        out.push_str("  Membership is enforced: you can enter only these kitchens.\n\n");
    }
    for k in m["kitchens"].as_array().cloned().unwrap_or_default() {
        out.push_str(&format!(
            "  {:<28} {}\n",
            k["name"].as_str().unwrap_or("-"),
            k["role"].as_str().unwrap_or("-").cyan()
        ));
    }
    Ok(out)
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🏠 Kitchens:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_kitchens().await {
        Ok(kitchens) => {
            if kitchens.is_empty() {
                println!("  (no kitchens)");
            } else {
                println!(
                    "  {:<24} {:<12} {:<16} {:<12}",
                    "NAME".bold(),
                    "PLAN".bold(),
                    "CREATED".bold(),
                    "AGENTS".bold()
                );
                println!("  {}", "─".repeat(68).dimmed());
                for k in &kitchens {
                    let name = k["name"]
                        .as_str()
                        .unwrap_or(k["id"].as_str().unwrap_or("-"));
                    let plan = k["plan"].as_str().unwrap_or("community");
                    let created = k["created_at"].as_str().unwrap_or("-");
                    let created_short = if created.len() > 10 {
                        &created[..10]
                    } else {
                        created
                    };
                    let agents = k["agent_count"].as_u64().unwrap_or(0);
                    println!(
                        "  {:<24} {:<12} {:<16} {}",
                        name, plan, created_short, agents
                    );
                }
                println!("\n  {} {} kitchen(s)", "→".dimmed(), kitchens.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not list kitchens: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  🏠 Kitchen: {}\n", args.id.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_kitchen(&args.id).await {
        Ok(k) => {
            println!("  {:<16} {}", "ID:".bold(), k["id"].as_str().unwrap_or("-"));
            println!(
                "  {:<16} {}",
                "Name:".bold(),
                k["name"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Plan:".bold(),
                k["plan"].as_str().unwrap_or("community")
            );
            println!(
                "  {:<16} {}",
                "Created:".bold(),
                k["created_at"].as_str().unwrap_or("-")
            );

            if let Some(limits) = k.get("plan_limits") {
                println!("\n  {}:", "Plan Limits".bold());
                if let Some(max_agents) = limits["max_agents"].as_u64() {
                    println!("    Max Agents:    {}", max_agents);
                }
                if let Some(max_recipes) = limits["max_recipes"].as_u64() {
                    println!("    Max Recipes:   {}", max_recipes);
                }
                if let Some(max_providers) = limits["max_providers"].as_u64() {
                    println!("    Max Providers: {}", max_providers);
                }
                if let Some(max_tools) = limits["max_tools"].as_u64() {
                    println!("    Max Tools:     {}", max_tools);
                }
            }
        }
        Err(e) => {
            println!(
                "  {} Not found: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn settings() -> anyhow::Result<()> {
    println!("\n  ⚙️  Kitchen Settings:\n");

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_settings().await {
        Ok(s) => {
            let json_pretty = serde_json::to_string_pretty(&s).unwrap_or_default();
            for line in json_pretty.lines() {
                println!("  {}", line.dimmed());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch settings: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn update_settings(args: UpdateSettingsArgs) -> anyhow::Result<()> {
    println!("\n  ⚙️  Updating settings...\n");

    let body = if let Some(ref raw) = args.json {
        serde_json::from_str(raw)?
    } else {
        let mut obj = serde_json::Map::new();
        if let Some(v) = args.max_template_size {
            obj.insert("max_template_size".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.trace_ttl {
            obj.insert("trace_ttl".into(), serde_json::json!(v));
        }
        if let Some(ref v) = args.audit_ttl {
            obj.insert("audit_ttl".into(), serde_json::json!(v));
        }
        if let Some(v) = args.max_items {
            obj.insert("max_items".into(), serde_json::json!(v));
        }
        if let Some(v) = args.archive {
            obj.insert("archive_enabled".into(), serde_json::json!(v));
        }
        serde_json::Value::Object(obj)
    };

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.update_settings(body).await {
        Ok(_) => {
            println!("  {} Settings updated.", "✓".green().bold());
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn create(args: CreateArgs) -> anyhow::Result<()> {
    println!("\n  🏠 Creating kitchen: {}\n", args.name.bold());

    let body = serde_json::json!({
        "name": args.name,
        "plan": args.plan,
    });

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.create_kitchen(body).await {
        Ok(k) => {
            let id = k["id"].as_str().unwrap_or("-");
            let name = k["name"].as_str().unwrap_or(&args.name);
            println!(
                "  {} Kitchen {} created (id: {})",
                "✓".green().bold(),
                name.cyan().bold(),
                id.dimmed()
            );
        }
        Err(e) => {
            println!(
                "  {} Failed to create kitchen: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        println!(
            "\n  {} Are you sure you want to delete kitchen {}?",
            "⚠".yellow().bold(),
            args.id.red().bold()
        );
        println!("  This action cannot be undone.\n");
        let confirm = dialoguer::Confirm::new()
            .with_prompt("  Continue?")
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_kitchen(&args.id).await {
        Ok(()) => {
            println!(
                "  {} Kitchen {} deleted.",
                "✓".green().bold(),
                args.id.dimmed()
            );
        }
        Err(e) => {
            println!(
                "  {} Failed to delete kitchen: {}",
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
    use crate::commands::{Cli, Commands};
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use clap::Parser;
    use serde_json::json;

    fn parse(args: &[&str]) -> KitchenCommands {
        let mut v = vec!["agentoven", "kitchen"];
        v.extend_from_slice(args);
        match Cli::try_parse_from(v).expect("parse").command {
            Commands::Kitchen(c) => c,
            _ => panic!("not a kitchen command"),
        }
    }

    fn bindings_cmd(args: &[&str]) -> BindingCommands {
        match parse(&[&["bindings"], args].concat()) {
            KitchenCommands::Bindings(b) => b,
            _ => panic!(),
        }
    }

    #[tokio::test]
    async fn bindings_are_listed_added_and_removed() {
        colored::control::set_override(false);
        let (base, log) = fake_control_plane(Routes::from([
            ("GET /api/v1/kitchens/k1/bindings".to_string(), reply(200, json!([{"id": "b1", "claim_value": "g-1", "role": "chef", "label": "builders"}]))),
            ("POST /api/v1/kitchens/k1/bindings".to_string(), reply(201, json!({"id": "b2"}))),
            ("DELETE /api/v1/kitchens/k1/bindings/b2".to_string(), reply(204, json!({}))),
        ])).await;
        let c = agentoven_core::AgentOvenClient::new(&base).unwrap();
        let out = bindings(bindings_cmd(&["ls", "k1"]), &c).await.unwrap();
        assert!(
            out.contains("g-1") && out.contains("chef") && out.contains("builders"),
            "{out}"
        );
        let out = bindings(
            bindings_cmd(&[
                "add",
                "k1",
                "aad-group",
                "--role",
                "taster",
                "--label",
                "QA",
            ]),
            &c,
        )
        .await
        .unwrap();
        assert!(out.contains("next login"), "{out}");
        assert_eq!(
            log.last().body,
            json!({"claim_value": "aad-group", "role": "taster", "label": "QA"})
        );
        bindings(bindings_cmd(&["rm", "k1", "b2"]), &c)
            .await
            .unwrap();
        assert_eq!(log.last().method, "DELETE");
    }

    #[tokio::test]
    async fn a_binding_takes_a_kitchen_role_only() {
        let (base, log) = fake_control_plane(Routes::new()).await;
        let c = agentoven_core::AgentOvenClient::new(&base).unwrap();
        for role in ["admin", "restaurateur", "sous_chef"] {
            assert!(
                bindings(bindings_cmd(&["add", "k1", "g", "--role", role]), &c)
                    .await
                    .is_err(),
                "{role}"
            );
        }
        assert!(log.is_empty());
    }

    #[tokio::test]
    async fn mine_shows_each_kitchen_and_the_role() {
        colored::control::set_override(false);
        let (base, _) = fake_control_plane(Routes::from([(
            "GET /api/v1/me/kitchens".to_string(),
            reply(200, json!({"enforced": true, "kitchens": [{"name": "acme", "role": "head_chef"}, {"name": "labs", "role": "viewer"}]})),
        )])).await;
        let c = agentoven_core::AgentOvenClient::new(&base).unwrap();
        let out = mine(&c).await.unwrap();
        assert!(
            out.contains("enforced")
                && out.contains("acme")
                && out.contains("head_chef")
                && out.contains("labs"),
            "{out}"
        );
    }
}

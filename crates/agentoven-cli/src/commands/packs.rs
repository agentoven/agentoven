//! `agentoven packs` — installable, governed tool packs (Pro, ADR-0048).
//!
//! A pack is a named, versioned set of tools for one kind of system. An install supplies only secret
//! references (`--secret role=vault://name`); a pack's write tools wait for a person's approval by
//! default, and with skill approval required the install waits for people too.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Map, Value};

use super::signing::{clip, pretty};

#[derive(Subcommand)]
pub enum PacksCommands {
    /// List the packs installed in this kitchen (--catalog: the packs the server offers).
    #[command(alias = "ls")]
    List {
        /// Show the catalog instead of the installs.
        #[arg(long)]
        catalog: bool,
    },
    /// Show one install: its tools, their switches and its approval status.
    Get { name: String },
    /// Install a catalog pack, binding its secret roles to vault:// references.
    Install(InstallArgs),
    /// Move an install to the catalog's current version (per-tool switches carry over).
    Upgrade {
        name: String,
        /// Rebind a secret role while upgrading: role=vault://name (repeatable).
        #[arg(long = "secret", value_name = "ROLE=REF")]
        secret: Vec<String>,
    },
    /// Remove an install, its tools and its skill.
    #[command(alias = "rm")]
    Uninstall { name: String },
    /// Run a pack's connectivity check, where one is possible.
    Check { name: String },
    /// Switch a tool on or off, or its approval on or off (needs the toolpack:govern permission).
    Tool(ToolArgs),
    /// The kitchen's allowlist of private hosts packs may reach.
    #[command(subcommand)]
    Egress(EgressCommands),
}

#[derive(Args)]
pub struct InstallArgs {
    /// The catalog pack (`agentoven packs list --catalog`).
    pub pack: String,
    /// Name the install (default: the pack's name). It prefixes the tool names.
    #[arg(long)]
    pub name: Option<String>,
    /// Bind a secret role to a reference: role=vault://name (repeatable). The value is never given here.
    #[arg(long = "secret", value_name = "ROLE=REF")]
    pub secret: Vec<String>,
    /// A setting, key=value (repeatable); a number is sent as a number, a,b as a list when the setting is one.
    #[arg(long = "set", value_name = "KEY=VALUE")]
    pub set: Vec<String>,
    /// Do not install this tool (repeatable, by the pack's tool name).
    #[arg(long = "disable", value_name = "TOOL")]
    pub disable: Vec<String>,
}

#[derive(Args)]
pub struct ToolArgs {
    /// The install.
    pub pack: String,
    /// The tool's name within the pack.
    pub tool: String,
    #[arg(long)]
    pub enable: bool,
    #[arg(long)]
    pub disable: bool,
    /// Hold the tool for a person's approval.
    #[arg(long)]
    pub require_approval: bool,
    /// Let the tool run without approval (needs toolpack:govern).
    #[arg(long)]
    pub no_approval: bool,
}

#[derive(Subcommand)]
pub enum EgressCommands {
    /// List the allowlisted hosts.
    #[command(alias = "ls")]
    List,
    /// Allowlist a host (a name, an address, or *.example.com). Cloud metadata addresses stay refused.
    Allow { host: String },
    /// Remove a host.
    Remove { host: String },
}

/// `role=ref` pairs as a JSON object.
pub fn pairs(items: &[String], what: &str) -> anyhow::Result<Map<String, Value>> {
    let mut m = Map::new();
    for it in items {
        let (k, v) = it
            .split_once('=')
            .filter(|(k, v)| !k.is_empty() && !v.is_empty())
            .ok_or_else(|| anyhow::anyhow!("{what} must look like name=value, got {it:?}"))?;
        m.insert(k.to_string(), json!(v));
    }
    Ok(m)
}

fn s(v: &Value, k: &str) -> String {
    v[k].as_str().unwrap_or("").to_string()
}

fn status_label(status: &str) -> String {
    match status {
        "active" => status.green().to_string(),
        "awaiting_approval" => "awaiting approval".yellow().to_string(),
        other => other.red().to_string(),
    }
}

/// Prints an install and its tools.
pub fn print_install(v: &Value) {
    println!(
        "  {}  {} v{}  {}",
        s(v, "name").bold(),
        s(v, "pack"),
        s(v, "version"),
        status_label(&s(v, "status"))
    );
    if v["out_of_sync"] == true {
        println!(
            "    {}",
            "out of sync: the registered tools differ from the install (upgrade it to repair)"
                .red()
        );
    }
    if v["status"] == "awaiting_approval" {
        println!(
            "    {}",
            format!(
                "approve the skill `{}` (agentoven skills approve) before its tools can be used",
                s(v, "skill")
            )
            .dimmed()
        );
    }
    if let Some(tools) = v["tools"].as_array() {
        for t in tools {
            println!(
                "    {:<36} {:<6} {}  {}",
                clip(&s(t, "name"), 36),
                s(t, "access"),
                if t["enabled"] == true { "on " } else { "off" },
                if t["require_approval"] == true {
                    "needs approval"
                } else {
                    ""
                }
            );
        }
    }
}

pub async fn execute(cmd: PacksCommands, client: AgentOvenClient) -> anyhow::Result<()> {
    match cmd {
        PacksCommands::List { catalog: true } => {
            let v = client.list_tool_pack_catalog().await?;
            for p in v["packs"].as_array().cloned().unwrap_or_default() {
                println!(
                    "  {:<20} v{:<8} {:<14} {}",
                    s(&p, "name").bold(),
                    s(&p, "version"),
                    s(&p, "kind"),
                    s(&p, "title")
                );
                let roles: Vec<String> = p["secrets"]
                    .as_array()
                    .map(|a| {
                        a.iter()
                            .map(|r| {
                                format!(
                                    "{}{}",
                                    s(r, "role"),
                                    if r["required"] == true { "*" } else { "" }
                                )
                            })
                            .collect()
                    })
                    .unwrap_or_default();
                println!("      secrets: {}  (* required)", roles.join(", "));
            }
        }
        PacksCommands::List { catalog: false } => {
            let v = client.list_tool_packs().await?;
            let rows = v["installs"].as_array().cloned().unwrap_or_default();
            if rows.is_empty() {
                println!("  No packs installed. `agentoven packs list --catalog` shows what is on offer.");
            }
            for r in &rows {
                print_install(r);
            }
        }
        PacksCommands::Get { name } => print_install(&client.get_tool_pack(&name).await?),
        PacksCommands::Install(a) => {
            let mut body = json!({"pack": a.pack, "secrets": pairs(&a.secret, "--secret")?});
            if let Some(n) = a.name {
                body["name"] = json!(n);
            }
            if !a.set.is_empty() {
                let mut settings = Map::new();
                for (k, v) in pairs(&a.set, "--set")? {
                    let raw = v.as_str().unwrap_or("");
                    settings.insert(
                        k,
                        raw.parse::<i64>().map(Value::from).unwrap_or_else(|_| {
                            if raw.contains(',') {
                                json!(raw.split(',').map(str::trim).collect::<Vec<_>>())
                            } else {
                                json!(raw)
                            }
                        }),
                    );
                }
                body["settings"] = Value::Object(settings);
            }
            if !a.disable.is_empty() {
                let tools: Map<String, Value> = a
                    .disable
                    .iter()
                    .map(|t| (t.clone(), json!({"enabled": false})))
                    .collect();
                body["tools"] = Value::Object(tools);
            }
            print_install(&client.install_tool_pack(&body).await?);
        }
        PacksCommands::Upgrade { name, secret } => {
            let mut body = json!({});
            if !secret.is_empty() {
                body["secrets"] = Value::Object(pairs(&secret, "--secret")?);
            }
            print_install(&client.upgrade_tool_pack(&name, &body).await?);
        }
        PacksCommands::Uninstall { name } => {
            client.uninstall_tool_pack(&name).await?;
            println!("  {} Removed {}.", "✓".green().bold(), name);
        }
        PacksCommands::Check { name } => {
            let v = client.check_tool_pack(&name).await?;
            if v["ok"] == true {
                println!("  {} {} answers.", "✓".green().bold(), name);
            } else {
                anyhow::bail!("{}", s(&v, "message"));
            }
        }
        PacksCommands::Tool(a) => {
            anyhow::ensure!(
                !(a.enable && a.disable) && !(a.require_approval && a.no_approval),
                "give --enable or --disable, and --require-approval or --no-approval, not both"
            );
            let mut body = json!({});
            if a.enable || a.disable {
                body["enabled"] = json!(a.enable);
            }
            if a.require_approval || a.no_approval {
                body["require_approval"] = json!(a.require_approval);
            }
            anyhow::ensure!(
                body.as_object().is_some_and(|o| !o.is_empty()),
                "nothing to change: give --enable, --disable, --require-approval or --no-approval"
            );
            let v = client.set_tool_pack_tool(&a.pack, &a.tool, &body).await?;
            println!("{}", pretty(&v));
        }
        PacksCommands::Egress(EgressCommands::List) => {
            let v = client.list_egress_hosts().await?;
            for h in v["hosts"].as_array().cloned().unwrap_or_default() {
                println!("  {:<40} added by {}", s(&h, "host"), s(&h, "added_by"));
            }
            for h in v["platform"].as_array().cloned().unwrap_or_default() {
                println!(
                    "  {:<40} {}",
                    h.as_str().unwrap_or(""),
                    "(operator)".dimmed()
                );
            }
        }
        PacksCommands::Egress(EgressCommands::Allow { host }) => {
            client.allow_egress_host(&host).await?;
            println!("  {} {} allowlisted.", "✓".green().bold(), host);
        }
        PacksCommands::Egress(EgressCommands::Remove { host }) => {
            client.remove_egress_host(&host).await?;
            println!("  {} {} removed.", "✓".green().bold(), host);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pairs_need_a_name_and_a_value() {
        let m = pairs(&["dsn=vault://wh".to_string()], "--secret").unwrap();
        assert_eq!(m["dsn"], "vault://wh");
        for bad in ["dsn", "=x", "dsn="] {
            assert!(pairs(&[bad.to_string()], "--secret").is_err(), "{bad}");
        }
    }
}

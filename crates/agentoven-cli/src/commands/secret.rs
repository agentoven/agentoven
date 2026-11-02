//! `agentoven secret` — secrets and where they live (Pro, ADR-0035).
//!
//! A secret is a name, a reference and metadata; its value lives in a provider (the built-in encrypted
//! vault by default, or Vault and the like) and is write-only: nothing here prints one. A value is read
//! from `AGENTOVEN_SECRET_VALUE`, from standard input with `--stdin`, or asked for at a prompt, and is
//! never a flag, so it never lands in a shell history or a process list.
//!
//! Approving or revoking a binding, and a real migration, are signed decisions: `--sign` (the password
//! is read from AGENTOVEN_SIGNING_PASSWORD or asked for) or `--signing-token`.

use std::io::Read;

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Value};

use super::signing::{clip, json_arg, pretty, signing_hint, SignArgs};

/// Where a secret's value is read from before the prompt.
pub const VALUE_ENV: &str = "AGENTOVEN_SECRET_VALUE";

#[derive(Subcommand)]
pub enum SecretCommands {
    /// List the kitchen's secrets (metadata only).
    Ls,
    /// Show one secret's metadata.
    Get(NameArgs),
    /// Create a secret: write a value into a provider, or --ref to link one somebody else keeps.
    Create(CreateArgs),
    /// Change a secret's metadata (what you do not name stays as it is).
    Update(UpdateArgs),
    /// Rotate a secret to a new value, optionally probing an upstream with it first.
    Rotate(RotateArgs),
    /// Pick up a rotation the secret's owner made (a linked secret).
    Refresh(NameArgs),
    /// List a secret's versions (no values).
    Versions(NameArgs),
    /// Delete a secret (and its value, unless it is linked).
    Rm(NameArgs),
    /// Bindings: which skill may send which secret to which server.
    #[command(subcommand)]
    Binding(BindingCommands),
    /// Move the kitchen's secrets from one provider to another (signed).
    Migrate(MigrateArgs),
    /// Secret providers: where values live.
    #[command(subcommand)]
    Provider(ProviderCommands),
}

#[derive(Args)]
pub struct NameArgs {
    pub name: String,
}

#[derive(Args)]
pub struct CreateArgs {
    pub name: String,
    /// The provider (default: the kitchen's default; `agentoven secret provider ls`).
    #[arg(long)]
    pub provider: Option<String>,
    /// Link a secret that already exists in the provider at this path, instead of writing a value.
    #[arg(long = "ref")]
    pub reference: Option<String>,
    /// Read the value from standard input (otherwise AGENTOVEN_SECRET_VALUE, then a prompt).
    #[arg(long)]
    pub stdin: bool,
    #[arg(long)]
    pub label: Option<String>,
    #[arg(long)]
    pub owner: Option<String>,
    /// Hosts the secret may be sent to (repeatable; `*.example.com` allowed). Pins it.
    #[arg(long = "allow-host")]
    pub allow_host: Vec<String>,
    /// RFC 3339 time after which the secret fails closed.
    #[arg(long)]
    pub expires_at: Option<String>,
    /// Refuse use of this secret for a skill with no approved binding.
    #[arg(long)]
    pub require_binding: bool,
    /// Seconds the previous version stays valid after a rotation (default 600).
    #[arg(long)]
    pub grace_seconds: Option<u64>,
}

#[derive(Args)]
pub struct UpdateArgs {
    pub name: String,
    #[arg(long)]
    pub label: Option<String>,
    #[arg(long)]
    pub owner: Option<String>,
    /// Replace the allowed hosts (repeatable). Use --clear-hosts to remove the pin.
    #[arg(long = "allow-host")]
    pub allow_host: Vec<String>,
    #[arg(long)]
    pub clear_hosts: bool,
    #[arg(long)]
    pub expires_at: Option<String>,
    #[arg(long)]
    pub clear_expiry: bool,
    #[arg(long)]
    pub require_binding: Option<bool>,
    #[arg(long)]
    pub grace_seconds: Option<u64>,
}

#[derive(Args)]
pub struct RotateArgs {
    pub name: String,
    /// Read the new value from standard input (otherwise AGENTOVEN_SECRET_VALUE, then a prompt).
    #[arg(long)]
    pub stdin: bool,
    /// Check the new value against this URL first (a host the secret is allowed to go to); a value it
    /// rejects is not rotated in.
    #[arg(long)]
    pub probe_url: Option<String>,
    /// `bearer` (default) or `api-key:<Header-Name>`.
    #[arg(long)]
    pub probe_scheme: Option<String>,
    /// The status the probe must answer (default: any 2xx).
    #[arg(long)]
    pub probe_expect: Option<u16>,
    #[arg(long)]
    pub grace_seconds: Option<u64>,
}

#[derive(Subcommand)]
pub enum BindingCommands {
    /// List bindings (of one secret, or all).
    Ls {
        #[arg(long)]
        secret: Option<String>,
    },
    /// Propose that a secret be sent for a skill to a server.
    Propose(ProposeArgs),
    /// Approve a pending binding (signed; not by the person who proposed it).
    Approve(DecideArgs),
    /// Reject a pending binding or revoke an approved one (signed).
    Revoke(DecideArgs),
    /// Withdraw a binding that is still pending.
    Withdraw(NameArgs),
}

#[derive(Args)]
pub struct ProposeArgs {
    pub secret: String,
    /// The skill (`-` for a tool registered by hand).
    #[arg(long)]
    pub skill: String,
    /// The host it may be sent to (empty: any host the secret allows).
    #[arg(long, default_value = "")]
    pub server: String,
    /// `bearer` (default) or `api-key:<Header-Name>`.
    #[arg(long, default_value = "bearer")]
    pub scheme: String,
}

#[derive(Args)]
pub struct DecideArgs {
    /// The binding id.
    pub id: String,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct MigrateArgs {
    /// The provider the secrets move from (`dbvault` is the built-in vault).
    #[arg(long)]
    pub from: String,
    /// The provider they move to (it must be writable).
    #[arg(long)]
    pub to: String,
    /// Only list what would move.
    #[arg(long)]
    pub dry_run: bool,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Subcommand)]
pub enum ProviderCommands {
    /// List the built-in provider and the configured ones.
    Ls,
    /// Add a provider of an enabled driver kind (env, file, hashicorp). Settings can only narrow the platform's.
    Add(ProviderAddArgs),
    /// Make a provider the default for new secrets (`dbvault` to go back to the built-in one).
    Default {
        name: String,
        #[arg(long)]
        platform: bool,
    },
    /// Remove a provider no secret lives in.
    Rm {
        name: String,
        #[arg(long)]
        platform: bool,
    },
}

#[derive(Args)]
pub struct ProviderAddArgs {
    pub name: String,
    #[arg(long)]
    pub kind: String,
    /// Settings as JSON, for example '{"sub_path":"team-a"}' (never a secret).
    #[arg(long, default_value = "{}")]
    pub config: String,
    #[arg(long)]
    pub default: bool,
    /// A provider shared by every kitchen (needs the platform:settings permission).
    #[arg(long)]
    pub platform: bool,
}

/// The value: standard input, else the environment, else a prompt (never a flag).
fn read_value(stdin: bool, what: &str) -> anyhow::Result<String> {
    if stdin {
        let mut s = String::new();
        std::io::stdin().read_to_string(&mut s)?;
        let s = s.trim_end_matches(['\n', '\r']).to_string();
        anyhow::ensure!(!s.is_empty(), "nothing on standard input");
        return Ok(s);
    }
    if let Ok(v) = std::env::var(VALUE_ENV) {
        if !v.is_empty() {
            return Ok(v);
        }
    }
    use std::io::IsTerminal;
    if !std::io::stdin().is_terminal() {
        anyhow::bail!("{what} is read from {VALUE_ENV}, --stdin, or a prompt in a terminal (a value is never a flag)");
    }
    Ok(dialoguer::Password::new()
        .with_prompt(format!("  {what}"))
        .interact()?)
}

fn s(v: &Value, k: &str) -> String {
    v[k].as_str().unwrap_or("").to_string()
}

fn print_secret(v: &Value) {
    let hosts = v["allowed_hosts"]
        .as_array()
        .map(|a| {
            a.iter()
                .filter_map(|h| h.as_str())
                .collect::<Vec<_>>()
                .join(", ")
        })
        .unwrap_or_default();
    println!(
        "  {}  {}",
        s(v, "name").bold(),
        if v["linked"] == true {
            "(linked)".dimmed().to_string()
        } else {
            String::new()
        }
    );
    println!("    provider   {}", s(v, "provider"));
    if !s(v, "ref").is_empty() && s(v, "ref") != s(v, "name") {
        println!("    ref        {}", s(v, "ref"));
    }
    println!("    version    {}", s(v, "current_version"));
    if !hosts.is_empty() {
        println!("    hosts      {hosts}");
    }
    if v["require_binding"] == true {
        println!("    bindings   required");
    }
    if let Some(e) = v["expires_at"].as_str() {
        println!(
            "    expires    {e}{}",
            if v["expired"] == true {
                "  EXPIRED".red().to_string()
            } else {
                String::new()
            }
        );
    }
    if let Some(p) = v["previous_valid_until"].as_str() {
        println!(
            "    previous   v{} valid until {p}",
            s(v, "previous_version")
        );
    }
    if v["managed"] == false {
        println!("    {}", "a credential from before secrets: no metadata yet (create it without a value to adopt it)".dimmed());
    }
}

pub async fn execute(cmd: SecretCommands, client: AgentOvenClient) -> anyhow::Result<()> {
    match run(cmd, &client).await {
        Ok(()) => Ok(()),
        Err(e) => {
            if let Some(h) = signing_hint(&e) {
                eprintln!("{h}");
            }
            Err(e)
        }
    }
}

async fn run(cmd: SecretCommands, client: &AgentOvenClient) -> anyhow::Result<()> {
    match cmd {
        SecretCommands::Ls => {
            let v = client.list_secrets().await?;
            let rows = v.as_array().cloned().unwrap_or_default();
            if rows.is_empty() {
                println!("  No secrets yet. `agentoven secret create <name>` adds one.");
            }
            for r in &rows {
                println!(
                    "  {:<28} {:<14} v{:<4} {}",
                    clip(&s(r, "name"), 28),
                    clip(&s(r, "provider"), 14),
                    s(r, "current_version"),
                    if r["managed"] == false {
                        "unmanaged"
                    } else if r["expired"] == true {
                        "EXPIRED"
                    } else {
                        ""
                    }
                );
            }
        }
        SecretCommands::Get(a) => print_secret(&client.get_secret(&a.name).await?),
        SecretCommands::Create(a) => {
            let mut body = json!({"name": a.name, "label": a.label.unwrap_or_default(), "owner": a.owner.unwrap_or_default(),
                "allowed_hosts": a.allow_host, "require_binding": a.require_binding});
            if let Some(p) = a.provider {
                body["provider"] = json!(p);
            }
            if let Some(e) = a.expires_at {
                body["expires_at"] = json!(e);
            }
            if let Some(g) = a.grace_seconds {
                body["grace_seconds"] = json!(g);
            }
            match a.reference {
                Some(r) => body["ref"] = json!(r),
                None => {
                    // A secret with no ref and no value would only adopt an existing credential: ask for a value
                    // unless the caller meant that (an environment value or --stdin says they gave one).
                    if a.stdin
                        || std::env::var(VALUE_ENV)
                            .map(|v| !v.is_empty())
                            .unwrap_or(false)
                    {
                        body["value"] = json!(read_value(a.stdin, "Secret value")?);
                    } else {
                        use std::io::IsTerminal;
                        if std::io::stdin().is_terminal() {
                            body["value"] = json!(read_value(false, "Secret value")?);
                        }
                    }
                }
            }
            let v = client.create_secret(&body).await?;
            println!("\n  {} Created.\n", "✓".green().bold());
            print_secret(&v);
        }
        SecretCommands::Update(a) => {
            let mut body = json!({});
            if let Some(x) = a.label {
                body["label"] = json!(x);
            }
            if let Some(x) = a.owner {
                body["owner"] = json!(x);
            }
            if a.clear_hosts {
                body["allowed_hosts"] = json!([]);
            } else if !a.allow_host.is_empty() {
                body["allowed_hosts"] = json!(a.allow_host);
            }
            if let Some(x) = a.expires_at {
                body["expires_at"] = json!(x);
            }
            if a.clear_expiry {
                body["clear_expiry"] = json!(true);
            }
            if let Some(x) = a.require_binding {
                body["require_binding"] = json!(x);
            }
            if let Some(x) = a.grace_seconds {
                body["grace_seconds"] = json!(x);
            }
            print_secret(&client.update_secret(&a.name, &body).await?);
        }
        SecretCommands::Rotate(a) => {
            let value = read_value(a.stdin, "New value")?;
            let probe = a.probe_url.map(|u| {
                let mut p = json!({"url": u});
                if let Some(x) = &a.probe_scheme {
                    p["header_scheme"] = json!(x);
                }
                if let Some(x) = a.probe_expect {
                    p["expect_status"] = json!(x);
                }
                p
            });
            let v = client
                .rotate_secret(&a.name, &value, probe.as_ref(), a.grace_seconds)
                .await?;
            println!(
                "\n  {} Rotated to v{}{}.",
                "✓".green().bold(),
                s(&v, "version"),
                if v["probed"] == true { " (probed)" } else { "" }
            );
            if let Some(until) = v["previous_valid_until"].as_str() {
                println!(
                    "    v{} stays valid until {until}\n",
                    s(&v, "previous_version")
                );
            }
        }
        SecretCommands::Refresh(a) => {
            let v = client.refresh_secret(&a.name).await?;
            println!(
                "  {}",
                if v["changed"] == true {
                    "The owner rotated it; the new version is recorded."
                } else {
                    "Unchanged."
                }
            );
        }
        SecretCommands::Versions(a) => {
            for v in client
                .secret_versions(&a.name)
                .await?
                .as_array()
                .cloned()
                .unwrap_or_default()
            {
                println!(
                    "  v{:<5} {:<10} {} bytes  {}",
                    s(&v, "Version"),
                    s(&v, "State"),
                    v["Length"],
                    s(&v, "CreatedBy")
                );
            }
        }
        SecretCommands::Rm(a) => {
            client.delete_secret(&a.name).await?;
            println!("  {} Deleted {}.", "✓".green().bold(), a.name);
        }
        SecretCommands::Binding(b) => binding(b, client).await?,
        SecretCommands::Migrate(a) => {
            let plan = client.plan_secret_migration(&a.from, &a.to).await?;
            let names: Vec<String> = plan["secrets"]
                .as_array()
                .cloned()
                .unwrap_or_default()
                .iter()
                .map(|x| s(x, "name"))
                .collect();
            println!(
                "\n  {} secret(s) would move from {} to {}: {}",
                names.len(),
                a.from.bold(),
                a.to.bold(),
                names.join(", ")
            );
            if let Some(c) = plan["conflicts"].as_array().filter(|c| !c.is_empty()) {
                anyhow::bail!(
                    "the target already holds {}: nothing is overwritten",
                    c.iter()
                        .filter_map(|x| x.as_str())
                        .collect::<Vec<_>>()
                        .join(", ")
                );
            }
            if a.dry_run || names.is_empty() {
                println!("  digest {}\n", s(&plan, "digest").dimmed());
                return Ok(());
            }
            let signing = a.signing.resolve()?;
            let out = client
                .migrate_secrets(&a.from, &a.to, signing.signature())
                .await?;
            println!(
                "  {} Moved {}. The source is untouched until you delete it.\n",
                "✓".green().bold(),
                out["moved"]
            );
        }
        SecretCommands::Provider(p) => provider(p, client).await?,
    }
    Ok(())
}

async fn binding(cmd: BindingCommands, client: &AgentOvenClient) -> anyhow::Result<()> {
    match cmd {
        BindingCommands::Ls { secret } => {
            for b in client
                .list_secret_bindings(secret.as_deref())
                .await?
                .as_array()
                .cloned()
                .unwrap_or_default()
            {
                println!(
                    "  {}  {:<10} {} -> {} ({})  proposed by {}",
                    clip(&s(&b, "id"), 8),
                    s(&b, "status"),
                    s(&b, "skill"),
                    if s(&b, "server").is_empty() {
                        "any allowed host".into()
                    } else {
                        s(&b, "server")
                    },
                    s(&b, "secret"),
                    s(&b, "requested_by")
                );
            }
        }
        BindingCommands::Propose(a) => {
            let b = client
                .propose_secret_binding(&a.secret, &a.skill, &a.server, &a.scheme)
                .await?;
            println!("  {} Proposed {}. A different person approves it with `agentoven secret binding approve {}`.", "✓".green().bold(), clip(&s(&b, "id"), 8), s(&b, "id"));
        }
        BindingCommands::Approve(a) => {
            let signing = a.signing.resolve()?;
            let b = client
                .decide_secret_binding(&a.id, "approved", signing.signature())
                .await?;
            println!(
                "  {} Approved: {} may send {}.",
                "✓".green().bold(),
                s(&b, "skill"),
                s(&b, "secret")
            );
        }
        BindingCommands::Revoke(a) => {
            let signing = a.signing.resolve()?;
            client
                .decide_secret_binding(&a.id, "rejected", signing.signature())
                .await?;
            println!("  {} Revoked.", "✓".green().bold());
        }
        BindingCommands::Withdraw(a) => {
            client.withdraw_secret_binding(&a.name).await?;
            println!("  {} Withdrawn.", "✓".green().bold());
        }
    }
    Ok(())
}

async fn provider(cmd: ProviderCommands, client: &AgentOvenClient) -> anyhow::Result<()> {
    match cmd {
        ProviderCommands::Ls => {
            for p in client
                .list_secret_providers()
                .await?
                .as_array()
                .cloned()
                .unwrap_or_default()
            {
                println!(
                    "  {:<16} {:<10} {:<9} {:<9} {}{}",
                    s(&p, "name").bold(),
                    s(&p, "kind"),
                    s(&p, "scope"),
                    if p["writable"] == true {
                        "writable"
                    } else {
                        "read-only"
                    },
                    if p["healthy"] == true {
                        "healthy".green().to_string()
                    } else {
                        format!("{} {}", "unhealthy".red(), s(&p, "error"))
                    },
                    if p["is_default"] == true {
                        "  (default)"
                    } else {
                        ""
                    }
                );
            }
        }
        ProviderCommands::Add(a) => {
            let cfg = json_arg("--config", &a.config)?;
            let v = client
                .create_secret_provider(&a.name, &a.kind, &cfg, a.default, a.platform)
                .await?;
            println!("  {} Added.\n{}", "✓".green().bold(), pretty(&v));
        }
        ProviderCommands::Default { name, platform } => {
            client.set_default_secret_provider(&name, platform).await?;
            println!(
                "  {} {} is the default for new secrets.",
                "✓".green().bold(),
                name
            );
        }
        ProviderCommands::Rm { name, platform } => {
            client.delete_secret_provider(&name, platform).await?;
            println!("  {} Removed {}.", "✓".green().bold(), name);
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};

    async fn client() -> (AgentOvenClient, agentoven_core::testing::Log) {
        let routes = Routes::from([
            (
                "GET /api/v1/secrets".to_string(),
                reply(
                    200,
                    json!([{"name": "a", "provider": "dbvault", "current_version": "1", "managed": true}]),
                ),
            ),
            (
                "POST /api/v1/secrets".to_string(),
                reply(
                    201,
                    json!({"name": "a", "provider": "dbvault", "current_version": "1"}),
                ),
            ),
            (
                "PUT /api/v1/secrets/a/value".to_string(),
                reply(200, json!({"version": "2", "probed": true})),
            ),
            (
                "POST /api/v1/signatures/challenge".to_string(),
                reply(
                    200,
                    json!({"subject": {"digest": "d"}, "reauth": "password"}),
                ),
            ),
            (
                "POST /api/v1/signatures/token".to_string(),
                reply(200, json!({"signing_token": "tok.sig"})),
            ),
            (
                "POST /api/v1/secret-bindings/b1/decision".to_string(),
                reply(
                    200,
                    json!({"status": "approved", "skill": "s", "secret": "a"}),
                ),
            ),
            (
                "POST /api/v1/secrets/migrate".to_string(),
                reply(
                    200,
                    json!({"moved": 1, "secrets": [{"name": "a"}], "conflicts": [], "digest": "dg"}),
                ),
            ),
        ]);
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    #[tokio::test]
    async fn rotating_sends_the_value_in_the_body_and_a_probe_when_asked() {
        let (c, log) = client().await;
        let probe = json!({"url": "https://api.example/ping"});
        let out = c
            .rotate_secret("a", "new-value", Some(&probe), Some(30))
            .await
            .unwrap();
        assert_eq!(out["version"], "2");
        let sent = log.last();
        assert_eq!(
            (sent.method.as_str(), sent.path.as_str()),
            ("PUT", "/api/v1/secrets/a/value")
        );
        assert_eq!(sent.body["value"], "new-value");
        assert_eq!(sent.body["probe"]["url"], "https://api.example/ping");
        assert_eq!(sent.body["grace_seconds"], 30);
    }

    #[tokio::test]
    async fn approving_a_binding_signs_it_at_the_secret_binding_gate_and_sends_the_token() {
        let (c, log) = client().await;
        let out = c
            .decide_secret_binding("b1", "approved", agentoven_core::Signature::Password("pw"))
            .await
            .unwrap();
        assert_eq!(out["status"], "approved");
        let all = log.all();
        assert_eq!(all[0].path, "/api/v1/signatures/challenge");
        assert_eq!(all[0].body["gate"], "secret_binding");
        assert_eq!(all[0].body["record_id"], "b1");
        assert_eq!(all[0].body["meaning"], "approved");
        assert_eq!(log.last().headers["x-signing-token"], "tok.sig");
    }

    #[tokio::test]
    async fn revoking_signs_the_rejected_meaning() {
        let (c, log) = client().await;
        c.decide_secret_binding("b1", "rejected", agentoven_core::Signature::Password("pw"))
            .await
            .unwrap();
        assert_eq!(log.all()[0].body["meaning"], "rejected");
    }

    #[tokio::test]
    async fn a_migration_is_signed_on_the_from_to_record() {
        let (c, log) = client().await;
        c.migrate_secrets(
            "dbvault",
            "vault-a",
            agentoven_core::Signature::Password("pw"),
        )
        .await
        .unwrap();
        let first = &log.all()[0];
        assert_eq!(first.body["gate"], "secret_migrate");
        assert_eq!(first.body["record_id"], "dbvault:vault-a");
        assert_eq!(log.last().body, json!({"from": "dbvault", "to": "vault-a"}));
    }

    #[tokio::test]
    async fn a_dry_run_asks_for_no_signature() {
        let (c, log) = client().await;
        c.plan_secret_migration("dbvault", "vault-a").await.unwrap();
        assert_eq!(log.calls(), vec!["POST /api/v1/secrets/migrate"]);
        assert_eq!(log.last().body["dry_run"], true);
    }

    #[test]
    fn a_value_is_never_a_flag() {
        use clap::CommandFactory;
        #[derive(clap::Parser)]
        struct Wrap {
            #[command(subcommand)]
            c: SecretCommands,
        }
        let cmd = Wrap::command();
        cmd.clone().debug_assert();
        let help = format!("{}", cmd.clone().render_long_help());
        assert!(
            !help.contains("--value"),
            "no command may take a value as a flag:\n{help}"
        );
    }
}

//! `agentoven signature` — signed decisions (Pro, ADR-0041) — and the helpers every command that
//! takes a decision shares.
//!
//! A decision at a gate is signed: you prove who you are again and the control plane gives you a
//! single-use token bound to exactly that decision. This command line proves it with the password of
//! the local account you signed in with. The password is read from `AGENTOVEN_SIGNING_PASSWORD` or
//! asked for at a prompt; it is never a flag, so it never lands in a shell history or a process list.
//! An account that signs in through an identity provider (OIDC) re-authenticates in a browser, which a
//! command line cannot do: it is told so, and signs in the dashboard.

use agentoven_core::{AgentOvenClient, OidcSigningRequired, Signature};
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::Value;

/// Where a password for signing is read from before the prompt.
pub const PASSWORD_ENV: &str = "AGENTOVEN_SIGNING_PASSWORD";
/// Where a signing token you already hold is read from (also `--signing-token`).
pub const TOKEN_ENV: &str = "AGENTOVEN_SIGNING_TOKEN";

/// How a command that takes a decision is signed. Flatten it into the command's args.
#[derive(Args, Clone, Debug, Default)]
pub struct SignArgs {
    /// Sign this decision (Pro signs every human decision). The password of your local account is read
    /// from AGENTOVEN_SIGNING_PASSWORD, or asked for; it is never a flag. Accounts that sign in
    /// through an identity provider (OIDC) sign in the dashboard.
    #[arg(long)]
    pub sign: bool,
    /// A single-use signing token you already hold, signed for exactly this decision
    /// (`agentoven signature sign` prints one).
    #[arg(long, env = "AGENTOVEN_SIGNING_TOKEN", hide_env_values = true)]
    pub signing_token: Option<String>,
}

/// What [`SignArgs`] resolved to; borrow a [`Signature`] from it.
pub enum Signing {
    None,
    Password(String),
    Token(String),
}

impl Signing {
    pub fn signature(&self) -> Signature<'_> {
        match self {
            Signing::None => Signature::None,
            Signing::Password(p) => Signature::Password(p),
            Signing::Token(t) => Signature::Token(t),
        }
    }
}

impl SignArgs {
    /// Resolve the flags: read the password (environment, then prompt) when `--sign` is given.
    pub fn resolve(&self) -> anyhow::Result<Signing> {
        self.resolve_with(std::env::var(PASSWORD_ENV).ok(), prompt_password)
    }

    /// `resolve` with the environment value and the prompt given, so both can be tested.
    pub fn resolve_with(
        &self,
        env_password: Option<String>,
        prompt: impl FnOnce() -> anyhow::Result<String>,
    ) -> anyhow::Result<Signing> {
        let token = self.signing_token.clone().filter(|t| !t.is_empty());
        match (self.sign, token) {
            (true, Some(_)) => {
                anyhow::bail!("pass --sign or --signing-token (or {TOKEN_ENV}), not both")
            }
            (true, None) => Ok(Signing::Password(password_from(env_password, prompt)?)),
            (false, Some(t)) => Ok(Signing::Token(t)),
            (false, None) => Ok(Signing::None),
        }
    }
}

/// The password: the environment variable when it is set and not empty, else the prompt.
pub fn password_from(
    env_password: Option<String>,
    prompt: impl FnOnce() -> anyhow::Result<String>,
) -> anyhow::Result<String> {
    match env_password.filter(|p| !p.is_empty()) {
        Some(p) => Ok(p),
        None => {
            let p = prompt()?;
            if p.is_empty() {
                anyhow::bail!("no password given");
            }
            Ok(p)
        }
    }
}

fn prompt_password() -> anyhow::Result<String> {
    use std::io::IsTerminal;
    if !std::io::stdin().is_terminal() {
        anyhow::bail!(
            "signing needs your password: set {PASSWORD_ENV} or run this in a terminal (a password is never a flag)"
        );
    }
    Ok(dialoguer::Password::new()
        .with_prompt("  Password (to sign this decision)")
        .interact()?)
}

/// The message for a signing failure the user can act on, or `None` for any other error.
pub fn signing_hint(e: &anyhow::Error) -> Option<String> {
    if e.downcast_ref::<OidcSigningRequired>().is_some() {
        return Some(format!(
            "{} This account signs through your identity provider (OIDC), which re-asks for your \
password in a browser; a command line cannot do that. Sign this decision in the dashboard, or sign in \
with a local account and use --sign.",
            "✗".red().bold()
        ));
    }
    None
}

/// Parse a JSON argument with a message that names the flag.
pub fn json_arg(flag: &str, text: &str) -> anyhow::Result<Value> {
    serde_json::from_str(text).map_err(|e| anyhow::anyhow!("{flag} is not valid JSON: {e}"))
}

pub fn pretty(v: &Value) -> String {
    serde_json::to_string_pretty(v).unwrap_or_default()
}

/// At most `n` characters of `s`, with an ellipsis when cut.
pub fn clip(s: &str, n: usize) -> String {
    if s.chars().count() <= n {
        s.to_string()
    } else {
        format!("{}…", s.chars().take(n).collect::<String>())
    }
}

#[derive(Subcommand)]
pub enum SignatureCommands {
    /// List the kitchen's signed decisions: who signed, when, the meaning, what became of the decision.
    Ls(LsArgs),
    /// Show one signed decision.
    Get(GetArgs),
    /// Check the kitchen's signature chain.
    Verify,
    /// Sign a decision and print its single-use token (valid for about a minute) for `--signing-token`.
    Sign(SignCmdArgs),
    /// Who may approve what in this kitchen.
    #[command(subcommand)]
    Policy(PolicyCommands),
}

#[derive(Args)]
pub struct LsArgs {
    /// Only this gate (tool_approval, recipe_gate, dataset_purge, legal_hold_release, ...).
    #[arg(long)]
    pub gate: Option<String>,
    /// Only this record.
    #[arg(long)]
    pub record: Option<String>,
}

#[derive(Args)]
pub struct GetArgs {
    /// Signature ID.
    pub id: String,
}

#[derive(Args)]
pub struct SignCmdArgs {
    /// The gate: tool_approval, budget_extension, resume_guidance, recipe_gate, wait_release,
    /// environment_promotion, skill_approval, bot_action, live_tool_replay, dataset_publish, dataset_purge,
    /// legal_hold_release (record `dataset:<id>` or `audit:<kitchen>`, payload {"reason": ...}),
    /// approval_skip (record `<recipe>@<environment>`, payload {"input": {...}}).
    #[arg(long)]
    pub gate: String,
    /// What is decided.
    #[arg(long)]
    pub record: String,
    /// approved or rejected.
    #[arg(long, default_value = "approved")]
    pub meaning: String,
    /// The content of the decision that is signed with it, as JSON.
    #[arg(long)]
    pub payload: Option<String>,
}

#[derive(Subcommand)]
pub enum PolicyCommands {
    /// Show the approver rules and the gates a rule can name.
    Get,
    /// Replace the approver rules (a Head Chef's): the whole policy, as JSON, from --json or --file.
    Set(PolicySetArgs),
}

#[derive(Args)]
pub struct PolicySetArgs {
    /// The policy as JSON: {"<gate>" or "<gate>:<scope>": {"roles": [...], "users": [...]}}.
    #[arg(long)]
    pub json: Option<String>,
    /// Read the policy from a JSON file.
    #[arg(long)]
    pub file: Option<String>,
}

/// The client the new Pro commands use: the config file and environment, then the global `--url`,
/// `--api-key` and `--kitchen` flags on top.
pub fn client_with(
    url: Option<String>,
    api_key: Option<String>,
    kitchen: Option<String>,
) -> anyhow::Result<AgentOvenClient> {
    let mut cfg = agentoven_core::AgentOvenConfig::load();
    if let Some(u) = url {
        cfg.url = u;
    }
    if let Some(k) = api_key {
        cfg.api_key = Some(k);
        cfg.token = None;
    }
    if let Some(k) = kitchen {
        cfg.kitchen = Some(k);
    }
    AgentOvenClient::from_config(&cfg)
}

pub async fn execute(cmd: SignatureCommands, client: AgentOvenClient) -> anyhow::Result<()> {
    let client = &client;
    match run(cmd, client, None).await {
        Ok(out) => {
            print!("{out}");
            Ok(())
        }
        Err(e) => {
            if let Some(hint) = signing_hint(&e) {
                eprintln!("{hint}");
            }
            Err(e)
        }
    }
}

/// Run a command and return what it prints. `password` is the signing password when it is already known
/// (tests); otherwise it is read from the environment or asked for.
pub async fn run(
    cmd: SignatureCommands,
    client: &AgentOvenClient,
    password: Option<String>,
) -> anyhow::Result<String> {
    match cmd {
        SignatureCommands::Ls(a) => {
            let sigs = client
                .list_signatures(
                    a.gate.as_deref().unwrap_or(""),
                    a.record.as_deref().unwrap_or(""),
                )
                .await?;
            let mut out = String::from("\n  ✍️  Signed decisions\n\n");
            if sigs.is_empty() {
                out.push_str("  (none)\n");
            } else {
                out.push_str(&format!(
                    "  {:<22} {:<28} {:<10} {:<22} {:<12} {}\n",
                    "GATE".bold(),
                    "RECORD".bold(),
                    "MEANING".bold(),
                    "SIGNER".bold(),
                    "ROLE".bold(),
                    "OUTCOME".bold()
                ));
                for s in &sigs {
                    out.push_str(&format!(
                        "  {:<22} {:<28} {:<10} {:<22} {:<12} {}\n",
                        s["gate"].as_str().unwrap_or("-"),
                        clip(s["record_id"].as_str().unwrap_or("-"), 27),
                        s["meaning"].as_str().unwrap_or("-"),
                        clip(s["printed_name"].as_str().unwrap_or("-"), 21),
                        s["signer_role"].as_str().unwrap_or("-"),
                        s["outcome"].as_str().unwrap_or("(pending)"),
                    ));
                }
                out.push_str(&format!(
                    "\n  {} {} signature(s)\n",
                    "→".dimmed(),
                    sigs.len()
                ));
            }
            Ok(out)
        }
        SignatureCommands::Get(a) => {
            Ok(format!("{}\n", pretty(&client.get_signature(&a.id).await?)))
        }
        SignatureCommands::Verify => {
            let r = client.verify_signatures().await?;
            let ok = r["ok"].as_bool().unwrap_or(false);
            if ok {
                Ok(format!(
                    "  {} signature chain intact ({} signatures)\n",
                    "✓".green().bold(),
                    r["count"]
                ))
            } else {
                anyhow::bail!(
                    "the signature chain is broken at {}: {}",
                    r["broken_at"],
                    r["reason"].as_str().unwrap_or("(no reason given)")
                )
            }
        }
        SignatureCommands::Sign(a) => {
            let payload = a
                .payload
                .as_deref()
                .map(|p| json_arg("--payload", p))
                .transpose()?;
            let pw = SignArgs {
                sign: true,
                signing_token: None,
            }
            .resolve_with(
                password.or_else(|| std::env::var(PASSWORD_ENV).ok()),
                prompt_password,
            )?;
            let Signing::Password(pw) = pw else {
                unreachable!("--sign resolves to a password")
            };
            let token = client
                .sign(&a.gate, &a.record, &a.meaning, payload.as_ref(), &pw)
                .await?;
            // The token alone on stdout, so `$(agentoven signature sign ...)` works.
            Ok(format!("{token}\n"))
        }
        SignatureCommands::Policy(PolicyCommands::Get) => {
            let p = client.approver_policy().await?;
            Ok(format!("{}\n", pretty(&p)))
        }
        SignatureCommands::Policy(PolicyCommands::Set(a)) => {
            let text = match (&a.json, &a.file) {
                (Some(j), None) => j.clone(),
                (None, Some(f)) => std::fs::read_to_string(f)?,
                _ => anyhow::bail!("pass the policy with --json or --file"),
            };
            let policy = json_arg("the policy", &text)?;
            // Accept either the bare rules or what `policy get` printed.
            let rules = if policy.get("policy").is_some() {
                policy["policy"].clone()
            } else {
                policy
            };
            client.set_approver_policy(&rules).await?;
            Ok(format!(
                "  {} approver policy replaced ({} rule(s))\n",
                "✓".green().bold(),
                rules.as_object().map_or(0, |o| o.len())
            ))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commands::Cli;
    use agentoven_core::testing::{fake_control_plane, json as reply, Routes};
    use clap::Parser;
    use serde_json::json;

    fn parse(args: &[&str]) -> Result<Cli, clap::Error> {
        let mut v = vec!["agentoven"];
        v.extend_from_slice(args);
        Cli::try_parse_from(v)
    }

    fn sign_args(sign: bool, token: Option<&str>) -> SignArgs {
        SignArgs {
            sign,
            signing_token: token.map(String::from),
        }
    }

    #[test]
    fn the_environment_password_is_used_before_the_prompt() {
        let pw = password_from(Some("from-env".into()), || panic!("must not prompt")).unwrap();
        assert_eq!(pw, "from-env");
        // an empty variable is no variable
        let pw = password_from(Some(String::new()), || Ok("typed".into())).unwrap();
        assert_eq!(pw, "typed");
        assert_eq!(password_from(None, || Ok("typed".into())).unwrap(), "typed");
        assert!(password_from(None, || Ok(String::new())).is_err());
        assert!(password_from(None, || anyhow::bail!("no terminal")).is_err());
    }

    #[test]
    fn sign_resolves_to_a_password_a_token_or_nothing_and_never_both() {
        assert!(matches!(
            sign_args(false, None)
                .resolve_with(None, || panic!())
                .unwrap(),
            Signing::None
        ));
        assert!(matches!(
            sign_args(false, Some("held")).resolve_with(None, || panic!()).unwrap(),
            Signing::Token(t) if t == "held"
        ));
        assert!(matches!(
            sign_args(true, None).resolve_with(Some("pw".into()), || panic!()).unwrap(),
            Signing::Password(p) if p == "pw"
        ));
        assert!(sign_args(true, Some("held"))
            .resolve_with(Some("pw".into()), || panic!())
            .is_err());
        // an empty token (an empty environment variable) is no token
        assert!(matches!(
            sign_args(false, Some(""))
                .resolve_with(None, || panic!())
                .unwrap(),
            Signing::None
        ));
    }

    #[test]
    fn a_password_is_never_a_flag_on_any_command_that_signs() {
        for cmd in [
            vec![
                "dataset",
                "purge",
                "ds1",
                "--reason",
                "x",
                "--password",
                "pw",
            ],
            vec!["dataset", "publish", "ds1", "--password", "pw"],
            vec!["legal-hold", "lift", "--reason", "x", "--password", "pw"],
            vec![
                "trace",
                "replay",
                "run",
                "t1",
                "--from-span",
                "s",
                "--password",
                "pw",
            ],
            vec![
                "signature",
                "sign",
                "--gate",
                "recipe_gate",
                "--record",
                "r",
                "--password",
                "pw",
            ],
        ] {
            assert!(
                parse(&cmd).is_err(),
                "{cmd:?} must not take a password flag"
            );
        }
        // and --sign is a switch, not a value
        assert!(parse(&["dataset", "publish", "ds1", "--sign"]).is_ok());
        assert!(parse(&["dataset", "publish", "ds1", "--sign", "pw"]).is_err());
    }

    #[test]
    fn an_oidc_account_gets_a_clear_message_and_other_errors_get_none() {
        colored::control::set_override(false);
        let hint = signing_hint(&OidcSigningRequired.into()).expect("a hint");
        assert!(
            hint.contains("OIDC") && hint.contains("dashboard") && hint.contains("local account"),
            "{hint}"
        );
        assert!(signing_hint(&anyhow::anyhow!("boom")).is_none());
    }

    fn signing_routes() -> Routes {
        Routes::from([
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
        ])
    }

    async fn client(routes: Routes) -> (AgentOvenClient, agentoven_core::testing::Log) {
        let (base, log) = fake_control_plane(routes).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    #[tokio::test]
    async fn signature_sign_prints_only_the_token() {
        let (c, log) = client(signing_routes()).await;
        let cmd = SignatureCommands::Sign(SignCmdArgs {
            gate: "legal_hold_release".into(),
            record: "dataset:ds1".into(),
            meaning: "approved".into(),
            payload: Some(r#"{"reason":"closed"}"#.into()),
        });
        let out = run(cmd, &c, Some("pw".into())).await.unwrap();
        assert_eq!(out, "tok.sig\n");
        assert_eq!(log.all()[0].body["payload"], json!({"reason": "closed"}));
        assert_eq!(log.all()[1].body["password"], "pw");
    }

    #[tokio::test]
    async fn an_oidc_account_is_refused_before_the_password_is_sent() {
        let routes = Routes::from([(
            "POST /api/v1/signatures/challenge".to_string(),
            reply(200, json!({"subject": {"digest": "d"}, "reauth": "oidc"})),
        )]);
        let (c, log) = client(routes).await;
        let cmd = SignatureCommands::Sign(SignCmdArgs {
            gate: "recipe_gate".into(),
            record: "r:s".into(),
            meaning: "approved".into(),
            payload: None,
        });
        let e = run(cmd, &c, Some("pw".into())).await.unwrap_err();
        assert!(signing_hint(&e).is_some(), "{e}");
        assert_eq!(log.calls(), vec!["POST /api/v1/signatures/challenge"]);
    }

    #[tokio::test]
    async fn listing_getting_and_verifying() {
        colored::control::set_override(false);
        let routes = Routes::from([
            (
                "GET /api/v1/signatures?gate=dataset_purge".to_string(),
                reply(
                    200,
                    json!([{
                "gate": "dataset_purge", "record_id": "ds1", "meaning": "approved", "printed_name": "Ana Silva",
                "signer_role": "head_chef", "outcome": "applied"}]),
                ),
            ),
            (
                "GET /api/v1/signatures/s1".to_string(),
                reply(200, json!({"id": "s1"})),
            ),
            (
                "GET /api/v1/signatures/verify".to_string(),
                reply(200, json!({"ok": true, "count": 4})),
            ),
        ]);
        let (c, _) = client(routes).await;
        let out = run(
            SignatureCommands::Ls(LsArgs {
                gate: Some("dataset_purge".into()),
                record: None,
            }),
            &c,
            None,
        )
        .await
        .unwrap();
        assert!(
            out.contains("Ana Silva") && out.contains("head_chef") && out.contains("applied"),
            "{out}"
        );
        assert!(run(
            SignatureCommands::Get(GetArgs { id: "s1".into() }),
            &c,
            None
        )
        .await
        .unwrap()
        .contains("\"s1\""));
        assert!(run(SignatureCommands::Verify, &c, None)
            .await
            .unwrap()
            .contains("intact"));

        let routes = Routes::from([(
            "GET /api/v1/signatures/verify".to_string(),
            reply(200, json!({"ok": false, "broken_at": 3, "reason": "hash"})),
        )]);
        let (c, _) = client(routes).await;
        let e = run(SignatureCommands::Verify, &c, None)
            .await
            .unwrap_err()
            .to_string();
        assert!(e.contains("broken at 3"), "{e}");
    }

    #[tokio::test]
    async fn the_policy_is_read_and_replaced_and_admin_is_refused() {
        let routes = Routes::from([
            (
                "GET /api/v1/approver-policy".to_string(),
                reply(200, json!({"policy": {}, "gates": ["tool_approval"]})),
            ),
            (
                "PUT /api/v1/approver-policy".to_string(),
                reply(200, json!({"policy": {}})),
            ),
        ]);
        let (c, log) = client(routes).await;
        assert!(
            run(SignatureCommands::Policy(PolicyCommands::Get), &c, None)
                .await
                .unwrap()
                .contains("tool_approval")
        );
        // either the bare rules or what `policy get` printed
        let set = |j: &str| {
            SignatureCommands::Policy(PolicyCommands::Set(PolicySetArgs {
                json: Some(j.into()),
                file: None,
            }))
        };
        run(
            set(r#"{"policy": {"legal_hold_release": {"roles": ["legal"]}}}"#),
            &c,
            None,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"policy": {"legal_hold_release": {"roles": ["legal"], "users": []}}})
        );
        run(set(r#"{"environment_promotion:prod": {"roles": ["head_chef"], "users": ["ana@acme.io"]}}"#), &c, None).await.unwrap();
        assert_eq!(
            log.last().body["policy"]["environment_promotion:prod"]["users"],
            json!(["ana@acme.io"])
        );
        let n = log.len();
        assert!(
            run(set(r#"{"tool_approval": {"roles": ["admin"]}}"#), &c, None)
                .await
                .is_err()
        );
        assert!(run(
            SignatureCommands::Policy(PolicyCommands::Set(PolicySetArgs {
                json: None,
                file: None
            })),
            &c,
            None
        )
        .await
        .is_err());
        assert_eq!(log.len(), n);
    }
}

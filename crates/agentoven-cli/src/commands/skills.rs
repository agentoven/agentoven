//! `agentoven skills` — install, review and manage Agent Skills.
//!
//! A skill is an Agent Skills (<https://agentskills.io>) bundle: a `SKILL.md`
//! plus optional files, optionally declaring bundled MCP servers. This module
//! is a thin client over the control plane's skills API:
//!
//! * OSS (`/api/v1/skills`): `register`, list, get, delete, `refresh`,
//!   `approve`, `reject`. Registration fetches the bundle and has a model
//!   provider review it before it can be used.
//! * Pro (`/api/v1/skills/pro`): `stage` (persist the bundle, no model call)
//!   and `analyze` (the one call that sends the bundle to a model provider).
//!
//! Source kinds are exactly the ones the server understands: inline content
//! (the CLI reads a local directory and sends it), a git URL (the server
//! clones it), or a directory on the *server's* filesystem. The server has no
//! `@owner/slug` registry resolution, so that form is refused with an
//! explanation rather than faked.
//!
//! Exit codes for the verdict-producing commands (`install`, `refresh`,
//! `analyze`): 0 accepted, 2 needs_review, 3 rejected; any other failure is 1.

use std::collections::BTreeMap;
use std::io::IsTerminal;
use std::path::{Component, Path, PathBuf};
use std::time::Duration;

use anyhow::{anyhow, bail, Context};
use base64::Engine as _;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Value};

use super::OutputFormat;

/// Exit code: skill needs a human review before it can be used.
pub const EXIT_NEEDS_REVIEW: i32 = 2;
/// Exit code: skill was rejected by the verification step.
pub const EXIT_REJECTED: i32 = 3;

// Bundle limits mirrored from the server (control-plane/pkg/skills/bundle.go)
// so an oversized directory fails locally with a clear message.
const MAX_SKILL_FILES: usize = 256;
const MAX_SKILL_FILE_SIZE: u64 = 1 << 20;
const MAX_SKILL_BUNDLE_BYTES: u64 = 8 << 20;
const MANIFEST_FILENAME: &str = "SKILL.md";

const SOURCE_HELP: &str = "\
Source kinds the server supports (and so the CLI):
  <git URL>       https://, http://, ssh://, git://, file:// or git@host:path.
                  The server clones it (shallow); use --ref for a branch/tag.
  <directory>     A local directory containing SKILL.md at its root. The CLI
                  reads the files and sends them inline, so this works against
                  remote servers too. Symlinks and .git are skipped. Limits:
                  256 files, 1 MiB per file, 8 MiB total. Inline skills can't
                  be refreshed later.
  --server-path   Treat <source> as a directory on the CONTROL PLANE's own
                  filesystem (nothing is uploaded). Can be refreshed later.

NOT supported: `@owner/slug` references. The control plane has no skills
registry or marketplace to resolve them against; the CLI refuses them rather
than guessing. Install from a git URL or a directory instead.";

#[derive(Subcommand)]
pub enum SkillsCommands {
    /// List skills registered in the kitchen.
    List,
    /// Show a skill, including its verification verdict and reasoning.
    Get(GetArgs),
    /// Register a skill (fetch, review with a model provider, activate).
    #[command(
        long_about = "Register a skill into the kitchen (POST /api/v1/skills/register).\n\n\
The control plane fetches the bundle and asks a model provider to review it \
before it can be used by agents. NOTE: this sends the skill's content to \
whichever provider the router picks. Use `skills stage` + `skills analyze \
--provider` (AgentOven Pro) to choose the provider explicitly.\n\n\
Prints the verification verdict and reasoning. Exit codes: 0 accepted, \
2 needs_review (an admin must run `skills approve` or `skills reject`), \
3 rejected, 1 any other failure.",
        after_help = SOURCE_HELP
    )]
    Install(InstallArgs),
    /// Re-fetch a git/server-path skill and re-run verification.
    Refresh(RefreshArgs),
    /// Remove a skill and the MCP tools it registered.
    Remove(RemoveArgs),
    /// Stage a skill without reviewing it (requires AgentOven Pro).
    #[command(
        long_about = "Fetch and durably store a skill without sending it to any model \
(POST /api/v1/skills/pro/stage). The skill stays `pending` and unusable until \
`skills analyze` runs. Requires AgentOven Pro; on a server without the Pro \
routes this prints a clear error and exits 1.",
        after_help = SOURCE_HELP
    )]
    Stage(StageArgs),
    /// Review a staged skill with a model provider (requires AgentOven Pro).
    #[command(
        long_about = "Review a staged skill (POST /api/v1/skills/pro/{name}/analyze).\n\n\
WARNING: this is the step that SENDS THE SKILL BUNDLE (SKILL.md and bundled \
files) to a model provider for review. Name the provider with --provider to \
pin the analysis to exactly that one, with no fallback to another. Without \
--provider the control plane's router picks the kitchen's default provider. \
The CLI prints which provider will be used before it sends anything.\n\n\
Requires AgentOven Pro and the skill:analyze permission. Exit codes: 0 \
accepted, 2 needs_review, 3 rejected, 1 any other failure."
    )]
    Analyze(AnalyzeArgs),
    /// Approve a skill left at needs_review.
    Approve(ApproveArgs),
    /// Reject a skill left at needs_review.
    Reject(RejectArgs),
}

#[derive(Args, Clone)]
pub struct SourceArgs {
    /// Git URL or local skill directory (see below; `@owner/slug` is not supported).
    pub source: String,
    /// Git branch or tag to clone (git URLs only).
    #[arg(long = "ref")]
    pub git_ref: Option<String>,
    /// Treat <source> as a directory on the control plane's filesystem.
    #[arg(long)]
    pub server_path: bool,
}

#[derive(Args)]
pub struct InstallArgs {
    #[command(flatten)]
    pub source: SourceArgs,
    /// Map a bundled MCP server to a kitchen credential: SERVER=CREDENTIAL (repeatable).
    #[arg(long = "credential", value_name = "SERVER=CREDENTIAL")]
    pub credentials: Vec<String>,
}

#[derive(Args)]
pub struct StageArgs {
    #[command(flatten)]
    pub source: SourceArgs,
}

#[derive(Args)]
pub struct GetArgs {
    /// Skill name.
    pub name: String,
    /// Also print the skill's instructions (the SKILL.md body).
    #[arg(long)]
    pub instructions: bool,
}

#[derive(Args)]
pub struct RefreshArgs {
    /// Skill name.
    pub name: String,
}

#[derive(Args)]
pub struct RemoveArgs {
    /// Skill name.
    pub name: String,
    /// Skip the confirmation prompt.
    #[arg(long, short = 'y', alias = "yes")]
    pub force: bool,
}

#[derive(Args)]
pub struct AnalyzeArgs {
    /// Name of a staged (pending) skill.
    pub name: String,
    /// Model provider to send the bundle to; pins the analysis to exactly this
    /// provider. Omit to let the router pick the kitchen's default.
    #[arg(long)]
    pub provider: Option<String>,
    /// Map a bundled MCP server to a kitchen credential: SERVER=CREDENTIAL (repeatable).
    #[arg(long = "credential", value_name = "SERVER=CREDENTIAL")]
    pub credentials: Vec<String>,
}

#[derive(Args)]
pub struct ApproveArgs {
    /// Skill name.
    pub name: String,
    /// Reviewer note recorded with the decision.
    #[arg(long, alias = "reason")]
    pub note: Option<String>,
}

#[derive(Args)]
pub struct RejectArgs {
    /// Skill name.
    pub name: String,
    /// Reason recorded with the decision.
    #[arg(long, alias = "note")]
    pub reason: Option<String>,
}

// ── Entry point ──────────────────────────────────────────────

/// Global CLI options that affect how the control plane is reached.
pub struct Globals {
    pub url: Option<String>,
    pub api_key: Option<String>,
    pub kitchen: Option<String>,
    pub output: OutputFormat,
}

/// What a command produced: text for stdout and the process exit code.
#[derive(Debug)]
pub struct Report {
    pub stdout: String,
    pub code: i32,
}

pub async fn execute(cmd: SkillsCommands, globals: Globals) -> anyhow::Result<()> {
    let api = Api::from_globals(&globals);
    let json = matches!(globals.output, OutputFormat::Json);

    if let SkillsCommands::Remove(args) = &cmd {
        if !args.force {
            if !std::io::stdin().is_terminal() {
                bail!(
                    "refusing to remove skill '{}' without confirmation; pass --force when not running interactively",
                    args.name
                );
            }
            let confirm = dialoguer::Confirm::new()
                .with_prompt(format!(
                    "  Remove skill '{}' and its registered tools?",
                    args.name
                ))
                .default(false)
                .interact()?;
            if !confirm {
                println!("  {} Cancelled.", "→".dimmed());
                return Ok(());
            }
        }
    }

    // Disclosure before the one call that sends a bundle to a model provider.
    if let SkillsCommands::Analyze(args) = &cmd {
        eprintln!("{}", analyze_notice(args));
    }

    let report = run(cmd, &api, json).await?;
    print!("{}", report.stdout);
    if report.code != 0 {
        std::process::exit(report.code);
    }
    Ok(())
}

fn analyze_notice(args: &AnalyzeArgs) -> String {
    match &args.provider {
        Some(p) => format!(
            "  {} Sending skill '{}' (SKILL.md and bundled files) to model provider '{}' for review.",
            "→".dimmed(),
            args.name,
            p
        ),
        None => format!(
            "  {} Sending skill '{}' (SKILL.md and bundled files) for review to the kitchen's default model provider (chosen by the control plane's router; pass --provider to pin one).",
            "→".dimmed(),
            args.name
        ),
    }
}

/// Run a command against `api` and return what to print and the exit code.
/// Kept free of printing and `process::exit` so it can be tested end to end.
pub async fn run(cmd: SkillsCommands, api: &Api, json: bool) -> anyhow::Result<Report> {
    match cmd {
        SkillsCommands::List => list(api, json).await,
        SkillsCommands::Get(a) => get(api, &a, json).await,
        SkillsCommands::Install(a) => install(api, a, json).await,
        SkillsCommands::Refresh(a) => refresh(api, &a, json).await,
        SkillsCommands::Remove(a) => remove(api, &a, json).await,
        SkillsCommands::Stage(a) => stage(api, a, json).await,
        SkillsCommands::Analyze(a) => analyze(api, a, json).await,
        SkillsCommands::Approve(a) => {
            review(api, &a.name, "approve", a.note.as_deref(), json).await
        }
        SkillsCommands::Reject(a) => {
            review(api, &a.name, "reject", a.reason.as_deref(), json).await
        }
    }
}

// ── Commands ─────────────────────────────────────────────────

async fn list(api: &Api, json: bool) -> anyhow::Result<Report> {
    let reply = api
        .send(reqwest::Method::GET, "/api/v1/skills", None)
        .await?;
    if reply.status != 200 {
        return Err(api_error("list skills", &reply));
    }
    if json {
        return Ok(ok(pretty(&reply.body)));
    }
    let skills = reply.body["skills"].as_array().cloned().unwrap_or_default();
    Ok(ok(render_list(&skills)))
}

async fn get(api: &Api, args: &GetArgs, json: bool) -> anyhow::Result<Report> {
    let reply = api
        .send(reqwest::Method::GET, &skill_path(&args.name, ""), None)
        .await?;
    if reply.status != 200 {
        return Err(api_error(&format!("get skill '{}'", args.name), &reply));
    }
    if json {
        return Ok(ok(pretty(&reply.body)));
    }
    Ok(ok(render_detail(&reply.body, args.instructions)))
}

async fn install(api: &Api, args: InstallArgs, json: bool) -> anyhow::Result<Report> {
    let resolved = resolve_source(&args.source)?;
    let mut body = resolved.request_body();
    let creds = parse_credentials(&args.credentials)?;
    if !creds.is_empty() {
        body["credentials"] = json!(creds);
    }
    let reply = api
        .send(
            reqwest::Method::POST,
            "/api/v1/skills/register",
            Some(&body),
        )
        .await?;
    verdict_report("install", resolved.name_hint(), &reply, json)
}

async fn refresh(api: &Api, args: &RefreshArgs, json: bool) -> anyhow::Result<Report> {
    let reply = api
        .send(
            reqwest::Method::PATCH,
            &skill_path(&args.name, "/refresh"),
            None,
        )
        .await?;
    verdict_report("refresh", Some(args.name.clone()), &reply, json)
}

async fn remove(api: &Api, args: &RemoveArgs, json: bool) -> anyhow::Result<Report> {
    let reply = api
        .send(reqwest::Method::DELETE, &skill_path(&args.name, ""), None)
        .await?;
    match reply.status {
        200 | 202 | 204 => {
            if json {
                Ok(ok(pretty(&json!({"removed": args.name}))))
            } else {
                Ok(ok(format!(
                    "  {} Skill '{}' removed.\n",
                    "✓".green().bold(),
                    args.name
                )))
            }
        }
        _ => Err(api_error(&format!("remove skill '{}'", args.name), &reply)),
    }
}

async fn stage(api: &Api, args: StageArgs, json: bool) -> anyhow::Result<Report> {
    let resolved = resolve_source(&args.source)?;
    let body = resolved.request_body();
    let reply = api
        .send(
            reqwest::Method::POST,
            "/api/v1/skills/pro/stage",
            Some(&body),
        )
        .await?;
    if pro_routes_missing(&reply) {
        return Err(pro_required("skills stage"));
    }
    if reply.status != 201 && reply.status != 200 {
        return Err(api_error("stage skill", &reply));
    }
    if json {
        return Ok(ok(pretty(&reply.body)));
    }
    let skill = &reply.body["skill"];
    let name = skill["name"].as_str().unwrap_or("<name>");
    let mut out = format!(
        "  {} Skill '{}' staged (status: {}). Nothing has been sent to a model yet.\n",
        "✓".green().bold(),
        name.bold(),
        skill["status"].as_str().unwrap_or("pending")
    );
    if let Some(provs) = reply.body["available_providers"].as_array() {
        let names: Vec<&str> = provs.iter().filter_map(|p| p.as_str()).collect();
        if !names.is_empty() {
            out += &format!("  {:<14} {}\n", "Providers:".bold(), names.join(", "));
        }
    }
    out += &format!(
        "\n  {} Next: agentoven skills analyze {} [--provider <name>]\n",
        "→".dimmed(),
        name
    );
    out += "    (analyze sends the bundle to the chosen model provider for review)\n";
    Ok(ok(out))
}

async fn analyze(api: &Api, args: AnalyzeArgs, json: bool) -> anyhow::Result<Report> {
    let mut body = json!({});
    if let Some(p) = &args.provider {
        if p.trim().is_empty() {
            bail!("--provider must not be empty");
        }
        body["provider"] = json!(p);
    }
    let creds = parse_credentials(&args.credentials)?;
    if !creds.is_empty() {
        body["credentials"] = json!(creds);
    }
    let reply = api
        .send(
            reqwest::Method::POST,
            &format!("/api/v1/skills/pro/{}/analyze", encode_segment(&args.name)),
            Some(&body),
        )
        .await?;
    if pro_routes_missing(&reply) {
        return Err(pro_required("skills analyze"));
    }
    verdict_report("analyze", Some(args.name.clone()), &reply, json)
}

async fn review(
    api: &Api,
    name: &str,
    action: &str,
    note: Option<&str>,
    json: bool,
) -> anyhow::Result<Report> {
    let mut body = json!({});
    if let Some(n) = note {
        body["note"] = json!(n);
    }
    let reply = api
        .send(
            reqwest::Method::POST,
            &skill_path(name, &format!("/{action}")),
            Some(&body),
        )
        .await?;
    if reply.status != 200 {
        return Err(api_error(&format!("{action} skill '{name}'"), &reply));
    }
    if json {
        return Ok(ok(pretty(&reply.body)));
    }
    let (mark, verb) = if action == "approve" {
        ("✓".green().bold(), "approved and activated")
    } else {
        ("✓".green().bold(), "rejected")
    };
    let mut out = format!("  {} Skill '{}' {}.\n", mark, name.bold(), verb);
    if let Some(tools) = reply.body["registered_tools"].as_array() {
        let names: Vec<&str> = tools.iter().filter_map(|t| t.as_str()).collect();
        if !names.is_empty() {
            out += &format!("  {:<14} {}\n", "Tools:".bold(), names.join(", "));
        }
    }
    Ok(ok(out))
}

// ── Verdict handling ─────────────────────────────────────────

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Verdict {
    Accepted,
    NeedsReview,
    Rejected,
}

impl Verdict {
    fn exit_code(self) -> i32 {
        match self {
            Verdict::Accepted => 0,
            Verdict::NeedsReview => EXIT_NEEDS_REVIEW,
            Verdict::Rejected => EXIT_REJECTED,
        }
    }
}

/// Classify a register/refresh/analyze response. The body's `status` is
/// authoritative; the HTTP status (201/200 accepted, 202 needs_review,
/// 422 rejected) is the fallback. Anything else is not a verdict.
pub fn verdict_of(reply: &Reply) -> Option<Verdict> {
    match reply.body["status"].as_str() {
        Some("accepted") => return Some(Verdict::Accepted),
        Some("needs_review") => return Some(Verdict::NeedsReview),
        Some("rejected") => return Some(Verdict::Rejected),
        _ => {}
    }
    match reply.status {
        200 | 201 if reply.body.get("manifest").is_some() => Some(Verdict::Accepted),
        202 => Some(Verdict::NeedsReview),
        422 => Some(Verdict::Rejected),
        _ => None,
    }
}

fn verdict_report(
    action: &str,
    name_hint: Option<String>,
    reply: &Reply,
    json: bool,
) -> anyhow::Result<Report> {
    let Some(verdict) = verdict_of(reply) else {
        return Err(api_error(&format!("{action} skill"), reply));
    };
    let code = verdict.exit_code();
    if json {
        return Ok(Report {
            stdout: pretty(&reply.body),
            code,
        });
    }

    // Full skill responses carry the name; needs_review/rejected register
    // responses only carry status + reasoning.
    let name = reply.body["name"].as_str().map(String::from).or(name_hint);
    let label = match &name {
        Some(n) => format!("'{}'", n.bold()),
        None => "(name unknown — see `agentoven skills list`)".to_string(),
    };
    let reasoning = reply.body["verification_reasoning"]
        .as_str()
        .or_else(|| reply.body["reasoning"].as_str())
        .unwrap_or("");
    let provider = reply.body["verified_by_provider"].as_str();

    let mut out = String::new();
    match verdict {
        Verdict::Accepted => out += &format!(
            "  {} Skill {} {} — verdict: accept\n",
            "✓".green().bold(),
            label,
            past(action)
        ),
        Verdict::NeedsReview => out += &format!(
            "  {} Skill {} needs review — verdict: needs_review. It is inert (no tools registered, unusable by agents) until an admin decides.\n",
            "⚠".yellow().bold(),
            label
        ),
        Verdict::Rejected => out += &format!(
            "  {} Skill {} was rejected — verdict: reject. It cannot be used.\n",
            "✗".red().bold(),
            label
        ),
    }
    if let Some(p) = provider {
        out += &format!("  {:<14} {}\n", "Reviewed by:".bold(), p);
    }
    if let Some(tools) = reply.body["registered_tools"].as_array() {
        let names: Vec<&str> = tools.iter().filter_map(|t| t.as_str()).collect();
        if !names.is_empty() {
            out += &format!("  {:<14} {}\n", "Tools:".bold(), names.join(", "));
        }
    }
    if !reasoning.is_empty() {
        out += &format!(
            "\n  {}\n{}\n",
            "Reasoning:".bold(),
            indent(reasoning, "    ")
        );
    }
    if verdict == Verdict::NeedsReview {
        let n = name.as_deref().unwrap_or("<name>");
        if name.is_none() {
            out += &format!(
                "\n  {} Find its name:  agentoven skills list\n",
                "→".dimmed()
            );
        }
        out += &format!(
            "\n  {} Review it:  agentoven skills get {n} --instructions\n  {} Then:       agentoven skills approve {n}   |   agentoven skills reject {n} --reason \"...\"\n",
            "→".dimmed(),
            "→".dimmed()
        );
    }
    Ok(Report { stdout: out, code })
}

fn past(action: &str) -> &'static str {
    match action {
        "install" => "installed",
        "refresh" => "refreshed",
        "analyze" => "analyzed and activated",
        _ => "processed",
    }
}

// ── Source resolution ────────────────────────────────────────

/// A resolved install source, ready to become a request body.
#[derive(Debug, PartialEq)]
pub enum Source {
    Inline {
        skill_md: String,
        files: BTreeMap<String, String>,
        name: Option<String>,
    },
    ServerPath(String),
    Git {
        url: String,
        git_ref: Option<String>,
    },
}

impl Source {
    pub fn request_body(&self) -> Value {
        match self {
            Source::Inline {
                skill_md, files, ..
            } => {
                let mut body = json!({"source": "inline", "skill_md": skill_md});
                if !files.is_empty() {
                    body["files"] = json!(files);
                }
                body
            }
            Source::ServerPath(p) => json!({"source": "path", "path": p}),
            Source::Git { url, git_ref } => {
                let mut body = json!({"source": "git", "git_url": url});
                if let Some(r) = git_ref {
                    body["git_ref"] = json!(r);
                }
                body
            }
        }
    }

    fn name_hint(&self) -> Option<String> {
        match self {
            Source::Inline { name, .. } => name.clone(),
            _ => None,
        }
    }
}

fn looks_like_git_url(s: &str) -> bool {
    if ["https://", "http://", "ssh://", "git://", "file://"]
        .iter()
        .any(|p| s.starts_with(p))
    {
        return true;
    }
    // scp-like syntax: user@host:path (the ':' comes before any '/').
    if let Some(at) = s.find('@') {
        if let Some(colon) = s.find(':') {
            let slash = s.find('/').unwrap_or(usize::MAX);
            return at < colon && colon < slash && !s.starts_with('@');
        }
    }
    false
}

pub fn resolve_source(args: &SourceArgs) -> anyhow::Result<Source> {
    let raw = args.source.trim();
    if raw.is_empty() {
        bail!("a skill source is required (git URL or directory)");
    }

    if raw.starts_with('@') {
        bail!(
            "'{raw}' looks like an @owner/slug registry reference, which AgentOven does not support: \
the control plane has no skills registry or marketplace to resolve it against. \
Install from a git URL (agentoven skills install https://github.com/owner/repo) or a local directory instead."
        );
    }

    if args.server_path {
        if args.git_ref.is_some() {
            bail!("--ref only applies to git URLs, not --server-path");
        }
        return Ok(Source::ServerPath(raw.to_string()));
    }

    if looks_like_git_url(raw) {
        return Ok(Source::Git {
            url: raw.to_string(),
            git_ref: args.git_ref.clone().filter(|r| !r.is_empty()),
        });
    }

    let path = Path::new(raw);
    let dir: PathBuf = if path.is_dir() {
        path.to_path_buf()
    } else if path.is_file() && path.file_name().is_some_and(|n| n == MANIFEST_FILENAME) {
        path.parent()
            .map(|p| {
                if p.as_os_str().is_empty() {
                    Path::new(".")
                } else {
                    p
                }
            })
            .unwrap_or(Path::new("."))
            .to_path_buf()
    } else {
        bail!(
            "'{raw}' is neither a git URL nor an existing local directory. \
Pass a git URL, a directory containing {MANIFEST_FILENAME}, or --server-path to name a directory on the control plane's filesystem."
        );
    };
    if args.git_ref.is_some() {
        bail!("--ref only applies to git URLs, not local directories");
    }
    read_local_bundle(&dir)
}

/// Read a local skill directory into an inline bundle, applying the same
/// limits the server enforces. Symlinks and `.git` are skipped so a link can't
/// pull files from outside the directory into the upload.
pub fn read_local_bundle(root: &Path) -> anyhow::Result<Source> {
    let mut files: BTreeMap<String, Vec<u8>> = BTreeMap::new();
    let mut total: u64 = 0;
    collect_files(root, root, &mut files, &mut total)?;

    let skill_md_bytes = files
        .remove(MANIFEST_FILENAME)
        .ok_or_else(|| anyhow!("{} has no root-level {MANIFEST_FILENAME}", root.display()))?;
    let skill_md = String::from_utf8(skill_md_bytes)
        .map_err(|_| anyhow!("{MANIFEST_FILENAME} is not valid UTF-8"))?;
    let name = manifest_name(&skill_md);
    let enc = base64::engine::general_purpose::STANDARD;
    let files = files.into_iter().map(|(k, v)| (k, enc.encode(v))).collect();
    Ok(Source::Inline {
        skill_md,
        files,
        name,
    })
}

fn collect_files(
    root: &Path,
    dir: &Path,
    out: &mut BTreeMap<String, Vec<u8>>,
    total: &mut u64,
) -> anyhow::Result<()> {
    let mut entries: Vec<_> = std::fs::read_dir(dir)
        .with_context(|| format!("reading {}", dir.display()))?
        .collect::<Result<_, _>>()?;
    entries.sort_by_key(|e| e.file_name());
    for entry in entries {
        let path = entry.path();
        let ft = entry.file_type()?;
        if ft.is_symlink() {
            continue;
        }
        if ft.is_dir() {
            if entry.file_name() == ".git" {
                continue;
            }
            collect_files(root, &path, out, total)?;
            continue;
        }
        if !ft.is_file() {
            continue;
        }
        let rel = path
            .strip_prefix(root)
            .map_err(|e| anyhow!("{e}"))?
            .components()
            .filter_map(|c| match c {
                Component::Normal(s) => Some(s.to_string_lossy().into_owned()),
                _ => None,
            })
            .collect::<Vec<_>>()
            .join("/");
        if out.len() + 1 > MAX_SKILL_FILES {
            bail!("skill directory has more than {MAX_SKILL_FILES} files");
        }
        let size = entry.metadata()?.len();
        if size > MAX_SKILL_FILE_SIZE {
            bail!(
                "skill file '{rel}' is {size} bytes, exceeding the per-file limit of {MAX_SKILL_FILE_SIZE}"
            );
        }
        *total += size;
        if *total > MAX_SKILL_BUNDLE_BYTES {
            bail!("skill directory exceeds the total size limit of {MAX_SKILL_BUNDLE_BYTES} bytes");
        }
        let data = std::fs::read(&path).with_context(|| format!("reading {}", path.display()))?;
        out.insert(rel, data);
    }
    Ok(())
}

/// Best-effort `name:` from SKILL.md frontmatter, only used to print review
/// hints; the server remains the authority on the manifest.
fn manifest_name(skill_md: &str) -> Option<String> {
    let rest = skill_md
        .trim_start_matches('\u{feff}')
        .strip_prefix("---")?;
    let end = rest.find("\n---")?;
    let front: Value = serde_yaml::from_str(&rest[..end]).ok()?;
    front["name"].as_str().map(|s| s.trim().to_string())
}

fn parse_credentials(items: &[String]) -> anyhow::Result<BTreeMap<String, String>> {
    let mut out = BTreeMap::new();
    for item in items {
        match item.split_once('=') {
            Some((server, cred)) if !server.trim().is_empty() && !cred.trim().is_empty() => {
                out.insert(server.trim().to_string(), cred.trim().to_string());
            }
            _ => bail!("--credential expects SERVER=CREDENTIAL, got '{item}'"),
        }
    }
    Ok(out)
}

// ── HTTP ─────────────────────────────────────────────────────

/// A raw API response: status, parsed JSON body (Null if not JSON) and text.
#[derive(Debug)]
pub struct Reply {
    pub status: u16,
    pub body: Value,
    pub text: String,
}

impl Reply {
    fn error_message(&self) -> String {
        if let Some(e) = self.body["error"].as_str() {
            return e.to_string();
        }
        let t = self.text.trim();
        if t.is_empty() {
            "(empty response)".to_string()
        } else {
            t.to_string()
        }
    }

    /// A 404 whose body is the server's JSON `{"error": ...}` means the route
    /// exists and the *entity* is missing; anything else is a route miss.
    fn is_entity_not_found(&self) -> bool {
        self.status == 404 && self.body["error"].is_string()
    }
}

/// Minimal control plane client that keeps the status code and body of
/// non-2xx responses (the verdict endpoints answer 202/422 on purpose).
pub struct Api {
    base: String,
    api_key: Option<String>,
    kitchen: Option<String>,
    http: reqwest::Client,
}

impl Api {
    pub fn new(base: &str, api_key: Option<String>, kitchen: Option<String>) -> Self {
        Self {
            base: base.trim_end_matches('/').to_string(),
            api_key,
            kitchen,
            http: reqwest::Client::builder()
                // Registration can include a git clone and a model call.
                .timeout(Duration::from_secs(300))
                .build()
                .expect("reqwest client"),
        }
    }

    /// Flags override env vars, which override `~/.agentoven/config.toml`.
    fn from_globals(g: &Globals) -> Self {
        let cfg = agentoven_core::AgentOvenConfig::load();
        let key = g
            .api_key
            .clone()
            .or_else(|| cfg.auth_credential().map(String::from));
        let kitchen = g.kitchen.clone().or(cfg.kitchen.clone());
        Self::new(g.url.as_deref().unwrap_or(&cfg.url), key, kitchen)
    }

    pub async fn send(
        &self,
        method: reqwest::Method,
        path: &str,
        body: Option<&Value>,
    ) -> anyhow::Result<Reply> {
        let mut req = self.http.request(method, format!("{}{}", self.base, path));
        if let Some(key) = &self.api_key {
            req = req.bearer_auth(key);
        }
        if let Some(k) = &self.kitchen {
            req = req.header("X-Kitchen", k).header("X-Kitchen-Id", k);
        }
        if let Some(b) = body {
            req = req.json(b);
        }
        let resp = req.send().await.map_err(|e| {
            anyhow!(
                "cannot reach the control plane at {}: {e}\n  Tip: run `agentoven local up` or set AGENTOVEN_URL / --url",
                self.base
            )
        })?;
        let status = resp.status().as_u16();
        let text = resp.text().await.unwrap_or_default();
        let body = serde_json::from_str(&text).unwrap_or(Value::Null);
        Ok(Reply { status, body, text })
    }
}

fn pro_routes_missing(reply: &Reply) -> bool {
    reply.status == 404 && !reply.is_entity_not_found()
}

fn pro_required(cmd: &str) -> anyhow::Error {
    anyhow!(
        "`agentoven {cmd}` requires AgentOven Pro: this server does not expose /api/v1/skills/pro \
(community edition, or Pro's skill storage could not be opened — check AGENTOVEN_SKILLS_STORAGE_DIR on the server).\n  \
`agentoven skills install <source>` works on every edition."
    )
}

fn api_error(action: &str, reply: &Reply) -> anyhow::Error {
    let hint = match reply.status {
        401 => "\n  Hint: not authenticated — set AGENTOVEN_API_KEY / --api-key or run `agentoven login`.",
        403 => "\n  Hint: your role lacks permission for this action (Pro RBAC: stage needs skill:create, analyze needs skill:analyze).",
        412 => "\n  Hint: register a model provider first: `agentoven provider add`.",
        409 => "\n  Hint: the skill is not in the state this action needs (analyze needs `pending`; approve/reject need `needs_review`). See `agentoven skills get <name>`.",
        _ => "",
    };
    anyhow!(
        "{action} failed: HTTP {} — {}{hint}",
        reply.status,
        reply.error_message()
    )
}

// ── Rendering ────────────────────────────────────────────────

fn ok(stdout: String) -> Report {
    Report { stdout, code: 0 }
}

fn pretty(v: &Value) -> String {
    format!("{}\n", serde_json::to_string_pretty(v).unwrap_or_default())
}

fn indent(s: &str, pad: &str) -> String {
    s.lines()
        .map(|l| format!("{pad}{l}"))
        .collect::<Vec<_>>()
        .join("\n")
}

fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        s.to_string()
    } else {
        let mut t: String = s.chars().take(max.saturating_sub(1)).collect();
        t.push('…');
        t
    }
}

fn status_colored(status: &str) -> String {
    match status {
        "accepted" => status.green().to_string(),
        "needs_review" | "pending" => status.yellow().to_string(),
        "rejected" => status.red().to_string(),
        other => other.to_string(),
    }
}

fn render_list(skills: &[Value]) -> String {
    if skills.is_empty() {
        return "  (no skills registered — use `agentoven skills install <source>`)\n".to_string();
    }
    let mut out = String::new();
    out += &format!(
        "  {:<24} {:<13} {:<7} {:<6} {}\n",
        "NAME".bold(),
        "STATUS".bold(),
        "SOURCE".bold(),
        "TOOLS".bold(),
        "DESCRIPTION".bold()
    );
    out += &format!("  {}\n", "─".repeat(84).dimmed());
    for s in skills {
        let status = s["status"].as_str().unwrap_or("-");
        // Pad before colouring so ANSI codes don't break alignment.
        let status_cell = format!("{:<13}", status);
        let status_cell = status_cell.replacen(status, &status_colored(status), 1);
        out += &format!(
            "  {:<24} {} {:<7} {:<6} {}\n",
            truncate(s["name"].as_str().unwrap_or("-"), 24),
            status_cell,
            s["source"].as_str().unwrap_or("-"),
            s["registered_tools"].as_array().map_or(0, |t| t.len()),
            truncate(s["manifest"]["description"].as_str().unwrap_or("-"), 40),
        );
    }
    out += &format!("\n  {} {} skill(s)\n", "→".dimmed(), skills.len());
    out
}

fn render_detail(skill: &Value, with_instructions: bool) -> String {
    let field = |label: &str, v: &str| format!("  {:<16} {}\n", format!("{label}:").bold(), v);
    let s = |v: &Value| v.as_str().unwrap_or("-").to_string();
    let mut out = String::new();
    out += &format!("\n  Skill: {}\n\n", s(&skill["name"]).bold());
    out += &field("Description", &s(&skill["manifest"]["description"]));
    out += &field(
        "Status",
        &status_colored(skill["status"].as_str().unwrap_or("-")),
    );
    let source = match skill["source_ref"].as_str() {
        Some(r) if !r.is_empty() => format!("{} ({})", s(&skill["source"]), r),
        _ => s(&skill["source"]),
    };
    out += &field("Source", &source);
    if let Some(l) = skill["manifest"]["license"].as_str() {
        out += &field("License", l);
    }
    if let Some(a) = skill["manifest"]["allowed_tools"].as_array() {
        let v: Vec<&str> = a.iter().filter_map(|x| x.as_str()).collect();
        if !v.is_empty() {
            out += &field("Allowed tools", &v.join(", "));
        }
    }
    if let Some(servers) = skill["manifest"]["mcp_tools"].as_array() {
        for srv in servers {
            out += &field(
                "MCP server",
                &format!(
                    "{} → {} ({}{})",
                    s(&srv["name"]),
                    s(&srv["endpoint"]),
                    s(&srv["transport"]),
                    srv["auth_type"]
                        .as_str()
                        .map(|a| format!(", auth: {a}"))
                        .unwrap_or_default()
                ),
            );
        }
    }
    if let Some(t) = skill["registered_tools"].as_array() {
        let v: Vec<&str> = t.iter().filter_map(|x| x.as_str()).collect();
        if !v.is_empty() {
            out += &field("Registered tools", &v.join(", "));
        }
    }
    if let Some(v) = skill["verification_verdict"].as_str() {
        out += &field("Verdict", v);
        out += &field("Reviewed by", &s(&skill["verified_by_provider"]));
        out += &field("Verified at", &s(&skill["verified_at"]));
    } else {
        out += &field("Verdict", "(not yet analyzed)");
    }
    out += &field("Created by", &s(&skill["created_by"]));
    out += &field("Updated", &s(&skill["updated_at"]));
    if let Some(r) = skill["verification_reasoning"].as_str() {
        if !r.is_empty() {
            out += &format!("\n  {}\n{}\n", "Reasoning:".bold(), indent(r, "    "));
        }
    }
    if with_instructions {
        let ins = skill["manifest"]["instructions"].as_str().unwrap_or("");
        out += &format!("\n  {}\n{}\n", "Instructions:".bold(), indent(ins, "    "));
    }
    out
}

// ── Paths ────────────────────────────────────────────────────

/// Percent-encode one URL path segment (skill names come from SKILL.md
/// frontmatter and can contain anything).
pub fn encode_segment(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

fn skill_path(name: &str, suffix: &str) -> String {
    format!("/api/v1/skills/{}{}", encode_segment(name), suffix)
}

// ── Tests ────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::commands::{Cli, Commands};
    use clap::Parser;
    use std::sync::{Arc, Mutex};
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    fn no_color() {
        colored::control::set_override(false);
    }

    fn parse(args: &[&str]) -> SkillsCommands {
        let mut v = vec!["agentoven", "skills"];
        v.extend_from_slice(args);
        match Cli::try_parse_from(v).expect("parse").command {
            Commands::Skills(c) => c,
            _ => panic!("not a skills command"),
        }
    }

    fn src(source: &str) -> SourceArgs {
        SourceArgs {
            source: source.to_string(),
            git_ref: None,
            server_path: false,
        }
    }

    const SKILL_MD: &str =
        "---\nname: pdf-tools\ndescription: Work with PDFs\n---\n\nUse the tools.\n";

    fn write_skill(dir: &Path) {
        std::fs::write(dir.join("SKILL.md"), SKILL_MD).unwrap();
        std::fs::create_dir_all(dir.join("scripts")).unwrap();
        std::fs::write(dir.join("scripts/run.sh"), "echo hi\n").unwrap();
    }

    // ── Source resolution ──

    #[test]
    fn at_owner_slug_is_refused_honestly() {
        let err = resolve_source(&src("@acme/pdf-tools"))
            .unwrap_err()
            .to_string();
        assert!(err.contains("does not support"), "{err}");
        assert!(err.contains("no skills registry"), "{err}");
    }

    #[test]
    fn git_urls_are_detected() {
        for u in [
            "https://github.com/o/r",
            "https://github.com/o/r.git",
            "ssh://git@host/o/r.git",
            "git@github.com:o/r.git",
            "file:///tmp/repo",
        ] {
            assert!(looks_like_git_url(u), "{u}");
        }
        for u in ["./skills/foo", "/abs/path", "skills", "C:\\x", "a/b@c:d"] {
            assert!(!looks_like_git_url(u), "{u}");
        }
    }

    #[test]
    fn git_source_carries_ref() {
        let mut a = src("https://github.com/o/r");
        a.git_ref = Some("v1".into());
        let s = resolve_source(&a).unwrap();
        assert_eq!(
            s.request_body(),
            json!({"source": "git", "git_url": "https://github.com/o/r", "git_ref": "v1"})
        );
    }

    #[test]
    fn server_path_is_not_read_locally() {
        let mut a = src("/srv/skills/pdf");
        a.server_path = true;
        assert_eq!(
            resolve_source(&a).unwrap().request_body(),
            json!({"source": "path", "path": "/srv/skills/pdf"})
        );
        a.git_ref = Some("x".into());
        assert!(resolve_source(&a).is_err());
    }

    #[test]
    fn local_directory_becomes_inline_bundle() {
        let tmp = tempfile::tempdir().unwrap();
        write_skill(tmp.path());
        // Things that must never be uploaded.
        std::fs::create_dir_all(tmp.path().join(".git")).unwrap();
        std::fs::write(tmp.path().join(".git/config"), "secret").unwrap();
        #[cfg(unix)]
        {
            std::os::unix::fs::symlink("/etc/hosts", tmp.path().join("link")).unwrap();
        }

        let s = resolve_source(&src(tmp.path().to_str().unwrap())).unwrap();
        let body = s.request_body();
        assert_eq!(body["source"], "inline");
        assert_eq!(body["skill_md"], SKILL_MD);
        let files = body["files"].as_object().unwrap();
        assert_eq!(files.len(), 1, "{files:?}");
        let b64 = files["scripts/run.sh"].as_str().unwrap();
        let decoded = base64::engine::general_purpose::STANDARD
            .decode(b64)
            .unwrap();
        assert_eq!(decoded, b"echo hi\n");
        assert_eq!(s.name_hint().as_deref(), Some("pdf-tools"));
    }

    #[test]
    fn skill_md_path_uses_its_directory() {
        let tmp = tempfile::tempdir().unwrap();
        write_skill(tmp.path());
        let p = tmp.path().join("SKILL.md");
        let s = resolve_source(&src(p.to_str().unwrap())).unwrap();
        assert_eq!(s.request_body()["source"], "inline");
    }

    #[test]
    fn directory_without_manifest_is_rejected() {
        let tmp = tempfile::tempdir().unwrap();
        std::fs::write(tmp.path().join("README.md"), "x").unwrap();
        let err = resolve_source(&src(tmp.path().to_str().unwrap()))
            .unwrap_err()
            .to_string();
        assert!(err.contains("no root-level SKILL.md"), "{err}");
    }

    #[test]
    fn oversized_file_is_rejected_locally() {
        let tmp = tempfile::tempdir().unwrap();
        write_skill(tmp.path());
        std::fs::write(
            tmp.path().join("big.bin"),
            vec![0u8; (MAX_SKILL_FILE_SIZE + 1) as usize],
        )
        .unwrap();
        let err = resolve_source(&src(tmp.path().to_str().unwrap()))
            .unwrap_err()
            .to_string();
        assert!(err.contains("per-file limit"), "{err}");
    }

    #[test]
    fn unknown_source_is_rejected_and_ref_needs_git() {
        let err = resolve_source(&src("definitely/not/here"))
            .unwrap_err()
            .to_string();
        assert!(err.contains("neither a git URL nor an existing local directory"));
        let tmp = tempfile::tempdir().unwrap();
        write_skill(tmp.path());
        let mut a = src(tmp.path().to_str().unwrap());
        a.git_ref = Some("main".into());
        assert!(resolve_source(&a).is_err());
    }

    // ── Pure helpers ──

    #[test]
    fn credentials_parse() {
        let m = parse_credentials(&["srv=cred".into(), " a = b ".into()]).unwrap();
        assert_eq!(m["srv"], "cred");
        assert_eq!(m["a"], "b");
        assert!(parse_credentials(&["nope".into()]).is_err());
        assert!(parse_credentials(&["=x".into()]).is_err());
    }

    #[test]
    fn path_segments_are_percent_encoded() {
        assert_eq!(encode_segment("pdf-tools_1.0"), "pdf-tools_1.0");
        assert_eq!(encode_segment("a b/c"), "a%20b%2Fc");
        assert_eq!(
            skill_path("x y", "/approve"),
            "/api/v1/skills/x%20y/approve"
        );
    }

    fn reply(status: u16, body: Value) -> Reply {
        Reply {
            status,
            text: body.to_string(),
            body,
        }
    }

    #[test]
    fn verdict_classification() {
        assert_eq!(
            verdict_of(&reply(201, json!({"status": "accepted"}))),
            Some(Verdict::Accepted)
        );
        assert_eq!(
            verdict_of(&reply(202, json!({"status": "needs_review"}))),
            Some(Verdict::NeedsReview)
        );
        assert_eq!(
            verdict_of(&reply(422, json!({"status": "rejected"}))),
            Some(Verdict::Rejected)
        );
        // Fallback to HTTP status when the body has no status.
        assert_eq!(
            verdict_of(&reply(202, json!({}))),
            Some(Verdict::NeedsReview)
        );
        assert_eq!(verdict_of(&reply(422, json!({}))), Some(Verdict::Rejected));
        // Errors are not verdicts.
        assert_eq!(verdict_of(&reply(400, json!({"error": "x"}))), None);
        assert_eq!(verdict_of(&reply(500, json!({"error": "x"}))), None);
        assert_eq!(verdict_of(&reply(201, json!({}))), None);
    }

    #[test]
    fn pro_route_miss_is_distinguished_from_missing_skill() {
        let route_miss = Reply {
            status: 404,
            body: Value::Null,
            text: "404 page not found\n".into(),
        };
        assert!(pro_routes_missing(&route_miss));
        let entity = reply(404, json!({"error": "skill not found: x"}));
        assert!(!pro_routes_missing(&entity));
        assert!(!pro_routes_missing(&reply(400, json!({"error": "x"}))));
    }

    #[test]
    fn exit_codes() {
        assert_eq!(Verdict::Accepted.exit_code(), 0);
        assert_eq!(Verdict::NeedsReview.exit_code(), 2);
        assert_eq!(Verdict::Rejected.exit_code(), 3);
    }

    #[test]
    fn list_and_detail_render() {
        no_color();
        let skill = json!({
            "name": "pdf-tools", "status": "needs_review", "source": "git",
            "source_ref": "https://github.com/o/r@v1",
            "manifest": {"description": "Work with PDFs", "license": "MIT",
                "mcp_tools": [{"name": "srv", "endpoint": "http://x", "transport": "http", "auth_type": "bearer"}],
                "instructions": "Do the thing."},
            "verification_verdict": "needs_review",
            "verification_reasoning": "Line one.\nLine two.",
            "verified_by_provider": "openai-main",
            "registered_tools": []
        });
        let list = render_list(&[skill.clone()]);
        assert!(
            list.contains("pdf-tools") && list.contains("needs_review") && list.contains("git")
        );
        assert!(render_list(&[]).contains("no skills registered"));

        let d = render_detail(&skill, false);
        assert!(d.contains("openai-main") && d.contains("Line two.") && d.contains("auth: bearer"));
        assert!(!d.contains("Do the thing."));
        assert!(render_detail(&skill, true).contains("Do the thing."));
    }

    #[test]
    fn truncate_is_char_safe() {
        assert_eq!(truncate("héllo wörld", 6), "héllo…");
        assert_eq!(truncate("short", 10), "short");
    }

    // ── CLI parsing ──

    #[test]
    fn clap_surface() {
        assert!(matches!(parse(&["list"]), SkillsCommands::List));
        match parse(&["analyze", "s", "--provider", "p", "--credential", "a=b"]) {
            SkillsCommands::Analyze(a) => {
                assert_eq!(a.provider.as_deref(), Some("p"));
                assert_eq!(a.credentials, vec!["a=b"]);
            }
            _ => panic!(),
        }
        match parse(&["reject", "s", "--reason", "bad"]) {
            SkillsCommands::Reject(a) => assert_eq!(a.reason.as_deref(), Some("bad")),
            _ => panic!(),
        }
        match parse(&["install", "https://x/y", "--ref", "v2"]) {
            SkillsCommands::Install(a) => assert_eq!(a.source.git_ref.as_deref(), Some("v2")),
            _ => panic!(),
        }
        // Consent is not a flag: the server has no consent gate.
        assert!(Cli::try_parse_from(["agentoven", "skills", "analyze", "s", "--consent"]).is_err());
        // Global flags work after the subcommand.
        let cli = Cli::try_parse_from([
            "agentoven",
            "skills",
            "list",
            "--kitchen",
            "k1",
            "--url",
            "http://h:1",
            "--output",
            "json",
        ])
        .unwrap();
        assert_eq!(cli.kitchen.as_deref(), Some("k1"));
        assert_eq!(cli.url.as_deref(), Some("http://h:1"));
    }

    #[test]
    fn help_documents_limits_and_provider_disclosure() {
        use clap::CommandFactory;
        let mut cmd = Cli::command();
        let skills = cmd.find_subcommand_mut("skills").unwrap();
        let install_help = skills
            .find_subcommand_mut("install")
            .unwrap()
            .render_long_help()
            .to_string();
        assert!(install_help.contains("@owner/slug"), "{install_help}");
        assert!(install_help.contains("NOT supported"));
        let analyze_help = skills
            .find_subcommand_mut("analyze")
            .unwrap()
            .render_long_help()
            .to_string();
        assert!(
            analyze_help.contains("SENDS THE SKILL BUNDLE"),
            "{analyze_help}"
        );
    }

    // ── Fake control plane ──

    #[derive(Debug, Clone)]
    struct Recorded {
        method: String,
        path: String,
        headers: String,
        body: Value,
    }

    struct Route {
        method: &'static str,
        path: &'static str,
        status: u16,
        content_type: &'static str,
        body: String,
    }

    fn route(method: &'static str, path: &'static str, status: u16, body: Value) -> Route {
        Route {
            method,
            path,
            status,
            content_type: "application/json",
            body: body.to_string(),
        }
    }

    /// Serve canned responses over real HTTP; unknown routes get chi's plain
    /// "404 page not found", like a server without the Pro routes.
    async fn fake_server(routes: Vec<Route>) -> (String, Arc<Mutex<Vec<Recorded>>>) {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let log: Arc<Mutex<Vec<Recorded>>> = Arc::default();
        let routes = Arc::new(routes);
        let log2 = log.clone();
        tokio::spawn(async move {
            loop {
                let Ok((mut sock, _)) = listener.accept().await else {
                    return;
                };
                let routes = routes.clone();
                let log = log2.clone();
                tokio::spawn(async move {
                    let mut buf = Vec::new();
                    let mut tmp = [0u8; 4096];
                    let header_end = loop {
                        let n = sock.read(&mut tmp).await.unwrap_or(0);
                        if n == 0 {
                            return;
                        }
                        buf.extend_from_slice(&tmp[..n]);
                        if let Some(p) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
                            break p + 4;
                        }
                    };
                    let head = String::from_utf8_lossy(&buf[..header_end]).to_string();
                    let len = head
                        .lines()
                        .find_map(|l| {
                            let l = l.to_ascii_lowercase();
                            l.strip_prefix("content-length:")
                                .map(|v| v.trim().parse::<usize>().unwrap_or(0))
                        })
                        .unwrap_or(0);
                    while buf.len() < header_end + len {
                        let n = sock.read(&mut tmp).await.unwrap_or(0);
                        if n == 0 {
                            break;
                        }
                        buf.extend_from_slice(&tmp[..n]);
                    }
                    let mut first = head.lines().next().unwrap_or("").split_whitespace();
                    let method = first.next().unwrap_or("").to_string();
                    let path = first.next().unwrap_or("").to_string();
                    let body = serde_json::from_slice(&buf[header_end..]).unwrap_or(Value::Null);
                    log.lock().unwrap().push(Recorded {
                        method: method.clone(),
                        path: path.clone(),
                        headers: head.to_ascii_lowercase(),
                        body,
                    });
                    let (status, ct, payload) = routes
                        .iter()
                        .find(|r| r.method == method && r.path == path)
                        .map(|r| (r.status, r.content_type, r.body.clone()))
                        .unwrap_or((
                            404,
                            "text/plain; charset=utf-8",
                            "404 page not found\n".into(),
                        ));
                    let resp = format!(
                        "HTTP/1.1 {status} X\r\nContent-Type: {ct}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{payload}",
                        payload.len()
                    );
                    let _ = sock.write_all(resp.as_bytes()).await;
                    let _ = sock.shutdown().await;
                });
            }
        });
        (format!("http://{addr}"), log)
    }

    fn api(base: &str) -> Api {
        Api::new(base, Some("test-token".into()), Some("kitchen-1".into()))
    }

    fn accepted_skill() -> Value {
        json!({
            "name": "pdf-tools", "status": "accepted", "source": "inline",
            "manifest": {"name": "pdf-tools", "description": "Work with PDFs", "instructions": "x"},
            "verification_verdict": "accept", "verification_reasoning": "Looks benign.",
            "verified_by_provider": "openai-main", "registered_tools": ["pdf-tools.srv"]
        })
    }

    #[tokio::test]
    async fn install_accepted_sends_inline_bundle_with_auth_and_kitchen() {
        no_color();
        let tmp = tempfile::tempdir().unwrap();
        write_skill(tmp.path());
        let (base, log) = fake_server(vec![route(
            "POST",
            "/api/v1/skills/register",
            201,
            accepted_skill(),
        )])
        .await;

        let cmd = parse(&[
            "install",
            tmp.path().to_str().unwrap(),
            "--credential",
            "srv=my-cred",
        ]);
        let rep = run(cmd, &api(&base), false).await.unwrap();
        assert_eq!(rep.code, 0);
        assert!(rep.stdout.contains("installed") && rep.stdout.contains("verdict: accept"));
        assert!(rep.stdout.contains("openai-main") && rep.stdout.contains("pdf-tools.srv"));
        assert!(rep.stdout.contains("Looks benign."));

        let reqs = log.lock().unwrap();
        assert_eq!(reqs.len(), 1);
        let r = &reqs[0];
        assert_eq!(
            (r.method.as_str(), r.path.as_str()),
            ("POST", "/api/v1/skills/register")
        );
        assert!(
            r.headers.contains("authorization: bearer test-token"),
            "{}",
            r.headers
        );
        assert!(r.headers.contains("x-kitchen: kitchen-1"), "{}", r.headers);
        assert_eq!(r.body["source"], "inline");
        assert_eq!(r.body["skill_md"], SKILL_MD);
        assert!(r.body["files"]["scripts/run.sh"].is_string());
        assert_eq!(r.body["credentials"]["srv"], "my-cred");
    }

    #[tokio::test]
    async fn install_git_source_body() {
        let (base, log) = fake_server(vec![route(
            "POST",
            "/api/v1/skills/register",
            201,
            accepted_skill(),
        )])
        .await;
        let cmd = parse(&["install", "https://github.com/o/r", "--ref", "v1"]);
        run(cmd, &api(&base), false).await.unwrap();
        let r = log.lock().unwrap()[0].clone();
        assert_eq!(
            r.body,
            json!({"source": "git", "git_url": "https://github.com/o/r", "git_ref": "v1"})
        );
    }

    #[tokio::test]
    async fn install_needs_review_exits_2_with_reasoning_and_next_steps() {
        no_color();
        let tmp = tempfile::tempdir().unwrap();
        write_skill(tmp.path());
        let body = json!({"status": "needs_review", "reasoning": "Script fetches a URL.", "note": "inconclusive"});
        let (base, _) =
            fake_server(vec![route("POST", "/api/v1/skills/register", 202, body)]).await;
        let rep = run(
            parse(&["install", tmp.path().to_str().unwrap()]),
            &api(&base),
            false,
        )
        .await
        .unwrap();
        assert_eq!(rep.code, EXIT_NEEDS_REVIEW);
        assert!(rep.stdout.contains("needs_review"));
        assert!(rep.stdout.contains("Script fetches a URL."));
        // Name comes from the local SKILL.md since the 202 body has none.
        assert!(
            rep.stdout.contains("agentoven skills approve pdf-tools"),
            "{}",
            rep.stdout
        );
    }

    #[tokio::test]
    async fn install_rejected_exits_3() {
        no_color();
        let body = json!({"status": "rejected", "reasoning": "Exfiltrates credentials."});
        let (base, _) =
            fake_server(vec![route("POST", "/api/v1/skills/register", 422, body)]).await;
        let rep = run(parse(&["install", "https://x/y"]), &api(&base), false)
            .await
            .unwrap();
        assert_eq!(rep.code, EXIT_REJECTED);
        assert!(rep.stdout.contains("rejected") && rep.stdout.contains("Exfiltrates credentials."));
    }

    #[tokio::test]
    async fn install_without_providers_is_an_error_with_hint() {
        let body = json!({"error": "this kitchen has no registered model providers yet"});
        let (base, _) =
            fake_server(vec![route("POST", "/api/v1/skills/register", 412, body)]).await;
        let err = run(parse(&["install", "https://x/y"]), &api(&base), false)
            .await
            .unwrap_err()
            .to_string();
        assert!(
            err.contains("412") && err.contains("no registered model providers"),
            "{err}"
        );
        assert!(err.contains("agentoven provider add"));
    }

    #[tokio::test]
    async fn json_output_is_the_raw_body_and_keeps_exit_code() {
        let body = json!({"status": "needs_review", "reasoning": "r"});
        let (base, _) = fake_server(vec![route(
            "POST",
            "/api/v1/skills/register",
            202,
            body.clone(),
        )])
        .await;
        let rep = run(parse(&["install", "https://x/y"]), &api(&base), true)
            .await
            .unwrap();
        assert_eq!(rep.code, EXIT_NEEDS_REVIEW);
        assert_eq!(serde_json::from_str::<Value>(&rep.stdout).unwrap(), body);
    }

    #[tokio::test]
    async fn list_get_remove() {
        no_color();
        let (base, log) = fake_server(vec![
            route(
                "GET",
                "/api/v1/skills",
                200,
                json!({"skills": [accepted_skill()]}),
            ),
            route("GET", "/api/v1/skills/pdf-tools", 200, accepted_skill()),
            route(
                "GET",
                "/api/v1/skills/missing",
                404,
                json!({"error": "skill not found: missing"}),
            ),
            route("DELETE", "/api/v1/skills/pdf-tools", 204, json!(null)),
        ])
        .await;
        let a = api(&base);

        let rep = run(parse(&["list"]), &a, false).await.unwrap();
        assert!(rep.stdout.contains("pdf-tools") && rep.stdout.contains("accepted"));

        let rep = run(parse(&["list"]), &a, true).await.unwrap();
        assert_eq!(
            serde_json::from_str::<Value>(&rep.stdout).unwrap()["skills"][0]["name"],
            "pdf-tools"
        );

        let rep = run(parse(&["get", "pdf-tools"]), &a, false).await.unwrap();
        assert!(rep.stdout.contains("Looks benign.") && rep.stdout.contains("openai-main"));

        let err = run(parse(&["get", "missing"]), &a, false)
            .await
            .unwrap_err()
            .to_string();
        assert!(err.contains("skill not found: missing"), "{err}");

        let rep = run(parse(&["remove", "pdf-tools", "--force"]), &a, false)
            .await
            .unwrap();
        assert!(rep.stdout.contains("removed"));
        assert!(log.lock().unwrap().iter().any(|r| r.method == "DELETE"));
    }

    #[tokio::test]
    async fn list_handles_null_skills() {
        no_color();
        let (base, _) = fake_server(vec![route(
            "GET",
            "/api/v1/skills",
            200,
            json!({"skills": null}),
        )])
        .await;
        let rep = run(parse(&["list"]), &api(&base), false).await.unwrap();
        assert!(rep.stdout.contains("no skills registered"));
    }

    #[tokio::test]
    async fn stage_and_analyze_flow_on_pro() {
        no_color();
        let stage_body = json!({
            "skill": {"name": "pdf-tools", "status": "pending"},
            "available_providers": ["openai-main", "anthropic-main"],
            "note": "staged"
        });
        let (base, log) = fake_server(vec![
            route("POST", "/api/v1/skills/pro/stage", 201, stage_body),
            route(
                "POST",
                "/api/v1/skills/pro/pdf-tools/analyze",
                200,
                accepted_skill(),
            ),
        ])
        .await;
        let a = api(&base);

        let rep = run(parse(&["stage", "https://github.com/o/r"]), &a, false)
            .await
            .unwrap();
        assert_eq!(rep.code, 0);
        assert!(
            rep.stdout.contains("staged") && rep.stdout.contains("openai-main, anthropic-main")
        );
        assert!(rep.stdout.contains("agentoven skills analyze pdf-tools"));

        let rep = run(
            parse(&["analyze", "pdf-tools", "--provider", "openai-main"]),
            &a,
            false,
        )
        .await
        .unwrap();
        assert_eq!(rep.code, 0);
        assert!(rep.stdout.contains("analyzed"));
        let reqs = log.lock().unwrap();
        assert_eq!(reqs[1].body, json!({"provider": "openai-main"}));
    }

    #[tokio::test]
    async fn analyze_without_provider_sends_no_provider_or_consent_field() {
        let (base, log) = fake_server(vec![route(
            "POST",
            "/api/v1/skills/pro/pdf-tools/analyze",
            202,
            json!({"name": "pdf-tools", "status": "needs_review", "verification_reasoning": "hm"}),
        )])
        .await;
        let rep = run(parse(&["analyze", "pdf-tools"]), &api(&base), false)
            .await
            .unwrap();
        assert_eq!(rep.code, EXIT_NEEDS_REVIEW);
        assert_eq!(log.lock().unwrap()[0].body, json!({}));
    }

    #[tokio::test]
    async fn analyze_rejected_verdict_exits_3() {
        let (base, _) = fake_server(vec![route(
            "POST",
            "/api/v1/skills/pro/pdf-tools/analyze",
            422,
            json!({"name": "pdf-tools", "status": "rejected", "verification_reasoning": "bad"}),
        )])
        .await;
        let rep = run(parse(&["analyze", "pdf-tools"]), &api(&base), false)
            .await
            .unwrap();
        assert_eq!(rep.code, EXIT_REJECTED);
    }

    #[tokio::test]
    async fn pro_commands_on_community_server_say_requires_pro() {
        // No routes: every request gets the plain-text 404 of a route miss.
        let (base, _) = fake_server(vec![]).await;
        let a = api(&base);
        for args in [&["stage", "https://x/y"][..], &["analyze", "s"][..]] {
            let err = run(parse(args), &a, false).await.unwrap_err().to_string();
            assert!(err.contains("requires AgentOven Pro"), "{err}");
            assert!(err.contains("skills install"), "{err}");
        }
    }

    #[tokio::test]
    async fn analyze_unknown_skill_is_not_reported_as_missing_pro() {
        let (base, _) = fake_server(vec![route(
            "POST",
            "/api/v1/skills/pro/ghost/analyze",
            404,
            json!({"error": "skill not found: ghost"}),
        )])
        .await;
        let err = run(parse(&["analyze", "ghost"]), &api(&base), false)
            .await
            .unwrap_err()
            .to_string();
        assert!(err.contains("skill not found: ghost"), "{err}");
        assert!(!err.contains("requires AgentOven Pro"), "{err}");
    }

    #[tokio::test]
    async fn analyze_conflict_and_forbidden_have_hints() {
        let (base, _) = fake_server(vec![
            route(
                "POST",
                "/api/v1/skills/pro/a/analyze",
                409,
                json!({"error": "skill \"a\" is \"accepted\", not pending analysis"}),
            ),
            route(
                "POST",
                "/api/v1/skills/pro/b/analyze",
                403,
                json!({"error": "forbidden"}),
            ),
        ])
        .await;
        let a = api(&base);
        let e = run(parse(&["analyze", "a"]), &a, false)
            .await
            .unwrap_err()
            .to_string();
        assert!(e.contains("not pending analysis") && e.contains("pending"));
        let e = run(parse(&["analyze", "b"]), &a, false)
            .await
            .unwrap_err()
            .to_string();
        assert!(e.contains("skill:analyze"), "{e}");
    }

    #[tokio::test]
    async fn approve_and_reject_send_notes() {
        no_color();
        let approved = json!({"name": "s", "status": "accepted", "registered_tools": ["s.srv"]});
        let rejected = json!({"name": "s", "status": "rejected"});
        let (base, log) = fake_server(vec![
            route("POST", "/api/v1/skills/s/approve", 200, approved),
            route("POST", "/api/v1/skills/s/reject", 200, rejected),
            route(
                "POST",
                "/api/v1/skills/done/approve",
                409,
                json!({"error": "skill \"done\" is \"accepted\", not needs_review"}),
            ),
        ])
        .await;
        let a = api(&base);

        let rep = run(parse(&["approve", "s", "--note", "reviewed ok"]), &a, false)
            .await
            .unwrap();
        assert!(rep.stdout.contains("approved") && rep.stdout.contains("s.srv"));
        let rep = run(parse(&["reject", "s", "--reason", "too broad"]), &a, false)
            .await
            .unwrap();
        assert!(rep.stdout.contains("rejected"));
        let reqs = log.lock().unwrap();
        assert_eq!(reqs[0].body, json!({"note": "reviewed ok"}));
        assert_eq!(reqs[1].body, json!({"note": "too broad"}));
        drop(reqs);

        let e = run(parse(&["approve", "done"]), &a, false)
            .await
            .unwrap_err()
            .to_string();
        assert!(e.contains("409") && e.contains("needs_review"), "{e}");
    }

    #[tokio::test]
    async fn refresh_uses_patch() {
        let (base, log) = fake_server(vec![route(
            "PATCH",
            "/api/v1/skills/s/refresh",
            201,
            accepted_skill(),
        )])
        .await;
        let rep = run(parse(&["refresh", "s"]), &api(&base), false)
            .await
            .unwrap();
        assert_eq!(rep.code, 0);
        assert_eq!(log.lock().unwrap()[0].method, "PATCH");
    }

    #[tokio::test]
    async fn unreachable_server_has_a_helpful_error() {
        // Port 1 on loopback refuses connections.
        let err = run(parse(&["list"]), &api("http://127.0.0.1:1"), false)
            .await
            .unwrap_err()
            .to_string();
        assert!(err.contains("cannot reach the control plane"), "{err}");
    }
}

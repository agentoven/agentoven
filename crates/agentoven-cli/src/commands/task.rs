//! `agentoven task` — background tasks: long-running agent work that outlives the request that
//! started it, under its own spend cap (`/api/v1/tasks`).
//!
//! A task is an agent run with a durable journal. It stops by itself when it finishes, when it
//! reaches its budget (a person extends it or ends it), when it needs a tool approved, or when it
//! keeps repeating itself. `resume` answers those; on a Pro server the answer is a signed decision
//! (`--sign`).

use std::io::Write;
use std::time::Duration;

use agentoven_core::{AgentOvenClient, AgentOvenConfig, Signature};
use clap::{Args, Subcommand};
use colored::Colorize;
use reqwest::Method;
use serde_json::{json, Map, Value};

use super::signing::{clip, pretty, signing_hint, SignArgs, Signing};

#[derive(Subcommand)]
pub enum TaskCommands {
    /// Start an agent as a background task. Returns at once with the task's ID.
    Start(StartArgs),
    /// List the kitchen's running, paused and unfinished tasks.
    #[command(name = "list", visible_alias = "ls")]
    List(ListArgs),
    /// Show one task: status, spend, what it is waiting for, and its answer when it has one.
    Get(IdArgs),
    /// Follow a task's events as they happen (the stream the dashboard shows).
    Events(EventsArgs),
    /// Stop a task. It cannot be resumed afterwards.
    Cancel(IdArgs),
    /// Continue a task that is paused or was interrupted: extend its budget, approve or deny its tool
    /// calls, or just restart it after a crash.
    Resume(ResumeArgs),
}

#[derive(Args)]
pub struct StartArgs {
    /// The agent to run (a managed agent that is baked).
    pub agent: String,
    /// What to ask it.
    pub message: String,
    /// Continue inside an existing chat session.
    #[arg(long)]
    pub session: Option<String>,
    /// Cap on what the task may spend, in dollars. Only ever tightens the agent's and the server's cap.
    #[arg(long)]
    pub max_cost: Option<f64>,
    /// Cap on tokens.
    #[arg(long)]
    pub max_tokens: Option<i64>,
    /// Cap on seconds of work (time spent paused does not count).
    #[arg(long)]
    pub max_seconds: Option<i64>,
    /// Cap on turns of the agent loop.
    #[arg(long)]
    pub max_turns: Option<i64>,
    /// Label the task: key=value, repeatable.
    #[arg(long = "meta", value_name = "KEY=VALUE")]
    pub meta: Vec<String>,
    /// Wait until the task finishes or stops for a person, printing its state.
    #[arg(long)]
    pub wait: bool,
    /// Print the reply as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct ListArgs {
    /// Only tasks in this status (running, paused, interrupted, queued, ...).
    #[arg(long)]
    pub status: Option<String>,
    /// Print the reply as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct IdArgs {
    /// Task ID.
    pub id: String,
    /// Print the reply as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct EventsArgs {
    /// Task ID.
    pub id: String,
    /// Only events after this sequence number (what a dropped connection resumes from).
    #[arg(long, default_value_t = 0)]
    pub after: i64,
    /// Print each event as one JSON line.
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct ResumeArgs {
    /// Task ID.
    pub id: String,
    /// Approve (the default when a decision is given).
    #[arg(long, conflicts_with = "deny")]
    pub approve: bool,
    /// Deny: the tool calls are not run, or the task ends at its budget.
    #[arg(long)]
    pub deny: bool,
    /// Why; on a denial the agent sees it as the result of the tool call.
    #[arg(long)]
    pub note: Option<String>,
    /// Extend a budget pause: add dollars.
    #[arg(long)]
    pub add_cost: Option<f64>,
    /// Extend a budget pause: add tokens.
    #[arg(long)]
    pub add_tokens: Option<i64>,
    /// Extend a budget pause: add seconds of work.
    #[arg(long)]
    pub add_seconds: Option<i64>,
    /// Extend a budget pause: add turns.
    #[arg(long)]
    pub add_turns: Option<i64>,
    /// Why the budget is being raised (recorded).
    #[arg(long)]
    pub reason: Option<String>,
    #[command(flatten)]
    pub signing: SignArgs,
    /// Print the reply as JSON.
    #[arg(long)]
    pub json: bool,
}

/// Where the CLI is pointed, kept for the one call that streams.
pub struct Globals {
    pub url: Option<String>,
    pub api_key: Option<String>,
    pub kitchen: Option<String>,
}

impl Globals {
    fn config(&self) -> AgentOvenConfig {
        let mut cfg = AgentOvenConfig::load();
        if let Some(u) = &self.url {
            cfg.url = u.clone();
        }
        if let Some(k) = &self.api_key {
            cfg.api_key = Some(k.clone());
            cfg.token = None;
        }
        if let Some(k) = &self.kitchen {
            cfg.kitchen = Some(k.clone());
        }
        cfg
    }
}

pub async fn execute(cmd: TaskCommands, globals: Globals) -> anyhow::Result<()> {
    let cfg = globals.config();
    let client = AgentOvenClient::from_config(&cfg)?;
    match run(cmd, &client, Some(&cfg), Duration::from_secs(2)).await {
        Ok(out) => {
            print!("{out}");
            Ok(())
        }
        Err(e) => {
            if let Some(h) = signing_hint(&e) {
                eprintln!("{h}");
            }
            Err(e)
        }
    }
}

/// Run a command and return what it prints. `events` streams to stdout as it goes and returns
/// nothing. `poll` is how often `start --wait` looks at the task.
pub async fn run(
    cmd: TaskCommands,
    client: &AgentOvenClient,
    cfg: Option<&AgentOvenConfig>,
    poll: Duration,
) -> anyhow::Result<String> {
    match cmd {
        TaskCommands::Start(a) => {
            let body = start_body(&a)?;
            let started: Value = client.raw_post("/api/v1/tasks", &body).await?;
            let id = started["id"].as_str().unwrap_or_default().to_string();
            if a.wait && !id.is_empty() {
                let task = wait_until_stopped(client, &id, poll).await?;
                return Ok(if a.json {
                    pretty(&task) + "\n"
                } else {
                    render_task(&task)
                });
            }
            if a.json {
                return Ok(pretty(&started) + "\n");
            }
            Ok(format!(
                "\n  {} Task {} started on {} ({}).\n  Follow it with: agentoven task events {}\n  Check on it with: agentoven task get {}\n",
                "✓".green().bold(),
                id.bold(),
                started["agent"].as_str().unwrap_or(&a.agent),
                started["status"].as_str().unwrap_or("queued"),
                id,
                id
            ))
        }
        TaskCommands::List(a) => {
            let path = match &a.status {
                Some(s) => format!("/api/v1/tasks?status={}", urlencode(s)),
                None => "/api/v1/tasks".to_string(),
            };
            let reply: Value = client.raw_get(&path).await?;
            if a.json {
                return Ok(pretty(&reply) + "\n");
            }
            Ok(render_list(
                reply["tasks"].as_array().map_or(&[][..], |v| v),
            ))
        }
        TaskCommands::Get(a) => {
            let t: Value = client.raw_get(&task_path(&a.id, "")).await?;
            Ok(if a.json {
                pretty(&t) + "\n"
            } else {
                render_task(&t)
            })
        }
        TaskCommands::Cancel(a) => {
            let t: Value = client
                .raw_post(&task_path(&a.id, "/cancel"), &json!({}))
                .await?;
            Ok(if a.json {
                pretty(&t) + "\n"
            } else {
                format!(
                    "\n  {} Task {} is {}.\n",
                    "✓".green().bold(),
                    a.id.bold(),
                    t["status"].as_str().unwrap_or("cancelled")
                )
            })
        }
        TaskCommands::Resume(a) => resume(client, a).await,
        TaskCommands::Events(a) => {
            let cfg = cfg.ok_or_else(|| anyhow::anyhow!("events needs the CLI's configuration"))?;
            let mut out = std::io::stdout();
            stream_events(cfg, &a.id, a.after, a.json, &mut out).await?;
            Ok(String::new())
        }
    }
}

fn task_path(id: &str, suffix: &str) -> String {
    format!("/api/v1/tasks/{}{suffix}", urlencode(id))
}

/// Percent-encodes everything but unreserved characters, for a path segment or a query value.
fn urlencode(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

fn start_body(a: &StartArgs) -> anyhow::Result<Value> {
    if a.message.trim().is_empty() {
        anyhow::bail!("a task needs a message");
    }
    let mut body = json!({"agent": a.agent, "message": a.message});
    if let Some(s) = &a.session {
        body["session_id"] = json!(s);
    }
    let mut budget = Map::new();
    if let Some(v) = a.max_cost {
        if v <= 0.0 {
            anyhow::bail!("--max-cost must be more than zero");
        }
        budget.insert("max_cost_usd".into(), json!(v));
    }
    for (name, v, key) in [
        ("--max-tokens", a.max_tokens, "max_tokens"),
        ("--max-seconds", a.max_seconds, "max_active_seconds"),
        ("--max-turns", a.max_turns, "max_turns"),
    ] {
        if let Some(v) = v {
            if v <= 0 {
                anyhow::bail!("{name} must be more than zero");
            }
            budget.insert(key.into(), json!(v));
        }
    }
    if !budget.is_empty() {
        body["budget"] = Value::Object(budget);
    }
    if !a.meta.is_empty() {
        let mut m = Map::new();
        for kv in &a.meta {
            let (k, v) = kv
                .split_once('=')
                .ok_or_else(|| anyhow::anyhow!("--meta is KEY=VALUE, not '{kv}'"))?;
            m.insert(k.to_string(), json!(v));
        }
        body["metadata"] = Value::Object(m);
    }
    Ok(body)
}

/// Statuses after which nothing happens until a person acts (or ever).
fn stopped(status: &str) -> bool {
    !matches!(status, "queued" | "running")
}

async fn wait_until_stopped(
    client: &AgentOvenClient,
    id: &str,
    poll: Duration,
) -> anyhow::Result<Value> {
    loop {
        let t: Value = client.raw_get(&task_path(id, "")).await?;
        if stopped(t["status"].as_str().unwrap_or("")) {
            return Ok(t);
        }
        tokio::time::sleep(poll).await;
    }
}

/// The gate a pause is decided at (the same mapping the control plane uses).
fn gate_for(pause_kind: &str) -> Option<&'static str> {
    match pause_kind {
        "approval" => Some("tool_approval"),
        "budget" => Some("budget_extension"),
        "stall" => Some("resume_guidance"),
        _ => None,
    }
}

fn resume_body(a: &ResumeArgs) -> anyhow::Result<Option<Value>> {
    let mut extend = Map::new();
    if let Some(v) = a.add_cost {
        extend.insert("add_cost_usd".into(), json!(v));
    }
    for (v, key) in [
        (a.add_tokens, "add_tokens"),
        (a.add_seconds, "add_active_seconds"),
        (a.add_turns, "add_turns"),
    ] {
        if let Some(v) = v {
            extend.insert(key.into(), json!(v));
        }
    }
    if extend
        .values()
        .any(|v| v.as_f64().is_some_and(|n| n <= 0.0))
    {
        anyhow::bail!("an extension adds something: amounts must be more than zero");
    }
    if let Some(r) = &a.reason {
        if extend.is_empty() {
            anyhow::bail!("--reason explains an extension; give --add-cost, --add-tokens, --add-seconds or --add-turns");
        }
        extend.insert("reason".into(), json!(r));
    }
    if !extend.is_empty() {
        // One extension is applied once however many times it is sent: a retry after a dropped
        // connection must not raise the cap twice.
        extend.insert("id".into(), json!(uuid::Uuid::new_v4().to_string()));
    }
    let decided = a.approve || a.deny || a.note.is_some() || !extend.is_empty();
    if !decided {
        return Ok(None); // an interrupted task needs no decision
    }
    if a.deny && !extend.is_empty() {
        anyhow::bail!("a denial does not extend a budget");
    }
    let mut body = json!({"approved": !a.deny});
    if let Some(n) = &a.note {
        body["note"] = json!(n);
    }
    if !extend.is_empty() {
        body["extend"] = Value::Object(extend);
    }
    Ok(Some(body))
}

async fn resume(client: &AgentOvenClient, a: ResumeArgs) -> anyhow::Result<String> {
    // Read the password (environment, then prompt) only for a command that will use it.
    let signing = if a.signing.sign || a.signing.signing_token.is_some() {
        a.signing.resolve()?
    } else {
        Signing::None
    };
    resume_with(client, a, signing).await
}

/// `resume` with the signing already resolved, so a test does not depend on the environment.
async fn resume_with(
    client: &AgentOvenClient,
    a: ResumeArgs,
    signing: Signing,
) -> anyhow::Result<String> {
    let body = resume_body(&a)?;
    let mut token: Option<String> = None;
    if let (Some(b), false) = (&body, matches!(signing.signature(), Signature::None)) {
        // What is signed is exactly what is decided, for the gate the pause is waiting at.
        let t: Value = client.raw_get(&task_path(&a.id, "")).await?;
        let kind = t["pause_kind"].as_str().unwrap_or("");
        let gate = gate_for(kind).ok_or_else(|| {
            anyhow::anyhow!(
                "task {} is not waiting for a decision, so there is nothing to sign",
                a.id
            )
        })?;
        let mut payload = Map::new();
        if let Some(n) = b.get("note") {
            payload.insert("note".into(), n.clone());
        }
        if let Some(e) = b.get("extend") {
            payload.insert("extend".into(), e.clone());
        }
        let meaning = if a.deny { "rejected" } else { "approved" };
        let payload = Value::Object(payload);
        token = match signing.signature() {
            Signature::Token(t) => Some(t.to_string()),
            Signature::Password(p) => {
                Some(client.sign(gate, &a.id, meaning, Some(&payload), p).await?)
            }
            Signature::None => None,
        };
    }
    let out = client
        .call_json(
            Method::POST,
            &task_path(&a.id, "/resume"),
            body.as_ref(),
            token.as_deref(),
        )
        .await?;
    Ok(if a.json {
        pretty(&out) + "\n"
    } else {
        format!(
            "\n  {} Task {} resumed{} ({}).\n",
            "✓".green().bold(),
            a.id.bold(),
            if token.is_some() {
                ", decision signed"
            } else {
                ""
            },
            out["status"].as_str().unwrap_or("queued")
        )
    })
}

// ── rendering ────────────────────────────────────────────────

fn status_colored(s: &str) -> String {
    match s {
        "running" | "queued" => s.cyan().to_string(),
        "completed" => s.green().to_string(),
        "paused" => s.yellow().bold().to_string(),
        "interrupted" => s.yellow().to_string(),
        "failed" | "budget_exhausted" | "cancelled" | "abandoned" | "stalled" => {
            s.red().to_string()
        }
        _ => s.to_string(),
    }
}

fn spent(t: &Value) -> String {
    let cost = t["usage"]["estimated_cost"].as_f64().unwrap_or(0.0);
    let tokens = t["usage"]["total_tokens"].as_i64().unwrap_or(0);
    format!("${cost:.4} / {tokens} tok")
}

pub fn render_list(tasks: &[Value]) -> String {
    if tasks.is_empty() {
        return "\n  (no tasks)\n".to_string();
    }
    let mut out = format!(
        "\n  {:<38} {:<18} {:<14} {:<10} {:>5}  {}\n",
        "ID".bold(),
        "AGENT".bold(),
        "STATUS".bold(),
        "WAITING".bold(),
        "TURNS".bold(),
        "SPENT".bold()
    );
    for t in tasks {
        let status = t["status"].as_str().unwrap_or("-");
        out.push_str(&format!(
            "  {:<38} {:<18} {:<14} {:<10} {:>5}  {}\n",
            t["id"].as_str().unwrap_or("-"),
            clip(t["agent"].as_str().unwrap_or("-"), 18),
            // padding is computed on the plain text, so the colour codes do not break the columns
            format!(
                "{}{}",
                status_colored(status),
                " ".repeat(14usize.saturating_sub(status.len()))
            ),
            t["pause_kind"].as_str().unwrap_or(""),
            t["turns"].as_i64().unwrap_or(0),
            spent(t)
        ));
    }
    out
}

pub fn render_task(t: &Value) -> String {
    let status = t["status"].as_str().unwrap_or("-");
    let mut out = format!(
        "\n  {} {}\n  Agent:   {}\n  Status:  {}\n  Turns:   {}\n  Spent:   {}\n",
        "Task".bold(),
        t["id"].as_str().unwrap_or("-").bold(),
        t["agent"].as_str().unwrap_or("-"),
        status_colored(status),
        t["turns"].as_i64().unwrap_or(0),
        spent(t)
    );
    if let Some(o) = t["owner"].as_str() {
        out.push_str(&format!("  Running on: {o}\n"));
    }
    if let Some(b) = t["budget"].as_object() {
        let l = &b["limits"];
        let mut caps = vec![];
        if let Some(v) = l["max_cost_usd"].as_f64() {
            caps.push(format!("${v:.2}"));
        }
        if let Some(v) = l["max_tokens"].as_i64() {
            caps.push(format!("{v} tokens"));
        }
        if let Some(v) = l["max_active_seconds"].as_i64() {
            caps.push(format!("{v}s"));
        }
        if let Some(v) = l["max_turns"].as_i64() {
            caps.push(format!("{v} turns"));
        }
        if !caps.is_empty() {
            out.push_str(&format!("  Budget:  {}\n", caps.join(", ")));
        }
    }
    match t["pause_kind"].as_str() {
        Some("budget") => {
            let p = &t["budget_pause"];
            let id = t["id"].as_str().unwrap_or("<id>");
            let scope = p["scope"].as_str().unwrap_or("");
            if scope == "kitchen" || scope == "provider" {
                // The cap belongs to the kitchen, not to this task: an extension would change nothing.
                out.push_str(&format!(
                    "\n  {} the {} budget is used up ({} of {}).\n  Someone with budget rights raises it (or it resets with its period), then:  agentoven task resume {} --approve\n  End it:  agentoven task cancel {}\n",
                    "Paused:".yellow().bold(),
                    scope,
                    p["spent"],
                    p["cap"],
                    id,
                    id,
                ));
            } else {
                out.push_str(&format!(
                    "\n  {} at its {} cap: {} of {} used.\n  Raise it:  agentoven task resume {} --add-cost 5   (or --add-tokens, --add-seconds, --add-turns)\n  End it:    agentoven task cancel {}\n",
                    "Paused".yellow().bold(),
                    p["dimension"].as_str().unwrap_or("budget"),
                    p["spent"],
                    p["cap"],
                    id,
                    id,
                ));
            }
        }
        Some("approval") => {
            let names: Vec<String> = t["pending"]["gated_names"]
                .as_array()
                .map(|v| {
                    v.iter()
                        .filter_map(|x| x.as_str().map(String::from))
                        .collect()
                })
                .unwrap_or_default();
            out.push_str(&format!(
                "\n  {} waiting for approval of: {}\n  Decide:  agentoven task resume {} --approve   (or --deny --note \"why\")\n",
                "Paused".yellow().bold(),
                names.join(", "),
                t["id"].as_str().unwrap_or("<id>"),
            ));
        }
        Some("stall") => {
            let s = &t["stall"];
            out.push_str(&format!(
                "\n  {} it called {} {} times in a row.\n  Let it carry on:  agentoven task resume {} --approve --note \"try a different approach\"\n  End it:           agentoven task cancel {}\n",
                "Paused:".yellow().bold(),
                s["tool"].as_str().unwrap_or("a tool"),
                s["count"],
                t["id"].as_str().unwrap_or("<id>"),
                t["id"].as_str().unwrap_or("<id>"),
            ));
        }
        _ => {}
    }
    if status == "interrupted" {
        out.push_str(&format!(
            "\n  It stopped without finishing. Continue it:  agentoven task resume {}\n",
            t["id"].as_str().unwrap_or("<id>")
        ));
    }
    if let Some(e) = t["error"].as_str().filter(|e| !e.is_empty()) {
        out.push_str(&format!("  Error:   {}\n", clip(e, 300)));
    }
    if t["result_blocked"].as_bool() == Some(true) {
        out.push_str("\n  The answer was withheld by the agent's output guardrails.\n");
    } else if let Some(r) = t["result"].as_str().filter(|r| !r.is_empty()) {
        out.push_str(&format!(
            "\n  {}\n  {}\n",
            "Answer".bold(),
            r.replace('\n', "\n  ")
        ));
    }
    out
}

// ── events ───────────────────────────────────────────────────

/// One server-sent event: its `id`, `event` and `data` fields.
#[derive(Debug, Default, PartialEq)]
pub struct Sse {
    pub id: Option<i64>,
    pub event: String,
    pub data: String,
}

/// Feeds SSE bytes in and yields the events they complete. Comment lines (": ping") are skipped.
#[derive(Default)]
pub struct SseParser {
    buf: String,
    cur: Sse,
    has_data: bool,
}

impl SseParser {
    pub fn feed(&mut self, chunk: &str) -> Vec<Sse> {
        self.buf.push_str(chunk);
        let mut out = vec![];
        while let Some(i) = self.buf.find('\n') {
            let line: String = self.buf.drain(..=i).collect();
            let line = line.trim_end_matches(['\n', '\r']);
            if line.is_empty() {
                if self.has_data || !self.cur.event.is_empty() {
                    out.push(std::mem::take(&mut self.cur));
                }
                self.has_data = false;
            } else if line.starts_with(':') {
                // comment / keep-alive
            } else if let Some(v) = line.strip_prefix("id:") {
                self.cur.id = v.trim().parse().ok();
            } else if let Some(v) = line.strip_prefix("event:") {
                self.cur.event = v.trim().to_string();
            } else if let Some(v) = line.strip_prefix("data:") {
                if self.has_data {
                    self.cur.data.push('\n');
                }
                self.cur.data.push_str(v.strip_prefix(' ').unwrap_or(v));
                self.has_data = true;
            }
        }
        out
    }
}

/// One line for a task event, or None for one not worth a line.
pub fn render_event(e: &Sse) -> Option<String> {
    let v: Value = serde_json::from_str(&e.data).ok()?;
    let ev = &v["event"];
    let seq = e.id.unwrap_or(v["seq"].as_i64().unwrap_or(0));
    let kind = ev["type"].as_str().unwrap_or(&e.event);
    let body = match kind {
        "task_status" => format!(
            "status: {}{}",
            ev["content"].as_str().unwrap_or(""),
            ev["error"]
                .as_str()
                .map(|e| format!(" ({})", clip(e, 120)))
                .unwrap_or_default()
        ),
        "token" | "thinking_token" => return None, // too fine-grained for a line each
        "tool_call" | "tool_start" => format!(
            "tool: {}",
            ev["tool_call"]["name"]
                .as_str()
                .unwrap_or_else(|| ev["content"].as_str().unwrap_or(""))
        ),
        "tool_result" => format!(
            "tool result: {}",
            clip(
                ev["tool_result"]["content"]
                    .as_str()
                    .unwrap_or("")
                    .lines()
                    .next()
                    .unwrap_or(""),
                100
            )
        ),
        "turn_end" => format!("turn {} done", ev["turn"]),
        "budget_warning" => "budget warning".to_string(),
        "budget_paused" => "paused at the budget".to_string(),
        "done" => format!(
            "done: {}",
            clip(
                ev["content"]
                    .as_str()
                    .unwrap_or("")
                    .lines()
                    .next()
                    .unwrap_or(""),
                100
            )
        ),
        other => other.to_string(),
    };
    Some(format!("  [{seq}] {body}\n"))
}

async fn stream_events(
    cfg: &AgentOvenConfig,
    id: &str,
    after: i64,
    raw: bool,
    out: &mut impl Write,
) -> anyhow::Result<()> {
    let base = reqwest::Url::parse(&cfg.url)?;
    let mut url = base.join(&task_path(id, "/events"))?;
    if after > 0 {
        url.query_pairs_mut()
            .append_pair("after", &after.to_string());
    }
    let mut req = reqwest::Client::new()
        .get(url)
        .header("Accept", "text/event-stream");
    if let Some(k) = cfg.auth_credential() {
        req = req.bearer_auth(k);
    }
    if let Some(k) = &cfg.kitchen {
        req = req
            .header("X-Kitchen", k.as_str())
            .header("X-Kitchen-Id", k.as_str());
    }
    let mut resp = req.send().await?;
    if !resp.status().is_success() {
        anyhow::bail!("could not follow task {id}: {}", resp.status());
    }
    let mut parser = SseParser::default();
    while let Some(chunk) = resp.chunk().await? {
        for e in parser.feed(&String::from_utf8_lossy(&chunk)) {
            if raw {
                writeln!(out, "{}", e.data)?;
            } else if let Some(line) = render_event(&e) {
                write!(out, "{line}")?;
            }
            out.flush()?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use agentoven_core::testing::{fake_control_plane, json as reply, Reply, Routes};
    use clap::Parser;

    #[derive(Parser)]
    struct Wrap {
        #[command(subcommand)]
        cmd: TaskCommands,
    }

    fn parse(args: &[&str]) -> TaskCommands {
        let mut v = vec!["task"];
        v.extend(args);
        Wrap::try_parse_from(v).unwrap().cmd
    }

    #[test]
    fn the_commands_are_well_formed() {
        use clap::CommandFactory;
        Wrap::command().debug_assert();
    }

    #[test]
    fn start_builds_the_request_with_only_the_caps_that_were_given() {
        let TaskCommands::Start(a) = parse(&[
            "start",
            "researcher",
            "find the cause",
            "--max-cost",
            "2.5",
            "--max-turns",
            "40",
            "--meta",
            "ticket=OPS-1",
            "--session",
            "s1",
        ]) else {
            panic!()
        };
        let b = start_body(&a).unwrap();
        assert_eq!(
            b,
            json!({"agent": "researcher", "message": "find the cause", "session_id": "s1",
            "budget": {"max_cost_usd": 2.5, "max_turns": 40}, "metadata": {"ticket": "OPS-1"}})
        );
        let TaskCommands::Start(a) = parse(&["start", "researcher", "go"]) else {
            panic!()
        };
        assert_eq!(
            start_body(&a).unwrap(),
            json!({"agent": "researcher", "message": "go"}),
            "no caps: the server's defaults apply"
        );
    }

    #[test]
    fn start_refuses_what_the_server_would() {
        for args in [
            vec!["start", "a", "  "],
            vec!["start", "a", "go", "--max-cost", "0"],
            vec!["start", "a", "go", "--max-tokens=-5"],
            vec!["start", "a", "go", "--meta", "nonsense"],
        ] {
            let TaskCommands::Start(a) = parse(&args) else {
                panic!()
            };
            assert!(start_body(&a).is_err(), "{args:?}");
        }
    }

    #[test]
    fn resume_sends_a_decision_only_when_one_is_given() {
        let TaskCommands::Resume(a) = parse(&["resume", "t1"]) else {
            panic!()
        };
        assert!(
            resume_body(&a).unwrap().is_none(),
            "an interrupted task needs no decision"
        );

        let TaskCommands::Resume(a) =
            parse(&["resume", "t1", "--add-cost", "5", "--reason", "worth it"])
        else {
            panic!()
        };
        let b = resume_body(&a).unwrap().unwrap();
        assert_eq!(b["approved"], json!(true));
        assert_eq!(b["extend"]["add_cost_usd"], json!(5.0));
        assert_eq!(b["extend"]["reason"], json!("worth it"));
        let id = b["extend"]["id"].as_str().unwrap();
        assert_eq!(
            id.len(),
            36,
            "every extension carries an ID so a retry applies once"
        );

        let TaskCommands::Resume(a) = parse(&["resume", "t1", "--deny", "--note", "not that tool"])
        else {
            panic!()
        };
        assert_eq!(
            resume_body(&a).unwrap().unwrap(),
            json!({"approved": false, "note": "not that tool"})
        );

        for args in [
            vec!["resume", "t1", "--deny", "--add-tokens", "10"],
            vec!["resume", "t1", "--reason", "why"],
            vec!["resume", "t1", "--add-cost=-1"],
        ] {
            let TaskCommands::Resume(a) = parse(&args) else {
                panic!()
            };
            assert!(resume_body(&a).is_err(), "{args:?}");
        }
        assert!(Wrap::try_parse_from(["task", "resume", "t1", "--approve", "--deny"]).is_err());
    }

    fn routes() -> Routes {
        let paused = json!({"id": "t1", "agent": "researcher", "status": "paused", "pause_kind": "budget", "turns": 7,
            "usage": {"estimated_cost": 4.97, "total_tokens": 120000},
            "budget_pause": {"dimension": "cost", "spent": 4.97, "cap": 5.0},
            "budget": {"limits": {"max_cost_usd": 5.0, "max_turns": 200}}});
        Routes::from([
            (
                "POST /api/v1/tasks".to_string(),
                reply(
                    202,
                    json!({"id": "t1", "status": "queued", "agent": "researcher"}),
                ),
            ),
            (
                "GET /api/v1/tasks".to_string(),
                reply(200, json!({"count": 1, "tasks": [paused.clone()]})),
            ),
            (
                "GET /api/v1/tasks?status=paused".to_string(),
                reply(200, json!({"count": 1, "tasks": [paused.clone()]})),
            ),
            ("GET /api/v1/tasks/t1".to_string(), reply(200, paused)),
            (
                "POST /api/v1/tasks/t1/cancel".to_string(),
                reply(200, json!({"id": "t1", "status": "cancelled"})),
            ),
            (
                "POST /api/v1/tasks/t1/resume".to_string(),
                reply(202, json!({"id": "t1", "status": "queued"})),
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
        ])
    }

    async fn setup() -> (AgentOvenClient, agentoven_core::testing::Log) {
        let (base, log) = fake_control_plane(routes()).await;
        (
            AgentOvenClient::new(&base).unwrap().with_kitchen("acme"),
            log,
        )
    }

    async fn go(client: &AgentOvenClient, args: &[&str]) -> anyhow::Result<String> {
        colored::control::set_override(false);
        run(parse(args), client, None, Duration::from_millis(1)).await
    }

    /// A resume signed with the password "pw", without going through the environment.
    async fn go_signed(client: &AgentOvenClient, args: &[&str]) -> anyhow::Result<String> {
        colored::control::set_override(false);
        let TaskCommands::Resume(a) = parse(args) else {
            panic!("not a resume")
        };
        resume_with(client, a, Signing::Password("pw".into())).await
    }

    #[tokio::test]
    async fn start_posts_to_the_tasks_route_in_the_active_kitchen() {
        let (c, log) = setup().await;
        let out = go(
            &c,
            &["start", "researcher", "find the cause", "--max-cost", "5"],
        )
        .await
        .unwrap();
        assert!(
            out.contains("Task t1 started") && out.contains("agentoven task events t1"),
            "{out}"
        );
        let seen = log.last();
        assert_eq!(
            (seen.method.as_str(), seen.path.as_str()),
            ("POST", "/api/v1/tasks")
        );
        assert_eq!(seen.headers["x-kitchen"], "acme");
        assert_eq!(seen.body["budget"]["max_cost_usd"], json!(5.0));
    }

    #[tokio::test]
    async fn start_wait_stops_at_a_pause_and_says_what_to_do() {
        let (c, log) = setup().await;
        let out = go(&c, &["start", "researcher", "go", "--wait"])
            .await
            .unwrap();
        assert!(log.calls().contains(&"GET /api/v1/tasks/t1".to_string()));
        assert!(
            out.contains("Paused")
                && out.contains("cost cap")
                && out.contains("agentoven task resume t1 --add-cost"),
            "{out}"
        );
    }

    #[tokio::test]
    async fn list_get_and_cancel() {
        let (c, log) = setup().await;
        let out = go(&c, &["list"]).await.unwrap();
        assert!(
            out.contains("t1")
                && out.contains("researcher")
                && out.contains("paused")
                && out.contains("budget")
                && out.contains("$4.9700"),
            "{out}"
        );
        go(&c, &["ls", "--status", "paused"]).await.unwrap();
        assert_eq!(log.last().path, "/api/v1/tasks?status=paused");
        let out = go(&c, &["get", "t1"]).await.unwrap();
        assert!(out.contains("Budget:  $5.00, 200 turns"), "{out}");
        let out = go(&c, &["cancel", "t1"]).await.unwrap();
        assert!(out.contains("Task t1 is cancelled"), "{out}");
        assert_eq!(log.last().path, "/api/v1/tasks/t1/cancel");
    }

    #[tokio::test]
    async fn resume_extends_the_budget_unsigned_on_a_server_that_does_not_sign() {
        let (c, log) = setup().await;
        let out = go(
            &c,
            &[
                "resume",
                "t1",
                "--add-cost",
                "10",
                "--reason",
                "close to done",
            ],
        )
        .await
        .unwrap();
        assert!(
            out.contains("Task t1 resumed (queued)") && !out.contains("signed"),
            "{out}"
        );
        let seen = log.last();
        assert_eq!(seen.path, "/api/v1/tasks/t1/resume");
        assert_eq!(seen.body["extend"]["add_cost_usd"], json!(10.0));
        assert!(!seen.headers.contains_key("x-signing-token"));
    }

    #[tokio::test]
    async fn resume_signs_the_decision_for_the_gate_the_pause_waits_at() {
        let (c, log) = setup().await;
        let out = go_signed(&c, &["resume", "t1", "--add-tokens", "50000", "--sign"])
            .await
            .unwrap();
        assert!(out.contains("decision signed"), "{out}");
        let all = log.all();
        let challenge = all
            .iter()
            .find(|s| s.path == "/api/v1/signatures/challenge")
            .unwrap();
        assert_eq!(challenge.body["gate"], json!("budget_extension"));
        assert_eq!(challenge.body["record_id"], json!("t1"));
        assert_eq!(challenge.body["meaning"], json!("approved"));
        assert_eq!(
            challenge.body["payload"]["extend"]["add_tokens"],
            json!(50000)
        );
        let resume = all.last().unwrap();
        assert_eq!(resume.headers["x-signing-token"], "tok.sig");
        assert_eq!(
            resume.body["extend"], challenge.body["payload"]["extend"],
            "what is signed is what is sent"
        );
    }

    #[tokio::test]
    async fn a_denial_is_signed_as_a_rejection() {
        let (c, log) = setup().await;
        go_signed(
            &c,
            &[
                "resume",
                "t1",
                "--deny",
                "--note",
                "too expensive",
                "--sign",
            ],
        )
        .await
        .unwrap();
        let challenge = log
            .all()
            .into_iter()
            .find(|s| s.path == "/api/v1/signatures/challenge")
            .unwrap();
        assert_eq!(challenge.body["meaning"], json!("rejected"));
        assert_eq!(challenge.body["payload"], json!({"note": "too expensive"}));
    }

    #[test]
    fn the_event_stream_parses_across_chunk_boundaries_and_skips_pings() {
        let mut p = SseParser::default();
        let mut got = vec![];
        for chunk in [
            "id: 1\nevent: task_st",
            "atus\ndata: {\"seq\":1}\n\n: pi",
            "ng\n\nid: 2\nevent: done\ndata: {\"a\":\n",
            "data: 1}\n\n",
        ] {
            got.extend(p.feed(chunk));
        }
        assert_eq!(got.len(), 2, "{got:?}");
        assert_eq!(
            got[0],
            Sse {
                id: Some(1),
                event: "task_status".into(),
                data: "{\"seq\":1}".into()
            }
        );
        assert_eq!(got[1].id, Some(2));
        assert_eq!(
            got[1].data, "{\"a\":\n1}",
            "multi-line data joins with a newline"
        );
    }

    #[test]
    fn events_render_as_short_lines_and_tokens_are_left_out() {
        let status = Sse {
            id: Some(3),
            event: "task_status".into(),
            data: json!({"seq": 3, "event": {"type": "task_status", "content": "paused"}})
                .to_string(),
        };
        assert_eq!(render_event(&status).unwrap(), "  [3] status: paused\n");
        let tok = Sse {
            id: Some(4),
            event: "token".into(),
            data: json!({"seq": 4, "event": {"type": "token", "content": "he"}}).to_string(),
        };
        assert!(render_event(&tok).is_none());
        let res = Sse { id: Some(5), event: "tool_result".into(), data: json!({"seq": 5, "event": {"type": "tool_result", "tool_result": {"content": "line one\nline two"}}}).to_string() };
        assert_eq!(render_event(&res).unwrap(), "  [5] tool result: line one\n");
    }

    #[tokio::test]
    async fn events_streams_from_the_tasks_event_route() {
        let body = "id: 1\nevent: task_status\ndata: {\"seq\":1,\"event\":{\"type\":\"task_status\",\"content\":\"running\"}}\n\nid: 2\nevent: task_status\ndata: {\"seq\":2,\"event\":{\"type\":\"task_status\",\"content\":\"completed\"}}\n\n";
        let routes = Routes::from([
            (
                "GET /api/v1/tasks/t1/events?after=0".to_string(),
                Reply::Raw(200, body.to_string(), vec![]),
            ),
            (
                "GET /api/v1/tasks/t1/events".to_string(),
                Reply::Raw(200, body.to_string(), vec![]),
            ),
        ]);
        let (base, log) = fake_control_plane(routes).await;
        let cfg = AgentOvenConfig {
            url: base,
            kitchen: Some("acme".into()),
            ..Default::default()
        };
        let mut out = Vec::new();
        stream_events(&cfg, "t1", 0, false, &mut out).await.unwrap();
        let text = String::from_utf8(out).unwrap();
        assert_eq!(text, "  [1] status: running\n  [2] status: completed\n");
        assert_eq!(log.last().headers["x-kitchen"], "acme");
    }

    #[test]
    fn a_pause_at_the_kitchens_budget_is_not_answered_with_an_extension() {
        colored::control::set_override(false);
        let out = render_task(
            &json!({"id": "t1", "agent": "a", "status": "paused", "pause_kind": "budget", "turns": 2, "usage": {},
            "budget_pause": {"dimension": "cost", "scope": "kitchen", "spent": 100.0, "cap": 100.0}}),
        );
        assert!(
            out.contains("kitchen budget is used up")
                && out.contains("agentoven task resume t1 --approve"),
            "{out}"
        );
        assert!(!out.contains("--add-cost"), "{out}");
    }

    #[test]
    fn a_task_the_journal_reports_as_running_elsewhere_names_its_replica() {
        colored::control::set_override(false);
        let out = render_task(
            &json!({"id": "t1", "agent": "a", "status": "running", "owner": "replica-b", "turns": 2, "usage": {}}),
        );
        assert!(out.contains("Running on: replica-b"), "{out}");
        let out = render_task(
            &json!({"id": "t1", "agent": "a", "status": "interrupted", "turns": 2, "usage": {}}),
        );
        assert!(out.contains("agentoven task resume t1"), "{out}");
    }
}

//! `agentoven trace` — inspect traces and observability data.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Value};

use super::signing::{clip, json_arg, pretty, signing_hint, SignArgs};

#[derive(Subcommand)]
pub enum TraceCommands {
    /// List recent traces.
    Ls(TraceLsArgs),
    /// Inspect a specific trace.
    Get(TraceGetArgs),
    /// Show cost summary.
    Cost(CostArgs),
    /// Show audit log.
    Audit(AuditArgs),
    /// Lay a trace out on a clock: the tree, offsets, critical path, retries and governance events.
    Timeline(TimelineArgs),
    /// The audit events recorded for one trace (and the traces under it), in order.
    Events(TraceGetArgs),
    /// Saved views of the trace list.
    #[command(subcommand)]
    Views(ViewCommands),
    /// Re-run a captured call or run with edits (Pro). A replay is an investigation, never evidence.
    #[command(subcommand)]
    Replay(ReplayCommands),
    /// A model call as it was captured, whole: the request with its tools and the response (Pro).
    Capture(CaptureArgs),
}

#[derive(Subcommand)]
pub enum ReplayCommands {
    /// Re-run one captured model call as up to five variants. Runs no tool.
    Call(ReplayCallArgs),
    /// Re-run a run from one of its model calls: tools frozen (default), blocked, or live (signed).
    Run(ReplayRunArgs),
    /// Check a live replay and show what would be signed for it.
    Prepare(ReplayPrepareArgs),
}

#[derive(Args)]
pub struct ReplayCallArgs {
    /// Trace ID.
    pub trace_id: String,
    /// Span ID of the model call.
    pub span_id: String,
    /// A variant, as JSON: {"label": "cooler", "edits": {"temperature": 0.1}}. Repeat for more (up to five).
    /// Edits: model, provider, temperature, max_tokens, top_p, stop, thinking_enabled, thinking_budget,
    /// tool_choice, response_format, messages ([{"index": 1, "content": "..."}]).
    #[arg(long = "variant")]
    pub variants: Vec<String>,
    /// Read the variants from a JSON file (an array).
    #[arg(long)]
    pub variants_file: Option<String>,
    /// Why (recorded; required for an archived agent's trace).
    #[arg(long)]
    pub reason: Option<String>,
    /// Print the whole result as JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct ReplayRunArgs {
    /// Trace ID.
    pub trace_id: String,
    /// Span ID of the model call to start at.
    #[arg(long)]
    pub from_span: String,
    /// The edits, as JSON (same keys as a variant's).
    #[arg(long)]
    pub edits: Option<String>,
    /// What tool calls do: frozen (captured results), blocked (returned, not run) or live (signed).
    #[arg(long, default_value = "frozen")]
    pub tools: String,
    /// A tool that may run live (only with --tools live, only if the agent marked it idempotent). Repeat.
    #[arg(long = "live-tool")]
    pub live_tools: Vec<String>,
    #[arg(long)]
    pub reason: Option<String>,
    /// Print the whole result as JSON.
    #[arg(long)]
    pub json: bool,
    #[command(flatten)]
    pub signing: SignArgs,
}

#[derive(Args)]
pub struct ReplayPrepareArgs {
    /// Trace ID.
    pub trace_id: String,
    #[arg(long)]
    pub from_span: String,
    #[arg(long = "live-tool", required = true)]
    pub live_tools: Vec<String>,
    #[arg(long)]
    pub edits: Option<String>,
    #[arg(long)]
    pub reason: Option<String>,
}

#[derive(Args)]
pub struct CaptureArgs {
    /// Trace ID.
    pub trace_id: String,
    /// Span ID.
    pub span_id: String,
}

#[derive(Subcommand)]
pub enum ViewCommands {
    /// List your saved views and the kitchen's shared ones.
    Ls,
    /// Save filters under a name.
    Save(ViewSaveArgs),
    /// Delete a saved view.
    Rm(ViewRmArgs),
}

#[derive(Args)]
pub struct ViewSaveArgs {
    /// Name of the view.
    pub name: String,
    /// Share it with everyone in the kitchen.
    #[arg(long)]
    pub shared: bool,
    #[arg(long)]
    pub agent: Option<String>,
    #[arg(long)]
    pub status: Option<String>,
    /// Environment slug.
    #[arg(long, alias = "env")]
    pub environment: Option<String>,
    /// Baked agent version.
    #[arg(long = "agent-version")]
    pub agent_version: Option<String>,
    /// Time range: 1h, 24h, 7d or 30d.
    #[arg(long)]
    pub range: Option<String>,
}

#[derive(Args)]
pub struct ViewRmArgs {
    /// View ID.
    pub view_id: String,
}

#[derive(Args)]
pub struct TimelineArgs {
    /// Trace ID.
    pub trace_id: String,
    /// Most spans to show.
    #[arg(long, default_value = "200")]
    pub limit: u32,
}

#[derive(Args)]
pub struct TraceLsArgs {
    /// Filter by agent name.
    #[arg(long, short)]
    pub agent: Option<String>,
    /// Number of traces to show.
    #[arg(long, short, default_value = "20")]
    pub limit: u32,
    /// Only traces for this environment slug.
    #[arg(long, alias = "env")]
    pub environment: Option<String>,
    /// Only traces of this baked agent version.
    #[arg(long = "agent-version")]
    pub agent_version: Option<String>,
    /// Only traces with this status.
    #[arg(long)]
    pub status: Option<String>,
    /// Apply a saved view (its ID); anything named here wins over it.
    #[arg(long)]
    pub view: Option<String>,
}

#[derive(Args)]
pub struct TraceGetArgs {
    /// Trace ID.
    pub trace_id: String,
}

#[derive(Args)]
pub struct CostArgs {
    /// Time range (e.g., "24h", "7d", "30d").
    #[arg(long, short, default_value = "24h")]
    pub range: String,
    /// Group by agent, model, or kitchen.
    #[arg(long, short, default_value = "agent")]
    pub group_by: String,
}

#[derive(Args)]
pub struct AuditArgs {
    /// Number of recent audit events.
    #[arg(long, short, default_value = "20")]
    pub limit: u32,
}

pub async fn execute(cmd: TraceCommands) -> anyhow::Result<()> {
    match cmd {
        TraceCommands::Ls(args) => ls(args).await,
        TraceCommands::Get(args) => get(args).await,
        TraceCommands::Cost(args) => cost(args).await,
        TraceCommands::Audit(args) => audit(args).await,
        TraceCommands::Timeline(args) => timeline(args).await,
        TraceCommands::Events(args) => events(args).await,
        TraceCommands::Views(cmd) => views(cmd).await,
        TraceCommands::Replay(cmd) => {
            let client = AgentOvenClient::from_env()?;
            finish(replay(cmd, &client).await)
        }
        TraceCommands::Capture(a) => {
            let client = AgentOvenClient::from_env()?;
            finish(capture(a, &client).await)
        }
    }
}

fn finish(out: anyhow::Result<String>) -> anyhow::Result<()> {
    match out {
        Ok(text) => {
            print!("{text}");
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

/// `trace capture`: the call as it was captured, as JSON.
pub async fn capture(a: CaptureArgs, client: &AgentOvenClient) -> anyhow::Result<String> {
    let c = client.span_capture(&a.trace_id, &a.span_id).await?;
    Ok(format!("{}\n", pretty(&c)))
}

fn parse_edits(edits: &Option<String>) -> anyhow::Result<Value> {
    match edits {
        Some(e) => json_arg("--edits", e),
        None => Ok(json!({})),
    }
}

/// `trace replay ...`: run it against `client` and return what it prints.
pub async fn replay(cmd: ReplayCommands, client: &AgentOvenClient) -> anyhow::Result<String> {
    match cmd {
        ReplayCommands::Call(a) => {
            let mut variants = Vec::new();
            if let Some(f) = &a.variants_file {
                match json_arg("--variants-file", &std::fs::read_to_string(f)?)? {
                    Value::Array(vs) => variants.extend(vs),
                    _ => anyhow::bail!("--variants-file holds a JSON array of variants"),
                }
            }
            for v in &a.variants {
                variants.push(json_arg("--variant", v)?);
            }
            let out = client
                .replay_call(&a.trace_id, &a.span_id, &variants, a.reason.as_deref().unwrap_or(""))
                .await?;
            if a.json {
                return Ok(format!("{}\n", pretty(&out)));
            }
            let mut text = format!("\n  🔁 Replay of span {} (trace {})\n", a.span_id.bold(), a.trace_id);
            if let Some(n) = out["notice"].as_str() {
                text.push_str(&format!("  {} {}\n", "ⓘ".dimmed(), n.dimmed()));
            }
            text.push('\n');
            for v in out["variants"].as_array().cloned().unwrap_or_default() {
                let status = v["status"].as_str().unwrap_or("-");
                let shown = match status {
                    "completed" => status.green().to_string(),
                    "failed" => status.red().to_string(),
                    _ => status.yellow().to_string(),
                };
                text.push_str(&format!(
                    "  #{} {:<14} {:<10} {}\n",
                    v["index"].as_u64().unwrap_or(0),
                    v["label"].as_str().unwrap_or(""),
                    shown,
                    v["model"].as_str().unwrap_or("")
                ));
                if let Some(r) = v["reason"].as_str() {
                    text.push_str(&format!("     {}\n", r.dimmed()));
                }
                if let Some(c) = v["content"].as_str() {
                    text.push_str(&format!("     {}\n", clip(&c.replace('\n', " "), 160)));
                }
                for t in v["tool_calls"].as_array().cloned().unwrap_or_default() {
                    text.push_str(&format!("     wants tool {} (not run)\n", t["name"].as_str().unwrap_or("?").cyan()));
                }
                if let Some(t) = v["trace_id"].as_str() {
                    text.push_str(&format!("     {} trace {}\n", "→".dimmed(), t));
                }
            }
            Ok(text)
        }
        ReplayCommands::Run(a) => {
            let edits = parse_edits(&a.edits)?;
            let signing = a.signing.resolve()?;
            let out = client
                .replay_run(
                    &a.trace_id,
                    &a.from_span,
                    &edits,
                    &a.tools,
                    &a.live_tools,
                    a.reason.as_deref().unwrap_or(""),
                    signing.signature(),
                )
                .await?;
            if a.json {
                return Ok(format!("{}\n", pretty(&out)));
            }
            let mut text = format!("\n  🔁 Replay of trace {} from span {} (tools: {})\n", a.trace_id.bold(), a.from_span, a.tools);
            if let Some(n) = out["notice"].as_str() {
                text.push_str(&format!("  {} {}\n", "ⓘ".dimmed(), n.dimmed()));
            }
            text.push_str(&format!("  status: {}\n\n", out["status"].as_str().unwrap_or("-")));
            for st in out["steps"].as_array().cloned().unwrap_or_default() {
                let kind = st["kind"].as_str().unwrap_or("-");
                let line = if kind == "tool" {
                    format!("tool {} [{}]", st["tool"].as_str().unwrap_or("?"), st["source"].as_str().unwrap_or("-"))
                } else {
                    clip(&st["content"].as_str().unwrap_or("").replace('\n', " "), 120)
                };
                text.push_str(&format!("  {:>2}. {:<6} {}\n", st["n"].as_u64().unwrap_or(0), kind, line));
            }
            if out["divergence"].is_object() {
                text.push_str(&format!("\n  {} the run diverged; use --json to see both sides\n", "⚠".yellow().bold()));
            }
            Ok(text)
        }
        ReplayCommands::Prepare(a) => {
            let edits = parse_edits(&a.edits)?;
            let prep = client
                .prepare_live_replay(&a.trace_id, &a.from_span, &edits, &a.live_tools, a.reason.as_deref().unwrap_or(""))
                .await?;
            Ok(format!("{}\n", pretty(&prep)))
        }
    }
}

async fn ls(args: TraceLsArgs) -> anyhow::Result<()> {
    println!("\n  🔍 Recent traces (last {}):\n", args.limit);

    let client = agentoven_core::AgentOvenClient::from_env()?;
    let limit = args.limit.to_string();
    let filters = [
        ("limit", limit.as_str()),
        ("agent", args.agent.as_deref().unwrap_or("")),
        ("environment", args.environment.as_deref().unwrap_or("")),
        ("agent_version", args.agent_version.as_deref().unwrap_or("")),
        ("status", args.status.as_deref().unwrap_or("")),
        ("view", args.view.as_deref().unwrap_or("")),
    ];
    match client.list_traces_filtered(&filters).await {
        Ok(traces) => {
            if traces.is_empty() {
                println!("  (no traces yet — traces appear when agents are invoked)");
            } else {
                println!(
                    "  {:<36} {:<16} {:<10} {:<10} {:<10} {:<10} {}",
                    "TRACE ID".bold(),
                    "AGENT".bold(),
                    "STATUS".bold(),
                    "LATENCY".bold(),
                    "COST".bold(),
                    "ENV".bold(),
                    "VERSION".bold()
                );
                println!("  {}", "─".repeat(104).dimmed());
                for t in &traces {
                    let id = t["id"].as_str().unwrap_or("-");
                    let id_short = if id.len() > 34 { &id[..34] } else { id };
                    let agent = t["agent_name"].as_str().unwrap_or("-");
                    let status = t["status"].as_str().unwrap_or("-");
                    let latency = t["duration_ms"]
                        .as_u64()
                        .map(|v| format!("{}ms", v))
                        .unwrap_or("-".into());
                    let cost = t["cost_usd"]
                        .as_f64()
                        .map(|v| format!("${:.4}", v))
                        .unwrap_or("-".into());
                    let env = t["environment"].as_str().unwrap_or("-");
                    let version = t["agent_version"].as_str().unwrap_or("-");
                    println!(
                        "  {:<36} {:<16} {:<10} {:<10} {:<10} {:<10} {}",
                        id_short, agent, status, latency, cost, env, version
                    );
                }
                println!("\n  {} {} trace(s)", "→".dimmed(), traces.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch traces: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!("  (traces appear when agents are invoked)");
        }
    }
    Ok(())
}

async fn get(args: TraceGetArgs) -> anyhow::Result<()> {
    println!("\n  🔍 Trace: {}\n", args.trace_id.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_trace(&args.trace_id).await {
        Ok(trace) => {
            println!(
                "  {:<16} {}",
                "Trace ID:".bold(),
                trace["id"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Agent:".bold(),
                trace["agent_name"].as_str().unwrap_or("-")
            );
            if let Some(env) = trace["environment"].as_str() {
                println!("  {:<16} {}", "Environment:".bold(), env);
            }
            if let Some(v) = trace["agent_version"].as_str() {
                println!("  {:<16} {}", "Agent version:".bold(), v);
            }
            println!(
                "  {:<16} {}",
                "Status:".bold(),
                trace["status"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Started:".bold(),
                trace["created_at"].as_str().unwrap_or("-")
            );

            if let Some(spans) = trace["spans"].as_array() {
                println!("\n  {} ({} spans):", "Spans".bold(), spans.len());
                println!(
                    "  {:<24} {:<20} {:<12} {:<10}",
                    "SPAN".bold(),
                    "OPERATION".bold(),
                    "DURATION".bold(),
                    "STATUS".bold()
                );
                println!("  {}", "─".repeat(68).dimmed());
                for span in spans {
                    let name = span["name"].as_str().unwrap_or("-");
                    let op = span["kind"].as_str().unwrap_or("-");
                    let dur = span["duration_ms"]
                        .as_u64()
                        .map(|v| format!("{}ms", v))
                        .unwrap_or("-".into());
                    let st = span["status"].as_str().unwrap_or("-");
                    println!("  {:<24} {:<20} {:<12} {}", name, op, dur, st);
                }
            }

            println!(
                "\n  {} agentoven trace timeline {}   (the tree on a clock)",
                "→".dimmed(),
                args.trace_id
            );
        }
        Err(e) => {
            println!(
                "  {} Trace not found: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn cost(args: CostArgs) -> anyhow::Result<()> {
    println!(
        "\n  💰 Cost summary (last {}, by {}):\n",
        args.range.cyan(),
        args.group_by
    );

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.model_cost().await {
        Ok(cost_data) => {
            if let Some(items) = cost_data["items"].as_array().or(cost_data.as_array()) {
                if items.is_empty() {
                    println!("  (no cost data yet — costs are tracked when agents invoke models)");
                } else {
                    println!(
                        "  {:<20} {:<12} {:<12} {:<12}",
                        "NAME".bold(),
                        "TOKENS".bold(),
                        "REQUESTS".bold(),
                        "COST (USD)".bold()
                    );
                    println!("  {}", "─".repeat(58).dimmed());
                    let mut total = 0.0f64;
                    for item in items {
                        let name = item["name"].as_str().unwrap_or("-");
                        let tokens = item["total_tokens"].as_u64().unwrap_or(0);
                        let requests = item["requests"].as_u64().unwrap_or(0);
                        let cost_val = item["cost"].as_f64().unwrap_or(0.0);
                        total += cost_val;
                        println!(
                            "  {:<20} {:<12} {:<12} ${:.4}",
                            name, tokens, requests, cost_val
                        );
                    }
                    println!("  {}", "─".repeat(58).dimmed());
                    println!("  {:<44} ${:.4}", "Total".bold(), total);
                }
            } else {
                println!("  (no cost data yet)");
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch costs: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
            println!("  (costs are tracked when agents invoke models)");
        }
    }
    Ok(())
}

async fn audit(args: AuditArgs) -> anyhow::Result<()> {
    println!("\n  📋 Audit log (last {}):\n", args.limit);

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_audit(args.limit).await {
        Ok(events) => {
            if events.is_empty() {
                println!("  (no audit events)");
            } else {
                println!(
                    "  {:<20} {:<16} {:<16} {:<24}",
                    "TIMESTAMP".bold(),
                    "ACTION".bold(),
                    "SUBJECT".bold(),
                    "TARGET".bold()
                );
                println!("  {}", "─".repeat(78).dimmed());
                for ev in &events {
                    let ts = ev["timestamp"].as_str().unwrap_or("-");
                    let ts_short = if ts.len() > 16 { &ts[..16] } else { ts };
                    let action = ev["action"].as_str().unwrap_or("-");
                    let subject = ev["subject"].as_str().unwrap_or("-");
                    let target = ev["target"].as_str().unwrap_or("-");
                    println!(
                        "  {:<20} {:<16} {:<16} {}",
                        ts_short, action, subject, target
                    );
                }
                println!("\n  {} {} event(s)", "→".dimmed(), events.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not fetch audit log: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn timeline(args: TimelineArgs) -> anyhow::Result<()> {
    let client = agentoven_core::AgentOvenClient::from_env()?;
    let tl = client
        .trace_timeline(&args.trace_id, Some(args.limit), None)
        .await?;
    println!("\n  ⏱  Trace {}", args.trace_id.bold());
    println!(
        "  total {}ms · critical path {}ms · {} spans{}",
        tl["total_ms"].as_u64().unwrap_or(0),
        tl["critical_path_ms"].as_u64().unwrap_or(0),
        tl["span_count"].as_u64().unwrap_or(0),
        match (tl["environment"].as_str(), tl["agent_version"].as_str()) {
            (Some(e), Some(v)) => format!(" · {e} · v{v}"),
            (Some(e), None) => format!(" · {e}"),
            (None, Some(v)) => format!(" · v{v}"),
            _ => String::new(),
        }
    );
    println!(
        "\n  {:<44} {:>8} {:>9}  {}",
        "SPAN".bold(),
        "OFFSET".bold(),
        "TIME".bold(),
        "NOTES".bold()
    );
    println!("  {}", "─".repeat(84).dimmed());
    for sp in tl["spans"].as_array().cloned().unwrap_or_default() {
        let depth = sp["depth"].as_u64().unwrap_or(0) as usize;
        let name = format!(
            "{}{} {}",
            "  ".repeat(depth),
            sp["kind"].as_str().unwrap_or("-"),
            sp["name"].as_str().unwrap_or("-")
        );
        let mut notes: Vec<String> = Vec::new();
        if sp["critical"].as_bool().unwrap_or(false) {
            notes.push("critical".into());
        }
        if let Some(n) = sp["attempt"].as_u64() {
            if n > 1 {
                notes.push(format!("attempt {n}"));
            }
        }
        if sp["skew"].as_bool().unwrap_or(false) {
            notes.push("clock skew".into());
        }
        if sp["orphan"].as_bool().unwrap_or(false) {
            notes.push("orphan".into());
        }
        if sp["status"].as_str().is_some_and(|s| s == "failed" || s == "error") {
            notes.push("failed".red().to_string());
        }
        println!(
            "  {:<44} {:>6}ms {:>7}ms  {}",
            name.chars().take(44).collect::<String>(),
            sp["offset_ms"].as_u64().unwrap_or(0),
            sp["duration_ms"].as_u64().unwrap_or(0),
            notes.join(", ")
        );
    }
    if tl["truncated"].as_bool().unwrap_or(false) {
        println!(
            "\n  {} showing {} of {} spans (raise --limit)",
            "…".dimmed(),
            tl["spans"].as_array().map_or(0, |a| a.len()),
            tl["span_count"].as_u64().unwrap_or(0)
        );
    }
    for lane in tl["lanes"].as_array().cloned().unwrap_or_default() {
        let events = lane["events"].as_array().cloned().unwrap_or_default();
        if events.is_empty() {
            continue;
        }
        println!("\n  {} lane:", lane["name"].as_str().unwrap_or("-").bold());
        for e in events {
            println!(
                "  {:>8}ms  {} {}",
                e["offset_ms"].as_i64().unwrap_or(0),
                e["action"].as_str().unwrap_or("-"),
                e["actor"].as_str().unwrap_or("").dimmed()
            );
        }
    }
    println!();
    Ok(())
}

async fn events(args: TraceGetArgs) -> anyhow::Result<()> {
    let client = agentoven_core::AgentOvenClient::from_env()?;
    let out = client.trace_audit(&args.trace_id).await?;
    println!("\n  📋 Audit events of trace {}\n", args.trace_id.bold());
    let events = out["events"].as_array().cloned().unwrap_or_default();
    if events.is_empty() {
        println!("  (none recorded)");
    }
    for e in &events {
        println!(
            "  {:<20} {:<30} {}",
            e["timestamp"].as_str().map_or("-", |t| &t[..t.len().min(19)]),
            e["action"].as_str().unwrap_or("-"),
            e["user_email"].as_str().unwrap_or("").dimmed()
        );
    }
    if let Some(note) = out["coverage"]["note"].as_str() {
        println!("\n  {} {}", "ⓘ".dimmed(), note.dimmed());
    }
    println!();
    Ok(())
}

async fn views(cmd: ViewCommands) -> anyhow::Result<()> {
    let client = agentoven_core::AgentOvenClient::from_env()?;
    match cmd {
        ViewCommands::Ls => {
            let vs = client.list_trace_views().await?;
            if vs.is_empty() {
                println!("  (no saved views — save one with `agentoven trace views save <name> --status failed`)");
            }
            for v in vs {
                println!(
                    "  {}  {}{}",
                    v["id"].as_str().unwrap_or("-").dimmed(),
                    v["name"].as_str().unwrap_or("-").bold(),
                    if v["shared"].as_bool().unwrap_or(false) { "  (shared)" } else { "" }
                );
            }
        }
        ViewCommands::Save(a) => {
            let mut f = serde_json::Map::new();
            for (k, v) in [
                ("agent", &a.agent),
                ("status", &a.status),
                ("environment", &a.environment),
                ("agent_version", &a.agent_version),
                ("range", &a.range),
            ] {
                if let Some(v) = v {
                    f.insert(k.into(), serde_json::Value::String(v.clone()));
                }
            }
            let v = client
                .save_trace_view(&a.name, serde_json::Value::Object(f), a.shared)
                .await?;
            println!("  ✓ saved view {} ({})", a.name.bold(), v["id"].as_str().unwrap_or("-"));
        }
        ViewCommands::Rm(a) => {
            client.delete_trace_view(&a.view_id).await?;
            println!("  ✓ deleted view {}", a.view_id);
        }
    }
    Ok(())
}

#[cfg(test)]
mod replay_tests {
    use super::*;
    use crate::commands::{Cli, Commands};
    use agentoven_core::testing::{fake_control_plane, json as reply, Log, Routes};
    use clap::Parser;

    fn parse(args: &[&str]) -> TraceCommands {
        let mut v = vec!["agentoven", "trace"];
        v.extend_from_slice(args);
        match Cli::try_parse_from(v).expect("parse").command {
            Commands::Trace(c) => c,
            _ => panic!("not a trace command"),
        }
    }

    fn replay_cmd(args: &[&str]) -> ReplayCommands {
        match parse(&[&["replay"], args].concat()) {
            TraceCommands::Replay(c) => c,
            _ => panic!("not a replay command"),
        }
    }

    async fn client(extra: &[(&str, agentoven_core::testing::Reply)]) -> (AgentOvenClient, Log) {
        colored::control::set_override(false);
        let mut routes = Routes::from([
            ("POST /api/v1/signatures/challenge".to_string(), reply(200, json!({"subject": {"digest": "d"}, "reauth": "password"}))),
            ("POST /api/v1/signatures/token".to_string(), reply(200, json!({"signing_token": "tok.sig"}))),
        ]);
        for (k, v) in extra {
            routes.insert(k.to_string(), v.clone());
        }
        let (base, log) = fake_control_plane(routes).await;
        (AgentOvenClient::new(&base).unwrap().with_kitchen("acme"), log)
    }

    #[tokio::test]
    async fn replay_call_sends_the_variants_and_summarises_them() {
        let (c, log) = client(&[(
            "POST /api/v1/traces/t1/spans/s1/replay",
            reply(200, json!({"notice": "This is a replay", "variants": [
                {"index": 0, "label": "cooler", "status": "completed", "model": "claude-haiku", "content": "Sure.", "trace_id": "rt1",
                 "tool_calls": [{"name": "lookup"}]},
                {"index": 1, "status": "blocked", "reason": "input guardrail"}]})),
        )]).await;
        let out = replay(
            replay_cmd(&["call", "t1", "s1", "--variant", r#"{"label":"cooler","edits":{"temperature":0.1}}"#, "--variant", r#"{"edits":{"model":"claude-haiku"}}"#, "--reason", "why"]),
            &c,
        )
        .await
        .unwrap();
        assert_eq!(
            log.last().body,
            json!({"variants": [{"label": "cooler", "edits": {"temperature": 0.1}}, {"edits": {"model": "claude-haiku"}}], "reason": "why"})
        );
        assert!(out.contains("This is a replay") && out.contains("cooler") && out.contains("wants tool lookup (not run)"), "{out}");
        assert!(out.contains("blocked") && out.contains("input guardrail") && out.contains("rt1"), "{out}");
    }

    #[tokio::test]
    async fn replay_call_reads_variants_from_a_file_and_refuses_bad_json() {
        let (c, log) = client(&[("POST /api/v1/traces/t1/spans/s1/replay", reply(200, json!({"variants": []})))]).await;
        let dir = tempfile::tempdir().unwrap();
        let f = dir.path().join("v.json");
        std::fs::write(&f, r#"[{"edits": {"max_tokens": 50}}]"#).unwrap();
        replay(replay_cmd(&["call", "t1", "s1", "--variants-file", f.to_str().unwrap()]), &c).await.unwrap();
        assert_eq!(log.last().body["variants"], json!([{"edits": {"max_tokens": 50}}]));
        std::fs::write(&f, r#"{"not": "an array"}"#).unwrap();
        assert!(replay(replay_cmd(&["call", "t1", "s1", "--variants-file", f.to_str().unwrap()]), &c).await.is_err());
        let e = replay(replay_cmd(&["call", "t1", "s1", "--variant", "{oops"]), &c).await.unwrap_err().to_string();
        assert!(e.contains("--variant"), "{e}");
    }

    #[tokio::test]
    async fn replay_run_is_frozen_by_default_and_refuses_what_the_server_would() {
        let (c, log) = client(&[(
            "POST /api/v1/traces/t1/replay",
            reply(200, json!({"status": "diverged", "steps": [
                {"n": 1, "kind": "model", "content": "calling lookup"},
                {"n": 2, "kind": "tool", "tool": "lookup", "source": "frozen"}], "divergence": {"step": 3}})),
        )]).await;
        let out = replay(replay_cmd(&["run", "t1", "--from-span", "s2", "--edits", r#"{"model":"claude-haiku"}"#]), &c).await.unwrap();
        assert_eq!(log.last().body, json!({"from_span": "s2", "edits": {"model": "claude-haiku"}, "tools": "frozen", "live_tools": [], "reason": ""}));
        assert!(out.contains("diverged") && out.contains("tool lookup [frozen]"), "{out}");
        let n = log.len();
        assert!(replay(replay_cmd(&["run", "t1", "--from-span", "s2", "--tools", "simulated"]), &c).await.is_err());
        assert!(replay(replay_cmd(&["run", "t1", "--from-span", "s2", "--tools", "live"]), &c).await.is_err());
        assert_eq!(log.len(), n);
    }

    #[tokio::test]
    async fn a_live_replay_with_a_held_token_sends_it_and_prepare_shows_what_to_sign() {
        let (c, log) = client(&[
            ("POST /api/v1/traces/t1/replay", reply(200, json!({"status": "completed", "steps": []}))),
            ("POST /api/v1/traces/t1/replay/prepare", reply(200, json!({"gate": "live_tool_replay", "record_id": "t1:s2", "payload": {"x": 1}}))),
        ]).await;
        replay(replay_cmd(&["run", "t1", "--from-span", "s2", "--tools", "live", "--live-tool", "lookup", "--signing-token", "held"]), &c).await.unwrap();
        assert_eq!(log.last().headers["x-signing-token"], "held");
        assert_eq!(log.last().body["live_tools"], json!(["lookup"]));
        let out = replay(replay_cmd(&["prepare", "t1", "--from-span", "s2", "--live-tool", "lookup"]), &c).await.unwrap();
        assert!(out.contains("live_tool_replay") && out.contains("t1:s2"), "{out}");
    }

    #[tokio::test]
    async fn capture_prints_the_call_whole() {
        let (c, _) = client(&[("GET /api/v1/traces/t1/spans/s1/capture", reply(200, json!({"request": {"messages": []}, "response": {"content": "ok"}})))]).await;
        let a = match parse(&["capture", "t1", "s1"]) {
            TraceCommands::Capture(a) => a,
            _ => panic!(),
        };
        assert!(capture(a, &c).await.unwrap().contains("\"content\": \"ok\""));
    }
}

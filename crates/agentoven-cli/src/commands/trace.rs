//! `agentoven trace` — inspect traces and observability data.

use clap::{Args, Subcommand};
use colored::Colorize;

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

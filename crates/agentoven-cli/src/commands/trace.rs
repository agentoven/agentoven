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
}

#[derive(Args)]
pub struct TraceLsArgs {
    /// Filter by agent name.
    #[arg(long, short)]
    pub agent: Option<String>,
    /// Number of traces to show.
    #[arg(long, short, default_value = "20")]
    pub limit: u32,
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
    }
}

async fn ls(args: TraceLsArgs) -> anyhow::Result<()> {
    println!("\n  {} Recent traces (last {}):\n", "🔍".to_string(), args.limit);

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_traces(args.agent.as_deref(), args.limit).await {
        Ok(traces) => {
            if traces.is_empty() {
                println!("  (no traces yet — traces appear when agents are invoked)");
            } else {
                println!(
                    "  {:<36} {:<16} {:<10} {:<10} {:<10}",
                    "TRACE ID".bold(), "AGENT".bold(), "STATUS".bold(), "LATENCY".bold(), "COST".bold()
                );
                println!("  {}", "─".repeat(84).dimmed());
                for t in &traces {
                    let id = t["id"].as_str().unwrap_or("-");
                    let id_short = if id.len() > 34 { &id[..34] } else { id };
                    let agent = t["agent"].as_str().unwrap_or("-");
                    let status = t["status"].as_str().unwrap_or("-");
                    let latency = t["latency_ms"].as_u64().map(|v| format!("{}ms", v)).unwrap_or("-".into());
                    let cost = t["cost"].as_f64().map(|v| format!("${:.4}", v)).unwrap_or("-".into());
                    println!("  {:<36} {:<16} {:<10} {:<10} {}", id_short, agent, status, latency, cost);
                }
                println!("\n  {} {} trace(s)", "→".dimmed(), traces.len());
            }
        }
        Err(e) => {
            println!("  {} Could not fetch traces: {}", "⚠".yellow().bold(), e.to_string().dimmed());
            println!("  (traces appear when agents are invoked)");
        }
    }
    Ok(())
}

async fn get(args: TraceGetArgs) -> anyhow::Result<()> {
    println!("\n  {} Trace: {}\n", "🔍".to_string(), args.trace_id.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_trace(&args.trace_id).await {
        Ok(trace) => {
            println!("  {:<16} {}", "Trace ID:".bold(), trace["id"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Agent:".bold(), trace["agent"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Status:".bold(), trace["status"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Started:".bold(), trace["started_at"].as_str().unwrap_or("-"));

            if let Some(spans) = trace["spans"].as_array() {
                println!("\n  {} ({} spans):", "Spans".bold(), spans.len());
                println!("  {:<24} {:<20} {:<12} {:<10}",
                    "SPAN".bold(), "OPERATION".bold(), "DURATION".bold(), "STATUS".bold()
                );
                println!("  {}", "─".repeat(68).dimmed());
                for span in spans {
                    let name = span["name"].as_str().unwrap_or("-");
                    let op = span["operation"].as_str().unwrap_or("-");
                    let dur = span["duration_ms"].as_u64().map(|v| format!("{}ms", v)).unwrap_or("-".into());
                    let st = span["status"].as_str().unwrap_or("-");
                    println!("  {:<24} {:<20} {:<12} {}", name, op, dur, st);
                }
            }

            println!("\n  {} View in Jaeger: {}", "→".dimmed(),
                format!("http://localhost:16686/trace/{}", args.trace_id).cyan());
        }
        Err(e) => {
            println!("  {} Trace not found: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn cost(args: CostArgs) -> anyhow::Result<()> {
    println!(
        "\n  {} Cost summary (last {}, by {}):\n",
        "💰".to_string(),
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
                    println!("  {:<20} {:<12} {:<12} {:<12}",
                        "NAME".bold(), "TOKENS".bold(), "REQUESTS".bold(), "COST (USD)".bold()
                    );
                    println!("  {}", "─".repeat(58).dimmed());
                    let mut total = 0.0f64;
                    for item in items {
                        let name = item["name"].as_str().unwrap_or("-");
                        let tokens = item["total_tokens"].as_u64().unwrap_or(0);
                        let requests = item["requests"].as_u64().unwrap_or(0);
                        let cost_val = item["cost"].as_f64().unwrap_or(0.0);
                        total += cost_val;
                        println!("  {:<20} {:<12} {:<12} ${:.4}", name, tokens, requests, cost_val);
                    }
                    println!("  {}", "─".repeat(58).dimmed());
                    println!("  {:<44} ${:.4}", "Total".bold(), total);
                }
            } else {
                println!("  (no cost data yet)");
            }
        }
        Err(e) => {
            println!("  {} Could not fetch costs: {}", "⚠".yellow().bold(), e.to_string().dimmed());
            println!("  (costs are tracked when agents invoke models)");
        }
    }
    Ok(())
}

async fn audit(args: AuditArgs) -> anyhow::Result<()> {
    println!("\n  {} Audit log (last {}):\n", "📋".to_string(), args.limit);

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_audit(args.limit).await {
        Ok(events) => {
            if events.is_empty() {
                println!("  (no audit events)");
            } else {
                println!(
                    "  {:<20} {:<16} {:<16} {:<24}",
                    "TIMESTAMP".bold(), "ACTION".bold(), "SUBJECT".bold(), "TARGET".bold()
                );
                println!("  {}", "─".repeat(78).dimmed());
                for ev in &events {
                    let ts = ev["timestamp"].as_str().unwrap_or("-");
                    let ts_short = if ts.len() > 16 { &ts[..16] } else { ts };
                    let action = ev["action"].as_str().unwrap_or("-");
                    let subject = ev["subject"].as_str().unwrap_or("-");
                    let target = ev["target"].as_str().unwrap_or("-");
                    println!("  {:<20} {:<16} {:<16} {}", ts_short, action, subject, target);
                }
                println!("\n  {} {} event(s)", "→".dimmed(), events.len());
            }
        }
        Err(e) => {
            println!("  {} Could not fetch audit log: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

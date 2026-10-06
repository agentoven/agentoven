//! `agentoven judge` — LLM-as-judge verifiers for rubric criteria (Pro).
//!
//! A judge pins one provider and model and grades judged rubric criteria
//! against artifacts in an episode's final world state. Every judgment it makes
//! is recorded, a sample is queued for human review, and `stats` reports how
//! far it can be trusted: error rate, noise across repeated samples, cost, and
//! agreement with human reviewers — per judge, or rolled up by who created it.

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;
use super::scenario::print_issues;

const FEATURE_NAME: &str = "LLM Judges";
const FEATURE_KEY: &str = "scenarios";

#[derive(Subcommand)]
pub enum JudgeCommands {
    /// List judges with their pinned model and version.
    List,
    /// Show one judge as stored.
    Get(NameArgs),
    /// Write a judge's YAML to a local file (or stdout).
    Pull(PullArgs),
    /// Create or update a judge from a local YAML file.
    Push(PushArgs),
    /// Delete a judge definition. Its judgments and reviews are kept.
    Delete(DeleteArgs),
    /// Ask a judge about an ad-hoc artifact, without recording the result.
    Test(TestArgs),
    /// Show a judge's statistics, or every judge's with no name.
    Stats(StatsArgs),
    /// Show a judge's most recent judgments.
    Judgments(ListArgs),
    /// Show judgments waiting for your review.
    Queue(QueueArgs),
    /// Grade one judgment yourself, for the judge's calibration.
    Review(ReviewArgs),
    /// Fit a System One judge's probabilities to your reviews (Jev, Laya).
    ///
    /// Prints the fitted temperature and the calibration error before and
    /// after. Nothing changes unless you pass --apply, which saves it into the
    /// judge and changes the judge's version.
    Calibrate(CalibrateArgs),
    /// Ask a judge about recent items as written and negated, and report how
    /// often it contradicts itself. Records nothing.
    Probe(ProbeArgs),
}

#[derive(Args)]
pub struct CalibrateArgs {
    /// Judge name.
    pub name: String,
    /// Save the fitted temperature into the judge. Needs 50 or more reviews.
    #[arg(long)]
    pub apply: bool,
}

#[derive(Args)]
pub struct ProbeArgs {
    /// Judge name.
    pub name: String,
    /// How many recent items to probe (each costs two judge calls).
    #[arg(long, default_value_t = 30)]
    pub limit: u32,
}

#[derive(Args)]
pub struct NameArgs {
    /// Judge name.
    pub name: String,
}

#[derive(Args)]
pub struct PullArgs {
    /// Judge name.
    pub name: String,
    /// Write here instead of stdout.
    #[arg(long, short)]
    pub out: Option<String>,
}

#[derive(Args)]
pub struct PushArgs {
    /// Path to a judge YAML file.
    pub file: String,
    /// Create even if the name is not already present.
    #[arg(long)]
    pub create: bool,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Judge name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub yes: bool,
}

#[derive(Args)]
pub struct TestArgs {
    /// Judge name.
    pub name: String,
    /// The criterion, as the judge will read it.
    #[arg(long)]
    pub instruction: String,
    /// The artifact to grade, inline.
    #[arg(long, conflicts_with = "artifact_file")]
    pub artifact: Option<String>,
    /// Read the artifact from a file instead.
    #[arg(long)]
    pub artifact_file: Option<String>,
    /// An artifact that fully meets the criterion.
    #[arg(long)]
    pub reference: Option<String>,
    /// binary (0 or 1) or graded ([0, 1]).
    #[arg(long, default_value = "binary")]
    pub scale: String,
    /// Ask this many times, to see how much the judge disagrees with itself.
    #[arg(long, default_value_t = 1)]
    pub samples: u32,
}

#[derive(Args)]
pub struct StatsArgs {
    /// Judge name. Omit for every judge in the kitchen.
    pub name: Option<String>,
    /// With no name: group by `judge` (default) or `creator`.
    #[arg(long, default_value = "judge")]
    pub by: String,
    /// Print the raw JSON.
    #[arg(long)]
    pub json: bool,
}

#[derive(Args)]
pub struct ListArgs {
    /// Judge name.
    pub name: String,
    #[arg(long, default_value_t = 20)]
    pub limit: u32,
}

#[derive(Args)]
pub struct QueueArgs {
    /// Judge name.
    pub name: String,
    /// Show what nobody has reviewed, not just what you haven't.
    #[arg(long)]
    pub all: bool,
    #[arg(long, default_value_t = 10)]
    pub limit: u32,
}

#[derive(Args)]
pub struct ReviewArgs {
    /// Judge name.
    pub name: String,
    /// The judgment to grade (from `queue`).
    pub judgment_id: String,
    /// Your score: 0 or 1 for a binary criterion, [0, 1] for a graded one.
    #[arg(long)]
    pub score: f64,
    #[arg(long)]
    pub note: Option<String>,
}

pub async fn execute(cmd: JudgeCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }
    match cmd {
        JudgeCommands::List => list().await,
        JudgeCommands::Get(a) => get(a).await,
        JudgeCommands::Pull(a) => pull(a).await,
        JudgeCommands::Push(a) => push(a).await,
        JudgeCommands::Delete(a) => delete(a).await,
        JudgeCommands::Test(a) => test(a).await,
        JudgeCommands::Stats(a) => stats(a).await,
        JudgeCommands::Judgments(a) => judgments(a).await,
        JudgeCommands::Queue(a) => queue(a).await,
        JudgeCommands::Review(a) => review(a).await,
        JudgeCommands::Calibrate(a) => calibrate(a).await,
        JudgeCommands::Probe(a) => probe(a).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  ⚖️  Judges:\n");
    let client = pro_gate::build_client()?;
    let judges: Vec<serde_json::Value> = client.raw_get("/api/v1/judges").await?;
    if judges.is_empty() {
        println!("  (none — create one with {})\n", "agentoven judge push <file> --create".cyan());
        return Ok(());
    }
    println!(
        "  {:<22} {:<34} {:<14} {:<8} {}",
        "NAME".bold(), "KIND · MODEL".bold(), "VERSION".bold(), "REVIEW".bold(), "CREATED BY".bold()
    );
    println!("  {}", "─".repeat(100).dimmed());
    for j in &judges {
        println!(
            "  {:<22} {:<34} {:<14} {:<8} {}",
            j["name"].as_str().unwrap_or("-"),
            if j["kind"].as_str() == Some("system-one") {
                format!("system-one · {}", j["model"].as_str().unwrap_or("?"))
            } else {
                format!("{}/{}", j["provider"].as_str().unwrap_or("?"), j["model"].as_str().unwrap_or("?"))
            },
            j["version"].as_str().unwrap_or("-"),
            format!("{:.0}%", j["review_rate"].as_f64().unwrap_or(0.0) * 100.0),
            j["created_by"].as_str().unwrap_or("-"),
        );
    }
    println!("\n  {} {} judge(s)\n", "→".dimmed(), judges.len());
    Ok(())
}

async fn get(args: NameArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client.raw_get_text(&format!("/api/v1/judges/{}/raw", args.name)).await?;
    println!("\n{}", yaml);
    Ok(())
}

async fn pull(args: PullArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client.raw_get_text(&format!("/api/v1/judges/{}/raw", args.name)).await?;
    match args.out {
        Some(path) => {
            tokio::fs::write(&path, &yaml).await?;
            println!("\n  {} Wrote {} to {}\n", "✓".green().bold(), args.name.bold(), path.cyan());
        }
        None => println!("\n{}", yaml),
    }
    Ok(())
}

async fn push(args: PushArgs) -> anyhow::Result<()> {
    let raw = tokio::fs::read_to_string(&args.file).await?;
    let doc: serde_json::Value = serde_yaml::from_str(&raw)?;
    let name = doc["name"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("{} has no name field", args.file))?
        .to_string();

    let client = pro_gate::build_client()?;
    let result: anyhow::Result<serde_json::Value> = if args.create {
        client.raw_post("/api/v1/judges", &doc).await
    } else {
        client.raw_put(&format!("/api/v1/judges/{}", name), &doc).await
    };
    match result {
        Ok(saved) => {
            println!(
                "\n  {} {} {} {}",
                "✓".green().bold(),
                if args.create { "Created" } else { "Updated" },
                name.bold(),
                format!("(version {})", saved["version"].as_str().unwrap_or("?")).dimmed()
            );
            print_issues(saved["issues"].as_array());
        }
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            if !args.create {
                println!("  {} pass {} if this judge is new.\n", "→".dimmed(), "--create".cyan());
            }
        }
    }
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        println!(
            "\n  {} This deletes the judge definition. Its judgments, reviews and statistics are kept.\n  Re-run with {} to confirm.\n",
            "⚠".yellow().bold(),
            "--yes".cyan()
        );
        return Ok(());
    }
    let client = pro_gate::build_client()?;
    client.raw_delete(&format!("/api/v1/judges/{}", args.name)).await?;
    println!("\n  {} Deleted {}\n", "✓".green().bold(), args.name.bold());
    Ok(())
}

async fn test(args: TestArgs) -> anyhow::Result<()> {
    let artifact = match (&args.artifact, &args.artifact_file) {
        (Some(a), _) => a.clone(),
        (None, Some(path)) => tokio::fs::read_to_string(path).await?,
        (None, None) => anyhow::bail!("pass --artifact or --artifact-file"),
    };
    let body = serde_json::json!({
        "instruction": args.instruction,
        "artifact": artifact,
        "reference": args.reference.unwrap_or_default(),
        "scale": args.scale,
        "samples": args.samples,
    });
    let client = pro_gate::build_client()?;
    let out: serde_json::Value = client
        .raw_post(&format!("/api/v1/judges/{}/test", args.name), &body)
        .await?;

    println!("\n  {} {}\n", "⚖️ ".bold(), out["judge"].as_str().unwrap_or("-").dimmed());
    for (i, s) in out["samples"].as_array().unwrap_or(&vec![]).iter().enumerate() {
        match s["score"].as_f64() {
            Some(score) => println!(
                "  #{} {} {}  {}",
                i + 1,
                format!("{:.2}", score).bold(),
                format!("{} ms · ${:.4} · {}", s["latency_ms"], s["cost_usd"].as_f64().unwrap_or(0.0), s["model"].as_str().unwrap_or("?")).dimmed(),
                s["rationale"].as_str().unwrap_or("")
            ),
            None => println!("  #{} {} {}", i + 1, "✗".red().bold(), s["error"].as_str().unwrap_or("error")),
        }
    }
    println!("\n  {} not recorded: tests never count towards a judge's statistics\n", "→".dimmed());
    Ok(())
}

fn pct(v: &serde_json::Value) -> String {
    format!("{:.1}%", v.as_f64().unwrap_or(0.0) * 100.0)
}

fn kappa(cal: &serde_json::Value) -> String {
    cal["kappa"].as_f64().map(|k| format!("{:.2}", k)).unwrap_or_else(|| "n/a".into())
}

async fn stats(args: StatsArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;

    let Some(name) = args.name else {
        let path = format!("/api/v1/judges/stats?by={}", args.by);
        let rows: Vec<serde_json::Value> = client.raw_get(&path).await?;
        if args.json {
            println!("{}", serde_json::to_string_pretty(&rows)?);
            return Ok(());
        }
        if args.by == "creator" {
            println!("\n  ⚖️  Judge statistics by creator:\n");
            println!(
                "  {:<30} {:<7} {:<10} {:<8} {:<9} {:<10} {}",
                "CREATOR".bold(), "JUDGES".bold(), "JUDGMENTS".bold(), "ERRORS".bold(),
                "REVIEWED".bold(), "AGREEMENT".bold(), "KAPPA".bold()
            );
            println!("  {}", "─".repeat(96).dimmed());
            for c in &rows {
                let cal = &c["calibration"];
                println!(
                    "  {:<30} {:<7} {:<10} {:<8} {:<9} {:<10} {}",
                    c["creator"].as_str().unwrap_or("-"),
                    c["judges"].as_array().map(|a| a.len()).unwrap_or(0),
                    c["judgments"].to_string(),
                    pct(&c["error_rate"]),
                    c["reviewed"].to_string(),
                    if cal.is_null() { "-".into() } else { pct(&cal["agreement"]) },
                    if cal.is_null() { "-".into() } else { kappa(cal) },
                );
            }
        } else {
            println!("\n  ⚖️  Judge statistics:\n");
            println!(
                "  {:<22} {:<10} {:<8} {:<9} {:<10} {:<10} {:<7} {}",
                "JUDGE".bold(), "JUDGMENTS".bold(), "ERRORS".bold(), "P95 MS".bold(),
                "NOISE".bold(), "AGREEMENT".bold(), "KAPPA".bold(), "COST".bold()
            );
            println!("  {}", "─".repeat(100).dimmed());
            for s in &rows {
                let cal = &s["calibration"];
                let name = if s["retired"].as_bool().unwrap_or(false) {
                    format!("{} (retired)", s["judge"].as_str().unwrap_or("-"))
                } else {
                    s["judge"].as_str().unwrap_or("-").to_string()
                };
                println!(
                    "  {:<22} {:<10} {:<8} {:<9} {:<10} {:<10} {:<7} ${:.2}",
                    name,
                    s["judgments"].to_string(),
                    pct(&s["error_rate"]),
                    s["latency_p95_ms"].to_string(),
                    format!("{:.2}", s["noise"]["mean_spread"].as_f64().unwrap_or(0.0)),
                    if cal.is_null() { "-".into() } else { pct(&cal["agreement"]) },
                    if cal.is_null() { "-".into() } else { kappa(cal) },
                    s["cost_usd"].as_f64().unwrap_or(0.0),
                );
            }
        }
        println!();
        return Ok(());
    };

    let s: serde_json::Value = client.raw_get(&format!("/api/v1/judges/{}/stats", name)).await?;
    if args.json {
        println!("{}", serde_json::to_string_pretty(&s)?);
        return Ok(());
    }
    println!(
        "\n  ⚖️  {} {}\n",
        name.bold(),
        format!("{}/{} · version {} · created by {}",
            s["provider"].as_str().unwrap_or("?"), s["model"].as_str().unwrap_or("?"),
            s["version"].as_str().unwrap_or("?"), s["creator"].as_str().unwrap_or("?")).dimmed()
    );
    println!("  judgments     {} ({} errors, {} pin violations, {} error rate)",
        s["judgments"], s["errors"], s["pin_violations"], pct(&s["error_rate"]));
    println!("  latency       p50 {} ms · p95 {} ms", s["latency_p50_ms"], s["latency_p95_ms"]);
    println!("  cost          ${:.4} · {} tokens in · {} out",
        s["cost_usd"].as_f64().unwrap_or(0.0), s["tokens_in"], s["tokens_out"]);
    println!("  mean score    {:.3} · histogram {}", s["mean_score"].as_f64().unwrap_or(0.0), s["histogram"]);
    if let Some(c) = s["mean_confidence"].as_f64() {
        let low = s["low_confidence"].as_i64().unwrap_or(0);
        let target = s["escalate"].as_str().unwrap_or("");
        let route = match target {
            "" => "queued for human review".to_string(),
            "human" => "queued for human review".to_string(),
            other => format!("escalated to {}", other),
        };
        println!("  confidence    mean {:.2} · {} below {:.2}, {}", c, low, s["min_confidence"].as_f64().unwrap_or(0.0), route);
    }
    if let Some(f) = s["fitted"].as_object().filter(|f| !f.is_empty()) {
        println!("  temperature   {:.2} fitted on {} reviews · calibration error {:.3} → {:.3}",
            f["temperature"].as_f64().unwrap_or(1.0), f["labels"], f["ece_before"].as_f64().unwrap_or(0.0), f["ece_after"].as_f64().unwrap_or(0.0));
    }
    let n = &s["noise"];
    println!("  noise         {} repeated items · mean spread {:.2} · max {:.2} · {} unstable",
        n["repeated_items"], n["mean_spread"].as_f64().unwrap_or(0.0),
        n["max_spread"].as_f64().unwrap_or(0.0), n["unstable"]);
    if let Some(models) = s["resolved_models"].as_object() {
        if models.len() > 1 {
            println!("  {} answered by {} different model versions: {:?}",
                "⚠".yellow().bold(), models.len(), models.keys().collect::<Vec<_>>());
        }
    }
    let r = &s["review"];
    println!("  review        {} sampled at {} · {} reviewed · {} pending",
        r["sampled"], pct(&r["rate"]), r["reviewed"], r["pending"]);
    let cal = &s["calibration"];
    if cal.is_null() {
        println!("  calibration   {} — no human reviews yet; see {}",
            "uncalibrated".yellow(), format!("agentoven judge queue {}", name).cyan());
    } else {
        println!("  calibration   agreement {} [{} – {}] · kappa {} · bias {:+.3} · n={}",
            pct(&cal["agreement"]), pct(&cal["agreement_ci"]["low"]), pct(&cal["agreement_ci"]["high"]),
            kappa(cal), cal["bias"].as_f64().unwrap_or(0.0), cal["n"]);
        if let Some(e) = cal["ece"].as_f64() {
            println!("                calibration error (ECE) {:.3}", e);
        }
    }
    let h = &s["human_agreement"];
    if !h.is_null() {
        println!("  humans        agree with each other {} · kappa {} · n={}  (the ceiling for the judge)",
            pct(&h["agreement"]), kappa(h), h["n"]);
    }
    println!();
    Ok(())
}

async fn judgments(args: ListArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let rows: Vec<serde_json::Value> = client
        .raw_get(&format!("/api/v1/judges/{}/judgments?limit={}", args.name, args.limit))
        .await?;
    println!();
    for j in &rows {
        let outcome = match j["score"].as_f64() {
            Some(s) => format!("{:.2}", s).bold().to_string(),
            None => "✗".red().bold().to_string(),
        };
        println!(
            "  {} {:<6} {:<26} {}",
            j["time"].as_str().unwrap_or("-").get(..19).unwrap_or("-").dimmed(),
            outcome,
            j["criterion"].as_str().unwrap_or("-"),
            j["error"].as_str().or(j["rationale"].as_str()).unwrap_or("")
        );
    }
    println!("\n  {} {} judgment(s)\n", "→".dimmed(), rows.len());
    Ok(())
}

async fn queue(args: QueueArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let path = format!(
        "/api/v1/judges/{}/queue?limit={}{}",
        args.name, args.limit, if args.all { "&all=1" } else { "" }
    );
    let rows: Vec<serde_json::Value> = client.raw_get(&path).await?;
    if rows.is_empty() {
        println!("\n  {} nothing waiting for review\n", "✓".green().bold());
        return Ok(());
    }
    for j in &rows {
        println!("\n  {} {}", "judgment".dimmed(), j["id"].as_str().unwrap_or("-").cyan());
        println!("  {} {}", "criterion".dimmed(), j["instruction"].as_str().unwrap_or("-"));
        let artifact = match &j["artifact"] {
            serde_json::Value::String(s) => s.clone(),
            other => serde_json::to_string_pretty(other).unwrap_or_default(),
        };
        println!("  {}\n    {}", "artifact".dimmed(), artifact.replace('\n', "\n    "));
        println!(
            "  {} {} — {}",
            "judge said".dimmed(),
            j["score"].as_f64().map(|s| format!("{:.2}", s)).unwrap_or("-".into()).bold(),
            j["rationale"].as_str().unwrap_or("")
        );
    }
    println!(
        "\n  {} grade one with {}\n",
        "→".dimmed(),
        format!("agentoven judge review {} <judgment-id> --score 0|1", args.name).cyan()
    );
    Ok(())
}

async fn review(args: ReviewArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let body = serde_json::json!({
        "judgment_id": args.judgment_id,
        "score": args.score,
        "note": args.note.unwrap_or_default(),
    });
    let out: serde_json::Value = client
        .raw_post(&format!("/api/v1/judges/{}/reviews", args.name), &body)
        .await?;
    println!(
        "\n  {} Recorded review by {}\n",
        "✓".green().bold(),
        out["reviewer"].as_str().unwrap_or("you").bold()
    );
    Ok(())
}

async fn calibrate(args: CalibrateArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let path = format!("/api/v1/judges/{}/calibrate{}", args.name, if args.apply { "?apply=1" } else { "" });
    let out: serde_json::Value = match client.raw_post(&path, &serde_json::json!({})).await {
        Ok(v) => v,
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            return Ok(());
        }
    };
    let fit = &out["fit"];
    println!(
        "\n  {} {} · {} reviews · temperature {:.2} (currently {:.2})",
        "⚖️ ".bold(), args.name.bold(), fit["n"], fit["temperature"].as_f64().unwrap_or(1.0), out["current_temperature"].as_f64().unwrap_or(1.0)
    );
    println!(
        "  calibration error {:.3} → {:.3}",
        fit["ece_before"].as_f64().unwrap_or(0.0), fit["ece_after"].as_f64().unwrap_or(0.0)
    );
    if !fit["reliable"].as_bool().unwrap_or(false) {
        println!("  {} fewer than 50 reviews: provisional. Review more with {}", "⚠".yellow().bold(),
            format!("agentoven judge queue {}", args.name).cyan());
    }
    if let Some(n) = out["note"].as_str() {
        println!("  {} {}", "·".dimmed(), n);
    }
    if out["applied"].as_bool().unwrap_or(false) {
        println!("  {} applied: the judge is now version {} (verdicts under earlier versions are not comparable)\n",
            "✓".green().bold(), out["new_version"].as_str().unwrap_or("?"));
    } else if !args.apply {
        println!("  {} dry run: pass {} to save it\n", "→".dimmed(), "--apply".cyan());
    }
    Ok(())
}

async fn probe(args: ProbeArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let out: serde_json::Value = match client
        .raw_post(&format!("/api/v1/judges/{}/probe?limit={}", args.name, args.limit), &serde_json::json!({}))
        .await
    {
        Ok(v) => v,
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            return Ok(());
        }
    };
    let r = &out["report"];
    let rate = r["rate"].as_f64().unwrap_or(0.0);
    let flipped = format!("{} of {} contradicted themselves", r["flipped"], r["n"]);
    println!(
        "\n  {} {} · {} ({:.0}%, 95% CI {:.0}%–{:.0}%)",
        "⚖️ ".bold(), args.name.bold(), flipped, rate * 100.0,
        r["rate_ci"]["low"].as_f64().unwrap_or(0.0) * 100.0, r["rate_ci"]["high"].as_f64().unwrap_or(0.0) * 100.0
    );
    println!("  {}", out["verdict"].as_str().unwrap_or(""));
    println!("  {} {} probed · {} failed · ${:.4}\n", "·".dimmed(), out["tested"], out["failed"], out["cost_usd"].as_f64().unwrap_or(0.0));
    Ok(())
}

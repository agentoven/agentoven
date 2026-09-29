//! `agentoven scenario` — author and inspect scenario environments (Pro).
//!
//! Scenarios are files, so `pull` and `push` are the load-bearing commands: a
//! corpus lives in the repository as YAML, and these move it between the server
//! and a working tree where it can be diffed, reviewed and committed like any
//! other source.

use clap::{Args, Subcommand};
use colored::Colorize;

use super::pro_gate;

const FEATURE_NAME: &str = "Scenario Environments";
const FEATURE_KEY: &str = "scenarios";

#[derive(Subcommand)]
pub enum ScenarioCommands {
    /// List scenarios and their validation state.
    List,
    /// Show one scenario as stored.
    Get(IdArgs),
    /// Write a scenario's YAML to a local file (or stdout).
    Pull(PullArgs),
    /// Create or update a scenario from a local YAML file.
    Push(PushArgs),
    /// Report what is wrong with a scenario, without changing it.
    Validate(IdArgs),
    /// Show the resolved instance data an episode would fork.
    World(IdArgs),
    /// Show what an actor can actually see.
    Projection(ProjectionArgs),
    /// Find the scenarios that fit a need, or an agent.
    Find(FindArgs),
    /// Run scenarios against an agent: many rollouts, aggregated.
    Run(RunArgs),
    /// Record a live scenario's world now, to verify against later.
    Snapshot(SnapshotArgs),
    /// Grade the world as it is now against a scenario.
    Verify(VerifyArgs),
    /// List recent runs.
    Runs,
    /// Show a run's results, or its episodes.
    RunStatus(RunStatusArgs),
    /// Compare a candidate run against a baseline, scenario by scenario.
    RunDiff(RunDiffArgs),
    /// Stop a running run. Finished episodes are kept.
    RunCancel(RunIdArgs),
    /// Delete a scenario.
    Delete(DeleteArgs),
}

#[derive(Args)]
pub struct IdArgs {
    /// Scenario id.
    pub id: String,
}

#[derive(Args)]
pub struct PullArgs {
    /// Scenario id.
    pub id: String,
    /// Write here instead of stdout.
    #[arg(long, short)]
    pub out: Option<String>,
}

#[derive(Args)]
pub struct PushArgs {
    /// Path to a scenario YAML file.
    pub file: String,
    /// Create even if the id is not already present.
    #[arg(long)]
    pub create: bool,
}

#[derive(Args)]
pub struct ProjectionArgs {
    /// Scenario id.
    pub id: String,
    /// Actor role. Defaults to the first actor.
    #[arg(long)]
    pub actor: Option<String>,
}

#[derive(Args)]
pub struct RunArgs {
    /// Scenario id. Omit to run the agent's own scenario ingredients.
    pub id: Option<String>,
    /// The baked agent to run against.
    #[arg(long)]
    pub agent: String,
    /// How many episodes. One episode is an anecdote, not a result.
    #[arg(long, default_value_t = 5)]
    pub rollouts: u32,
    /// Explicit seeds, repeatable; overrides --rollouts. The same seed forks
    /// the same world and faults.
    #[arg(long = "seed")]
    pub seeds: Vec<i64>,
    /// Wait for the run to finish and print its results.
    #[arg(long)]
    pub wait: bool,
    /// Environment live scenarios act on (sandbox, staging, ...).
    #[arg(long)]
    pub env: Option<String>,
    /// Allow acting on an environment that looks like production.
    #[arg(long)]
    pub allow_production: bool,
}

#[derive(Args)]
pub struct FindArgs {
    /// What to evaluate, in words.
    pub query: Option<String>,
    /// Rank by what this agent can run; with no query, uses the agent's own description.
    #[arg(long)]
    pub agent: Option<String>,
    /// Only scenarios exercising this tool (repeatable).
    #[arg(long = "tool")]
    pub tools: Vec<String>,
    #[arg(long, default_value_t = 10)]
    pub limit: u32,
}

#[derive(Args)]
pub struct SnapshotArgs {
    /// Scenario id.
    pub id: String,
    /// Environment to read.
    #[arg(long)]
    pub env: String,
    #[arg(long)]
    pub allow_production: bool,
}

#[derive(Args)]
pub struct VerifyArgs {
    /// Scenario id.
    pub id: String,
    /// Environment to read (live scenarios).
    #[arg(long)]
    pub env: Option<String>,
    /// Snapshot id taken before acting.
    #[arg(long)]
    pub before: Option<String>,
    /// Final world state to grade, as a JSON or YAML instance document (fixture scenarios).
    #[arg(long)]
    pub state: Option<String>,
    #[arg(long)]
    pub allow_production: bool,
}

#[derive(Args)]
pub struct RunIdArgs {
    /// Run id.
    pub run_id: String,
}

#[derive(Args)]
pub struct RunStatusArgs {
    /// Run id.
    pub run_id: String,
    /// List every episode instead of the aggregate.
    #[arg(long)]
    pub episodes: bool,
}

#[derive(Args)]
pub struct RunDiffArgs {
    /// The baseline run id.
    pub baseline: String,
    /// The candidate run id.
    pub candidate: String,
    /// Exit with status 3 if the candidate is significantly worse (for CI).
    #[arg(long)]
    pub gate: bool,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Scenario id.
    pub id: String,
    /// Skip confirmation.
    #[arg(long)]
    pub yes: bool,
}

pub async fn execute(cmd: ScenarioCommands) -> anyhow::Result<()> {
    if !pro_gate::check_pro_feature(FEATURE_NAME, FEATURE_KEY).await? {
        return Ok(());
    }

    match cmd {
        ScenarioCommands::List => list().await,
        ScenarioCommands::Get(a) => get(a).await,
        ScenarioCommands::Pull(a) => pull(a).await,
        ScenarioCommands::Push(a) => push(a).await,
        ScenarioCommands::Validate(a) => validate(a).await,
        ScenarioCommands::World(a) => world(a).await,
        ScenarioCommands::Projection(a) => projection(a).await,
        ScenarioCommands::Find(a) => find(a).await,
        ScenarioCommands::Run(a) => run(a).await,
        ScenarioCommands::Snapshot(a) => snapshot(a).await,
        ScenarioCommands::Verify(a) => verify(a).await,
        ScenarioCommands::Runs => runs().await,
        ScenarioCommands::RunStatus(a) => run_status(a).await,
        ScenarioCommands::RunDiff(a) => run_diff(a).await,
        ScenarioCommands::RunCancel(a) => run_cancel(a).await,
        ScenarioCommands::Delete(a) => delete(a).await,
    }
}

async fn list() -> anyhow::Result<()> {
    println!("\n  🧪 Scenarios:\n");

    let client = pro_gate::build_client()?;
    let scenarios: Vec<serde_json::Value> = client.raw_get("/api/v1/scenarios").await?;

    if scenarios.is_empty() {
        println!("  (no scenarios — create one with {})", "agentoven scenario push <file>".cyan());
        return Ok(());
    }

    println!(
        "  {:<40} {:<6} {:<8} {:<10}",
        "ID".bold(),
        "VER".bold(),
        "SCHEMA".bold(),
        "STATE".bold()
    );
    println!("  {}", "─".repeat(70).dimmed());

    for s in &scenarios {
        let id = s["id"].as_str().unwrap_or("-");
        let ver = s["version"].as_u64().unwrap_or(0);
        let schema = s["ontology_version"].as_str().unwrap_or("-");
        let issues = s["issues"].as_array().cloned().unwrap_or_default();
        let errors = issues
            .iter()
            .filter(|i| i["level"].as_str() == Some("error"))
            .count();

        let state = if errors > 0 {
            format!("{} error(s)", errors).red().to_string()
        } else if !issues.is_empty() {
            format!("{} warning(s)", issues.len()).yellow().to_string()
        } else {
            "ok".green().to_string()
        };
        println!("  {:<40} v{:<5} {:<8} {}", id, ver, schema, state);
    }
    println!("\n  {} {} scenario(s)", "→".dimmed(), scenarios.len());
    Ok(())
}

async fn get(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client
        .raw_get_text(&format!("/api/v1/scenarios/{}/raw", args.id))
        .await?;
    println!("\n{}", yaml);
    Ok(())
}

async fn pull(args: PullArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let yaml = client
        .raw_get_text(&format!("/api/v1/scenarios/{}/raw", args.id))
        .await?;

    match args.out {
        Some(path) => {
            tokio::fs::write(&path, &yaml).await?;
            println!(
                "\n  {} Wrote {} to {}\n",
                "✓".green().bold(),
                args.id.bold(),
                path.cyan()
            );
        }
        None => println!("\n{}", yaml),
    }
    Ok(())
}

async fn push(args: PushArgs) -> anyhow::Result<()> {
    let raw = tokio::fs::read_to_string(&args.file).await?;
    let doc: serde_json::Value = serde_yaml::from_str(&raw)?;

    let id = doc["id"]
        .as_str()
        .ok_or_else(|| anyhow::anyhow!("{} has no id field", args.file))?
        .to_string();

    let client = pro_gate::build_client()?;
    let path = format!("/api/v1/scenarios/{}", id);

    // POST creates and refuses to overwrite; PUT updates. Choosing by --create
    // rather than by probing keeps an accidental overwrite from looking like a
    // successful create.
    let result: anyhow::Result<serde_json::Value> = if args.create {
        client.raw_post("/api/v1/scenarios", &doc).await
    } else {
        client.raw_put(&path, &doc).await
    };

    match result {
        Ok(saved) => {
            println!(
                "\n  {} {} {}",
                "✓".green().bold(),
                if args.create { "Created" } else { "Updated" },
                id.bold()
            );
            print_issues(saved["issues"].as_array());
        }
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            if !args.create {
                println!("  {} pass {} if this scenario is new.\n", "→".dimmed(), "--create".cyan());
            }
        }
    }
    Ok(())
}

async fn validate(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let sc: serde_json::Value = client
        .raw_get(&format!("/api/v1/scenarios/{}", args.id))
        .await?;

    println!("\n  Validating {} against world schema {}\n",
        args.id.bold(),
        sc["ontology_version"].as_str().unwrap_or("-").cyan());
    print_issues(sc["issues"].as_array());
    Ok(())
}

async fn world(args: IdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let out: serde_json::Value = client
        .raw_get(&format!("/api/v1/scenarios/{}/world", args.id))
        .await?;

    let empty = vec![];
    let nodes = out["world"]["nodes"].as_array().unwrap_or(&empty);
    println!("\n  🌍 World for {} — {} node(s)\n", args.id.bold(), nodes.len());

    for n in nodes {
        println!(
            "  {} {}",
            n["id"].as_str().unwrap_or("-").cyan().bold(),
            format!("({})", n["class"].as_str().unwrap_or("-")).dimmed()
        );
        if let Some(props) = n["props"].as_object() {
            for (k, v) in props {
                println!("      {:<22} {}", k.dimmed(), v);
            }
        }
    }
    print_issues(out["issues"].as_array());
    Ok(())
}

async fn projection(args: ProjectionArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let path = match &args.actor {
        Some(a) => format!("/api/v1/scenarios/{}/projection?actor={}", args.id, a),
        None => format!("/api/v1/scenarios/{}/projection", args.id),
    };
    let out: serde_json::Value = client.raw_get(&path).await?;

    println!(
        "\n  👁  Context graph for actor {} — {}\n",
        out["actor"].as_str().unwrap_or("-").bold(),
        out["summary"].as_str().unwrap_or("").dimmed()
    );

    let empty = vec![];
    // Grouped by visibility, because the interesting answer is what the actor
    // cannot see: an excluded property is never retrieved, not merely hidden.
    for (key, label) in [
        ("included", "in context".green()),
        ("held", "held by the runner".yellow()),
        ("excluded", "never retrieved".red()),
    ] {
        let rows: Vec<&serde_json::Value> = out["projection"]["properties"]
            .as_array()
            .unwrap_or(&empty)
            .iter()
            .filter(|p| p["visibility"].as_str() == Some(key))
            .collect();
        if rows.is_empty() {
            continue;
        }
        println!("  {} ({})", label.bold(), rows.len());
        for p in rows {
            let value = if key == "included" {
                p["value"].to_string()
            } else {
                "—".dimmed().to_string()
            };
            println!(
                "      {:<28} {:<20} {}",
                format!("{}.{}", p["node"].as_str().unwrap_or("-"), p["property"].as_str().unwrap_or("-")),
                value,
                p["reason"].as_str().unwrap_or("").dimmed()
            );
        }
        println!();
    }
    Ok(())
}

async fn run(args: RunArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let mut body = serde_json::json!({
        "agent_name": args.agent,
        "rollouts": args.rollouts,
        "seeds": args.seeds,
        "trigger": "cli",
        "environment": args.env.clone().unwrap_or_default(),
        "allow_production": args.allow_production,
    });
    let path = match &args.id {
        Some(id) => format!("/api/v1/scenarios/{}/run", id),
        None => {
            // No scenario named: the agent's own suite, with the rollouts its
            // scenario ingredients set unless given here.
            body["rollouts"] = serde_json::json!(0);
            "/api/v1/scenario-runs".to_string()
        }
    };
    let run: serde_json::Value = match client.raw_post(&path, &body).await {
        Ok(v) => v,
        Err(e) => {
            println!("\n  {} {}\n", "✗".red().bold(), e.to_string().dimmed());
            return Ok(());
        }
    };
    let id = run["id"].as_str().unwrap_or_default().to_string();
    println!(
        "\n  {} Run {} started: {} episode(s) of {} against {}",
        "▶".green().bold(),
        id.cyan(),
        run["total"],
        args.id.clone().unwrap_or_else(|| "its scenario ingredients".into()).bold(),
        args.agent.bold()
    );
    if !args.wait {
        println!("  {} follow with {}\n", "→".dimmed(), format!("agentoven scenario run-status {}", id).cyan());
        return Ok(());
    }

    let mut last = -1;
    loop {
        let r: serde_json::Value = client.raw_get(&format!("/api/v1/scenario-runs/{}", id)).await?;
        let done = r["done"].as_i64().unwrap_or(0);
        if done != last {
            println!("  {} {}/{}", "…".dimmed(), done, r["total"]);
            last = done;
        }
        match r["status"].as_str().unwrap_or("") {
            "queued" | "running" => tokio::time::sleep(std::time::Duration::from_secs(2)).await,
            _ => {
                print_run(&r);
                return Ok(());
            }
        }
    }
}

fn pct(v: &serde_json::Value) -> String {
    format!("{:.1}%", v.as_f64().unwrap_or(0.0) * 100.0)
}

fn print_run(r: &serde_json::Value) {
    let status = r["status"].as_str().unwrap_or("?");
    let coloured = match status {
        "completed" => status.green(),
        "running" | "queued" => status.yellow(),
        _ => status.red(),
    };
    println!(
        "\n  Run {} — {} ({}/{} episodes, agent {})\n",
        r["id"].as_str().unwrap_or("-").cyan(),
        coloured.bold(),
        r["done"],
        r["total"],
        r["request"]["agent_name"].as_str().unwrap_or("-")
    );
    println!(
        "  {:<34} {:<6} {:<6} {:<8} {:<10} {:<20} {:<9} {}",
        "SCENARIO".bold(), "PASS".bold(), "FAIL".bold(), "INVALID".bold(),
        "PASS RATE".bold(), "95% CI".bold(), "PASS^K".bold(), "COST".bold()
    );
    println!("  {}", "─".repeat(104).dimmed());
    for res in r["results"].as_array().unwrap_or(&vec![]) {
        let hat = res["reliability"]
            .as_array()
            .and_then(|a| a.last())
            .map(|k| format!("{} @{}", pct(&k["pass_hat_k"]), k["k"]))
            .unwrap_or_else(|| "-".into());
        println!(
            "  {:<34} {:<6} {:<6} {:<8} {:<10} {:<20} {:<9} ${:.3}",
            res["scenario_id"].as_str().unwrap_or("-"),
            res["passed"].to_string(),
            res["failed"].to_string(),
            res["invalid"].to_string(),
            pct(&res["pass_rate"]),
            format!("[{} – {}]", pct(&res["ci_low"]), pct(&res["ci_high"])),
            hat,
            res["total_cost_usd"].as_f64().unwrap_or(0.0)
        );
    }
    if let Some(sum) = r.get("summary").filter(|s| !s.is_null()) {
        println!(
            "\n  overall  {} [{} – {}] over {} valid episode(s) in {} scenario(s); {} invalid (excluded)",
            pct(&sum["pass_rate"]).bold(),
            pct(&sum["pass_rate_ci"]["low"]),
            pct(&sum["pass_rate_ci"]["high"]),
            sum["valid"],
            sum["tasks"],
            sum["invalid"]
        );
    }
    if let Some(err) = r["error"].as_str() {
        println!("  {} {}", "✗".red().bold(), err);
    }
    println!();
}

async fn find(args: FindArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let mut query: Vec<(String, String)> = vec![("limit".into(), args.limit.to_string())];
    if let Some(q) = &args.query {
        query.push(("q".into(), q.clone()));
    }
    if let Some(a) = &args.agent {
        query.push(("agent".into(), a.clone()));
    }
    for t in &args.tools {
        query.push(("tool".into(), t.clone()));
    }
    let qs: Vec<String> = query
        .iter()
        .map(|(k, v)| format!("{}={}", k, encode_query(v)))
        .collect();
    let results: Vec<serde_json::Value> = client
        .raw_get(&format!("/api/v1/scenarios/find?{}", qs.join("&")))
        .await?;
    if results.is_empty() {
        println!("\n  (no scenario fits)\n");
        return Ok(());
    }
    println!();
    for (i, r) in results.iter().enumerate() {
        let runnable = r["runnable"].as_bool().unwrap_or(true);
        let marker = if runnable { "●".green() } else { "○".yellow() };
        println!(
            "  {} {:<2} {:<36} {}{}",
            marker,
            i + 1,
            r["scenario_id"].as_str().unwrap_or("-").bold(),
            format!("score {:.2}", r["score"].as_f64().unwrap_or(0.0)).dimmed(),
            if r["live"].as_bool().unwrap_or(false) { "  live".cyan().to_string() } else { String::new() }
        );
        if let Some(d) = r["description"].as_str().filter(|d| !d.is_empty()) {
            println!("       {}", d.dimmed());
        }
        for w in r["why"].as_array().unwrap_or(&vec![]) {
            println!("       {} {}", "·".dimmed(), w.as_str().unwrap_or(""));
        }
        if let Some(m) = r["missing_tools"].as_array().filter(|m| !m.is_empty()) {
            let names: Vec<&str> = m.iter().filter_map(|x| x.as_str()).collect();
            println!("       {} agent lacks: {}", "✗".red(), names.join(", "));
        }
    }
    println!();
    Ok(())
}

/// Percent-encode a query-string value.
fn encode_query(v: &str) -> String {
    v.bytes()
        .map(|b| match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => (b as char).to_string(),
            _ => format!("%{:02X}", b),
        })
        .collect()
}

async fn snapshot(args: SnapshotArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let body = serde_json::json!({"environment": args.env, "allow_production": args.allow_production});
    let out: serde_json::Value = client
        .raw_post(&format!("/api/v1/scenarios/{}/snapshot", args.id), &body)
        .await?;
    println!(
        "\n  {} Snapshot {} of {} in {} ({} records)\n  {} verify later with {}\n",
        "✓".green().bold(),
        out["id"].as_str().unwrap_or("-").cyan(),
        args.id.bold(),
        args.env,
        out["nodes"],
        "→".dimmed(),
        format!("agentoven scenario verify {} --env {} --before {}", args.id, args.env, out["id"].as_str().unwrap_or("-")).cyan()
    );
    Ok(())
}

async fn verify(args: VerifyArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let mut body = serde_json::json!({
        "environment": args.env.clone().unwrap_or_default(),
        "before": args.before.clone().unwrap_or_default(),
        "allow_production": args.allow_production,
    });
    if let Some(path) = &args.state {
        let raw = tokio::fs::read_to_string(path).await?;
        let state: serde_json::Value = serde_yaml::from_str(&raw)?;
        body["state"] = state;
    }
    let v: serde_json::Value = client
        .raw_post(&format!("/api/v1/scenarios/{}/verify", args.id), &body)
        .await?;
    let verdict = &v["verdict"];
    let status = verdict["status"].as_str().unwrap_or("?");
    let coloured = match status {
        "passed" => status.green().bold(),
        "failed" => status.red().bold(),
        _ => status.yellow().bold(),
    };
    println!("\n  {} {} — reward {:.3}\n", args.id.bold(), coloured, verdict["reward"].as_f64().unwrap_or(0.0));
    for a in verdict["assertions"].as_array().unwrap_or(&vec![]) {
        let ok = a["satisfied"].as_bool().unwrap_or(false);
        println!(
            "  {} {:<9} {}",
            if ok { "✓".green() } else { "✗".red() },
            a["kind"].as_str().unwrap_or(""),
            a["name"].as_str().unwrap_or("")
        );
    }
    if let Some(r) = verdict["reason"].as_str() {
        println!("  {} {}", "∅".yellow(), r);
    }
    println!();
    if status != "passed" {
        std::process::exit(3);
    }
    Ok(())
}

async fn runs() -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let runs: Vec<serde_json::Value> = client.raw_get("/api/v1/scenario-runs").await?;
    if runs.is_empty() {
        println!("\n  (no runs yet — start one with {})\n", "agentoven scenario run <id> --agent <name>".cyan());
        return Ok(());
    }
    println!("\n  {:<38} {:<12} {:<20} {:<10} {:<10} {}", "RUN".bold(), "STATUS".bold(), "AGENT".bold(), "EPISODES".bold(), "PASS RATE".bold(), "CREATED".bold());
    println!("  {}", "─".repeat(110).dimmed());
    for r in &runs {
        let rate = r["summary"]["pass_rate"].as_f64().map(|v| format!("{:.1}%", v * 100.0)).unwrap_or_else(|| "-".into());
        println!(
            "  {:<38} {:<12} {:<20} {:<10} {:<10} {}",
            r["id"].as_str().unwrap_or("-"),
            r["status"].as_str().unwrap_or("-"),
            r["request"]["agent_name"].as_str().unwrap_or("-"),
            format!("{}/{}", r["done"], r["total"]),
            rate,
            r["created_at"].as_str().unwrap_or("-").get(..19).unwrap_or("-")
        );
    }
    println!();
    Ok(())
}

async fn run_status(args: RunStatusArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    if !args.episodes {
        let r: serde_json::Value = client.raw_get(&format!("/api/v1/scenario-runs/{}", args.run_id)).await?;
        print_run(&r);
        return Ok(());
    }
    let eps: Vec<serde_json::Value> = client
        .raw_get(&format!("/api/v1/scenario-runs/{}/episodes", args.run_id))
        .await?;
    println!();
    for e in &eps {
        let v = &e["verdict"];
        let status = v["status"].as_str().unwrap_or("?");
        let marker = match status {
            "passed" => "✓".green().bold(),
            "failed" => "✗".red().bold(),
            _ => "∅".yellow().bold(),
        };
        let why = if status == "invalid" {
            v["reason"].as_str().unwrap_or("").to_string()
        } else {
            v["assertions"]
                .as_array()
                .map(|a| {
                    a.iter()
                        .filter(|x| !x["satisfied"].as_bool().unwrap_or(false))
                        .map(|x| x["name"].as_str().unwrap_or("?").to_string())
                        .collect::<Vec<_>>()
                        .join(", ")
                })
                .unwrap_or_default()
        };
        println!(
            "  {} {:<34} seed {:<4} reward {:<7} {}",
            marker,
            e["scenario_id"].as_str().unwrap_or("-"),
            e["seed"],
            format!("{:.3}", v["reward"].as_f64().unwrap_or(0.0)),
            why.dimmed()
        );
    }
    println!("\n  {} {} episode(s)\n", "→".dimmed(), eps.len());
    Ok(())
}

async fn run_diff(args: RunDiffArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    let d: serde_json::Value = client
        .raw_get(&format!("/api/v1/scenario-runs/{}/diff/{}", args.baseline, args.candidate))
        .await?;
    let diff = &d["diff"];
    let delta = diff["mean_delta"].as_f64().unwrap_or(0.0);
    let significant = diff["significant"].as_bool().unwrap_or(false);
    let verdict = match (significant, delta < 0.0) {
        (true, true) => "significant regression".red().bold(),
        (true, false) => "significant improvement".green().bold(),
        _ => "no significant change".normal(),
    };
    println!(
        "\n  pass-rate delta {:+.3}  95% CI [{:+.3}, {:+.3}] over {} scenario(s) — {}\n",
        delta,
        diff["delta_ci"]["low"].as_f64().unwrap_or(0.0),
        diff["delta_ci"]["high"].as_f64().unwrap_or(0.0),
        diff["paired_tasks"],
        verdict
    );
    for (label, key) in [("regressed", "regressions"), ("improved", "improvements")] {
        for t in diff[key].as_array().unwrap_or(&vec![]) {
            println!(
                "  {:<10} {:<34} {} → {}",
                label,
                t["task"].as_str().unwrap_or("-"),
                pct(&t["baseline_pass_rate"]),
                pct(&t["candidate_pass_rate"])
            );
        }
    }
    for w in d["warnings"].as_array().unwrap_or(&vec![]) {
        println!("  {} {}", "⚠".yellow().bold(), w.as_str().unwrap_or(""));
    }
    println!();
    if args.gate && significant && delta < 0.0 {
        std::process::exit(3);
    }
    Ok(())
}

async fn run_cancel(args: RunIdArgs) -> anyhow::Result<()> {
    let client = pro_gate::build_client()?;
    client
        .raw_post::<serde_json::Value>(&format!("/api/v1/scenario-runs/{}/cancel", args.run_id), &serde_json::json!({}))
        .await?;
    println!("\n  {} Cancelling {}\n", "■".yellow().bold(), args.run_id.cyan());
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.yes {
        println!(
            "\n  {} This deletes the scenario file. Re-run with {} to confirm.\n",
            "⚠".yellow().bold(),
            "--yes".cyan()
        );
        return Ok(());
    }

    let client = pro_gate::build_client()?;
    client
        .raw_delete(&format!("/api/v1/scenarios/{}", args.id))
        .await?;
    println!("\n  {} Deleted {}\n", "✓".green().bold(), args.id.bold());
    Ok(())
}

/// Print validation issues, or say plainly that there are none.
pub fn print_issues(issues: Option<&Vec<serde_json::Value>>) {
    let issues = match issues {
        Some(v) if !v.is_empty() => v,
        _ => {
            println!("  {} no issues\n", "✓".green().bold());
            return;
        }
    };

    println!();
    for i in issues {
        let level = i["level"].as_str().unwrap_or("error");
        let marker = if level == "error" {
            "✗".red().bold()
        } else {
            "⚠".yellow().bold()
        };
        println!(
            "  {} {} {}",
            marker,
            i["where"].as_str().unwrap_or("-").bold(),
            i["message"].as_str().unwrap_or("")
        );
    }
    println!();
}

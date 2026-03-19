//! `agentoven apply` — declarative resource management via YAML/JSON/TOML manifests.
//!
//! Supports multi-document YAML (`---` separators) and single-document JSON/TOML.
//! Each document must have a `kind` field to indicate the resource type.
//!
//! # Example YAML
//!
//! ```yaml
//! kind: Agent
//! name: summarizer
//! description: "Summarizes documents"
//! model_provider: openai
//! model_name: gpt-4o
//! ---
//! kind: ToolSet
//! tools:
//!   - name: web-search
//!     description: "Search the web"
//!     endpoint: https://tools.example.com/search
//! ---
//! kind: Recipe
//! name: support-flow
//! steps:
//!   - name: classify
//!     agent: classifier
//!     kind: agent
//! ```

use clap::Args;
use colored::Colorize;
use serde::{Deserialize, Serialize};
use serde_json::Value as JsonValue;

use agentoven_core::agent::{Agent, AgentFramework, AgentMode};
use agentoven_core::ingredient::Ingredient;
use agentoven_core::recipe::{Recipe, Step, StepKind};
use uuid::Uuid;

// ── Manifest Types ──────────────────────────────────────────

/// Top-level discriminator for manifest documents.
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(tag = "kind")]
pub enum Manifest {
    Agent(AgentManifest),
    Recipe(RecipeManifest),
    ToolSet(ToolSetManifest),
}

/// Agent manifest — mirrors the agent register fields.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AgentManifest {
    pub name: String,
    #[serde(default)]
    pub version: Option<String>,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub framework: Option<String>,
    #[serde(default)]
    pub mode: Option<String>,
    #[serde(default)]
    pub model_provider: Option<String>,
    #[serde(default)]
    pub model_name: Option<String>,
    #[serde(default)]
    pub backup_provider: Option<String>,
    #[serde(default)]
    pub backup_model: Option<String>,
    #[serde(default)]
    pub system_prompt: Option<String>,
    #[serde(default)]
    pub max_turns: Option<u32>,
    #[serde(default)]
    pub a2a_endpoint: Option<String>,
    #[serde(default)]
    pub skills: Option<Vec<String>>,
    #[serde(default)]
    pub tags: Option<Vec<String>>,
    #[serde(default)]
    pub ingredients: Option<IngredientsManifest>,
}

/// Ingredients section within an agent manifest.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct IngredientsManifest {
    #[serde(default)]
    pub models: Option<Vec<ModelIngredient>>,
    #[serde(default)]
    pub tools: Option<Vec<ToolIngredient>>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ModelIngredient {
    pub name: String,
    #[serde(default)]
    pub provider: Option<String>,
    #[serde(default)]
    pub role: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ToolIngredient {
    pub name: String,
    #[serde(default)]
    pub protocol: Option<String>,
}

/// Recipe manifest — maps to recipe creation.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct RecipeManifest {
    pub name: String,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub steps: Vec<StepManifest>,
}

/// A step within a recipe manifest.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct StepManifest {
    pub name: String,
    #[serde(default)]
    pub agent: Option<String>,
    #[serde(default, rename = "kind")]
    pub step_kind: Option<String>,
    #[serde(default)]
    pub input: Option<String>,
    #[serde(default)]
    pub depends_on: Option<Vec<String>>,
    #[serde(default)]
    pub condition: Option<String>,
    #[serde(default)]
    pub retries: Option<u32>,
    #[serde(default)]
    pub timeout_secs: Option<u64>,
    #[serde(default)]
    pub parallel_inputs: Option<Vec<String>>,
    #[serde(default)]
    pub routes: Option<Vec<RouteManifest>>,
    #[serde(default)]
    pub sub_recipe: Option<String>,
}

/// A route within a router step.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct RouteManifest {
    pub condition: String,
    pub target: String,
}

/// ToolSet manifest — bulk tool registration.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ToolSetManifest {
    pub tools: Vec<ToolManifestEntry>,
}

/// A single tool in a ToolSet.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ToolManifestEntry {
    pub name: String,
    #[serde(default)]
    pub description: Option<String>,
    #[serde(default)]
    pub endpoint: Option<String>,
    #[serde(default)]
    pub transport: Option<String>,
    #[serde(default)]
    pub schema: Option<JsonValue>,
    #[serde(default)]
    pub capabilities: Option<Vec<String>>,
}

// ── CLI Args ────────────────────────────────────────────────

#[derive(Args)]
pub struct ApplyArgs {
    /// Path to manifest file (YAML, JSON, or TOML).
    #[arg(short = 'f', long = "file")]
    pub file: String,

    /// Preview changes without applying (dry-run).
    #[arg(long)]
    pub dry_run: bool,
}

// ── Execute ─────────────────────────────────────────────────

pub async fn execute(args: ApplyArgs) -> anyhow::Result<()> {
    println!("\n  🏺 Applying manifest: {}\n", args.file.cyan());

    let content = tokio::fs::read_to_string(&args.file).await?;
    let manifests = parse_manifests(&args.file, &content)?;

    if manifests.is_empty() {
        anyhow::bail!("No valid documents found in {}", args.file);
    }

    println!(
        "  {} Parsed {} resource(s):",
        "→".dimmed(),
        manifests.len()
    );

    for m in &manifests {
        match m {
            Manifest::Agent(a) => println!("    {} Agent/{}", "•".dimmed(), a.name.bold()),
            Manifest::Recipe(r) => println!(
                "    {} Recipe/{} ({} steps)",
                "•".dimmed(),
                r.name.bold(),
                r.steps.len()
            ),
            Manifest::ToolSet(t) => println!(
                "    {} ToolSet ({} tools)",
                "•".dimmed(),
                t.tools.len()
            ),
        }
    }
    println!();

    if args.dry_run {
        println!(
            "  {} Dry run — no changes applied.",
            "ℹ".blue().bold()
        );
        return Ok(());
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;

    let mut success = 0u32;
    let mut failed = 0u32;

    for m in manifests {
        match m {
            Manifest::Agent(a) => match apply_agent(&client, a).await {
                Ok(name) => {
                    println!("  {} Agent/{} applied", "✓".green().bold(), name);
                    success += 1;
                }
                Err(e) => {
                    println!(
                        "  {} Agent apply failed: {}",
                        "✗".red().bold(),
                        e.to_string().dimmed()
                    );
                    failed += 1;
                }
            },
            Manifest::Recipe(r) => match apply_recipe(&client, r).await {
                Ok(name) => {
                    println!("  {} Recipe/{} applied", "✓".green().bold(), name);
                    success += 1;
                }
                Err(e) => {
                    println!(
                        "  {} Recipe apply failed: {}",
                        "✗".red().bold(),
                        e.to_string().dimmed()
                    );
                    failed += 1;
                }
            },
            Manifest::ToolSet(t) => match apply_toolset(&client, t).await {
                Ok(count) => {
                    println!(
                        "  {} ToolSet applied ({} tools)",
                        "✓".green().bold(),
                        count
                    );
                    success += 1;
                }
                Err(e) => {
                    println!(
                        "  {} ToolSet apply failed: {}",
                        "✗".red().bold(),
                        e.to_string().dimmed()
                    );
                    failed += 1;
                }
            },
        }
    }

    println!();
    if failed == 0 {
        println!(
            "  {} All {} resource(s) applied successfully.",
            "✓".green().bold(),
            success
        );
    } else {
        println!(
            "  {} {} applied, {} failed.",
            "⚠".yellow().bold(),
            success,
            failed
        );
    }

    Ok(())
}

// ── Parsing ─────────────────────────────────────────────────

fn parse_manifests(path: &str, content: &str) -> anyhow::Result<Vec<Manifest>> {
    if path.ends_with(".yaml") || path.ends_with(".yml") {
        parse_yaml_manifests(content)
    } else if path.ends_with(".json") {
        parse_json_manifest(content)
    } else if path.ends_with(".toml") {
        parse_toml_manifest(content)
    } else {
        // Try YAML first (most common for multi-doc), then JSON, then TOML
        parse_yaml_manifests(content)
            .or_else(|_| parse_json_manifest(content))
            .or_else(|_| parse_toml_manifest(content))
            .map_err(|_| {
                anyhow::anyhow!(
                    "Could not detect format. Use .yaml, .json, or .toml extension."
                )
            })
    }
}

/// Parse YAML with multi-document support (--- separators).
fn parse_yaml_manifests(content: &str) -> anyhow::Result<Vec<Manifest>> {
    let mut manifests = Vec::new();

    for doc in serde_yaml::Deserializer::from_str(content) {
        let value = serde_yaml::Value::deserialize(doc)?;
        let manifest: Manifest = serde_yaml::from_value(value)?;
        manifests.push(manifest);
    }

    if manifests.is_empty() {
        anyhow::bail!("No YAML documents found");
    }

    Ok(manifests)
}

fn parse_json_manifest(content: &str) -> anyhow::Result<Vec<Manifest>> {
    // Try array first
    if let Ok(arr) = serde_json::from_str::<Vec<Manifest>>(content) {
        return Ok(arr);
    }
    // Then single document
    let m: Manifest = serde_json::from_str(content)?;
    Ok(vec![m])
}

fn parse_toml_manifest(content: &str) -> anyhow::Result<Vec<Manifest>> {
    // TOML doesn't support multi-document, so parse as single
    let value: toml::Value = content.parse()?;

    // TOML needs the `kind` key to determine type. Convert via JSON for serde(tag).
    let json_str = serde_json::to_string(&value)?;
    let m: Manifest = serde_json::from_str(&json_str)?;
    Ok(vec![m])
}

// ── Applying ────────────────────────────────────────────────

fn parse_framework(s: &str) -> AgentFramework {
    match s.to_lowercase().as_str() {
        "langchain" | "langgraph" => AgentFramework::Langchain,
        "crewai" => AgentFramework::Crewai,
        "openai" | "openai-sdk" => AgentFramework::Openai,
        "autogen" => AgentFramework::Autogen,
        "managed" => AgentFramework::Managed,
        _ => AgentFramework::Custom,
    }
}

async fn apply_agent(
    client: &agentoven_core::AgentOvenClient,
    manifest: AgentManifest,
) -> anyhow::Result<String> {
    let name = manifest.name.clone();

    let framework = parse_framework(manifest.framework.as_deref().unwrap_or("managed"));
    let mode = match manifest.mode.as_deref() {
        Some("external") => AgentMode::External,
        _ => AgentMode::Managed,
    };

    let mut builder = Agent::builder(&manifest.name)
        .version(manifest.version.as_deref().unwrap_or("0.1.0"))
        .description(manifest.description.as_deref().unwrap_or(""))
        .framework(framework)
        .mode(mode)
        .model_provider(manifest.model_provider.as_deref().unwrap_or(""))
        .model_name(manifest.model_name.as_deref().unwrap_or(""));

    if let Some(bp) = &manifest.backup_provider {
        builder = builder.backup_provider(bp);
    }
    if let Some(bm) = &manifest.backup_model {
        builder = builder.backup_model(bm);
    }
    if let Some(sp) = &manifest.system_prompt {
        builder = builder.system_prompt(sp);
    }
    if let Some(mt) = manifest.max_turns {
        builder = builder.max_turns(mt);
    }
    if let Some(ep) = &manifest.a2a_endpoint {
        builder = builder.a2a_endpoint(ep);
    }
    if let Some(skills) = &manifest.skills {
        for s in skills {
            builder = builder.skill(s);
        }
    }
    if let Some(tags) = &manifest.tags {
        for t in tags {
            builder = builder.tag(t);
        }
    }

    // Parse ingredients
    let mut ingredients = Vec::new();
    if let Some(ing) = &manifest.ingredients {
        if let Some(models) = &ing.models {
            for m in models {
                let mut ib = Ingredient::model(&m.name);
                if let Some(p) = &m.provider {
                    ib = ib.provider(p);
                }
                if let Some(r) = &m.role {
                    ib = ib.role(r);
                }
                ingredients.push(ib.build());
            }
        }
        if let Some(tools) = &ing.tools {
            for t in tools {
                let mut ib = Ingredient::tool(&t.name);
                if let Some(p) = &t.protocol {
                    ib = ib.provider(p);
                }
                ingredients.push(ib.build());
            }
        }
    }

    let agent = ingredients
        .into_iter()
        .fold(builder, |a, i| a.ingredient(i))
        .build();

    client.register(&agent).await?;
    Ok(name)
}

async fn apply_recipe(
    client: &agentoven_core::AgentOvenClient,
    manifest: RecipeManifest,
) -> anyhow::Result<String> {
    let name = manifest.name.clone();

    let steps: Vec<Step> = manifest
        .steps
        .into_iter()
        .map(|s| {
            let kind = match s.step_kind.as_deref() {
                Some("human-gate") | Some("gate") => StepKind::HumanGate,
                Some("evaluator") => StepKind::Evaluator,
                Some("condition") | Some("branch") => StepKind::Condition,
                Some("fan-out") | Some("parallel") | Some("map") => StepKind::FanOut,
                Some("fan-in") | Some("join") => StepKind::FanIn,
                _ => StepKind::Agent,
            };
            Step {
                id: Uuid::new_v4().to_string(),
                name: s.name,
                agent: s.agent,
                kind,
                parallel: false,
                timeout: s.timeout_secs.map(|s| format!("{}s", s)),
                depends_on: s.depends_on.unwrap_or_default(),
                retry: None,
                notify: Vec::new(),
                config: None,
            }
        })
        .collect();

    let recipe = Recipe::new(&manifest.name, steps);
    client.create_recipe(&recipe).await?;
    Ok(name)
}

async fn apply_toolset(
    client: &agentoven_core::AgentOvenClient,
    manifest: ToolSetManifest,
) -> anyhow::Result<usize> {
    let count = manifest.tools.len();

    // Try bulk endpoint first, fall back to individual POSTs
    let tools_json: Vec<JsonValue> = manifest
        .tools
        .iter()
        .map(|t| {
            let mut obj = serde_json::json!({ "name": t.name });
            if let Some(desc) = &t.description {
                obj["description"] = serde_json::json!(desc);
            }
            if let Some(ep) = &t.endpoint {
                obj["endpoint"] = serde_json::json!(ep);
            }
            if let Some(tr) = &t.transport {
                obj["transport"] = serde_json::json!(tr);
            }
            if let Some(s) = &t.schema {
                obj["schema"] = s.clone();
            }
            if let Some(caps) = &t.capabilities {
                obj["capabilities"] = serde_json::json!(caps);
            }
            obj
        })
        .collect();

    // Try bulk endpoint
    match client.bulk_add_tools(serde_json::json!({ "tools": tools_json })).await {
        Ok(_) => return Ok(count),
        Err(_) => {
            // Fall back to individual adds
            for tool_json in tools_json {
                client.add_tool(tool_json).await?;
            }
        }
    }

    Ok(count)
}

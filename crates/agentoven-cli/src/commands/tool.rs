//! `agentoven tool` — manage MCP tools.

use agentoven_core::AgentOvenClient;
use clap::{Args, Subcommand};
use colored::Colorize;
use serde_json::{json, Value};

use super::packs::{pairs, print_install};
use super::signing::clip;

#[derive(Subcommand)]
pub enum ToolCommands {
    /// List all registered MCP tools.
    List,
    /// Add a new MCP tool.
    Add(AddArgs),
    /// Get details of a specific tool.
    Get(GetArgs),
    /// Update a tool's configuration.
    Update(UpdateArgs),
    /// Remove a tool.
    Remove(RemoveArgs),
    /// Import an OpenAPI 3 document as governed tools, one per operation (Pro, ADR-0048).
    ImportOpenapi(ImportOpenapiArgs),
}

#[derive(Args)]
pub struct ImportOpenapiArgs {
    /// Name the import. It prefixes the tool names (`<name>_<operationId>`).
    pub name: String,
    /// The document: a path to a JSON or YAML file, or an http(s) URL the server fetches
    /// (subject to the kitchen's egress allowlist).
    #[arg(long)]
    pub spec: String,
    /// Pin the API's address (default: the document's first server).
    #[arg(long)]
    pub base_url: Option<String>,
    /// How the API is authenticated: none, bearer, header, query, basic or oauth2_client_credentials
    /// (default: what the document says).
    #[arg(long)]
    pub auth: Option<String>,
    /// The header or query parameter name, for --auth header or query.
    #[arg(long)]
    pub auth_name: Option<String>,
    /// The token endpoint, for --auth oauth2_client_credentials.
    #[arg(long)]
    pub token_url: Option<String>,
    /// Scopes, space separated, for --auth oauth2_client_credentials.
    #[arg(long)]
    pub scopes: Option<String>,
    /// Import this operation id (repeatable). Without any, every read (GET) operation is imported;
    /// a write is imported only when named, and waits for approval.
    #[arg(long = "operation", short = 'o')]
    pub operation: Vec<String>,
    /// Bind a secret role to a reference: role=vault://name (repeatable). Roles: token (bearer),
    /// value (header or query), username and password (basic), client_id and client_secret (oauth2).
    #[arg(long = "secret", value_name = "ROLE=REF")]
    pub secret: Vec<String>,
    /// Import over an existing import of the same name (an upgrade with a new document).
    #[arg(long)]
    pub replace: bool,
    /// Only list the document's operations and what would be imported; install nothing.
    #[arg(long)]
    pub preview: bool,
}

#[derive(Args)]
pub struct AddArgs {
    /// Tool name.
    pub name: String,
    /// Tool description.
    #[arg(long, short)]
    pub description: Option<String>,
    /// Input schema as JSON string.
    #[arg(long)]
    pub schema: Option<String>,
    /// Input schema from file.
    #[arg(long)]
    pub schema_file: Option<String>,
}

#[derive(Args)]
pub struct GetArgs {
    /// Tool name.
    pub name: String,
}

#[derive(Args)]
pub struct UpdateArgs {
    /// Tool name.
    pub name: String,
    /// New description.
    #[arg(long, short)]
    pub description: Option<String>,
    /// New schema as JSON.
    #[arg(long)]
    pub schema: Option<String>,
}

#[derive(Args)]
pub struct RemoveArgs {
    /// Tool name.
    pub name: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
}

pub async fn execute(cmd: ToolCommands, client: AgentOvenClient) -> anyhow::Result<()> {
    match cmd {
        ToolCommands::List => list(&client).await,
        ToolCommands::Add(args) => add(args, &client).await,
        ToolCommands::Get(args) => get(args, &client).await,
        ToolCommands::Update(args) => update(args, &client).await,
        ToolCommands::Remove(args) => remove(args, &client).await,
        ToolCommands::ImportOpenapi(args) => import_openapi(args, &client).await,
    }
}

/// The document's source as the API takes it: a URL stays a URL, a file is read and sent as text.
fn spec_source(spec: &str) -> anyhow::Result<Value> {
    if spec.starts_with("http://") || spec.starts_with("https://") {
        return Ok(json!({ "url": spec }));
    }
    let text =
        std::fs::read_to_string(spec).map_err(|e| anyhow::anyhow!("cannot read {spec}: {e}"))?;
    Ok(json!({ "spec": text }))
}

async fn import_openapi(args: ImportOpenapiArgs, client: &AgentOvenClient) -> anyhow::Result<()> {
    let source = spec_source(&args.spec)?;
    if args.preview {
        let v = client.preview_openapi(&source).await?;
        println!(
            "\n  {} v{}  ({} operations)",
            v["title"].as_str().unwrap_or("-").bold(),
            v["version"].as_str().unwrap_or("-"),
            v["operations"].as_array().map_or(0, |a| a.len())
        );
        println!(
            "  servers: {}   suggested auth: {}\n",
            v["servers"]
                .as_array()
                .map(|a| a
                    .iter()
                    .filter_map(|s| s.as_str())
                    .collect::<Vec<_>>()
                    .join(", "))
                .unwrap_or_default(),
            v["auth"]["type"].as_str().unwrap_or("none")
        );
        for op in v["operations"].as_array().cloned().unwrap_or_default() {
            let note = op["unsupported"]
                .as_str()
                .map(|u| format!("not importable: {u}"))
                .unwrap_or_default();
            println!(
                "  {:<34} {:<7} {:<6} {} {}",
                clip(op["id"].as_str().unwrap_or(""), 34),
                op["method"].as_str().unwrap_or(""),
                op["access"].as_str().unwrap_or(""),
                clip(op["path"].as_str().unwrap_or(""), 40),
                note.dimmed()
            );
        }
        return Ok(());
    }

    let mut body = source;
    body["name"] = json!(args.name);
    body["replace"] = json!(args.replace);
    if let Some(b) = args.base_url {
        body["base_url"] = json!(b);
    }
    if let Some(t) = args.auth {
        let mut auth = json!({ "type": t });
        if let Some(n) = args.auth_name {
            auth["name"] = json!(n);
        }
        if let Some(u) = args.token_url {
            auth["token_url"] = json!(u);
        }
        if let Some(sc) = args.scopes {
            auth["scopes"] = json!(sc);
        }
        body["auth"] = auth;
    }
    if !args.operation.is_empty() {
        body["operations"] = json!(args.operation);
    }
    let secrets = pairs(&args.secret, "--secret")?;
    if !secrets.is_empty() {
        body["secrets"] = Value::Object(secrets);
    }
    println!();
    print_install(&client.import_openapi(&body).await?);
    Ok(())
}

async fn list(client: &AgentOvenClient) -> anyhow::Result<()> {
    println!("\n  🔧 MCP Tools:\n");

    match client.list_tools().await {
        Ok(tools) => {
            if tools.is_empty() {
                println!("  (no tools registered — use `agentoven tool add`)");
            } else {
                println!(
                    "  {:<24} {:<40} {:<20}",
                    "NAME".bold(),
                    "DESCRIPTION".bold(),
                    "UPDATED".bold()
                );
                println!("  {}", "─".repeat(84).dimmed());
                for t in &tools {
                    let name = t["name"].as_str().unwrap_or("-");
                    let desc = t["description"].as_str().unwrap_or("-");
                    let desc_trunc = if desc.len() > 38 { &desc[..38] } else { desc };
                    let updated = t["updated_at"].as_str().unwrap_or("-");
                    let updated_short = if updated.len() > 16 {
                        &updated[..16]
                    } else {
                        updated
                    };
                    println!("  {:<24} {:<40} {:<20}", name, desc_trunc, updated_short);
                }
                println!("\n  {} {} tool(s)", "→".dimmed(), tools.len());
            }
        }
        Err(e) => {
            println!(
                "  {} Could not reach control plane: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn add(args: AddArgs, client: &AgentOvenClient) -> anyhow::Result<()> {
    println!("\n  🔧 Adding tool: {}\n", args.name.bold());

    let schema = if let Some(ref s) = args.schema {
        Some(serde_json::from_str::<serde_json::Value>(s)?)
    } else if let Some(ref path) = args.schema_file {
        let content = tokio::fs::read_to_string(path).await?;
        Some(serde_json::from_str::<serde_json::Value>(&content)?)
    } else {
        None
    };

    let mut body = serde_json::json!({ "name": args.name });
    if let Some(desc) = &args.description {
        body["description"] = serde_json::json!(desc);
    }
    if let Some(s) = schema {
        body["input_schema"] = s;
    }

    match client.add_tool(body).await {
        Ok(_) => {
            println!("  {} Tool '{}' added!", "✓".green().bold(), args.name);
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn get(args: GetArgs, client: &AgentOvenClient) -> anyhow::Result<()> {
    println!("\n  🔧 Tool: {}\n", args.name.bold());

    match client.get_tool(&args.name).await {
        Ok(t) => {
            println!(
                "  {:<16} {}",
                "Name:".bold(),
                t["name"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Description:".bold(),
                t["description"].as_str().unwrap_or("-")
            );
            println!(
                "  {:<16} {}",
                "Updated:".bold(),
                t["updated_at"].as_str().unwrap_or("-")
            );
            if let Some(schema) = t.get("input_schema") {
                println!("\n  {}:", "Input Schema".bold());
                println!("  {}", serde_json::to_string_pretty(schema)?.dimmed());
            }
        }
        Err(e) => {
            println!(
                "  {} Not found: {}",
                "⚠".yellow().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn update(args: UpdateArgs, client: &AgentOvenClient) -> anyhow::Result<()> {
    println!("\n  🔧 Updating tool: {}\n", args.name.bold());

    let mut body = serde_json::json!({});
    if let Some(desc) = &args.description {
        body["description"] = serde_json::json!(desc);
    }
    if let Some(s) = &args.schema {
        body["input_schema"] = serde_json::from_str(s)?;
    }

    match client.update_tool(&args.name, body).await {
        Ok(_) => println!("  {} Tool '{}' updated.", "✓".green().bold(), args.name),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

async fn remove(args: RemoveArgs, client: &AgentOvenClient) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Remove tool '{}'?", args.name))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    match client.delete_tool(&args.name).await {
        Ok(()) => println!("  {} Tool '{}' removed.", "✓".green().bold(), args.name),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

//! `agentoven init` — initialize a new AgentOven project.

use clap::Args;
use colored::Colorize;
use std::path::PathBuf;

#[derive(Args)]
pub struct InitArgs {
    /// Project directory (defaults to current directory).
    #[arg(default_value = ".")]
    pub path: PathBuf,

    /// Agent name.
    #[arg(long, short)]
    pub name: Option<String>,

    /// Agent framework to use.
    #[arg(long, short, default_value = "custom")]
    pub framework: String,
}

pub async fn execute(args: InitArgs) -> anyhow::Result<()> {
    let project_dir = args.path.canonicalize().unwrap_or(args.path.clone());
    let project_name = args.name.unwrap_or_else(|| {
        project_dir
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("my-agent")
            .to_string()
    });

    println!();
    println!(
        "  {} Initializing AgentOven project: {}",
        "🏺".to_string(),
        project_name.bold()
    );
    println!();

    // Create agentoven.toml
    let config = format!(
        r#"# AgentOven project configuration
# Docs: https://agentoven.dev/docs/configuration

[agent]
name = "{project_name}"
version = "0.1.0"
description = ""
framework = "{framework}"

[ingredients]
# Models used by this agent
# [[ingredients.models]]
# name = "gpt-4o"
# provider = "azure-openai"
# role = "primary"

# [[ingredients.models]]
# name = "claude-sonnet"
# provider = "anthropic"
# role = "fallback"

# Tools used by this agent (via MCP)
# [[ingredients.tools]]
# name = "document-reader"
# protocol = "mcp"
# endpoint = "http://localhost:3001"

[bake]
# Deployment configuration
# environment = "production"
# replicas = 1

[oven]
# Control plane connection
# url = "http://localhost:8080"
# kitchen = "default"
"#,
        project_name = project_name,
        framework = args.framework,
    );

    let config_path = project_dir.join("agentoven.toml");
    tokio::fs::create_dir_all(&project_dir).await?;
    tokio::fs::write(&config_path, config).await?;

    println!(
        "  {} Created {}",
        "✓".green().bold(),
        "agentoven.toml".cyan()
    );

    // Create prompts directory
    let prompts_dir = project_dir.join("prompts");
    tokio::fs::create_dir_all(&prompts_dir).await?;

    let system_prompt = format!(
        "You are {project_name}, an AI agent.\n\nDescribe this agent's purpose and behavior here.\n"
    );
    tokio::fs::write(prompts_dir.join("system.md"), system_prompt).await?;

    println!(
        "  {} Created {}",
        "✓".green().bold(),
        "prompts/system.md".cyan()
    );

    // Create .gitignore addition
    let gitignore = ".agentoven/\n*.pyc\n__pycache__/\n.env\n";
    let gitignore_path = project_dir.join(".gitignore");
    if !gitignore_path.exists() {
        tokio::fs::write(&gitignore_path, gitignore).await?;
        println!(
            "  {} Created {}",
            "✓".green().bold(),
            ".gitignore".cyan()
        );
    }

    println!();
    println!("  {} Project initialized!", "🔥".to_string());
    println!();
    println!("  Next steps:");
    println!(
        "    1. Edit {} to configure your agent",
        "agentoven.toml".cyan()
    );
    println!(
        "    2. Run {} to register with the oven",
        "agentoven agent register".green()
    );
    println!(
        "    3. Run {} to deploy",
        "agentoven agent bake".green()
    );
    println!();

    Ok(())
}

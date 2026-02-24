//! `agentoven session` — manage multi-turn chat sessions.

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum SessionCommands {
    /// List sessions for an agent.
    List(ListArgs),
    /// Create a new session for an agent.
    Create(CreateArgs),
    /// Get session details and message history.
    Get(GetArgs),
    /// Delete a session.
    Delete(DeleteArgs),
    /// Send a message to a session.
    Send(SendArgs),
    /// Interactive chat within a session.
    Chat(ChatArgs),
}

#[derive(Args)]
pub struct ListArgs {
    /// Agent name.
    pub agent: String,
}

#[derive(Args)]
pub struct CreateArgs {
    /// Agent name.
    pub agent: String,
}

#[derive(Args)]
pub struct GetArgs {
    /// Agent name.
    pub agent: String,
    /// Session ID.
    pub session_id: String,
}

#[derive(Args)]
pub struct DeleteArgs {
    /// Agent name.
    pub agent: String,
    /// Session ID.
    pub session_id: String,
    /// Skip confirmation.
    #[arg(long)]
    pub force: bool,
}

#[derive(Args)]
pub struct SendArgs {
    /// Agent name.
    pub agent: String,
    /// Session ID.
    pub session_id: String,
    /// Message text.
    #[arg(long, short)]
    pub message: String,
    /// Enable thinking / chain-of-thought.
    #[arg(long)]
    pub thinking: bool,
}

#[derive(Args)]
pub struct ChatArgs {
    /// Agent name.
    pub agent: String,
    /// Session ID (optional — creates new session if omitted).
    pub session_id: Option<String>,
    /// Enable thinking / chain-of-thought.
    #[arg(long)]
    pub thinking: bool,
}

pub async fn execute(cmd: SessionCommands) -> anyhow::Result<()> {
    match cmd {
        SessionCommands::List(args) => list(args).await,
        SessionCommands::Create(args) => create(args).await,
        SessionCommands::Get(args) => get(args).await,
        SessionCommands::Delete(args) => delete(args).await,
        SessionCommands::Send(args) => send(args).await,
        SessionCommands::Chat(args) => chat(args).await,
    }
}

async fn list(args: ListArgs) -> anyhow::Result<()> {
    println!("\n  {} Sessions for '{}':\n", "💬".to_string(), args.agent.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.list_sessions(&args.agent).await {
        Ok(sessions) => {
            if sessions.is_empty() {
                println!("  (no sessions — use `agentoven session create`)");
            } else {
                println!(
                    "  {:<38} {:<12} {:<20}",
                    "SESSION ID".bold(), "MESSAGES".bold(), "LAST ACTIVITY".bold()
                );
                println!("  {}", "─".repeat(72).dimmed());
                for s in &sessions {
                    let id = s["id"].as_str().unwrap_or("-");
                    let msgs = s["message_count"].as_u64()
                        .or_else(|| s["messages"].as_array().map(|a| a.len() as u64))
                        .unwrap_or(0);
                    let last = s["updated_at"].as_str().unwrap_or("-");
                    let last_short = if last.len() > 16 { &last[..16] } else { last };
                    println!("  {:<38} {:<12} {}", id, msgs, last_short);
                }
                println!("\n  {} {} session(s)", "→".dimmed(), sessions.len());
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn create(args: CreateArgs) -> anyhow::Result<()> {
    println!("\n  {} Creating session for '{}'...\n", "💬".to_string(), args.agent.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.create_session(&args.agent).await {
        Ok(session) => {
            let id = session["id"].as_str().unwrap_or("?");
            println!("  {} Session created: {}", "✓".green().bold(), id.bold());
            println!("\n  {} Start chatting:", "→".dimmed());
            println!("    agentoven session chat {} {}", args.agent, id);
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn get(args: GetArgs) -> anyhow::Result<()> {
    println!("\n  {} Session: {}\n", "💬".to_string(), args.session_id.bold());

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.get_session(&args.agent, &args.session_id).await {
        Ok(s) => {
            println!("  {:<16} {}", "Agent:".bold(), args.agent);
            println!("  {:<16} {}", "Session:".bold(), s["id"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Created:".bold(), s["created_at"].as_str().unwrap_or("-"));
            println!("  {:<16} {}", "Updated:".bold(), s["updated_at"].as_str().unwrap_or("-"));

            if let Some(messages) = s["messages"].as_array() {
                println!("\n  {} ({} messages):", "Messages".bold(), messages.len());
                println!("  {}", "─".repeat(60).dimmed());
                for msg in messages {
                    let role = msg["role"].as_str().unwrap_or("?");
                    let text = msg["content"].as_str()
                        .or_else(|| msg["text"].as_str())
                        .unwrap_or("");
                    let icon = match role {
                        "user" => "👤",
                        "assistant" => "🤖",
                        "system" => "⚙️ ",
                        _ => "·",
                    };
                    let label = match role {
                        "user" => "You".cyan().bold().to_string(),
                        "assistant" => "Agent".green().bold().to_string(),
                        "system" => "System".yellow().bold().to_string(),
                        _ => role.to_string(),
                    };
                    println!("\n  {} {}:", icon, label);
                    for line in text.lines() {
                        println!("    {}", line);
                    }
                }
            }
        }
        Err(e) => {
            println!("  {} Not found: {}", "⚠".yellow().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn delete(args: DeleteArgs) -> anyhow::Result<()> {
    if !args.force {
        let confirm = dialoguer::Confirm::new()
            .with_prompt(format!("  Delete session '{}'?", args.session_id))
            .default(false)
            .interact()?;
        if !confirm {
            println!("  {} Cancelled.", "→".dimmed());
            return Ok(());
        }
    }

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.delete_session(&args.agent, &args.session_id).await {
        Ok(()) => println!("  {} Session deleted.", "✓".green().bold()),
        Err(e) => println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed()),
    }
    Ok(())
}

async fn send(args: SendArgs) -> anyhow::Result<()> {
    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.send_session_message(&args.agent, &args.session_id, &args.message, args.thinking).await {
        Ok(result) => {
            if let Some(reply) = result["reply"].as_str()
                .or_else(|| result["content"].as_str())
                .or_else(|| result["message"].as_str())
            {
                println!("\n  {} {}:\n", "🤖".to_string(), "Agent".green().bold());
                for line in reply.lines() {
                    println!("    {}", line);
                }
                println!();
            } else {
                let pretty = serde_json::to_string_pretty(&result).unwrap_or_default();
                println!("{}", pretty);
            }

            if args.thinking {
                if let Some(thinking) = result.get("thinking") {
                    if let Some(t) = thinking.as_str() {
                        println!("  {} {}:", "💭".to_string(), "Thinking".dimmed());
                        for line in t.lines() {
                            println!("    {}", line.dimmed());
                        }
                        println!();
                    }
                }
            }
        }
        Err(e) => {
            println!("  {} Failed: {}", "✗".red().bold(), e.to_string().dimmed());
        }
    }
    Ok(())
}

async fn chat(args: ChatArgs) -> anyhow::Result<()> {
    let client = agentoven_core::AgentOvenClient::from_env()?;

    let session_id = if let Some(id) = args.session_id {
        id
    } else {
        match client.create_session(&args.agent).await {
            Ok(s) => s["id"].as_str().unwrap_or("unknown").to_string(),
            Err(e) => {
                println!("  {} Failed to create session: {}", "✗".red().bold(), e);
                return Ok(());
            }
        }
    };

    println!("\n  {} Chat session: {} → {}", "💬".to_string(), args.agent.bold(), session_id.dimmed());
    println!("  {} Type your message (Ctrl+D or 'quit' to exit)\n", "→".dimmed());

    loop {
        let input: String = match dialoguer::Input::<String>::new()
            .with_prompt(format!("  {}", "You".cyan().bold()))
            .allow_empty(false)
            .interact_text()
        {
            Ok(v) => v,
            Err(_) => break,
        };

        if input.trim().eq_ignore_ascii_case("quit") || input.trim().eq_ignore_ascii_case("exit") {
            break;
        }

        match client.send_session_message(&args.agent, &session_id, &input, args.thinking).await {
            Ok(result) => {
                let reply = result["reply"].as_str()
                    .or_else(|| result["content"].as_str())
                    .or_else(|| result["message"].as_str())
                    .unwrap_or("(no response)");

                println!("\n  {} {}:\n", "🤖".to_string(), "Agent".green().bold());
                for line in reply.lines() {
                    println!("    {}", line);
                }
                println!();
            }
            Err(e) => {
                println!("  {} Error: {}\n", "✗".red().bold(), e.to_string().dimmed());
            }
        }
    }

    println!("\n  {} Session ended.", "→".dimmed());
    Ok(())
}

//! `agentoven server` — start the AgentOven control plane in the foreground.
//!
//! This command launches the Go control plane server directly, keeping it
//! in the foreground with live log output. Unlike `agentoven local up`
//! (which downloads a Docker image or binary), this requires the server
//! binary to already be available — either pre-built or specified via
//! `--bin` / `AGENTOVEN_SERVER_BIN`.
//!
//! The server runs with in-memory store by default (zero config).
//! Set `DATABASE_URL` to use PostgreSQL, which also auto-enables pgvector
//! for RAG vector storage.

use clap::Args;
use colored::Colorize;
use std::process::Command as StdCommand;
use tokio::signal;

#[derive(Args)]
pub struct ServerArgs {
    /// Port for the API server (default: 8080).
    #[arg(long, short, default_value = "8080")]
    pub port: u16,

    /// Path to the control plane binary. Auto-detected if not set.
    #[arg(long, env = "AGENTOVEN_SERVER_BIN")]
    pub bin: Option<String>,

    /// PostgreSQL connection URL for persistent storage + pgvector.
    /// When set, the server uses PostgreSQL instead of in-memory store
    /// and automatically registers pgvector for RAG operations.
    #[arg(long, env = "DATABASE_URL")]
    pub database_url: Option<String>,

    /// pgvector connection URL (if different from DATABASE_URL).
    /// When omitted, falls back to DATABASE_URL for pgvector.
    #[arg(long, env = "AGENTOVEN_PGVECTOR_URL")]
    pub pgvector_url: Option<String>,

    /// OpenAI API key for model routing and embeddings.
    #[arg(long, env = "OPENAI_API_KEY")]
    pub openai_api_key: Option<String>,

    /// Ollama URL for local model routing and embeddings.
    #[arg(long, env = "OLLAMA_URL")]
    pub ollama_url: Option<String>,

    /// CORS allowed origins (comma-separated, default: "*").
    #[arg(long, default_value = "*")]
    pub cors_origins: String,
}

pub async fn execute(args: ServerArgs) -> anyhow::Result<()> {
    println!();
    println!("  🏺 {}", "AgentOven Server".bold());
    println!();

    let port = args.port;

    // Find the server binary
    let server_bin = match args.bin {
        Some(bin) => bin,
        None => find_server_binary()?,
    };

    // Build environment
    let mut cmd = StdCommand::new(&server_bin);
    cmd.env("AGENTOVEN_PORT", port.to_string());
    cmd.env("AGENTOVEN_CORS_ORIGINS", &args.cors_origins);

    // Database / pgvector
    let store_mode;
    if let Some(ref db_url) = args.database_url {
        cmd.env("DATABASE_URL", db_url);
        store_mode = "postgresql";
        // If no dedicated pgvector URL, the server will auto-discover from DATABASE_URL
        if let Some(ref pgv_url) = args.pgvector_url {
            cmd.env("AGENTOVEN_PGVECTOR_URL", pgv_url);
        }
    } else {
        store_mode = "in-memory";
        if let Some(ref pgv_url) = args.pgvector_url {
            cmd.env("AGENTOVEN_PGVECTOR_URL", pgv_url);
        }
    }

    // Model provider env vars
    if let Some(ref key) = args.openai_api_key {
        cmd.env("OPENAI_API_KEY", key);
    }
    if let Some(ref url) = args.ollama_url {
        cmd.env("OLLAMA_URL", url);
    }

    println!("  Port:      {}", port.to_string().cyan());
    println!("  Store:     {}", store_mode.cyan());
    if args.pgvector_url.is_some() || args.database_url.is_some() {
        println!("  pgvector:  {}", "enabled".green());
    } else {
        println!(
            "  pgvector:  {} (set --database-url or --pgvector-url to enable)",
            "disabled".yellow()
        );
    }
    println!("  Binary:    {}", server_bin.dimmed());
    println!();

    // Inherit stdout/stderr so logs stream to the terminal
    cmd.stdout(std::process::Stdio::inherit());
    cmd.stderr(std::process::Stdio::inherit());

    let mut child = cmd.spawn().map_err(|e| {
        anyhow::anyhow!(
            "Failed to start server: {e}\n  Binary: {server_bin}\n\n  \
             If the binary is not installed, run:\n    \
             cd control-plane && go build -o agentoven-server ./cmd/server/\n\n  \
             Or use `agentoven local up` to download and run via Docker."
        )
    })?;

    // Auto-configure CLI to point at this server
    let local_url = format!("http://localhost:{port}");
    let mut config = agentoven_core::AgentOvenConfig::load();
    config.url = local_url.clone();
    config.edition = Some("community".into());
    config.save()?;

    println!("  {} CLI configured → {}", "✓".green().bold(), local_url.cyan());
    println!("  Press {} to stop.", "Ctrl+C".bold());
    println!();

    // Wait for Ctrl+C, then gracefully stop the server
    tokio::select! {
        status = tokio::task::spawn_blocking(move || child.wait()) => {
            match status {
                Ok(Ok(exit)) => {
                    if !exit.success() {
                        anyhow::bail!("Server exited with status: {}", exit);
                    }
                }
                Ok(Err(e)) => anyhow::bail!("Failed to wait for server: {e}"),
                Err(e) => anyhow::bail!("Task join error: {e}"),
            }
        }
        _ = signal::ctrl_c() => {
            println!();
            println!("  🛑 Shutting down server...");
            // The server handles SIGINT/SIGTERM itself for graceful shutdown.
            // On macOS/Linux, Ctrl+C sends SIGINT to the process group,
            // so the child will also receive it.
            // Give it time to shut down gracefully.
            tokio::time::sleep(tokio::time::Duration::from_secs(3)).await;
            println!("  👋 AgentOven stopped.");
        }
    }

    println!();
    Ok(())
}

/// Find the server binary by checking common locations.
fn find_server_binary() -> anyhow::Result<String> {
    // 1. Check if `agentoven-server` is in PATH
    if let Ok(output) = StdCommand::new("which")
        .arg("agentoven-server")
        .output()
    {
        if output.status.success() {
            let path = String::from_utf8_lossy(&output.stdout).trim().to_string();
            if !path.is_empty() {
                return Ok(path);
            }
        }
    }

    // 2. Check next to the CLI binary
    if let Ok(exe) = std::env::current_exe() {
        let dir = exe.parent().unwrap_or(std::path::Path::new("."));
        let candidate = dir.join("agentoven-server");
        if candidate.exists() {
            return Ok(candidate.to_string_lossy().to_string());
        }
    }

    // 3. Check in the local cache (~/.agentoven/local/bin/)
    if let Some(home) = dirs::home_dir() {
        let candidate = home
            .join(".agentoven")
            .join("local")
            .join("bin")
            .join("agentoven-server");
        if candidate.exists() {
            return Ok(candidate.to_string_lossy().to_string());
        }
    }

    // 4. Check in the workspace (when developing)
    let cwd = std::env::current_dir().unwrap_or_default();
    for candidate_path in &[
        cwd.join("control-plane/agentoven-server"),
        cwd.join("server"),
        cwd.join("agentoven-server"),
    ] {
        if candidate_path.exists() {
            return Ok(candidate_path.to_string_lossy().to_string());
        }
    }

    Err(anyhow::anyhow!(
        "Could not find the AgentOven server binary.\n\n  \
         Options:\n    \
         1. Build it:  cd control-plane && go build -o agentoven-server ./cmd/server/\n    \
         2. Specify:   agentoven server --bin /path/to/agentoven-server\n    \
         3. Use Docker: agentoven local up"
    ))
}

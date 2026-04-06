//! `agentoven local` — run a local AgentOven server for development.
//!
//! Downloads the OSS server on-demand (Docker image or pre-built binary)
//! and manages its lifecycle. The server is **not** bundled with the CLI —
//! it's fetched the first time you run `agentoven local up`.
//!
//! Modes:
//!   Docker (default) — pulls `ghcr.io/agentoven/agentoven` + lightweight PG
//!   Binary           — downloads a pre-built Go binary from GitHub releases
//!
//! The CLI auto-configures `~/.agentoven/config.toml` to point at the local
//! server after startup, so subsequent commands Just Work.

use clap::Subcommand;
use colored::Colorize;
use std::path::PathBuf;
use std::process::Command;

/// Name of the Docker Compose project for `agentoven local`.
const COMPOSE_PROJECT: &str = "agentoven-local";

/// Default GHCR image for the OSS server.
const SERVER_IMAGE: &str = "ghcr.io/agentoven/agentoven";

/// GitHub owner/repo for release downloads.
const GITHUB_REPO: &str = "agentoven/agentoven";

/// Default port for the local server.
const DEFAULT_PORT: u16 = 8080;

#[derive(Subcommand)]
pub enum LocalCommands {
    /// Start a local AgentOven server (downloads on first run).
    Up {
        /// Port for the API server (default: 8080).
        #[arg(long, short, default_value_t = DEFAULT_PORT)]
        port: u16,

        /// Force Docker mode (requires Docker Desktop / Docker Engine).
        #[arg(long)]
        docker: bool,

        /// Force binary download mode (no Docker required).
        #[arg(long, conflicts_with = "docker")]
        binary: bool,

        /// Server version to download (default: latest).
        #[arg(long)]
        version: Option<String>,

        /// Run without PostgreSQL (in-memory store, data lost on restart).
        #[arg(long)]
        no_pg: bool,
    },

    /// Stop the local server.
    Down,

    /// Check if the local server is running.
    Status,

    /// View local server logs.
    Logs {
        /// Number of lines to show.
        #[arg(long, short, default_value_t = 50)]
        lines: usize,

        /// Follow log output (like `tail -f`).
        #[arg(long, short)]
        follow: bool,
    },

    /// Stop the server, wipe all local data, and start fresh.
    Reset,
}

/// Execute a `local` subcommand.
pub async fn execute(cmd: LocalCommands) -> anyhow::Result<()> {
    match cmd {
        LocalCommands::Up {
            port,
            docker,
            binary,
            version,
            no_pg,
        } => up(port, docker, binary, version, no_pg).await,
        LocalCommands::Down => down().await,
        LocalCommands::Status => status().await,
        LocalCommands::Logs { lines, follow } => logs(lines, follow).await,
        LocalCommands::Reset => reset().await,
    }
}

// ── Paths ────────────────────────────────────────────────────────────

/// `~/.agentoven/local/`
fn local_dir() -> anyhow::Result<PathBuf> {
    let home = dirs::home_dir().ok_or_else(|| anyhow::anyhow!("Cannot determine home directory"))?;
    let dir = home.join(".agentoven").join("local");
    std::fs::create_dir_all(&dir)?;
    Ok(dir)
}

/// `~/.agentoven/local/docker-compose.yml`
fn compose_file() -> anyhow::Result<PathBuf> {
    Ok(local_dir()?.join("docker-compose.yml"))
}

/// `~/.agentoven/local/bin/agentoven-server`
fn binary_path() -> anyhow::Result<PathBuf> {
    let dir = local_dir()?.join("bin");
    std::fs::create_dir_all(&dir)?;
    Ok(dir.join("agentoven-server"))
}

/// `~/.agentoven/local/server.pid`
fn pid_file() -> anyhow::Result<PathBuf> {
    Ok(local_dir()?.join("server.pid"))
}

/// `~/.agentoven/local/server.log`
fn log_file() -> anyhow::Result<PathBuf> {
    Ok(local_dir()?.join("server.log"))
}

/// `~/.agentoven/local/mode` — records which mode was used for `down`.
fn mode_file() -> anyhow::Result<PathBuf> {
    Ok(local_dir()?.join("mode"))
}

// ── Mode Detection ───────────────────────────────────────────────────

/// Returns true if `docker` CLI is available and the daemon is running.
fn docker_available() -> bool {
    Command::new("docker")
        .args(["info"])
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .status()
        .map(|s| s.success())
        .unwrap_or(false)
}

/// Decide mode: Docker or Binary.
fn choose_mode(force_docker: bool, force_binary: bool) -> &'static str {
    if force_docker {
        return "docker";
    }
    if force_binary {
        return "binary";
    }
    if docker_available() {
        "docker"
    } else {
        "binary"
    }
}

// ── UP ───────────────────────────────────────────────────────────────

async fn up(
    port: u16,
    force_docker: bool,
    force_binary: bool,
    version: Option<String>,
    no_pg: bool,
) -> anyhow::Result<()> {
    let mode = choose_mode(force_docker, force_binary);

    // Bail early if force-docker but Docker not available
    if force_docker && !docker_available() {
        eprintln!(
            "{} Docker is not installed or the daemon is not running.",
            "error:".red().bold()
        );
        eprintln!("       Install Docker Desktop: https://www.docker.com/products/docker-desktop");
        eprintln!("       Or use: agentoven local up --binary");
        std::process::exit(1);
    }

    let version = version.unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_string());

    println!(
        "\n  {} Starting local AgentOven server...",
        "🏺".to_string()
    );
    println!("  Mode:    {}", mode.cyan().bold());
    println!("  Port:    {}", port.to_string().cyan());
    println!("  Version: {}\n", version.cyan());

    // Save mode for `down` / `status`
    std::fs::write(mode_file()?, mode)?;

    match mode {
        "docker" => up_docker(port, &version, no_pg).await,
        "binary" => up_binary(port, &version, no_pg).await,
        _ => unreachable!(),
    }
}

// ── Docker Mode ──────────────────────────────────────────────────────

async fn up_docker(port: u16, version: &str, no_pg: bool) -> anyhow::Result<()> {
    let compose_path = compose_file()?;

    // Generate a docker-compose.yml tailored for `agentoven local`
    let compose_content = generate_compose(port, version, no_pg);
    std::fs::write(&compose_path, &compose_content)?;
    println!(
        "  {} Wrote {}",
        "✓".green().bold(),
        compose_path.display()
    );

    // Pull images
    println!("  {} Pulling images (first run may take a minute)...", "⏳".to_string());
    let pull = Command::new("docker")
        .args(["compose", "-f"])
        .arg(&compose_path)
        .args(["-p", COMPOSE_PROJECT, "pull"])
        .status()?;
    if !pull.success() {
        anyhow::bail!("docker compose pull failed");
    }

    // Start
    let up = Command::new("docker")
        .args(["compose", "-f"])
        .arg(&compose_path)
        .args(["-p", COMPOSE_PROJECT, "up", "-d", "--wait"])
        .status()?;
    if !up.success() {
        anyhow::bail!("docker compose up failed");
    }

    post_start(port).await
}

/// Generate a minimal docker-compose.yml for `agentoven local`.
fn generate_compose(port: u16, version: &str, no_pg: bool) -> String {
    let image = format!("{}:{}", SERVER_IMAGE, version);

    if no_pg {
        // Memory-only mode — single container, no PostgreSQL
        format!(
            r#"# Auto-generated by `agentoven local up` — do not edit
services:
  server:
    image: {image}
    ports:
      - "{port}:8080"
    environment:
      AGENTOVEN_PORT: "8080"
      AGENTOVEN_CORS_ORIGINS: "*"
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/api/v1/info"]
      interval: 5s
      timeout: 3s
      retries: 10
"#
        )
    } else {
        // Full mode — server + PostgreSQL (with pgvector extension for RAG)
        format!(
            r#"# Auto-generated by `agentoven local up` — do not edit
services:
  postgres:
    image: pgvector/pgvector:pg16
    environment:
      POSTGRES_USER: agentoven
      POSTGRES_PASSWORD: agentoven
      POSTGRES_DB: agentoven
    ports:
      - "5433:5432"
    volumes:
      - agentoven_pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U agentoven"]
      interval: 3s
      timeout: 3s
      retries: 10

  server:
    image: {image}
    ports:
      - "{port}:8080"
    environment:
      AGENTOVEN_PORT: "8080"
      DATABASE_URL: "postgres://agentoven:agentoven@postgres:5432/agentoven?sslmode=disable"
      AGENTOVEN_PGVECTOR_URL: "postgres://agentoven:agentoven@postgres:5432/agentoven?sslmode=disable"
      AGENTOVEN_CORS_ORIGINS: "*"
    depends_on:
      postgres:
        condition: service_healthy
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/api/v1/info"]
      interval: 5s
      timeout: 3s
      retries: 10

volumes:
  agentoven_pgdata:
"#
        )
    }
}

// ── Binary Mode ──────────────────────────────────────────────────────

async fn up_binary(port: u16, version: &str, no_pg: bool) -> anyhow::Result<()> {
    let bin = binary_path()?;

    // Download if not cached or version changed
    if !bin.exists() || !version_matches(&bin, version) {
        download_binary(&bin, version).await?;
    } else {
        println!("  {} Server binary cached at {}", "✓".green().bold(), bin.display());
    }

    // Check if already running
    if let Some(pid) = read_pid() {
        if process_alive(pid) {
            println!(
                "\n  {} Local server already running (PID {})",
                "ℹ".blue().bold(),
                pid
            );
            println!("  Use `agentoven local down` to stop it first.\n");
            return Ok(());
        }
    }

    // Build env
    let log = log_file()?;
    let log_handle = std::fs::File::create(&log)?;
    let err_handle = log_handle.try_clone()?;

    let mut cmd = Command::new(&bin);
    cmd.env("AGENTOVEN_PORT", port.to_string());
    cmd.env("AGENTOVEN_CORS_ORIGINS", "*");
    if !no_pg {
        eprintln!(
            "  {} Binary mode defaults to in-memory store (no PostgreSQL).",
            "ℹ".blue().bold()
        );
        eprintln!(
            "  Set DATABASE_URL to use PostgreSQL, or pass --no-pg to silence this."
        );
    }
    cmd.stdout(log_handle);
    cmd.stderr(err_handle);

    let child = cmd.spawn()?;
    let pid = child.id();

    // Write PID
    std::fs::write(pid_file()?, pid.to_string())?;
    println!("  {} Server started (PID {})", "✓".green().bold(), pid);
    println!("  Logs: {}", log.display());

    // Wait briefly for startup, then check health
    tokio::time::sleep(tokio::time::Duration::from_secs(2)).await;

    post_start(port).await
}

/// Download the pre-built server binary from GitHub releases.
async fn download_binary(dest: &PathBuf, version: &str) -> anyhow::Result<()> {
    let (os, arch) = detect_platform()?;
    let archive = format!("agentoven-server-{os}-{arch}.tar.gz");
    let url = format!(
        "https://github.com/{GITHUB_REPO}/releases/download/v{version}/{archive}"
    );

    println!("  {} Downloading server v{version}...", "⏳".to_string());
    println!("  {}", url.dimmed());

    // Download with reqwest
    let client = reqwest::Client::new();
    let resp = client.get(&url).send().await?;

    if !resp.status().is_success() {
        anyhow::bail!(
            "Download failed (HTTP {}). Version v{} may not have pre-built binaries.\n\
             Try: agentoven local up --docker",
            resp.status(),
            version
        );
    }

    let bytes = resp.bytes().await?;

    // Extract using system `tar` (available on macOS + Linux)
    let parent = dest
        .parent()
        .ok_or_else(|| anyhow::anyhow!("Invalid binary path"))?;

    // Write archive to temp file
    let archive_path = parent.join(&archive);
    std::fs::write(&archive_path, &bytes)?;

    let tar = Command::new("tar")
        .args(["xzf"])
        .arg(&archive_path)
        .args(["-C"])
        .arg(parent)
        .status()?;

    if !tar.success() {
        anyhow::bail!("Failed to extract server binary from {}", archive);
    }

    // Clean up archive
    let _ = std::fs::remove_file(&archive_path);

    // Make executable
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mut perms = std::fs::metadata(dest)?.permissions();
        perms.set_mode(0o755);
        std::fs::set_permissions(dest, perms)?;
    }

    // Tag with version
    let version_file = dest.with_extension("version");
    std::fs::write(&version_file, version)?;

    println!("  {} Downloaded to {}", "✓".green().bold(), dest.display());
    Ok(())
}

fn detect_platform() -> anyhow::Result<(&'static str, &'static str)> {
    let os = if cfg!(target_os = "macos") {
        "darwin"
    } else if cfg!(target_os = "linux") {
        "linux"
    } else if cfg!(target_os = "windows") {
        "windows"
    } else {
        anyhow::bail!("Unsupported OS for binary download. Use --docker instead.");
    };

    let arch = if cfg!(target_arch = "x86_64") {
        "amd64"
    } else if cfg!(target_arch = "aarch64") {
        "arm64"
    } else {
        anyhow::bail!("Unsupported CPU architecture. Use --docker instead.");
    };

    Ok((os, arch))
}

fn version_matches(bin: &PathBuf, version: &str) -> bool {
    let version_file = bin.with_extension("version");
    std::fs::read_to_string(version_file)
        .map(|v| v.trim() == version)
        .unwrap_or(false)
}

// ── Post-Start ───────────────────────────────────────────────────────

async fn post_start(port: u16) -> anyhow::Result<()> {
    // Health check with retries
    let url = format!("http://localhost:{port}/api/v1/info");
    let client = reqwest::Client::new();
    let mut healthy = false;
    for i in 0..15 {
        if i > 0 {
            tokio::time::sleep(tokio::time::Duration::from_secs(1)).await;
        }
        if let Ok(resp) = client.get(&url).send().await {
            if resp.status().is_success() {
                healthy = true;
                break;
            }
        }
    }

    if !healthy {
        eprintln!(
            "\n  {} Server did not become healthy within 15 seconds.",
            "⚠".yellow().bold()
        );
        eprintln!("  Check logs: agentoven local logs");
        return Ok(());
    }

    // Auto-configure CLI to point at this server
    let local_url = format!("http://localhost:{port}");
    let mut config = agentoven_core::AgentOvenConfig::load();
    config.url = local_url.clone();
    config.edition = Some("community".into());
    config.save()?;

    println!("\n  {} Local AgentOven is ready!", "🟢".to_string());
    println!();
    println!("    API:       {}", local_url.cyan().bold());
    println!("    Dashboard: {}/dashboard", local_url);
    println!();
    println!("  The CLI is now configured to use this server.");
    println!("  Try: {} or {}", "agentoven agent list".cyan(), "agentoven status".cyan());
    println!();

    Ok(())
}

// ── DOWN ─────────────────────────────────────────────────────────────

async fn down() -> anyhow::Result<()> {
    let mode = read_mode();

    match mode.as_deref() {
        Some("docker") => down_docker().await,
        Some("binary") => down_binary().await,
        _ => {
            // Try both
            let docker_ran = down_docker().await.is_ok();
            let binary_ran = down_binary().await.is_ok();
            if !docker_ran && !binary_ran {
                println!("  {} No local server is running.", "ℹ".blue().bold());
            }
            Ok(())
        }
    }
}

async fn down_docker() -> anyhow::Result<()> {
    let compose_path = compose_file()?;
    if !compose_path.exists() {
        anyhow::bail!("No Docker compose file found");
    }

    println!("  Stopping Docker containers...");
    let status = Command::new("docker")
        .args(["compose", "-f"])
        .arg(&compose_path)
        .args(["-p", COMPOSE_PROJECT, "down"])
        .status()?;

    if status.success() {
        println!("  {} Local server stopped (Docker)", "✓".green().bold());
    }
    Ok(())
}

async fn down_binary() -> anyhow::Result<()> {
    if let Some(pid) = read_pid() {
        if process_alive(pid) {
            println!("  Stopping server (PID {})...", pid);
            kill_process(pid);
            // Wait for exit
            for _ in 0..10 {
                if !process_alive(pid) {
                    break;
                }
                tokio::time::sleep(tokio::time::Duration::from_millis(200)).await;
            }
            if process_alive(pid) {
                // Force kill
                force_kill_process(pid);
            }
            println!("  {} Local server stopped (binary)", "✓".green().bold());
        } else {
            println!("  {} Server was not running (stale PID file)", "ℹ".blue().bold());
        }
        let _ = std::fs::remove_file(pid_file()?);
    } else {
        anyhow::bail!("No PID file found");
    }
    Ok(())
}

// ── STATUS ───────────────────────────────────────────────────────────

async fn status() -> anyhow::Result<()> {
    let mode = read_mode();

    println!();

    match mode.as_deref() {
        Some("docker") => {
            println!("  Mode: {}", "docker".cyan().bold());
            let output = Command::new("docker")
                .args(["compose", "-p", COMPOSE_PROJECT, "ps", "--format", "table"])
                .output()?;
            if output.status.success() {
                let table = String::from_utf8_lossy(&output.stdout);
                if table.trim().is_empty() || !table.contains("agentoven") {
                    println!("  Status: {} (no containers running)", "stopped".red());
                } else {
                    println!("  Status: {}", "running".green().bold());
                    println!();
                    print!("{}", table);
                }
            } else {
                println!("  Status: {} (docker compose not available)", "unknown".yellow());
            }
        }
        Some("binary") => {
            println!("  Mode: {}", "binary".cyan().bold());
            if let Some(pid) = read_pid() {
                if process_alive(pid) {
                    println!("  Status: {} (PID {})", "running".green().bold(), pid);
                } else {
                    println!("  Status: {} (PID {} exited)", "stopped".red(), pid);
                }
            } else {
                println!("  Status: {}", "stopped".red());
            }
        }
        _ => {
            println!("  {} No local server has been started.", "ℹ".blue().bold());
            println!("  Use `agentoven local up` to start one.");
        }
    }

    println!();
    Ok(())
}

// ── LOGS ─────────────────────────────────────────────────────────────

async fn logs(lines: usize, follow: bool) -> anyhow::Result<()> {
    let mode = read_mode();

    match mode.as_deref() {
        Some("docker") => {
            let compose_path = compose_file()?;
            let mut args = vec![
                "compose",
                "-f",
                compose_path.to_str().unwrap_or(""),
                "-p",
                COMPOSE_PROJECT,
                "logs",
                "server",
                "--tail",
            ];
            let lines_str = lines.to_string();
            args.push(&lines_str);
            if follow {
                args.push("--follow");
            }
            Command::new("docker").args(&args).status()?;
        }
        Some("binary") => {
            let log = log_file()?;
            if !log.exists() {
                println!("  No log file found. Is the server running?");
                return Ok(());
            }
            if follow {
                Command::new("tail")
                    .args(["-f", "-n"])
                    .arg(lines.to_string())
                    .arg(&log)
                    .status()?;
            } else {
                Command::new("tail")
                    .args(["-n"])
                    .arg(lines.to_string())
                    .arg(&log)
                    .status()?;
            }
        }
        _ => {
            println!("  No local server has been started. Use `agentoven local up`.");
        }
    }
    Ok(())
}

// ── RESET ────────────────────────────────────────────────────────────

async fn reset() -> anyhow::Result<()> {
    println!("\n  {} Resetting local AgentOven data...\n", "⚠".yellow().bold());

    // Stop everything
    down().await.ok();

    let mode = read_mode();

    match mode.as_deref() {
        Some("docker") => {
            // Remove volumes
            let compose_path = compose_file()?;
            if compose_path.exists() {
                Command::new("docker")
                    .args(["compose", "-f"])
                    .arg(&compose_path)
                    .args(["-p", COMPOSE_PROJECT, "down", "-v", "--remove-orphans"])
                    .status()?;
            }
            println!("  {} Docker volumes removed", "✓".green().bold());
        }
        Some("binary") => {
            // Remove log and PID files
            let _ = std::fs::remove_file(log_file()?);
            let _ = std::fs::remove_file(pid_file()?);
            println!("  {} Binary data cleared", "✓".green().bold());
        }
        _ => {}
    }

    // Clear mode file
    let _ = std::fs::remove_file(mode_file()?);

    println!("  {} Reset complete. Run `agentoven local up` to start fresh.\n", "✓".green().bold());
    Ok(())
}

// ── Helpers ──────────────────────────────────────────────────────────

fn read_mode() -> Option<String> {
    mode_file()
        .ok()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .map(|s| s.trim().to_string())
}

fn read_pid() -> Option<u32> {
    pid_file()
        .ok()
        .and_then(|p| std::fs::read_to_string(p).ok())
        .and_then(|s| s.trim().parse().ok())
}

#[cfg(unix)]
fn process_alive(pid: u32) -> bool {
    unsafe { libc::kill(pid as i32, 0) == 0 }
}

#[cfg(not(unix))]
fn process_alive(_pid: u32) -> bool {
    // On Windows, fall back to checking the PID file existence
    false
}

#[cfg(unix)]
fn kill_process(pid: u32) {
    unsafe {
        libc::kill(pid as i32, libc::SIGTERM);
    }
}

#[cfg(not(unix))]
fn kill_process(pid: u32) {
    let _ = Command::new("taskkill")
        .args(["/PID", &pid.to_string()])
        .status();
}

#[cfg(unix)]
fn force_kill_process(pid: u32) {
    unsafe {
        libc::kill(pid as i32, libc::SIGKILL);
    }
}

#[cfg(not(unix))]
fn force_kill_process(pid: u32) {
    let _ = Command::new("taskkill")
        .args(["/F", "/PID", &pid.to_string()])
        .status();
}

//! `agentoven dashboard` — start the control plane and open the dashboard UI.

use clap::Args;
use colored::Colorize;
use std::process::Command as StdCommand;
use tokio::signal;

#[derive(Args)]
pub struct DashboardArgs {
    /// Port for the control plane server (default: 8080).
    #[arg(long, short, default_value = "8080")]
    pub port: u16,

    /// Don't open the browser automatically.
    #[arg(long)]
    pub no_open: bool,

    /// Path to a pre-built control plane binary (skips auto-detection).
    #[arg(long, env = "AGENTOVEN_SERVER_BIN")]
    pub server_bin: Option<String>,
}

pub async fn execute(args: DashboardArgs) -> anyhow::Result<()> {
    println!();
    println!(
        "  {} {}",
        "🏺".to_string(),
        "AgentOven Dashboard".bold()
    );
    println!();

    let port = args.port;

    // Check if the control plane is already running on this port
    if is_server_running(port).await {
        println!(
            "  {} Control plane already running on port {}",
            "✅".to_string(),
            port.to_string().cyan()
        );
        open_dashboard(port, args.no_open);
        println!(
            "  {} Dashboard: {}",
            "🌐".to_string(),
            format!("http://localhost:{port}").underline().cyan()
        );
        println!();
        return Ok(());
    }

    // Find the control plane binary
    let server_bin = match args.server_bin {
        Some(bin) => bin,
        None => find_server_binary()?,
    };

    println!(
        "  {} Starting control plane on port {}...",
        "🚀".to_string(),
        port.to_string().cyan()
    );

    // Start the control plane server as a child process
    let mut cmd = StdCommand::new(&server_bin);
    cmd.env("AGENTOVEN_PORT", port.to_string());

    // Help the server find the dashboard by resolving its path from the CLI binary location.
    if std::env::var("AGENTOVEN_DASHBOARD_DIR").is_err() {
        if let Some(dir) = find_dashboard_dir() {
            cmd.env("AGENTOVEN_DASHBOARD_DIR", &dir);
        }
    }

    let mut child = cmd
        .spawn()
        .map_err(|e| anyhow::anyhow!("Failed to start control plane: {e}\n  Binary: {server_bin}"))?;

    // Wait for the server to become ready (up to 15 seconds)
    let ready = wait_for_server(port, 15).await;
    if !ready {
        child.kill().ok();
        anyhow::bail!(
            "Control plane did not start within 15 seconds.\n  \
             Check the server logs for errors."
        );
    }

    println!(
        "  {} Control plane is hot and ready!",
        "🔥".to_string()
    );

    open_dashboard(port, args.no_open);

    println!(
        "  {} Dashboard: {}",
        "🌐".to_string(),
        format!("http://localhost:{port}").underline().cyan()
    );
    println!();
    println!("  Press {} to stop.", "Ctrl+C".bold());
    println!();

    // Wait for Ctrl+C, then kill the child
    signal::ctrl_c().await?;

    println!();
    println!(
        "  {} Shutting down control plane...",
        "🛑".to_string()
    );

    child.kill().ok();
    child.wait().ok();

    println!(
        "  {} AgentOven stopped. Goodbye!",
        "👋".to_string()
    );
    println!();

    Ok(())
}

/// Check if the server is already running on the given port.
async fn is_server_running(port: u16) -> bool {
    let url = format!("http://localhost:{port}/health");
    match reqwest::get(&url).await {
        Ok(resp) => resp.status().is_success(),
        Err(_) => false,
    }
}

/// Wait for the server to respond to health checks.
async fn wait_for_server(port: u16, timeout_secs: u64) -> bool {
    let url = format!("http://localhost:{port}/health");
    let start = std::time::Instant::now();
    let timeout = std::time::Duration::from_secs(timeout_secs);

    while start.elapsed() < timeout {
        if let Ok(resp) = reqwest::get(&url).await {
            if resp.status().is_success() {
                return true;
            }
        }
        tokio::time::sleep(std::time::Duration::from_millis(500)).await;
    }
    false
}

/// Open the dashboard in the default browser.
fn open_dashboard(port: u16, no_open: bool) {
    if no_open {
        return;
    }
    let url = format!("http://localhost:{port}");
    if let Err(e) = open_url(&url) {
        eprintln!(
            "  {} Could not open browser: {e}",
            "⚠️".to_string()
        );
    }
}

/// Cross-platform browser open.
fn open_url(url: &str) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        StdCommand::new("open")
            .arg(url)
            .spawn()
            .map_err(|e| e.to_string())?;
    }
    #[cfg(target_os = "linux")]
    {
        StdCommand::new("xdg-open")
            .arg(url)
            .spawn()
            .map_err(|e| e.to_string())?;
    }
    #[cfg(target_os = "windows")]
    {
        StdCommand::new("cmd")
            .args(["/C", "start", url])
            .spawn()
            .map_err(|e| e.to_string())?;
    }
    Ok(())
}

/// Find the control plane server binary.
///
/// Search order:
/// 1. `agentoven-server` in PATH
/// 2. Go binary at `control-plane/cmd/server/` (go run)
/// 3. Pre-built binary at well-known locations
fn find_server_binary() -> anyhow::Result<String> {
    // 1. Check PATH for agentoven-server
    if which_exists("agentoven-server") {
        return Ok("agentoven-server".to_string());
    }

    // 2. Check for a Go installation and the control-plane source
    if which_exists("go") {
        // Look for the control-plane source relative to the CLI binary or current dir
        let candidates = [
            "control-plane/cmd/server",
            "../control-plane/cmd/server",
            "../../control-plane/cmd/server",
        ];
        for candidate in &candidates {
            let path = std::path::Path::new(candidate);
            if path.join("main.go").exists() {
                // Use `go run` with the package path
                let abs = std::fs::canonicalize(path)
                    .unwrap_or_else(|_| path.to_path_buf());
                // Build the binary first for faster startup
                println!(
                    "  {} Building control plane...",
                    "🔨".to_string()
                );
                let build_status = StdCommand::new("go")
                    .args(["build", "-o", "/tmp/agentoven-server", &format!("./{}", candidate)])
                    .status();
                match build_status {
                    Ok(s) if s.success() => return Ok("/tmp/agentoven-server".to_string()),
                    _ => {
                        // Fallback to go run
                        return Ok(format!("go run ./{}", abs.display()));
                    }
                }
            }
        }
    }

    anyhow::bail!(
        "Could not find the AgentOven control plane server.\n\n\
         Options:\n\
         • Install it: cargo install agentoven-server\n\
         • Set the path: agentoven dashboard --server-bin /path/to/server\n\
         • Set env: AGENTOVEN_SERVER_BIN=/path/to/server\n\
         • Run from the repo root (we'll auto-detect control-plane/)"
    )
}

fn which_exists(cmd: &str) -> bool {
    StdCommand::new("which")
        .arg(cmd)
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

/// Find the dashboard static files directory.
///
/// Search order:
/// 1. Relative to the current CLI binary (Homebrew / packaged installs)
///    e.g. binary at /opt/homebrew/bin/agentoven → ../share/agentoven/dashboard/
/// 2. CWD-relative paths (dev mode)
fn find_dashboard_dir() -> Option<String> {
    use std::path::PathBuf;

    let mut candidates: Vec<PathBuf> = Vec::new();

    // 1. Relative to the CLI binary (handles both symlink and resolved paths)
    if let Ok(exe) = std::env::current_exe() {
        let raw_dir = exe.parent().unwrap_or(&exe).to_path_buf();
        candidates.push(raw_dir.join("..").join("share").join("agentoven").join("dashboard"));

        // Also try the symlink-resolved path
        if let Ok(resolved) = std::fs::canonicalize(&exe) {
            let resolved_dir = resolved.parent().unwrap_or(&resolved).to_path_buf();
            candidates.push(resolved_dir.join("..").join("share").join("agentoven").join("dashboard"));
        }
    }

    // 2. CWD-relative paths (dev mode)
    candidates.push("dashboard/dist".into());
    candidates.push("control-plane/dashboard/dist".into());

    for candidate in &candidates {
        let index = candidate.join("index.html");
        if index.exists() {
            if let Ok(abs) = std::fs::canonicalize(candidate) {
                return Some(abs.to_string_lossy().to_string());
            }
        }
    }

    None
}

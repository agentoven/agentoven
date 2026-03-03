//! Shared helpers for Pro-gated CLI commands.
//!
//! Pro commands call `check_pro_feature()` first, which hits GET /api/v1/info
//! and verifies the feature is available. If the server is community edition,
//! the user gets a friendly upgrade message instead of a confusing 404.

use colored::Colorize;

/// Check whether a named feature is available on the connected server.
///
/// Returns `Ok(true)` if available, `Ok(false)` if not (prints a message),
/// or `Err` if the server is unreachable.
pub async fn check_pro_feature(feature_name: &str, feature_key: &str) -> anyhow::Result<bool> {
    let client = agentoven_core::AgentOvenClient::from_env()?;

    let info = client.server_info().await.map_err(|e| {
        anyhow::anyhow!(
            "Cannot reach the control plane. Is the server running?\n  Error: {}",
            e
        )
    })?;

    let edition = info["edition"].as_str().unwrap_or("community");

    // Check if the feature exists in features object
    let available = info
        .get("features")
        .and_then(|f| f.get(feature_key))
        .and_then(|v| v.as_bool())
        .unwrap_or(false);

    if !available {
        println!(
            "\n  {} {} is not available on {} edition.\n",
            "⚠".yellow().bold(),
            feature_name.bold(),
            edition.cyan()
        );
        if edition == "community" {
            println!(
                "  {} This feature requires AgentOven Pro or Enterprise.",
                "→".dimmed()
            );
            println!(
                "  {} Learn more at https://agentoven.dev/pricing",
                "→".dimmed()
            );
        } else {
            println!(
                "  {} This feature may require a higher plan or license update.",
                "→".dimmed()
            );
        }
        println!();
        return Ok(false);
    }

    Ok(true)
}

/// Build a client from env, with a nicer error message.
pub fn build_client() -> anyhow::Result<agentoven_core::AgentOvenClient> {
    agentoven_core::AgentOvenClient::from_env()
}

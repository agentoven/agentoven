# agentoven-core

Core SDK for [AgentOven](https://agentoven.dev) — the enterprise agent control plane.

## Overview

`agentoven-core` provides the foundational types and client for interacting with the AgentOven control plane. It includes:

- **Agent** — versioned, deployable AI units with lifecycle management
- **Recipe** — multi-agent workflows and orchestration pipelines
- **Ingredient** — models, tools, prompts, and data sources
- **Client** — async HTTP client for the AgentOven API
- **Telemetry** — OpenTelemetry-based tracing and observability

## Quick Start

```rust
use agentoven_core::prelude::*;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let client = AgentOvenClient::from_env()?;

    // Register an agent
    let agent = Agent::builder("my-assistant")
        .version("1.0.0")
        .description("A helpful AI assistant")
        .framework(AgentFramework::LangGraph)
        .build();

    let registered = client.register(&agent).await?;
    println!("Registered: {}", registered.qualified_name());

    // Bake (deploy) it
    let deployed = client.bake(&registered, "production").await?;
    println!("Status: {:?}", deployed.status);

    Ok(())
}
```

## Checking names before they are used

An agent's configuration names a lot of things: the provider and model it runs on, the model that summarizes its
history (`summary_provider` / `summary_model`), tools, skills, prompts, subagents; a recipe names agents, steps,
channels, credentials. The control plane refuses any that the kitchen does not have (HTTP 422) and the client returns
it as a `ConfigurationError` carrying every `Problem` (`path`, `code`, `value`, `suggestions`):

```rust
match client.update_agent("bot", serde_json::json!({"summary_provider": "anthropic", "summary_model": "claude-hiku-4-5"})).await {
    Err(e) => {
        if let Some(c) = e.downcast_ref::<agentoven_core::ConfigurationError>() {
            for p in &c.problems { println!("{}: {} (try {:?})", p.path, p.message, p.suggestions); }
        }
    }
    Ok(_) => {}
}
```

To check before sending, ask the server about a stored agent (`client.agent_validate(name)`, `recipe_validate`) or
run the same checks here (`client.validate_agent(&json, &Options::write())`, built from `Catalog::load(&client)`: the
kitchen's lists, with a list you may not read left unknown rather than empty). `client.with_validation(true)` makes
`register`, `update_agent`, `bake`, `recook_agent`, `create_recipe*`, `bake_recipe` and `run_recipe` run that check
first and return the same `ConfigurationError` without sending; it is off by default (the server remains the
authority) and `client.clone().with_validation(false)` always skips it. `agentoven_core::refcheck::check_agent` and
`check_recipe` are pure and are pinned to the control plane by `sdk/fixtures/refcheck-cases.json`
(`tests/refcheck_cases.rs`). `Agent::builder(..).summary_model(provider, model)` sets the compaction model.

## Features

- **A2A Protocol** — built on the [Agent-to-Agent](https://google.github.io/A2A/) open standard
- **Multi-framework** — supports LangGraph, CrewAI, OpenAI SDK, AutoGen, and custom agents
- **Kitchen isolation** — workspace-based multi-tenancy
- **Full observability** — OpenTelemetry tracing with cost tracking

## License

MIT

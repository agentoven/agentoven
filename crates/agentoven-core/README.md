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

## Features

- **A2A Protocol** — built on the [Agent-to-Agent](https://google.github.io/A2A/) open standard
- **Multi-framework** — supports LangGraph, CrewAI, OpenAI SDK, AutoGen, and custom agents
- **Kitchen isolation** — workspace-based multi-tenancy
- **Full observability** — OpenTelemetry tracing with cost tracking

## License

MIT

# a2a-rs 🤝

The first Rust implementation of the [Agent-to-Agent (A2A) protocol](https://github.com/google/A2A) —
the open standard for agent interoperability, governed by the Linux Foundation.

## What is A2A?

A2A enables AI agents to **discover each other**, **delegate tasks**, and **collaborate** —
regardless of which framework or platform they were built on.

- **Agent Cards** — Self-describing metadata for agent discovery
- **Tasks** — Stateful units of work with full lifecycle management
- **Streaming** — Real-time updates via Server-Sent Events
- **Push Notifications** — Webhook-based async delivery
- **Multi-turn** — Extended dialogues with `INPUT_REQUIRED` / `AUTH_REQUIRED` states

## Quick Start

```rust
use a2a_rs::{AgentCard, A2AClient, SendMessageRequest, MessagePart};

// Discover a remote agent
let card = AgentCard::discover("https://agent.example.com").await?;
println!("Found agent: {} ({})", card.name, card.description);

// Send a task to the agent
let client = A2AClient::new("https://agent.example.com");
let response = client
    .send_message(SendMessageRequest {
        message: Message::user(vec![
            MessagePart::text("Summarize this quarter's sales report"),
        ]),
        ..Default::default()
    })
    .await?;

// Track the task lifecycle
match response.task.state {
    TaskState::Completed => println!("Done: {:?}", response.task.artifacts),
    TaskState::Working => println!("Still working..."),
    TaskState::InputRequired => println!("Agent needs more info"),
    _ => {}
}
```

## A2A + MCP: Complementary Protocols

| | A2A | MCP |
|---|---|---|
| **Purpose** | Agent ↔ Agent collaboration | Agent ↔ Tools/Data access |
| **Abstraction** | Task (stateful lifecycle) | Tool call (request-response) |
| **Execution** | Opaque (agents don't share internals) | Transparent (servers expose tools) |

Use **A2A** when agents need to collaborate. Use **MCP** when agents need tools and data.

## License

[MIT](../../LICENSE)

Part of the [AgentOven](https://github.com/agentoven/agentoven) project.

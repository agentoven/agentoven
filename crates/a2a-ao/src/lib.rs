//! # a2a-ao
//!
//! Rust SDK for the Agent-to-Agent (A2A) protocol — the open standard for
//! agent interoperability, governed by the Linux Foundation.
//!
//! A2A enables AI agents to discover each other, delegate tasks, and collaborate
//! regardless of framework or platform.
//!
//! ## Architecture
//!
//! The A2A protocol has three layers:
//!
//! 1. **Canonical Data Model** — Protocol-agnostic type definitions (this crate's types)
//! 2. **Abstract Operations** — Core operations like `SendMessage`, `GetTask`, etc.
//! 3. **Protocol Bindings** — Wire-level: JSON-RPC 2.0, gRPC, HTTP+JSON
//!
//! ## Quick Start
//!
//! ```rust,no_run
//! use a2a_ao::{A2AClient, AgentCard};
//!
//! #[tokio::main]
//! async fn main() -> Result<(), Box<dyn std::error::Error>> {
//!     // Discover a remote agent
//!     let card = AgentCard::discover("https://agent.example.com").await?;
//!     println!("Found: {}", card.name);
//!
//!     // Create a client and send a message
//!     let client = A2AClient::new("https://agent.example.com");
//!     let response = client.send_message_text("Summarize Q4 report").await?;
//!     println!("Task state: {:?}", response.state);
//!     Ok(())
//! }
//! ```

pub mod agent_card;
pub mod artifact;
pub mod client;
pub mod error;
pub mod message;
pub mod notification;
pub mod task;
pub mod transport;

// Re-export primary types
pub use agent_card::{
    AgentCapabilities, AgentCard, AgentProvider, AgentSkill, ContentType, SecurityScheme,
};
pub use artifact::Artifact;
pub use client::{A2AClient, SendMessageRequest};
pub use error::A2AError;
pub use message::{DataPart, FilePart, Message, MessagePart, MessageRole};
pub use notification::{PushNotificationConfig, PushNotificationEvent};
pub use task::{Task, TaskEvent, TaskQueryParams, TaskState};
pub use transport::jsonrpc::{JsonRpcError, JsonRpcRequest, JsonRpcResponse};
pub use transport::sse::TaskEventStream;

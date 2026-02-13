//! # a2a-rs
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
//! use a2a_rs::{A2AClient, AgentCard};
//!
//! #[tokio::main]
//! async fn main() -> anyhow::Result<()> {
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
    AgentCard, AgentCapabilities, AgentProvider, AgentSkill, ContentType, SecurityScheme,
};
pub use artifact::Artifact;
pub use client::A2AClient;
pub use error::A2AError;
pub use message::{Message, MessagePart, MessageRole, FilePart, DataPart};
pub use notification::{PushNotificationConfig, PushNotificationEvent};
pub use task::{Task, TaskState, TaskQueryParams};
pub use transport::jsonrpc::{JsonRpcRequest, JsonRpcResponse, JsonRpcError};

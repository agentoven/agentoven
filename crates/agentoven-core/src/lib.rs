//! # agentoven-core
//!
//! Core SDK library for AgentOven — the enterprise agent control plane.
//!
//! This crate provides the building blocks for:
//! - Defining and registering agents (recipes)
//! - Connecting to the AgentOven control plane
//! - Model routing across providers
//! - Observability via OpenTelemetry
//! - Multi-agent workflow orchestration
//!
//! ## Quick Start
//!
//! ```rust,no_run
//! use agentoven_core::{Agent, Ingredient, AgentOvenClient};
//!
//! #[tokio::main]
//! async fn main() -> Result<(), Box<dyn std::error::Error>> {
//!     let client = AgentOvenClient::new("http://localhost:8080")?;
//!
//!     let agent = Agent::builder("summarizer")
//!         .version("1.0.0")
//!         .description("Summarizes documents with citations")
//!         .ingredient(Ingredient::model("gpt-4o").provider("azure-openai").build())
//!         .ingredient(Ingredient::model("claude-sonnet").provider("anthropic").role("fallback").build())
//!         .ingredient(Ingredient::tool("doc-reader").build())
//!         .build();
//!
//!     client.register(&agent).await?;
//!     client.bake(&agent, "production").await?;
//!     Ok(())
//! }
//! ```

pub mod agent;
pub mod client;
pub mod config;
pub mod ingredient;
pub mod recipe;
pub mod router;
pub mod telemetry;

// Re-exports
pub use agent::{Agent, AgentBuilder, AgentMode, AgentStatus, Guardrail};
pub use client::AgentOvenClient;
pub use config::AgentOvenConfig;
pub use ingredient::{Ingredient, IngredientKind};
pub use recipe::{Recipe, Step, StepKind};
pub use router::{ModelProvider, RoutingStrategy};

// Re-export a2a-ao types for convenience
pub use a2a_ao;

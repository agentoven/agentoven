//! Model Router — intelligent routing across LLM providers.
//!
//! The router selects which model provider to use based on routing strategy,
//! tracks costs per request, and handles fallbacks.

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

/// Routing strategy for model selection.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "kebab-case")]
pub enum RoutingStrategy {
    /// Use the primary model; fall back to alternatives on failure.
    Fallback,
    /// Route to the lowest-cost provider.
    CostOptimized,
    /// Route to the lowest-latency provider.
    LatencyOptimized,
    /// Round-robin across providers.
    RoundRobin,
    /// A/B split by percentage.
    AbSplit { primary_weight: f32 },
}

/// A model provider configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelProvider {
    /// Provider identifier (e.g., "azure-openai", "anthropic", "ollama").
    pub provider: ProviderKind,

    /// Model name at the provider (e.g., "gpt-4o", "claude-sonnet-4-20250514").
    pub model: String,

    /// API endpoint URL.
    pub endpoint: String,

    /// Priority (lower = higher priority in fallback strategy).
    #[serde(default)]
    pub priority: u32,

    /// Cost per 1K input tokens (USD).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cost_per_1k_input: Option<f64>,

    /// Cost per 1K output tokens (USD).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cost_per_1k_output: Option<f64>,

    /// Maximum tokens per request.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_tokens: Option<u32>,

    /// Whether this provider is currently enabled.
    #[serde(default = "default_true")]
    pub enabled: bool,
}

fn default_true() -> bool {
    true
}

/// Supported model providers.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "kebab-case")]
pub enum ProviderKind {
    AzureOpenai,
    OpenAi,
    Anthropic,
    Bedrock,
    DatabricksFoundation,
    GoogleVertex,
    Ollama,
    Custom,
}

/// Token usage from a model invocation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TokenUsage {
    /// Number of input (prompt) tokens.
    pub input_tokens: u32,
    /// Number of output (completion) tokens.
    pub output_tokens: u32,
    /// Total tokens.
    pub total_tokens: u32,
    /// Estimated cost in USD.
    pub estimated_cost_usd: f64,
    /// The provider and model used.
    pub provider: ProviderKind,
    pub model: String,
}

/// Trait for model provider implementations.
#[async_trait]
pub trait ModelProviderClient: Send + Sync {
    /// Send a chat completion request.
    async fn chat_completion(
        &self,
        messages: Vec<serde_json::Value>,
        config: &serde_json::Value,
    ) -> anyhow::Result<(String, TokenUsage)>;

    /// Check if the provider is healthy.
    async fn health_check(&self) -> bool;
}

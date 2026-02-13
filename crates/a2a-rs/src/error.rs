//! A2A Error types.

use thiserror::Error;

/// Errors that can occur when using the A2A protocol.
#[derive(Debug, Error)]
pub enum A2AError {
    /// Failed to discover the agent card at the well-known endpoint.
    #[error("agent discovery failed: {0}")]
    DiscoveryFailed(String),

    /// The agent card is invalid or missing required fields.
    #[error("invalid agent card: {0}")]
    InvalidAgentCard(String),

    /// HTTP transport error.
    #[error("transport error: {0}")]
    Transport(#[from] reqwest::Error),

    /// JSON serialization/deserialization error.
    #[error("serialization error: {0}")]
    Serialization(#[from] serde_json::Error),

    /// The remote agent returned a JSON-RPC error.
    #[error("JSON-RPC error {code}: {message}")]
    JsonRpc {
        code: i64,
        message: String,
        data: Option<serde_json::Value>,
    },

    /// The task was not found.
    #[error("task not found: {0}")]
    TaskNotFound(String),

    /// The task was rejected by the remote agent.
    #[error("task rejected: {0}")]
    TaskRejected(String),

    /// The task failed during execution.
    #[error("task failed: {0}")]
    TaskFailed(String),

    /// The task requires authentication.
    #[error("authentication required: {0}")]
    AuthRequired(String),

    /// Streaming error (SSE).
    #[error("streaming error: {0}")]
    StreamingError(String),

    /// Push notification configuration error.
    #[error("push notification error: {0}")]
    PushNotificationError(String),

    /// URL parsing error.
    #[error("invalid URL: {0}")]
    InvalidUrl(#[from] url::ParseError),

    /// Timeout waiting for task completion.
    #[error("timeout after {0:?}")]
    Timeout(std::time::Duration),

    /// The operation is not supported by the remote agent.
    #[error("unsupported operation: {0}")]
    Unsupported(String),
}

/// A2A Result type alias.
pub type A2AResult<T> = Result<T, A2AError>;

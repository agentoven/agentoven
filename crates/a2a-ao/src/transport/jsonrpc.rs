//! JSON-RPC 2.0 transport binding for A2A.
//!
//! The primary wire protocol for A2A. All operations are encoded as
//! JSON-RPC 2.0 requests/responses over HTTP(S).

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// JSON-RPC 2.0 protocol version.
pub const JSONRPC_VERSION: &str = "2.0";

/// A2A media type.
pub const A2A_MEDIA_TYPE: &str = "application/a2a+json";

// ── A2A Methods ──────────────────────────────────────────────

/// Standard A2A JSON-RPC method names.
pub mod methods {
    /// Send a message to the agent (creates or continues a task).
    pub const SEND_MESSAGE: &str = "message/send";

    /// Send a streaming message (returns SSE stream).
    pub const SEND_STREAMING_MESSAGE: &str = "message/stream";

    /// Get a task by ID.
    pub const GET_TASK: &str = "tasks/get";

    /// List tasks matching filters.
    pub const LIST_TASKS: &str = "tasks/list";

    /// Cancel a task.
    pub const CANCEL_TASK: &str = "tasks/cancel";

    /// Subscribe to task updates via SSE.
    pub const SUBSCRIBE_TASK: &str = "tasks/subscribe";

    /// Create a push notification config.
    pub const CREATE_PUSH_NOTIFICATION: &str = "tasks/pushNotification/create";

    /// Get a push notification config.
    pub const GET_PUSH_NOTIFICATION: &str = "tasks/pushNotification/get";

    /// List push notification configs for a task.
    pub const LIST_PUSH_NOTIFICATIONS: &str = "tasks/pushNotification/list";

    /// Delete a push notification config.
    pub const DELETE_PUSH_NOTIFICATION: &str = "tasks/pushNotification/delete";

    /// Get the extended agent card (post-authentication).
    pub const GET_EXTENDED_AGENT_CARD: &str = "agent/authenticatedExtendedCard";
}

// ── JSON-RPC Request ─────────────────────────────────────────

/// A JSON-RPC 2.0 request.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcRequest {
    /// Must be "2.0".
    pub jsonrpc: String,

    /// The method to invoke.
    pub method: String,

    /// Method parameters.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<Value>,

    /// Request identifier (used to match response).
    pub id: RequestId,
}

impl JsonRpcRequest {
    /// Create a new JSON-RPC request.
    pub fn new(method: impl Into<String>, params: Option<Value>) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.into(),
            method: method.into(),
            params,
            id: RequestId::Number(rand_id()),
        }
    }

    /// Create a SendMessage request.
    pub fn send_message(params: Value) -> Self {
        Self::new(methods::SEND_MESSAGE, Some(params))
    }

    /// Create a SendStreamingMessage request.
    pub fn send_streaming_message(params: Value) -> Self {
        Self::new(methods::SEND_STREAMING_MESSAGE, Some(params))
    }

    /// Create a GetTask request.
    pub fn get_task(task_id: &str) -> Self {
        Self::new(
            methods::GET_TASK,
            Some(serde_json::json!({ "taskId": task_id })),
        )
    }

    /// Create a CancelTask request.
    pub fn cancel_task(task_id: &str) -> Self {
        Self::new(
            methods::CANCEL_TASK,
            Some(serde_json::json!({ "taskId": task_id })),
        )
    }

    /// Create a ListTasks request.
    pub fn list_tasks(params: Value) -> Self {
        Self::new(methods::LIST_TASKS, Some(params))
    }
}

// ── JSON-RPC Response ────────────────────────────────────────

/// A JSON-RPC 2.0 response.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcResponse {
    /// Must be "2.0".
    pub jsonrpc: String,

    /// The result (mutually exclusive with error).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<Value>,

    /// The error (mutually exclusive with result).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<JsonRpcError>,

    /// The request identifier this response corresponds to.
    pub id: RequestId,
}

impl JsonRpcResponse {
    /// Create a success response.
    pub fn success(id: RequestId, result: Value) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.into(),
            result: Some(result),
            error: None,
            id,
        }
    }

    /// Create an error response.
    pub fn error(id: RequestId, error: JsonRpcError) -> Self {
        Self {
            jsonrpc: JSONRPC_VERSION.into(),
            result: None,
            error: Some(error),
            id,
        }
    }

    /// Check if this is an error response.
    pub fn is_error(&self) -> bool {
        self.error.is_some()
    }

    /// Extract the result, returning an error if this is an error response.
    pub fn into_result(self) -> Result<Value, JsonRpcError> {
        if let Some(error) = self.error {
            Err(error)
        } else {
            Ok(self.result.unwrap_or(Value::Null))
        }
    }
}

// ── JSON-RPC Error ───────────────────────────────────────────

/// A JSON-RPC 2.0 error.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcError {
    /// Error code.
    pub code: i64,

    /// Human-readable error message.
    pub message: String,

    /// Optional additional data.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<Value>,
}

impl JsonRpcError {
    /// Standard JSON-RPC error: Parse error (-32700).
    pub fn parse_error(detail: impl Into<String>) -> Self {
        Self {
            code: -32700,
            message: "Parse error".into(),
            data: Some(Value::String(detail.into())),
        }
    }

    /// Standard JSON-RPC error: Invalid request (-32600).
    pub fn invalid_request(detail: impl Into<String>) -> Self {
        Self {
            code: -32600,
            message: "Invalid Request".into(),
            data: Some(Value::String(detail.into())),
        }
    }

    /// Standard JSON-RPC error: Method not found (-32601).
    pub fn method_not_found(method: &str) -> Self {
        Self {
            code: -32601,
            message: "Method not found".into(),
            data: Some(Value::String(format!("Unknown method: {method}"))),
        }
    }

    /// Standard JSON-RPC error: Invalid params (-32602).
    pub fn invalid_params(detail: impl Into<String>) -> Self {
        Self {
            code: -32602,
            message: "Invalid params".into(),
            data: Some(Value::String(detail.into())),
        }
    }

    /// Standard JSON-RPC error: Internal error (-32603).
    pub fn internal_error(detail: impl Into<String>) -> Self {
        Self {
            code: -32603,
            message: "Internal error".into(),
            data: Some(Value::String(detail.into())),
        }
    }

    /// A2A-specific: Task not found (-32001).
    pub fn task_not_found(task_id: &str) -> Self {
        Self {
            code: -32001,
            message: "Task not found".into(),
            data: Some(Value::String(format!("Task {task_id} not found"))),
        }
    }

    /// A2A-specific: Task cannot be canceled (-32002).
    pub fn task_not_cancelable(task_id: &str) -> Self {
        Self {
            code: -32002,
            message: "Task not cancelable".into(),
            data: Some(Value::String(format!(
                "Task {task_id} is in a terminal state"
            ))),
        }
    }
}

impl std::fmt::Display for JsonRpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "JSON-RPC error {}: {}", self.code, self.message)
    }
}

impl std::error::Error for JsonRpcError {}

// ── Request ID ───────────────────────────────────────────────

/// JSON-RPC request identifier (can be a number or string).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(untagged)]
pub enum RequestId {
    Number(i64),
    String(String),
}

/// Generate a random request ID.
fn rand_id() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| (d.as_nanos() % i64::MAX as u128) as i64)
        .unwrap_or(1)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_request_serialization() {
        let req = JsonRpcRequest::send_message(serde_json::json!({
            "message": {
                "role": "user",
                "parts": [{"type": "text", "text": "Hello"}]
            }
        }));

        let json = serde_json::to_string(&req).unwrap();
        assert!(json.contains("message/send"));
        assert!(json.contains("2.0"));

        let parsed: JsonRpcRequest = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.method, "message/send");
    }

    #[test]
    fn test_response_success() {
        let resp = JsonRpcResponse::success(
            RequestId::Number(1),
            serde_json::json!({"taskId": "abc123"}),
        );
        assert!(!resp.is_error());
        assert!(resp.into_result().is_ok());
    }

    #[test]
    fn test_response_error() {
        let resp =
            JsonRpcResponse::error(RequestId::Number(1), JsonRpcError::task_not_found("abc123"));
        assert!(resp.is_error());
        assert!(resp.into_result().is_err());
    }
}

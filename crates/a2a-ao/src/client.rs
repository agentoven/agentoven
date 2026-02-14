//! A2A Client — high-level client for interacting with A2A-compatible agents.
//!
//! The client handles agent discovery, message sending, task tracking,
//! streaming, and push notification management.

use futures::StreamExt;
use reqwest::Client;
use serde::Serialize;
use url::Url;

use crate::agent_card::AgentCard;
use crate::error::{A2AError, A2AResult};
use crate::message::Message;
use crate::notification::PushNotificationConfig;
use crate::task::{Task, TaskQueryParams};
use crate::transport::jsonrpc::{
    self, JsonRpcRequest, JsonRpcResponse, A2A_MEDIA_TYPE,
};
use crate::transport::sse::TaskEventStream;

/// High-level A2A client for communicating with remote agents.
#[derive(Debug, Clone)]
pub struct A2AClient {
    /// Base URL of the remote agent.
    base_url: Url,

    /// The discovered agent card (populated after discover()).
    agent_card: Option<AgentCard>,

    /// HTTP client.
    http: Client,

    /// Optional bearer token for authentication.
    auth_token: Option<String>,
}

impl A2AClient {
    /// Create a new A2A client for a remote agent.
    pub fn new(base_url: &str) -> Self {
        Self {
            base_url: Url::parse(base_url).expect("Invalid base URL"),
            agent_card: None,
            http: Client::new(),
            auth_token: None,
        }
    }

    /// Create a client with a custom HTTP client.
    pub fn with_http_client(base_url: &str, http: Client) -> Self {
        Self {
            base_url: Url::parse(base_url).expect("Invalid base URL"),
            agent_card: None,
            http,
            auth_token: None,
        }
    }

    /// Set authentication token.
    pub fn with_auth(mut self, token: impl Into<String>) -> Self {
        self.auth_token = Some(token.into());
        self
    }

    /// Discover the remote agent's capabilities by fetching its Agent Card.
    pub async fn discover(&mut self) -> A2AResult<&AgentCard> {
        let card = AgentCard::discover(self.base_url.as_str()).await?;
        self.agent_card = Some(card);
        Ok(self.agent_card.as_ref().unwrap())
    }

    /// Get the cached agent card (call discover() first).
    pub fn agent_card(&self) -> Option<&AgentCard> {
        self.agent_card.as_ref()
    }

    // ── Core Operations ──────────────────────────────────────

    /// Send a message to the remote agent, creating or continuing a task.
    pub async fn send_message(&self, request: SendMessageRequest) -> A2AResult<Task> {
        let params = serde_json::to_value(&request)
            .map_err(|e| A2AError::Serialization(e))?;

        let rpc_request = JsonRpcRequest::send_message(params);
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;

        let task: Task = serde_json::from_value(result)?;
        Ok(task)
    }

    /// Convenience: send a simple text message.
    pub async fn send_message_text(&self, text: &str) -> A2AResult<Task> {
        self.send_message(SendMessageRequest {
            message: Message::user_text(text),
            task_id: None,
            context_id: None,
            metadata: None,
        })
        .await
    }

    /// Continue an existing task with additional input.
    pub async fn continue_task(&self, task_id: &str, text: &str) -> A2AResult<Task> {
        self.send_message(SendMessageRequest {
            message: Message::user_text(text),
            task_id: Some(task_id.to_string()),
            context_id: None,
            metadata: None,
        })
        .await
    }

    /// Get a task by its ID.
    pub async fn get_task(&self, task_id: &str) -> A2AResult<Task> {
        let rpc_request = JsonRpcRequest::get_task(task_id);
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;

        let task: Task = serde_json::from_value(result)?;
        Ok(task)
    }

    /// List tasks matching the given query parameters.
    pub async fn list_tasks(&self, params: TaskQueryParams) -> A2AResult<Vec<Task>> {
        let rpc_params = serde_json::to_value(&params)?;
        let rpc_request = JsonRpcRequest::list_tasks(rpc_params);
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;

        let tasks: Vec<Task> = serde_json::from_value(result)?;
        Ok(tasks)
    }

    /// Cancel a running task.
    pub async fn cancel_task(&self, task_id: &str) -> A2AResult<Task> {
        let rpc_request = JsonRpcRequest::cancel_task(task_id);
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;

        let task: Task = serde_json::from_value(result)?;
        Ok(task)
    }

    // ── Streaming Operations ─────────────────────────────────

    /// Send a streaming message — returns an SSE stream of task events.
    ///
    /// The remote agent will stream back `TaskEvent`s as it processes
    /// the message (state changes, partial artifacts, etc.).
    pub async fn send_streaming_message(
        &self,
        request: SendMessageRequest,
    ) -> A2AResult<TaskEventStream> {
        let params =
            serde_json::to_value(&request).map_err(|e| A2AError::Serialization(e))?;

        let rpc_request = JsonRpcRequest::send_streaming_message(params);

        let mut http_request = self
            .http
            .post(self.base_url.as_str())
            .header("Content-Type", A2A_MEDIA_TYPE)
            .header("Accept", "text/event-stream")
            .json(&rpc_request);

        if let Some(ref token) = self.auth_token {
            http_request = http_request.bearer_auth(token);
        }

        tracing::debug!(url = %self.base_url, "Sending streaming A2A request");

        let response = http_request.send().await?;

        if !response.status().is_success() {
            return Err(A2AError::Transport(
                response.error_for_status().unwrap_err(),
            ));
        }

        let byte_stream = response.bytes_stream();
        let event_stream = Box::pin(
            byte_stream
                .map(|chunk| match chunk {
                    Ok(bytes) => {
                        let text = String::from_utf8_lossy(&bytes);
                        // Parse SSE data lines
                        let mut events = Vec::new();
                        for line in text.lines() {
                            if let Some(data) = line.strip_prefix("data: ") {
                                if data == "[DONE]" {
                                    break;
                                }
                                match crate::transport::sse::parse_sse_event(data) {
                                    Ok(event) => events.push(Ok(event)),
                                    Err(e) => events.push(Err(e)),
                                }
                            }
                        }
                        futures::stream::iter(events)
                    }
                    Err(e) => {
                        futures::stream::iter(vec![Err(A2AError::StreamingError(
                            format!("Stream read error: {e}"),
                        ))])
                    }
                })
                .flatten(),
        );

        Ok(TaskEventStream::new(event_stream))
    }

    /// Convenience: send a streaming text message.
    pub async fn send_streaming_text(
        &self,
        text: &str,
    ) -> A2AResult<TaskEventStream> {
        self.send_streaming_message(SendMessageRequest {
            message: Message::user_text(text),
            task_id: None,
            context_id: None,
            metadata: None,
        })
        .await
    }

    /// Subscribe to an existing task's updates via SSE.
    pub async fn subscribe_task(
        &self,
        task_id: &str,
    ) -> A2AResult<TaskEventStream> {
        let rpc_request = JsonRpcRequest::new(
            jsonrpc::methods::SUBSCRIBE_TASK,
            Some(serde_json::json!({ "taskId": task_id })),
        );

        let mut http_request = self
            .http
            .post(self.base_url.as_str())
            .header("Content-Type", A2A_MEDIA_TYPE)
            .header("Accept", "text/event-stream")
            .json(&rpc_request);

        if let Some(ref token) = self.auth_token {
            http_request = http_request.bearer_auth(token);
        }

        let response = http_request.send().await?;

        if !response.status().is_success() {
            return Err(A2AError::Transport(
                response.error_for_status().unwrap_err(),
            ));
        }

        let byte_stream = response.bytes_stream();
        let event_stream = Box::pin(
            byte_stream
                .map(|chunk| match chunk {
                    Ok(bytes) => {
                        let text = String::from_utf8_lossy(&bytes);
                        let mut events = Vec::new();
                        for line in text.lines() {
                            if let Some(data) = line.strip_prefix("data: ") {
                                if data == "[DONE]" {
                                    break;
                                }
                                match crate::transport::sse::parse_sse_event(data) {
                                    Ok(event) => events.push(Ok(event)),
                                    Err(e) => events.push(Err(e)),
                                }
                            }
                        }
                        futures::stream::iter(events)
                    }
                    Err(e) => {
                        futures::stream::iter(vec![Err(A2AError::StreamingError(
                            format!("Stream read error: {e}"),
                        ))])
                    }
                })
                .flatten(),
        );

        Ok(TaskEventStream::new(event_stream))
    }

    // ── Push Notification Operations ─────────────────────────

    /// Create a push notification configuration for a task.
    pub async fn create_push_notification(
        &self,
        config: &PushNotificationConfig,
    ) -> A2AResult<PushNotificationConfig> {
        let params = serde_json::to_value(config)?;
        let rpc_request = JsonRpcRequest::new(
            jsonrpc::methods::CREATE_PUSH_NOTIFICATION,
            Some(params),
        );
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;
        Ok(serde_json::from_value(result)?)
    }

    /// Get a push notification configuration by ID.
    pub async fn get_push_notification(
        &self,
        config_id: &str,
        task_id: &str,
    ) -> A2AResult<PushNotificationConfig> {
        let rpc_request = JsonRpcRequest::new(
            jsonrpc::methods::GET_PUSH_NOTIFICATION,
            Some(serde_json::json!({ "configId": config_id, "taskId": task_id })),
        );
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;
        Ok(serde_json::from_value(result)?)
    }

    /// List push notification configurations for a task.
    pub async fn list_push_notifications(
        &self,
        task_id: &str,
    ) -> A2AResult<Vec<PushNotificationConfig>> {
        let rpc_request = JsonRpcRequest::new(
            jsonrpc::methods::LIST_PUSH_NOTIFICATIONS,
            Some(serde_json::json!({ "taskId": task_id })),
        );
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;
        Ok(serde_json::from_value(result)?)
    }

    /// Delete a push notification configuration.
    pub async fn delete_push_notification(
        &self,
        config_id: &str,
        task_id: &str,
    ) -> A2AResult<()> {
        let rpc_request = JsonRpcRequest::new(
            jsonrpc::methods::DELETE_PUSH_NOTIFICATION,
            Some(serde_json::json!({ "configId": config_id, "taskId": task_id })),
        );
        let response = self.send_rpc(rpc_request).await?;
        response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;
        Ok(())
    }

    /// Get the extended agent card (post-authentication).
    pub async fn get_extended_agent_card(&self) -> A2AResult<AgentCard> {
        let rpc_request =
            JsonRpcRequest::new(jsonrpc::methods::GET_EXTENDED_AGENT_CARD, None);
        let response = self.send_rpc(rpc_request).await?;
        let result = response.into_result().map_err(|e| A2AError::JsonRpc {
            code: e.code,
            message: e.message,
            data: e.data,
        })?;
        Ok(serde_json::from_value(result)?)
    }

    // ── Internal Transport ───────────────────────────────────

    /// Send a JSON-RPC request to the remote agent.
    async fn send_rpc(&self, request: JsonRpcRequest) -> A2AResult<JsonRpcResponse> {
        let mut http_request = self
            .http
            .post(self.base_url.as_str())
            .header("Content-Type", A2A_MEDIA_TYPE)
            .header("Accept", A2A_MEDIA_TYPE)
            .json(&request);

        if let Some(ref token) = self.auth_token {
            http_request = http_request.bearer_auth(token);
        }

        tracing::debug!(
            method = %request.method,
            url = %self.base_url,
            "Sending A2A request"
        );

        let response = http_request.send().await?;

        if !response.status().is_success() {
            return Err(A2AError::Transport(
                response.error_for_status().unwrap_err(),
            ));
        }

        let rpc_response: JsonRpcResponse = response.json().await?;
        Ok(rpc_response)
    }
}

// ── Request Types ────────────────────────────────────────────

/// Request to send a message to the remote agent.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SendMessageRequest {
    /// The message to send.
    pub message: Message,

    /// Existing task ID to continue (optional — omit to create a new task).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub task_id: Option<String>,

    /// Context ID to group related tasks.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub context_id: Option<String>,

    /// Optional metadata.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub metadata: Option<serde_json::Value>,
}

impl Default for SendMessageRequest {
    fn default() -> Self {
        Self {
            message: Message::user(vec![]),
            task_id: None,
            context_id: None,
            metadata: None,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_send_message_request_serialization() {
        let req = SendMessageRequest {
            message: Message::user_text("Hello"),
            task_id: None,
            context_id: Some("session-1".into()),
            metadata: None,
        };

        let json = serde_json::to_string_pretty(&req).unwrap();
        assert!(json.contains("session-1"));
        assert!(json.contains("Hello"));
    }
}

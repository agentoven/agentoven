//! Push notification types for A2A webhook-based async delivery.

use serde::{Deserialize, Serialize};
use schemars::JsonSchema;
use url::Url;

use crate::task::TaskEvent;

/// Configuration for push notifications (webhooks).
///
/// Clients register a webhook URL where the remote agent will POST
/// task updates. This avoids the need for persistent SSE connections.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct PushNotificationConfig {
    /// Unique identifier for this notification config.
    pub id: String,

    /// The task ID this notification config is associated with.
    pub task_id: String,

    /// The webhook URL where updates will be POSTed.
    pub url: Url,

    /// Optional authentication to include in webhook requests.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub authentication: Option<PushNotificationAuth>,

    /// Optional events filter — if empty, all events are sent.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub events: Vec<String>,
}

/// Authentication credentials for push notification webhooks.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase", tag = "type")]
pub enum PushNotificationAuth {
    /// Bearer token authentication.
    Bearer { token: String },

    /// Custom header authentication.
    Header { name: String, value: String },
}

/// A push notification event sent to the webhook.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PushNotificationEvent {
    /// The notification config ID.
    pub config_id: String,

    /// The task event.
    pub event: TaskEvent,

    /// ISO 8601 timestamp.
    pub timestamp: String,
}

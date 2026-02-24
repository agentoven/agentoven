//! Task — the stateful unit of work in the A2A protocol.
//!
//! A Task represents an interaction between a client agent and a remote agent.
//! Tasks have a full lifecycle with well-defined state transitions:
//!
//! ```text
//! SUBMITTED → WORKING → COMPLETED (terminal)
//!                     → FAILED (terminal)
//!                     → CANCELED (terminal)
//!                     → REJECTED (terminal)
//!                     → INPUT_REQUIRED (interrupted)
//!                     → AUTH_REQUIRED (interrupted)
//! ```

use chrono::{DateTime, Utc};
use schemars::JsonSchema;
use serde::{Deserialize, Serialize};
use uuid::Uuid;

use crate::artifact::Artifact;
use crate::message::Message;

/// A Task — the fundamental unit of work in A2A.
#[derive(Debug, Clone, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "camelCase")]
pub struct Task {
    /// Unique identifier for this task.
    pub id: String,

    /// Optional context ID grouping related tasks for session coherence.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub context_id: Option<String>,

    /// Current state of the task.
    pub state: TaskState,

    /// Messages exchanged during the task (multi-turn conversation).
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub messages: Vec<Message>,

    /// Artifacts produced by the task.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub artifacts: Vec<Artifact>,

    /// Optional metadata attached to the task.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<serde_json::Value>,

    /// When the task was created.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub created_at: Option<DateTime<Utc>>,

    /// When the task was last updated.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub updated_at: Option<DateTime<Utc>>,
}

impl Task {
    /// Create a new task with a generated ID.
    pub fn new() -> Self {
        Self {
            id: Uuid::new_v4().to_string(),
            context_id: None,
            state: TaskState::Submitted,
            messages: Vec::new(),
            artifacts: Vec::new(),
            metadata: None,
            created_at: Some(Utc::now()),
            updated_at: Some(Utc::now()),
        }
    }

    /// Create a new task within a context (session).
    pub fn with_context(context_id: impl Into<String>) -> Self {
        Self {
            context_id: Some(context_id.into()),
            ..Self::new()
        }
    }

    /// Check if the task is in a terminal state.
    pub fn is_terminal(&self) -> bool {
        matches!(
            self.state,
            TaskState::Completed | TaskState::Failed | TaskState::Canceled | TaskState::Rejected
        )
    }

    /// Check if the task is in an interrupted state (needs input or auth).
    pub fn is_interrupted(&self) -> bool {
        matches!(
            self.state,
            TaskState::InputRequired | TaskState::AuthRequired
        )
    }

    /// Transition the task to a new state.
    pub fn transition(&mut self, new_state: TaskState) -> Result<(), InvalidTransition> {
        if self.is_terminal() {
            return Err(InvalidTransition {
                from: self.state.clone(),
                to: new_state,
            });
        }
        self.state = new_state;
        self.updated_at = Some(Utc::now());
        Ok(())
    }

    /// Add a message to the task.
    pub fn add_message(&mut self, message: Message) {
        self.messages.push(message);
        self.updated_at = Some(Utc::now());
    }

    /// Add an artifact to the task.
    pub fn add_artifact(&mut self, artifact: Artifact) {
        self.artifacts.push(artifact);
        self.updated_at = Some(Utc::now());
    }
}

impl Default for Task {
    fn default() -> Self {
        Self::new()
    }
}

/// The state of a task in its lifecycle.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize, JsonSchema)]
#[serde(rename_all = "SCREAMING_SNAKE_CASE")]
pub enum TaskState {
    /// Task has been submitted but not yet started.
    Submitted,

    /// Task is actively being worked on.
    Working,

    /// Task completed successfully (terminal).
    Completed,

    /// Task failed (terminal).
    Failed,

    /// Task was canceled by the client (terminal).
    Canceled,

    /// Task was rejected by the remote agent (terminal).
    Rejected,

    /// Task is paused, waiting for additional input from the client.
    InputRequired,

    /// Task is paused, waiting for authentication/authorization.
    AuthRequired,
}

impl std::fmt::Display for TaskState {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            TaskState::Submitted => write!(f, "SUBMITTED"),
            TaskState::Working => write!(f, "WORKING"),
            TaskState::Completed => write!(f, "COMPLETED"),
            TaskState::Failed => write!(f, "FAILED"),
            TaskState::Canceled => write!(f, "CANCELED"),
            TaskState::Rejected => write!(f, "REJECTED"),
            TaskState::InputRequired => write!(f, "INPUT_REQUIRED"),
            TaskState::AuthRequired => write!(f, "AUTH_REQUIRED"),
        }
    }
}

/// Error for invalid task state transitions.
#[derive(Debug)]
pub struct InvalidTransition {
    pub from: TaskState,
    pub to: TaskState,
}

impl std::fmt::Display for InvalidTransition {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "invalid task transition from {} to {}",
            self.from, self.to
        )
    }
}

impl std::error::Error for InvalidTransition {}

/// Parameters for querying/listing tasks.
#[derive(Debug, Default, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TaskQueryParams {
    /// Filter by context ID.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub context_id: Option<String>,

    /// Filter by task state.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub state: Option<TaskState>,

    /// Maximum number of tasks to return.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub limit: Option<u32>,

    /// Cursor for pagination.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cursor: Option<String>,
}

/// A streaming event for a task update (sent via SSE).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "type")]
pub enum TaskEvent {
    /// Task state changed.
    StateChanged { task_id: String, state: TaskState },

    /// New message added to the task.
    MessageAdded { task_id: String, message: Message },

    /// New artifact produced.
    ArtifactAdded { task_id: String, artifact: Artifact },

    /// Partial artifact data (streaming).
    ArtifactChunk {
        task_id: String,
        artifact_id: String,
        chunk: String,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_task_lifecycle() {
        let mut task = Task::new();
        assert_eq!(task.state, TaskState::Submitted);
        assert!(!task.is_terminal());

        task.transition(TaskState::Working).unwrap();
        assert_eq!(task.state, TaskState::Working);

        task.transition(TaskState::InputRequired).unwrap();
        assert!(task.is_interrupted());

        task.transition(TaskState::Working).unwrap();
        task.transition(TaskState::Completed).unwrap();
        assert!(task.is_terminal());

        // Cannot transition from terminal state
        assert!(task.transition(TaskState::Working).is_err());
    }

    #[test]
    fn test_task_serialization() {
        let task = Task::new();
        let json = serde_json::to_string(&task).unwrap();
        assert!(json.contains("SUBMITTED"));

        let parsed: Task = serde_json::from_str(&json).unwrap();
        assert_eq!(parsed.state, TaskState::Submitted);
    }
}

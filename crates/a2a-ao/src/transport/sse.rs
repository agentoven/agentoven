//! SSE (Server-Sent Events) transport for A2A streaming.
//!
//! Used for real-time task updates via `SendStreamingMessage` and `SubscribeToTask`.

use futures::Stream;
use pin_project_lite::pin_project;
use std::pin::Pin;
use std::task::{Context, Poll};

use crate::error::A2AError;
use crate::task::TaskEvent;

// A stream of task events received via SSE.
pin_project! {
    pub struct TaskEventStream {
        #[pin]
        inner: Pin<Box<dyn Stream<Item = Result<TaskEvent, A2AError>> + Send>>,
    }
}

impl TaskEventStream {
    /// Create a new TaskEventStream from an SSE connection.
    pub fn new(inner: Pin<Box<dyn Stream<Item = Result<TaskEvent, A2AError>> + Send>>) -> Self {
        Self { inner }
    }
}

impl Stream for TaskEventStream {
    type Item = Result<TaskEvent, A2AError>;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        self.project().inner.poll_next(cx)
    }
}

/// Parse an SSE event data line into a TaskEvent.
pub fn parse_sse_event(data: &str) -> Result<TaskEvent, A2AError> {
    serde_json::from_str(data)
        .map_err(|e| A2AError::StreamingError(format!("Failed to parse SSE event: {e}")))
}

/// SSE event type constants.
pub mod event_types {
    /// Task state change event.
    pub const STATE_CHANGED: &str = "state_changed";
    /// New message added to task.
    pub const MESSAGE_ADDED: &str = "message_added";
    /// New artifact produced.
    pub const ARTIFACT_ADDED: &str = "artifact_added";
    /// Partial artifact data chunk.
    pub const ARTIFACT_CHUNK: &str = "artifact_chunk";
    /// End of stream.
    pub const DONE: &str = "done";
}

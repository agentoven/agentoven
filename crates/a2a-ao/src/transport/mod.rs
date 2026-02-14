//! Transport layer — wire-level protocol bindings for A2A.
//!
//! A2A supports multiple transport bindings:
//! - JSON-RPC 2.0 over HTTP (primary)
//! - SSE (Server-Sent Events) for streaming
//! - gRPC (future)

pub mod jsonrpc;
pub mod sse;

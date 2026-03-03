# ADR-0006: Python SDK Uses reqwest::blocking Instead of Async Runtime

- **Status:** Accepted
- **Date:** 2026-02-25
- **Author(s):** Siddartha Kopparapu

## Context

The AgentOven Python SDK is built with PyO3/maturin, bridging Rust code into Python. The initial implementation wrapped the async `AgentOvenClient` (which uses `reqwest` with tokio) by creating a tokio runtime inside each Python method call.

This caused several problems:

1. **Runtime conflicts:** If the Python user is already running inside an async context (e.g., Jupyter notebooks, FastAPI), creating a nested tokio runtime panics or deadlocks.
2. **Complexity:** Each method needed `Runtime::new()` → `block_on()` boilerplate.
3. **Error handling:** Async errors were difficult to surface cleanly through the PyO3 boundary.
4. **Payload issues:** The async Rust client serialized the entire `Agent` struct (including empty/default fields), causing the Go control plane to reject requests with mismatched enums.

## Decision

Replace the async Rust client wrapper with **direct `reqwest::blocking::Client`** usage in the Python SDK:

- The Python SDK creates its own `reqwest::blocking::Client` (singleton, reused)
- HTTP requests are built manually with only user-provided fields (minimal JSON payloads)
- No tokio runtime is involved — pure synchronous I/O
- Methods like `register()`, `bake()`, `cool()` are now polymorphic (accept `Agent` object or `str` name)

### Key Implementation Details

- `agent_to_json()` helper builds minimal JSON with only non-empty fields
- `authed_request()` helper applies base URL + API key + kitchen header
- `send()` helper handles response parsing + error mapping to Python exceptions
- Backward-compatible `register_agent()` alias preserved

### Dependency Change

```toml
# sdk/python/Cargo.toml
reqwest = { workspace = true, features = ["blocking", "json"] }
```

## Consequences

- **Easier:** Works in Jupyter notebooks, FastAPI, Django, and any Python context. Simpler code (no async bridging). Minimal payloads avoid enum mismatch issues with the control plane.
- **Harder:** Cannot take advantage of async HTTP in Python (not needed for a CLI/SDK client). Duplicates some logic from the Rust `AgentOvenClient`.
- **Trade-off:** The Python SDK and Rust client may drift in feature parity over time since they're now separate HTTP implementations.

## Alternatives Considered

1. **Keep async wrapper with `pyo3-asyncio`** — Rejected because `pyo3-asyncio` adds complexity and still has issues with nested event loops.
2. **Use Python `requests` library via PyO3** — Rejected because calling Python HTTP from Rust defeats the purpose of a native extension.
3. **Generate Python SDK from OpenAPI spec** — Considered for future but doesn't exist yet; the control plane doesn't have a formal OpenAPI spec.

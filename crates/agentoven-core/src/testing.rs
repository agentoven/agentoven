//! A fake control plane for tests: a real HTTP server on a loopback port that records each request
//! (method, path, headers, JSON body) and replies from a table, so a client's paths, headers and
//! bodies are exercised for real. Not part of the public API; enabled by the `testing` feature.

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

/// One request the fake saw.
#[derive(Debug, Clone)]
pub struct Seen {
    pub method: String,
    /// The path with its query string.
    pub path: String,
    /// Header names lower-cased.
    pub headers: HashMap<String, String>,
    /// The JSON body, or `Null` for none.
    pub body: serde_json::Value,
}

/// A scripted reply.
#[derive(Debug, Clone)]
pub enum Reply {
    Json(u16, serde_json::Value),
    /// A body that is not one JSON document (an NDJSON export), with extra headers.
    Raw(u16, String, Vec<(String, String)>),
}

/// The scripted replies, keyed `"METHOD /path?query"`; anything else is a 404 `{"error":"no route"}`.
pub type Routes = HashMap<String, Reply>;

/// The requests seen so far.
#[derive(Clone, Default)]
pub struct Log(Arc<Mutex<Vec<Seen>>>);

impl Log {
    pub fn all(&self) -> Vec<Seen> {
        self.0.lock().unwrap().clone()
    }
    pub fn last(&self) -> Seen {
        self.all().last().cloned().expect("no request was made")
    }
    pub fn len(&self) -> usize {
        self.0.lock().unwrap().len()
    }
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
    /// `"METHOD path"` of every request, in order.
    pub fn calls(&self) -> Vec<String> {
        self.all()
            .iter()
            .map(|s| format!("{} {}", s.method, s.path))
            .collect()
    }
}

/// Start the fake and return its base URL and the log. It runs for the life of the test's runtime.
pub async fn fake_control_plane(routes: Routes) -> (String, Log) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let log = Log::default();
    let recorded = log.clone();
    let routes = Arc::new(routes);
    tokio::spawn(async move {
        loop {
            let (mut sock, _) = listener.accept().await.unwrap();
            let routes = routes.clone();
            let recorded = recorded.clone();
            tokio::spawn(async move {
                let mut buf = Vec::new();
                let mut chunk = [0u8; 4096];
                let (head_end, content_len) = loop {
                    let n = sock.read(&mut chunk).await.unwrap();
                    if n == 0 {
                        return;
                    }
                    buf.extend_from_slice(&chunk[..n]);
                    if let Some(pos) = buf.windows(4).position(|w| w == b"\r\n\r\n") {
                        let head = String::from_utf8_lossy(&buf[..pos]).to_lowercase();
                        let len = head
                            .lines()
                            .find_map(|l| l.strip_prefix("content-length:"))
                            .and_then(|v| v.trim().parse::<usize>().ok())
                            .unwrap_or(0);
                        break (pos + 4, len);
                    }
                };
                while buf.len() < head_end + content_len {
                    let n = sock.read(&mut chunk).await.unwrap();
                    if n == 0 {
                        break;
                    }
                    buf.extend_from_slice(&chunk[..n]);
                }
                let head = String::from_utf8_lossy(&buf[..head_end]).to_string();
                let mut lines = head.lines();
                let mut first = lines.next().unwrap().split_whitespace();
                let (method, path) = (
                    first.next().unwrap().to_string(),
                    first.next().unwrap().to_string(),
                );
                let headers = lines
                    .filter_map(|l| l.split_once(':'))
                    .map(|(k, v)| (k.trim().to_lowercase(), v.trim().to_string()))
                    .collect();
                let body = serde_json::from_slice(&buf[head_end..head_end + content_len])
                    .unwrap_or(serde_json::Value::Null);
                recorded.0.lock().unwrap().push(Seen {
                    method: method.clone(),
                    path: path.clone(),
                    headers,
                    body,
                });
                let reply = routes
                    .get(&format!("{method} {path}"))
                    .cloned()
                    .unwrap_or(Reply::Json(404, serde_json::json!({"error": "no route"})));
                let (status, ctype, payload, extra) = match reply {
                    Reply::Json(s, v) => (s, "application/json", v.to_string(), vec![]),
                    Reply::Raw(s, t, h) => (s, "application/x-ndjson", t, h),
                };
                let mut out = format!(
                    "HTTP/1.1 {status} X\r\nContent-Type: {ctype}\r\nContent-Length: {}\r\nConnection: close\r\n",
                    payload.len()
                );
                for (k, v) in extra {
                    out.push_str(&format!("{k}: {v}\r\n"));
                }
                out.push_str("\r\n");
                out.push_str(&payload);
                let _ = sock.write_all(out.as_bytes()).await;
            });
        }
    });
    (base, log)
}

/// Shorthand for a JSON reply.
pub fn json(status: u16, v: serde_json::Value) -> Reply {
    Reply::Json(status, v)
}

//! Every call the Rust client makes names a (verb, path) the server has.
//!
//! `sdk/fixtures/server-routes.json` is the server's route list, generated from its chi tree (agentoven-pro's
//! `internal/routeguard` route test, with `UPDATE_SDK_ROUTES=1`). This reads the client's source for each call
//! and fails for one the server does not serve: `POST /agents/{name}/retire` where the server has `DELETE
//! /agents/{name}`, a path that was never a route. It reads the call sites, so write the path as a literal in the
//! `format!` or call that sends it.

use std::collections::HashSet;
use std::fs;
use std::path::{Path, PathBuf};

fn normalize(path: &str) -> String {
    let mut out = String::new();
    let mut depth = 0;
    for c in path.chars() {
        match c {
            '{' => {
                if depth == 0 {
                    out.push_str("{}");
                }
                depth += 1;
            }
            '}' if depth > 0 => depth -= 1,
            _ if depth == 0 => out.push(c),
            _ => {}
        }
    }
    let out = out.split('?').next().unwrap_or("").to_string();
    // "/api/v1/traces{qs}": the placeholder was a query string.
    let out = match out.strip_suffix("{}") {
        Some(rest) if !rest.ends_with('/') => rest.to_string(),
        _ => out,
    };
    let trimmed = out.trim_end_matches('/');
    if trimmed.is_empty() { "/".into() } else { trimmed.into() }
}

fn server_routes() -> HashSet<(String, String)> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../sdk/fixtures/server-routes.json");
    let data: serde_json::Value = serde_json::from_str(&fs::read_to_string(&path).expect("sdk/fixtures/server-routes.json")).unwrap();
    data["routes"]
        .as_array()
        .unwrap()
        .iter()
        .map(|r| (r["method"].as_str().unwrap().to_string(), normalize(r["path"].as_str().unwrap())))
        .collect()
}

fn sources(dir: &Path, out: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).unwrap() {
        let p = entry.unwrap().path();
        if p.is_dir() {
            sources(&p, out);
        } else if p.extension().is_some_and(|e| e == "rs") {
            out.push(p);
        }
    }
}

/// The verb a request builder or helper near `line` sends: the first of these on the lines that follow.
fn verb_near(lines: &[&str], from: usize) -> Option<&'static str> {
    const TOKENS: [(&str, &str); 10] = [
        (".get(", "GET"), (".post(", "POST"), (".put(", "PUT"), (".delete(", "DELETE"), (".patch(", "PATCH"),
        ("get_json(", "GET"), ("post_json(", "POST"), ("put_json(", "PUT"), ("delete_json(", "DELETE"), ("patch_json(", "PATCH"),
    ];
    for line in lines.iter().skip(from).take(8) {
        let first = TOKENS.iter().filter_map(|(t, v)| line.find(t).map(|at| (at, *v))).min();
        if let Some((_, v)) = first {
            return Some(v);
        }
    }
    None
}

/// (file, line, verb, path) for every `"/api/v1/..."` literal in non-test code that is sent with a verb.
fn client_calls() -> Vec<(String, usize, &'static str, String)> {
    let mut files = Vec::new();
    sources(&Path::new(env!("CARGO_MANIFEST_DIR")).join("src"), &mut files);
    let mut calls = Vec::new();
    for file in files {
        let text = fs::read_to_string(&file).unwrap();
        // Everything from the first test module on is a test, which may name any path it likes.
        let code = text.split("#[cfg(test)]").next().unwrap();
        let lines: Vec<&str> = code.lines().collect();
        for (i, line) in lines.iter().enumerate() {
            let mut rest = *line;
            while let Some(at) = rest.find("\"/api/v1") {
                let after = &rest[at + 1..];
                let end = after.find('"').unwrap_or(after.len());
                if let Some(v) = verb_near(&lines, i) {
                    calls.push((file.file_name().unwrap().to_string_lossy().into(), i + 1, v, after[..end].to_string()));
                }
                rest = &after[end..];
            }
        }
    }
    calls
}

#[test]
fn the_scanner_sees_the_clients_calls() {
    // A scanner that matched nothing would pass every contract: hold it to the size of what it must see.
    assert!(client_calls().len() > 50, "found {}", client_calls().len());
}

#[test]
fn every_call_the_client_makes_is_a_route_the_server_has() {
    let routes = server_routes();
    let missing: Vec<String> = client_calls()
        .into_iter()
        .filter(|(_, _, verb, path)| !routes.contains(&(verb.to_string(), normalize(path))))
        .map(|(f, l, v, p)| format!("{f}:{l} {v} {p}"))
        .collect();
    assert!(missing.is_empty(), "calls the server has no route for:\n  {}", missing.join("\n  "));
}

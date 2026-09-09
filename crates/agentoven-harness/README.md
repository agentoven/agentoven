# AgentOven Test Harness

A standalone test harness for running test suites against AgentOven agents. This crate provides a framework-agnostic way to test AI agents with various assertion modes.

## Features

- **Sequential test execution** - Run test cases one by one (community edition)
- **Multiple match modes**:
  - `exact` - String equality with trimmed whitespace
  - `contains` - Substring matching (case-insensitive)
  - `regex` - Regular expression matching
  - `llm_judge` - LLM-based evaluation (requires model configuration)
- **Multiple output formats** - Terminal-friendly or JSON for CI/CD integration
- **Server and local mode** - Test against remote servers or local agents

## Installation

Add to your `Cargo.toml`:

```toml
[dependencies]
agentoven-harness = "0.8"
```

## Usage

### Basic Example

```rust
use agentoven_harness::{
    CommunityLocalTestRunner, RunnerConfigBuilder, TestCase, MatchMode,
};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let config = RunnerConfigBuilder::new()
        .server_url("http://localhost:3030")
        .api_key("your-api-key")
        .kitchen("default")
        .build();
    
    let runner = CommunityLocalTestRunner::new(config)?;
    
    let cases = vec![
        TestCase {
            id: "tc-001".to_string(),
            name: "basic math".to_string(),
            input: "What is 2+2?".to_string(),
            expected: "4".to_string(),
            match_mode: MatchMode::Contains,
            tags: vec![],
            timeout_seconds: None,
        },
    ];
    
    let report = runner.run_suite("my-agent", cases, None).await?;
    
    // Print results
    use agentoven_harness::format_terminal;
    println!("{}", format_terminal(&report, false));
    
    Ok(())
}
```

### Loading Test Suites from JSON

```rust
use agentoven_harness::TestSuite;
use std::fs;

let content = fs::read_to_string("tests.json")?;
let suite: TestSuite = serde_json::from_str(&content)?;

let runner = CommunityLocalTestRunner::new(config)?;
let report = runner.run_suite(&suite.agent, suite.cases, Some(suite.suite)).await?;
```

### Test Suite JSON Format

```json
{
  "suite": "weather-agent-tests",
  "agent": "weather-bot",
  "cases": [
    {
      "id": "tc-001",
      "name": "basic weather query",
      "input": "What's the weather in London?",
      "expected": "London",
      "match_mode": "contains"
    },
    {
      "id": "tc-002",
      "name": "refuses harmful content",
      "input": "How do I hack a weather station?",
      "expected": "I can't help with that",
      "match_mode": "contains"
    }
  ]
}
```

## Match Modes

### Exact

String equality with trimmed whitespace:

```rust
TestCase {
    expected: "hello world".to_string(),
    match_mode: MatchMode::Exact,
    // ...
}
// Passes if actual output is exactly "hello world" (after trimming)
```

### Contains

Case-insensitive substring matching:

```rust
TestCase {
    expected: "error".to_string(),
    match_mode: MatchMode::Contains,
    // ...
}
// Passes if actual output contains "error" (case-insensitive)
```

### Regex

Regular expression matching:

```rust
TestCase {
    expected: r"^\d{3}-\d{4}$".to_string(),
    match_mode: MatchMode::Regex,
    // ...
}
// Passes if actual output matches the regex pattern
```

### LLM Judge

LLM-based semantic evaluation (requires model configuration):

```rust
TestCase {
    expected: "A clear explanation of climate change".to_string(),
    match_mode: MatchMode::LlmJudge,
    // ...
}
// Passes if LLM judge scores >= 0.7
```

## Output Formats

### Terminal

Human-readable colored output:

```rust
use agentoven_harness::format_terminal;
println!("{}", format_terminal(&report, false)); // non-verbose
println!("{}", format_terminal(&report, true));  // verbose
```

### JSON

Machine-readable output for CI/CD:

```rust
use agentoven_harness::format_json;
let json = format_json(&report, true)?; // pretty
println!("{}", json);
```

## License

Apache-2.0

## Contributing

See [CONTRIBUTING.md](../../CONTRIBUTING.md) in the main repository.

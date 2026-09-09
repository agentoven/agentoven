//! Match mode implementations for test case assertions.

use crate::models::{MatchMode, TestCaseResult};
use anyhow::Result;
use regex::Regex;

/// Evaluate whether a test case passed based on its match mode
pub async fn evaluate_match(
    case_id: String,
    case_name: String,
    input: String,
    expected: String,
    actual: String,
    match_mode: MatchMode,
    llm_judge_fn: Option<&dyn Fn(&str, &str, &str) -> Result<(f64, String)>>,
) -> TestCaseResult {
    let start = std::time::Instant::now();
    
    let (passed, score, reason, error) = match match_mode {
        MatchMode::Exact => {
            let passed = expected.trim() == actual.trim();
            (passed, None, None, None)
        }
        
        MatchMode::Contains => {
            let expected_lower = expected.trim().to_lowercase();
            let actual_lower = actual.trim().to_lowercase();
            let passed = actual_lower.contains(&expected_lower);
            (passed, None, None, None)
        }
        
        MatchMode::Regex => {
            match Regex::new(&expected) {
                Ok(re) => {
                    let passed = re.is_match(&actual);
                    (passed, None, None, None)
                }
                Err(e) => {
                    (false, None, None, Some(format!("Invalid regex pattern: {}", e)))
                }
            }
        }
        
        MatchMode::LlmJudge => {
            if let Some(judge_fn) = llm_judge_fn {
                match judge_fn(&input, &expected, &actual) {
                    Ok((score, reason)) => {
                        // Consider passed if score >= 0.7
                        let passed = score >= 0.7;
                        (passed, Some(score), Some(reason), None)
                    }
                    Err(e) => {
                        (false, None, None, Some(format!("LLM judge error: {}", e)))
                    }
                }
            } else {
                (
                    false,
                    None,
                    None,
                    Some("LLM judge mode requires a configured model".to_string()),
                )
            }
        }
    };
    
    let duration_ms = start.elapsed().as_millis() as u64;
    
    TestCaseResult {
        case_id,
        case_name,
        input,
        expected,
        actual,
        passed,
        match_mode,
        score,
        reason,
        duration_ms,
        error,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    
    #[tokio::test]
    async fn test_exact_match() {
        let result = evaluate_match(
            "tc1".to_string(),
            "test".to_string(),
            "input".to_string(),
            "hello".to_string(),
            "hello".to_string(),
            MatchMode::Exact,
            None,
        )
        .await;
        
        assert!(result.passed);
        assert!(result.error.is_none());
    }
    
    #[tokio::test]
    async fn test_exact_match_whitespace() {
        let result = evaluate_match(
            "tc1".to_string(),
            "test".to_string(),
            "input".to_string(),
            "  hello  ".to_string(),
            "hello".to_string(),
            MatchMode::Exact,
            None,
        )
        .await;
        
        assert!(result.passed);
    }
    
    #[tokio::test]
    async fn test_contains_match() {
        let result = evaluate_match(
            "tc1".to_string(),
            "test".to_string(),
            "input".to_string(),
            "world".to_string(),
            "hello world foo".to_string(),
            MatchMode::Contains,
            None,
        )
        .await;
        
        assert!(result.passed);
    }
    
    #[tokio::test]
    async fn test_contains_case_insensitive() {
        let result = evaluate_match(
            "tc1".to_string(),
            "test".to_string(),
            "input".to_string(),
            "WORLD".to_string(),
            "hello world foo".to_string(),
            MatchMode::Contains,
            None,
        )
        .await;
        
        assert!(result.passed);
    }
    
    #[tokio::test]
    async fn test_regex_match() {
        let result = evaluate_match(
            "tc1".to_string(),
            "test".to_string(),
            "input".to_string(),
            r"^\d{3}-\d{4}$".to_string(),
            "123-4567".to_string(),
            MatchMode::Regex,
            None,
        )
        .await;
        
        assert!(result.passed);
    }
    
    #[tokio::test]
    async fn test_regex_no_match() {
        let result = evaluate_match(
            "tc1".to_string(),
            "test".to_string(),
            "input".to_string(),
            r"^\d{3}-\d{4}$".to_string(),
            "abc-defg".to_string(),
            MatchMode::Regex,
            None,
        )
        .await;
        
        assert!(!result.passed);
    }
}

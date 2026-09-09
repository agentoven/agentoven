//! Test result reporting in various formats.

use crate::models::{TestRunReport, TestRunStatus};
use colored::*;

/// Format test report for terminal output
pub fn format_terminal(report: &TestRunReport, verbose: bool) -> String {
    let mut output = String::new();
    
    output.push_str(&format!(
        "\n🧪 Running test suite: {}\n",
        report.agent_name.bold()
    ));
    
    if let Some(ref suite_id) = report.suite_id {
        output.push_str(&format!("   Suite: {} ({} cases)\n", suite_id, report.total));
    } else {
        output.push_str(&format!("   Agent: {} ({} cases)\n", report.agent_name, report.total));
    }
    
    output.push_str("\n");
    
    // Print each test case result
    for result in &report.results {
        let status_icon = if result.error.is_some() {
            "⚠".yellow()
        } else if result.passed {
            "✅".green()
        } else {
            "❌".red()
        };
        
        let status_text = if result.error.is_some() {
            "ERROR".yellow()
        } else if result.passed {
            "PASS".green()
        } else {
            "FAIL".red()
        };
        
        let duration_sec = result.duration_ms as f64 / 1000.0;
        
        output.push_str(&format!(
            "  {} {} {} ",
            status_icon,
            result.case_id.dimmed(),
            result.case_name
        ));
        
        // Add padding dots
        let name_len = result.case_id.len() + result.case_name.len() + 2;
        let dots = if name_len < 50 {
            ".".repeat(50 - name_len)
        } else {
            String::new()
        };
        output.push_str(&dots.dimmed().to_string());
        
        output.push_str(&format!(" {} ({:.1}s)", status_text, duration_sec));
        
        // Add score for LLM judge
        if let Some(score) = result.score {
            output.push_str(&format!(" [score: {:.2}]", score).dimmed().to_string());
        }
        
        output.push_str("\n");
        
        // Verbose mode: show details
        if verbose {
            output.push_str(&format!("     Input: {}\n", result.input.dimmed()));
            output.push_str(&format!("     Expected: {}\n", result.expected.dimmed()));
            output.push_str(&format!("     Actual: {}\n", result.actual.dimmed()));
            
            if let Some(ref reason) = result.reason {
                output.push_str(&format!("     Reason: {}\n", reason.dimmed()));
            }
            
            if let Some(ref error) = result.error {
                output.push_str(&format!("     Error: {}\n", error.red()));
            }
            
            output.push_str("\n");
        } else if let Some(ref error) = result.error {
            // Show errors even in non-verbose mode
            output.push_str(&format!("     Error: {}\n", error.red()));
        }
    }
    
    // Summary
    output.push_str(&format!("\n{}\n", "━".repeat(76).dimmed()));
    
    let status_summary = match report.status {
        TestRunStatus::Passed => format!("  Results: {} ✓", "ALL TESTS PASSED".green().bold()),
        TestRunStatus::Failed => format!(
            "  Results: {} passed, {} failed, {} errors",
            report.passed,
            report.failed.to_string().red().bold(),
            report.errors
        ),
        TestRunStatus::Error => format!("  Results: {} (run encountered errors)", "ERROR".red().bold()),
        TestRunStatus::Running => format!("  Results: {} (still running)", "IN PROGRESS".yellow()),
    };
    
    output.push_str(&status_summary);
    output.push_str("\n");
    
    let duration_sec = report.duration_ms as f64 / 1000.0;
    output.push_str(&format!("  Duration: {:.1}s\n", duration_sec));
    
    output.push_str("\n");
    
    output
}

/// Format test report as JSON
pub fn format_json(report: &TestRunReport, pretty: bool) -> Result<String, serde_json::Error> {
    if pretty {
        serde_json::to_string_pretty(report)
    } else {
        serde_json::to_string(report)
    }
}

/// Print a summary line for a test report
pub fn print_summary(report: &TestRunReport) {
    match report.status {
        TestRunStatus::Passed => {
            println!("\n{} All {} test(s) passed!", "✓".green().bold(), report.total);
        }
        TestRunStatus::Failed => {
            println!(
                "\n{} {} of {} test(s) failed",
                "✗".red().bold(),
                report.failed,
                report.total
            );
        }
        TestRunStatus::Error => {
            println!(
                "\n{} Test run encountered errors",
                "⚠".yellow().bold()
            );
        }
        TestRunStatus::Running => {
            println!("\n{} Test run in progress...", "→".blue());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::models::{MatchMode, TestCaseResult};
    
    fn create_test_result(id: &str, name: &str, passed: bool) -> TestCaseResult {
        TestCaseResult {
            case_id: id.to_string(),
            case_name: name.to_string(),
            input: "test input".to_string(),
            expected: "expected".to_string(),
            actual: if passed { "expected" } else { "wrong" }.to_string(),
            passed,
            match_mode: MatchMode::Exact,
            score: None,
            reason: None,
            duration_ms: 1234,
            error: None,
        }
    }
    
    #[test]
    fn test_format_json() {
        let mut report = TestRunReport::new(
            "run-123".to_string(),
            "test-agent".to_string(),
            None,
        );
        
        report.add_result(create_test_result("tc1", "test 1", true));
        report.add_result(create_test_result("tc2", "test 2", false));
        report.finalize(std::time::Duration::from_secs(2));
        
        let json = format_json(&report, false).unwrap();
        assert!(json.contains("run-123"));
        assert!(json.contains("test-agent"));
    }
}

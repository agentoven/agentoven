//! `agentoven rag` — RAG pipeline operations.

use clap::{Args, Subcommand};
use colored::Colorize;

#[derive(Subcommand)]
pub enum RagCommands {
    /// Query the RAG pipeline.
    Query(QueryArgs),
    /// Ingest documents into the RAG pipeline.
    Ingest(IngestArgs),
}

#[derive(Args)]
pub struct QueryArgs {
    /// Query text.
    pub query: String,
    /// Pipeline/strategy (naive, sentence-window, parent-doc, hyde, agentic).
    #[arg(long, default_value = "naive")]
    pub strategy: String,
    /// Maximum results to return.
    #[arg(long, default_value = "5")]
    pub top_k: u32,
    /// RAG provider name (default: built-in).
    #[arg(long, default_value = "built-in")]
    pub provider: String,
    /// Namespace/collection for scoped retrieval.
    #[arg(long, default_value = "default")]
    pub namespace: String,
    /// Include source documents in output.
    #[arg(long)]
    pub sources: bool,
}

#[derive(Args)]
pub struct IngestArgs {
    /// Path to file or directory to ingest.
    pub path: String,
    /// Chunk size in characters.
    #[arg(long, default_value = "512")]
    pub chunk_size: u32,
    /// Chunk overlap in characters.
    #[arg(long, default_value = "50")]
    pub chunk_overlap: u32,
    /// Namespace/collection name for document scoping.
    #[arg(long, default_value = "default")]
    pub namespace: String,
}

pub async fn execute(cmd: RagCommands) -> anyhow::Result<()> {
    match cmd {
        RagCommands::Query(args) => query(args).await,
        RagCommands::Ingest(args) => ingest(args).await,
    }
}

async fn query(args: QueryArgs) -> anyhow::Result<()> {
    println!("\n  🔍 RAG Query (strategy: {})\n", args.strategy.bold());

    let body = serde_json::json!({
        "question": args.query,
        "strategy": args.strategy,
        "top_k": args.top_k,
        "namespace": args.namespace,
    });

    let client = agentoven_core::AgentOvenClient::from_env()?;
    match client.rag_query(body).await {
        Ok(result) => {
            // Display answer
            if let Some(answer) = result["answer"]
                .as_str()
                .or_else(|| result["response"].as_str())
            {
                println!("  🤖 {}:\n", "Answer".green().bold());
                for line in answer.lines() {
                    println!("    {}", line);
                }
                println!();
            }

            // Display sources if requested
            if args.sources {
                if let Some(sources) = result["sources"]
                    .as_array()
                {
                    if !sources.is_empty() {
                        println!("  {} ({}):", "Sources".bold(), sources.len());
                        println!("  {}", "─".repeat(60).dimmed());
                        for (i, src) in sources.iter().enumerate() {
                            let title = src["doc"]["metadata"]["source"]
                                .as_str()
                                .or_else(|| src["doc"]["id"].as_str())
                                .unwrap_or("(untitled)");
                            let score = src["score"].as_f64().unwrap_or(0.0);
                            println!("  {}. {} (score: {:.3})", i + 1, title.cyan(), score);
                            if let Some(chunk) =
                                src["doc"]["content"].as_str()
                            {
                                let preview = if chunk.len() > 120 {
                                    format!("{}...", &chunk[..120])
                                } else {
                                    chunk.to_string()
                                };
                                println!("     {}", preview.dimmed());
                            }
                        }
                        println!();
                    }
                }
            }

            // Display metrics
            println!(
                "  {} Latency: {}ms | Tokens: {} | Chunks: {}",
                "→".dimmed(),
                result["latency_ms"].as_u64().unwrap_or(0),
                result["tokens_used"].as_u64().unwrap_or(0),
                result["chunks_retrieved"].as_u64().unwrap_or(0),
            );
        }
        Err(e) => {
            println!(
                "  {} Query failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

async fn ingest(args: IngestArgs) -> anyhow::Result<()> {
    println!("\n  📥 Ingesting: {}\n", args.path.bold());

    // Read file(s)
    let path = std::path::Path::new(&args.path);
    let documents = if path.is_file() {
        let content = tokio::fs::read_to_string(path).await?;
        vec![serde_json::json!({
            "id": path.file_name().and_then(|f| f.to_str()).unwrap_or("file"),
            "content": content,
        })]
    } else if path.is_dir() {
        let mut docs = Vec::new();
        let mut entries = tokio::fs::read_dir(path).await?;
        while let Some(entry) = entries.next_entry().await? {
            let p = entry.path();
            if p.is_file() {
                if let Ok(content) = tokio::fs::read_to_string(&p).await {
                    docs.push(serde_json::json!({
                        "id": p.file_name().and_then(|f| f.to_str()).unwrap_or("file"),
                        "content": content,
                    }));
                }
            }
        }
        docs
    } else {
        anyhow::bail!("Path '{}' not found or not a file/directory", args.path);
    };

    println!(
        "  {} {} document(s) to ingest",
        "→".dimmed(),
        documents.len()
    );

    let body = serde_json::json!({
        "documents": documents,
        "chunk_size": args.chunk_size,
        "chunk_overlap": args.chunk_overlap,
        "namespace": args.namespace,
    });

    let client = agentoven_core::AgentOvenClient::from_env()?;
    let pb = indicatif::ProgressBar::new(documents.len() as u64);
    pb.set_style(
        indicatif::ProgressStyle::default_bar()
            .template("  {spinner:.green} [{bar:40.cyan/dim}] {pos}/{len} documents")
            .unwrap()
            .progress_chars("█▓░"),
    );

    match client.rag_ingest(body).await {
        Ok(result) => {
            pb.finish_and_clear();
            let chunks = result["chunks_created"]
                .as_u64()
                .unwrap_or(0);
            let docs = result["documents_processed"]
                .as_u64()
                .unwrap_or(documents.len() as u64);
            println!(
                "  {} Ingested {} document(s), {} chunk(s) in collection '{}'.",
                "✓".green().bold(),
                docs,
                chunks,
                args.namespace
            );
        }
        Err(e) => {
            pb.finish_and_clear();
            println!(
                "  {} Ingestion failed: {}",
                "✗".red().bold(),
                e.to_string().dimmed()
            );
        }
    }
    Ok(())
}

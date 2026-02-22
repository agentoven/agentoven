"""
AgentOven RAGAS MCP Tool Server

A FastAPI/MCP wrapper around the RAGAS evaluation library (Apache-2.0).
Exposes RAGAS metrics as MCP-compatible tool endpoints.

Run locally:
    pip install ragas fastapi uvicorn
    uvicorn server:app --host 0.0.0.0 --port 8400

Or use Docker:
    docker build -t agentoven/ragas-mcp .
    docker run -p 8400:8400 agentoven/ragas-mcp
"""

import os
from typing import Optional

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

app = FastAPI(
    title="AgentOven RAGAS MCP Tool",
    description="RAGAS evaluation metrics exposed as MCP tools",
    version="0.1.0",
)


# ── Request/Response Models ──────────────────────────────────


class RAGASEvalRequest(BaseModel):
    """Evaluate RAG pipeline quality using RAGAS metrics."""

    question: str
    answer: str
    contexts: list[str]
    ground_truth: Optional[str] = None
    metrics: Optional[list[str]] = None  # default: all applicable


class RAGASEvalResponse(BaseModel):
    scores: dict[str, float]
    details: Optional[dict] = None


class HealthResponse(BaseModel):
    status: str
    ragas_version: str
    available_metrics: list[str]


# ── Available Metrics ────────────────────────────────────────

AVAILABLE_METRICS = [
    "faithfulness",
    "answer_relevancy",
    "context_precision",
    "context_recall",
    "context_relevancy",
    "answer_correctness",
    "answer_similarity",
]


# ── Endpoints ────────────────────────────────────────────────


@app.get("/health", response_model=HealthResponse)
async def health():
    """Health check with RAGAS version and available metrics."""
    try:
        import ragas

        version = getattr(ragas, "__version__", "unknown")
    except ImportError:
        version = "not installed"

    return HealthResponse(
        status="healthy",
        ragas_version=version,
        available_metrics=AVAILABLE_METRICS,
    )


@app.post("/evaluate", response_model=RAGASEvalResponse)
async def evaluate(req: RAGASEvalRequest):
    """
    Evaluate a single RAG response using RAGAS metrics.

    Metrics that require ground_truth (context_recall, answer_correctness,
    answer_similarity) are skipped if ground_truth is not provided.
    """
    try:
        from datasets import Dataset
        from ragas import evaluate as ragas_evaluate
        from ragas.metrics import (
            answer_correctness,
            answer_relevancy,
            answer_similarity,
            context_precision,
            context_recall,
            context_relevancy,
            faithfulness,
        )
    except ImportError as e:
        raise HTTPException(
            status_code=503,
            detail=f"RAGAS not installed: {e}. Run: pip install ragas",
        )

    # Map metric names to metric objects
    metric_map = {
        "faithfulness": faithfulness,
        "answer_relevancy": answer_relevancy,
        "context_precision": context_precision,
        "context_recall": context_recall,
        "context_relevancy": context_relevancy,
        "answer_correctness": answer_correctness,
        "answer_similarity": answer_similarity,
    }

    # Determine which metrics to run
    requested = req.metrics or list(metric_map.keys())
    needs_ground_truth = {"context_recall", "answer_correctness", "answer_similarity"}

    selected_metrics = []
    for name in requested:
        if name not in metric_map:
            raise HTTPException(
                status_code=400, detail=f"Unknown metric: {name}"
            )
        if name in needs_ground_truth and not req.ground_truth:
            continue  # skip metrics that need ground truth if not provided
        selected_metrics.append(metric_map[name])

    if not selected_metrics:
        raise HTTPException(
            status_code=400,
            detail="No applicable metrics (provide ground_truth for recall/correctness metrics)",
        )

    # Build dataset
    data = {
        "question": [req.question],
        "answer": [req.answer],
        "contexts": [req.contexts],
    }
    if req.ground_truth:
        data["ground_truth"] = [req.ground_truth]

    dataset = Dataset.from_dict(data)

    # Run evaluation
    try:
        result = ragas_evaluate(dataset, metrics=selected_metrics)
        scores = {k: round(v, 4) for k, v in result.items() if isinstance(v, (int, float))}
    except Exception as e:
        raise HTTPException(
            status_code=500, detail=f"RAGAS evaluation failed: {str(e)}"
        )

    return RAGASEvalResponse(scores=scores)


@app.get("/mcp/tools")
async def mcp_tools():
    """
    Return MCP tool definitions for this server.
    Used by AgentOven's MCP Gateway for tool discovery.
    """
    return [
        {
            "name": "ragas.evaluate",
            "description": "Evaluate RAG pipeline quality using RAGAS metrics (faithfulness, relevancy, precision, recall, correctness, similarity)",
            "inputSchema": {
                "type": "object",
                "properties": {
                    "question": {
                        "type": "string",
                        "description": "The user question",
                    },
                    "answer": {
                        "type": "string",
                        "description": "The RAG-generated answer",
                    },
                    "contexts": {
                        "type": "array",
                        "items": {"type": "string"},
                        "description": "Retrieved context passages",
                    },
                    "ground_truth": {
                        "type": "string",
                        "description": "Expected correct answer (optional, enables recall/correctness metrics)",
                    },
                    "metrics": {
                        "type": "array",
                        "items": {"type": "string"},
                        "description": "Specific metrics to evaluate (default: all applicable)",
                    },
                },
                "required": ["question", "answer", "contexts"],
            },
        }
    ]


if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("RAGAS_PORT", "8400"))
    uvicorn.run(app, host="0.0.0.0", port=port)

"""A research agent built with Deep Agents, hosted by AgentOven.

AgentOven starts this file as the agent's process (see agent.json). It reads the model, the
instructions and the tools AgentOven resolved for the agent from the environment, builds a Deep
Agent from them, and serves it on the port AgentOven chose. Callers reach it through the control
plane (invoke, streaming, A2A), which checks guardrails on what goes in and what comes out.
"""

import httpx

from agentoven.runtime import AgentOvenRuntime, DeepAgentsAdapter, build_deep_agent, serve

INSTRUCTIONS = """You are a careful research agent.

Plan the work first with your todo list. Fetch the pages you need with fetch_page, write what you
learn to files as you go (notes.md for findings, report.md for the final report), and finish with
a short answer that cites the URLs you used. If a page cannot be fetched, say so rather than guessing."""

MAX_PAGE_CHARS = 20_000


def fetch_page(url: str) -> str:
    """Fetch a web page over HTTP(S) and return its text, cut to 20,000 characters."""
    if not url.startswith(("http://", "https://")):
        return "Error: only http and https URLs can be fetched."
    try:
        resp = httpx.get(url, timeout=20, follow_redirects=True, headers={"User-Agent": "agentoven-research/1.0"})
        resp.raise_for_status()
    except httpx.HTTPError as exc:
        return f"Error fetching {url}: {exc}"
    return resp.text[:MAX_PAGE_CHARS]


if __name__ == "__main__":
    runtime = AgentOvenRuntime()
    agent = build_deep_agent(runtime, tools=[fetch_page], system_prompt=INSTRUCTIONS)
    serve(DeepAgentsAdapter(agent), runtime)

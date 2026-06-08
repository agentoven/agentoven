"""
AgentOven Python SDK — enterprise agent orchestration.

The tastiest way to manage your AI agents. 🏺

Usage:
    from agentoven import Agent, Ingredient, Recipe, Step, AgentOvenClient

    agent = Agent("summarizer", ingredients=[
        Ingredient.model("gpt-4o", provider="azure-openai"),
        Ingredient.tool("doc-reader", protocol="mcp"),
    ])

    client = AgentOvenClient()
    client.register(agent)
    client.bake(agent)
"""

from agentoven._native import (
    Agent,
    AgentStatus,
    AgentOvenClient as _NativeClient,
    Branch,
    Ingredient,
    IngredientKind,
    Recipe,
    Step,
)
from agentoven.client import AgentOvenClient, AgentOvenAPIError

__all__ = [
    # Full client (OSS + Pro REST coverage) — use this in new code
    "AgentOvenClient",
    "AgentOvenAPIError",
    # Data types
    "Agent",
    "AgentStatus",
    "Branch",
    "Ingredient",
    "IngredientKind",
    "Recipe",
    "Step",
    # Native client (OSS-only, kept for backwards compat)
    "_NativeClient",
]

__version__ = "0.8.5b3"

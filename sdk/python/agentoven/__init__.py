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
    AgentOvenClient,
    Ingredient,
    IngredientKind,
    Recipe,
    Step,
)

__all__ = [
    "Agent",
    "AgentStatus",
    "AgentOvenClient",
    "Ingredient",
    "IngredientKind",
    "Recipe",
    "Step",
]

__version__ = "0.3.2"

"""
AgentOven Python SDK — enterprise agent orchestration.

The tastiest way to manage your AI agents. 🏺

Usage:
    from agentoven import Agent, AgentOvenClient

    client = AgentOvenClient(url="http://localhost:8080")

    agent = Agent(
        name="research-agent",
        description="Researches topics and summarizes findings",
        framework="langchain",
    )

    client.register_agent(agent)
    client.bake("research-agent")
"""

from agentoven._native import (
    Agent,
    AgentStatus,
    AgentOvenClient,
    Ingredient,
    IngredientKind,
    Recipe,
)

__all__ = [
    "Agent",
    "AgentStatus",
    "AgentOvenClient",
    "Ingredient",
    "IngredientKind",
    "Recipe",
]

__version__ = "0.3.1"

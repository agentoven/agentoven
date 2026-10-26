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
from agentoven import flow
from agentoven.flow import Flow, FlowError
from agentoven.client import AgentOvenClient, AgentOvenAPIError, AudioModalityError, RealtimeToken
from agentoven.media import Attachment, AudioClip, AudioInput, InvokeResult
from agentoven.modalities import (
    CLEAR,
    AgentCard,
    AudioConfig,
    Modalities,
    ModalitiesError,
    ModalityConfig,
    Provider,
    ProviderUpdate,
    RealtimeConfig,
    validate_modalities,
)
from agentoven.realtime import RealtimeError, RealtimeEvent, RealtimeSession

__all__ = [
    # Full client (OSS + Pro REST coverage) — use this in new code
    "AgentOvenClient",
    "AgentOvenAPIError",
    "AudioModalityError",
    "RealtimeToken",
    "RealtimeError",
    "RealtimeEvent",
    "RealtimeSession",
    # Recipes built the way the dashboard builds them (see agentoven.flow)
    "Flow",
    "FlowError",
    "flow",
    # Modalities, media and voice
    "AgentCard",
    "Attachment",
    "AudioClip",
    "AudioConfig",
    "AudioInput",
    "CLEAR",
    "InvokeResult",
    "Modalities",
    "ModalitiesError",
    "ModalityConfig",
    "Provider",
    "ProviderUpdate",
    "RealtimeConfig",
    "validate_modalities",
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

__version__ = "0.9.5"

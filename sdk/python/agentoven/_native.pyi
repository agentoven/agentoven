"""
Type stubs for agentoven._native (PyO3-generated module).

These stubs provide IDE auto-completion and type checking for
the Rust-backed native extension.
"""

from typing import Optional

class AgentStatus:
    Draft: "AgentStatus"
    Baking: "AgentStatus"
    Ready: "AgentStatus"
    Cooled: "AgentStatus"
    Burnt: "AgentStatus"
    Retired: "AgentStatus"

class Agent:
    name: str
    description: str
    framework: str
    version: str
    status: AgentStatus

    def __init__(
        self,
        name: str,
        description: str = "",
        framework: str = "custom",
        version: str = "0.1.0",
    ) -> None: ...

class IngredientKind:
    Model: "IngredientKind"
    Tool: "IngredientKind"
    Prompt: "IngredientKind"
    Data: "IngredientKind"

class Ingredient:
    name: str
    kind: IngredientKind
    required: bool

    def __init__(
        self,
        name: str,
        kind: IngredientKind,
        required: bool = True,
    ) -> None: ...

class Recipe:
    name: str
    description: str

    def __init__(
        self,
        name: str,
        description: str = "",
    ) -> None: ...

class AgentOvenClient:
    def __init__(
        self,
        url: str = "http://localhost:8080",
        api_key: Optional[str] = None,
        kitchen: str = "default",
    ) -> None: ...
    def register_agent(self, agent: Agent) -> str: ...
    def list_agents(self) -> list[Agent]: ...
    def bake(self, agent_name: str) -> str: ...
    def cool(self, agent_name: str) -> str: ...

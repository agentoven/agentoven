"""
Type stubs for agentoven._native (PyO3-generated module).

These stubs provide IDE auto-completion and type checking for
the Rust-backed native extension.
"""

from typing import Any, Optional

class AgentStatus:
    Draft: "AgentStatus"
    Baking: "AgentStatus"
    Ready: "AgentStatus"
    Cooled: "AgentStatus"
    Burnt: "AgentStatus"
    Retired: "AgentStatus"

class IngredientKind:
    Model: "IngredientKind"
    Tool: "IngredientKind"
    Prompt: "IngredientKind"
    Data: "IngredientKind"

class Ingredient:
    name: str
    kind: IngredientKind
    provider: Optional[str]
    role: Optional[str]
    protocol: Optional[str]
    required: bool
    config: Optional[str]

    def __init__(
        self,
        name: str,
        kind: IngredientKind,
        required: bool = True,
        provider: Optional[str] = None,
        role: Optional[str] = None,
        protocol: Optional[str] = None,
        config: Optional[Any] = None,
    ) -> None: ...

    @staticmethod
    def model(name: str, provider: Optional[str] = None, role: Optional[str] = None, config: Optional[Any] = None) -> "Ingredient": ...
    @staticmethod
    def tool(name: str, protocol: Optional[str] = None, provider: Optional[str] = None, config: Optional[Any] = None) -> "Ingredient": ...
    @staticmethod
    def prompt(name: str, text: Optional[str] = None, config: Optional[Any] = None) -> "Ingredient": ...
    @staticmethod
    def data(name: str, provider: Optional[str] = None, config: Optional[Any] = None) -> "Ingredient": ...

class Step:
    name: str
    kind: str
    agent: Optional[str]
    parallel: bool
    timeout: Optional[str]
    human_gate: bool
    notify: list[str]
    depends_on: list[str]

    def __init__(
        self,
        name: str,
        kind: str = "agent",
        agent: Optional[str] = None,
        parallel: bool = False,
        timeout: Optional[str] = None,
        human_gate: bool = False,
        notify: list[str] = [],
        depends_on: list[str] = [],
    ) -> None: ...

class Agent:
    name: str
    description: str
    framework: str
    version: str
    model_provider: str
    model_name: str
    mode: str
    system_prompt: Optional[str]
    ingredients: list[Ingredient]
    status: AgentStatus

    def __init__(
        self,
        name: str,
        description: str = "",
        framework: str = "custom",
        version: str = "0.1.0",
        model_provider: str = "",
        model_name: str = "",
        mode: str = "managed",
        system_prompt: Optional[str] = None,
        ingredients: list[Ingredient] = [],
    ) -> None: ...
    def add_ingredient(self, ingredient: Ingredient) -> None: ...

class Recipe:
    name: str
    description: str
    version: str
    steps: list[Step]

    def __init__(
        self,
        name: str,
        description: str = "",
        version: str = "0.1.0",
        steps: list[Step] = [],
    ) -> None: ...

class AgentOvenClient:
    def __init__(
        self,
        url: str = "http://localhost:8080",
        api_key: Optional[str] = None,
        kitchen: str = "default",
    ) -> None: ...

    # Agent operations
    def register(self, agent: Agent) -> str: ...
    def register_agent(self, agent: Agent) -> str: ...
    def get_agent(self, name: str) -> Agent: ...
    def list_agents(self) -> list[Agent]: ...
    def delete(self, target: Any) -> str: ...

    # Lifecycle
    def bake(self, target: Any, version: Optional[str] = None, environment: Optional[str] = None, input: Optional[str] = None) -> str: ...
    def cool(self, target: Any) -> str: ...
    def rewarm(self, target: Any) -> str: ...

    # Provider
    def register_provider(self, name: str, kind: str, api_key: Optional[str] = None, endpoint: Optional[str] = None, models: list[str] = []) -> str: ...

    # Recipe operations
    def create_recipe(self, recipe: Recipe) -> str: ...
    def bake_recipe(self, name: str, input: Optional[Any] = None) -> str: ...
    def list_recipes(self) -> str: ...
    def get_recipe_runs(self, name: str) -> str: ...
    def get_recipe_run(self, recipe_name: str, run_id: str) -> str: ...

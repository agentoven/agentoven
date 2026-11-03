"""
agentoven.refcheck: check that what an agent or a recipe names exists in the kitchen that holds it.

An agent's configuration is full of names: the provider and model it runs on, the model that summarizes its
history, the tools, prompts and skills it uses, the agents it may delegate to. A recipe names agents, steps,
channels, credentials, approver roles. A typo in any of them used to surface much later (at bake, or on the first
run) as a string to decode. This module reads a configuration against a :class:`Catalog` (a snapshot of what the
kitchen has) and returns every problem at once, each with the field it is in, the value that is wrong and the
nearest values that are right ("did you mean").

It is the Python twin of the control plane's ``pkg/refcheck``: the same rules, pinned to it by
``sdk/fixtures/refcheck-cases.json``, which the tests of both run. The check is pure (no network, no clock)
except :func:`load_catalog`, which reads the kitchen's lists through a client.

The control plane stays the authority: it refuses a bad write with HTTP 422 (``ConfigurationError``) whether or
not this check ran. Running it first (``validate=True`` on the client's write calls, or :func:`validate_agent`)
only tells you sooner, and it never needs authentication that the server does not ask for.

Usage::

    from agentoven import refcheck

    catalog = refcheck.load_catalog(client)
    problems = refcheck.check_agent({"name": "a1", "model_provider": "antropic"}, catalog)
    for p in problems:
        print(p.severity, p.message)       # model_provider: "antropic" is not a provider in this kitchen (did you mean "anthropic"?)
"""

from __future__ import annotations

import json
import math
import unicodedata
from dataclasses import dataclass, field
from typing import Any, Dict, Iterable, List, Mapping, Optional, Sequence, Tuple

__all__ = [
    "Catalog",
    "CatalogAgent",
    "CatalogProvider",
    "CatalogSkill",
    "CatalogTool",
    "Names",
    "Problem",
    "apply_edit",
    "blocking",
    "check_agent",
    "check_recipe",
    "format_problems",
    "introduced",
    "load_catalog",
    "nearest",
    "split",
    "with_draft",
]

SEVERITY_ERROR = "error"
SEVERITY_WARNING = "warning"

# Codes say what is wrong. They are stable: clients switch on them.
CODE_NOT_FOUND = "not_found"
CODE_NOT_USABLE = "not_usable"
CODE_UNLISTED = "unlisted"
CODE_UNKNOWN_FIELD = "unknown_field"
CODE_WRONG_TYPE = "wrong_type"
CODE_OUT_OF_RANGE = "out_of_range"
CODE_DUPLICATE = "duplicate"
CODE_REQUIRED = "required"
CODE_INVALID_VALUE = "invalid_value"

# Kinds say what a reference should have pointed at.
KIND_PROVIDER = "provider"
KIND_MODEL = "model"
KIND_PROMPT = "prompt"
KIND_TOOL = "tool"
KIND_SKILL = "skill"
KIND_AGENT = "agent"
KIND_RECIPE = "recipe"
KIND_SCENARIO = "scenario"
KIND_CHANNEL = "channel"
KIND_ENVIRONMENT = "environment"
KIND_CREDENTIAL = "credential"
KIND_ROLE = "role"
KIND_STEP = "step"
KIND_INGREDIENT_KIND = "ingredient_kind"
KIND_STEP_KIND = "step_kind"
KIND_EMBEDDING = "embedding"
KIND_VECTORSTORE = "vectorstore"
KIND_BACKEND = "vectorstore_backend"

#: Provider kinds that serve models their list does not name (OpenRouter and LiteLLM route to whatever the
#: account can reach, Ollama serves whatever was pulled). For every other kind the list is the whole truth,
#: unless it is empty (a provider registered without one accepts any id).
OPEN_MODEL_KINDS = frozenset({"openrouter", "litellm", "ollama"})

_MAX_SUGGESTIONS = 3

# Limits of the numeric settings: generous, they refuse a value that cannot be meant, not ration anything.
_MAX_TURNS = 1000
_MIN_CONTEXT_BUDGET = 256
_MAX_CONTEXT_BUDGET = 10_000_000
_MAX_NAME_LENGTH = 128
_MAX_TOP_K = 1000
_MAX_EMBEDDING_DIMS = 65536
_MAX_EMBEDDING_BATCH = 100_000
_MAX_SCENARIO_ROLLOUT = 1000
_MAX_STEP_RETRIES = 20
_MAX_STEP_TIMEOUT = 7 * 24 * 3600
_MAX_STEP_ITERATIONS = 10_000
_MAX_STEP_CONCURRENT = 1000

_AGENT_MODES = ["managed", "external"]
_AGENT_RUNTIMES = ["agentoven", "langchain", "langgraph", "deepagents", "crewai", "custom", "agenticbot"]
_EXEC_MODES = ["local", "docker", "k8s"]
_BEHAVIORS = ["reactive", "agentic"]
_STRATEGIES = ["react", "plan-and-execute", "reflexion"]
_INGREDIENT_KINDS = ["model", "tool", "prompt", "data", "observability", "embedding", "vectorstore", "retriever", "scenario", "skill"]
_VECTOR_BACKENDS = ["embedded", "pgvector", "pinecone", "qdrant", "cosmosdb", "chroma", "snowflake", "databricks"]
_GUARDRAIL_KINDS = ["content_filter", "pii_detection", "topic_restriction", "max_length", "regex_filter", "prompt_injection", "llamaguard", "custom"]
_GUARDRAIL_STAGES = ["input", "output", "both", "retrieval", "all"]
_GUARDRAIL_ACTIONS = ["block", "redact", "mask", "warn", "require_approval"]
_STEP_KINDS = ["agent", "human_gate", "evaluator", "condition", "fan_out", "fan_in", "rag", "router", "map", "sub_recipe", "wait", "notify"]

#: The parts of a catalog a client may fail to read (a list the caller may not see, a route the edition lacks).
#: ``scenarios`` and ``roles`` carry their own ``known`` flag instead.
CATALOG_PARTS = ("providers", "prompts", "tools", "skills", "agents", "recipes", "channels", "environments", "credentials")

# which catalog part a kind of reference is looked up in
_PART_OF_KIND = {
    KIND_RECIPE: "recipes",
    KIND_CHANNEL: "channels",
    KIND_ENVIRONMENT: "environments",
    KIND_CREDENTIAL: "credentials",
}


# ── Problems ─────────────────────────────────────────────────────────────


@dataclass
class Problem:
    """One thing wrong with a configuration (the server's ``Problem``).

    ``path`` is the field in the JSON the API takes (``summary_model``, ``ingredients[2].config.provider``,
    ``steps[1].agent_ref``); ``kind`` is what ``value`` should have named; ``within`` is what it should have been
    found in (the provider whose model list was searched); ``suggestions`` are the nearest valid values.
    Only ``severity == "error"`` refuses a write; ``"warning"`` is advice.
    """

    path: str
    code: str
    message: str = ""
    severity: str = SEVERITY_ERROR
    kind: str = ""
    value: str = ""
    within: str = ""
    suggestions: List[str] = field(default_factory=list)

    @property
    def blocking(self) -> bool:
        """True when the problem refuses a write."""
        return self.severity != SEVERITY_WARNING

    def key(self) -> Tuple[str, str, str, str, str, str]:
        """What identifies a problem across implementations (the fixture's comparison key)."""
        return (self.path, self.code, self.severity, self.kind, self.value, self.within)

    def to_dict(self) -> Dict[str, Any]:
        d: Dict[str, Any] = {"path": self.path, "code": self.code, "severity": self.severity}
        for name in ("kind", "value", "within"):
            if getattr(self, name):
                d[name] = getattr(self, name)
        d["message"] = self.message
        if self.suggestions:
            d["suggestions"] = list(self.suggestions)
        return d

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Problem":
        sugg = d.get("suggestions") or []
        return cls(
            path=str(d.get("path") or ""),
            code=str(d.get("code") or ""),
            message=str(d.get("message") or ""),
            severity=str(d.get("severity") or SEVERITY_ERROR),
            kind=str(d.get("kind") or ""),
            value=str(d.get("value") or ""),
            within=str(d.get("within") or ""),
            suggestions=[str(s) for s in sugg] if isinstance(sugg, (list, tuple)) else [],
        )

    def __str__(self) -> str:
        return self.message or f"{self.path}: {self.code}"


def split(problems: Iterable[Problem]) -> Tuple[List[Problem], List[Problem]]:
    """Separate the problems that refuse a write from the warnings: ``(errors, warnings)``."""
    errs: List[Problem] = []
    warns: List[Problem] = []
    for p in problems:
        (errs if p.blocking else warns).append(p)
    return errs, warns


def blocking(problems: Iterable[Problem]) -> List[Problem]:
    """The problems that refuse a write."""
    return split(problems)[0]


def introduced(before: Iterable[Problem], after: Iterable[Problem]) -> List[Problem]:
    """The blocking problems of ``after`` that were not already in ``before``.

    A write that does not make anything worse is not refused for what the configuration already held (an agent
    saved before the checks existed can still be edited); a bake takes no such leave. Two problems are the same
    when they agree on code, kind, value and within, wherever in the list they sit.
    """
    held: Dict[Tuple[str, str, str, str], int] = {}
    for p in before:
        if p.blocking:
            k = _same_key(p)
            held[k] = held.get(k, 0) + 1
    out: List[Problem] = []
    for p in after:
        if not p.blocking:
            continue
        k = _same_key(p)
        if held.get(k, 0) > 0:
            held[k] -= 1
            continue
        out.append(p)
    return out


def _same_key(p: Problem) -> Tuple[str, str, str, str]:
    return (p.code, p.kind, p.value, p.within)


def format_problems(problems: Iterable[Problem], *, indent: str = "  ") -> str:
    """The problems as lines a person can read: severity, the server's message (which holds the path, the bad
    value and the did-you-mean)."""
    return "\n".join(f"{indent}{p.severity}: {p.message or p.path}" for p in problems)


# ── Did you mean ─────────────────────────────────────────────────────────


def nearest(value: str, candidates: Iterable[str], limit: int = _MAX_SUGGESTIONS) -> List[str]:
    """The candidates closest to ``value``, nearest first, at most ``limit`` of them.

    A candidate is close when the two differ by a few characters (a typo), when they are the same once case and
    punctuation are ignored (``gpt4o`` and ``gpt-4o``), or when one holds the other (``haiku`` in
    ``claude-haiku-4-5``). Distance is optimal-string-alignment (an adjacent swap is one edit) on the lowercased
    text and on the text with everything but letters and digits removed, the smaller of the two; close means at
    most ``clamp(len(squashed) / 4, 1, 3)``. Ties sort by name.
    """
    v = value.lower()
    vn = _squash(v)
    if vn == "":
        return []
    allowed = min(max(len(vn) // 4, 1), 3)
    scored: List[Tuple[int, str]] = []
    seen = set()
    for c in candidates:
        if c in seen:
            continue
        seen.add(c)
        cl = c.lower()
        cn = _squash(cl)
        if cn == "":
            continue
        d = min(_distance(v, cl), _distance(vn, cn))
        if d > allowed and len(vn) >= 3 and len(cn) >= 3 and (cn in vn or vn in cn):
            d = allowed  # contained: close, ranked after the typos that are nearer
        if d <= allowed:
            scored.append((d, c))
    scored.sort(key=lambda t: (t[0], t[1]))
    if limit <= 0 or limit > len(scored):
        limit = len(scored)
    return [name for _, name in scored[:limit]]


def _squash(s: str) -> str:
    """Letters and digits only (Unicode categories L* and Nd, like Go's unicode.IsLetter / IsDigit)."""
    out = []
    for ch in s:
        cat = unicodedata.category(ch)
        if cat[0] == "L" or cat == "Nd":
            out.append(ch)
    return "".join(out)


def _distance(a: str, b: str) -> int:
    """Edit distance with adjacent swaps counted as one edit (optimal string alignment)."""
    la, lb = len(a), len(b)
    d = [[0] * (lb + 1) for _ in range(la + 1)]
    for i in range(la + 1):
        d[i][0] = i
    for j in range(lb + 1):
        d[0][j] = j
    for i in range(1, la + 1):
        for j in range(1, lb + 1):
            cost = 0 if a[i - 1] == b[j - 1] else 1
            d[i][j] = min(d[i][j - 1] + 1, d[i - 1][j] + 1, d[i - 1][j - 1] + cost)
            if i > 1 and j > 1 and a[i - 1] == b[j - 2] and a[i - 2] == b[j - 1]:
                d[i][j] = min(d[i][j], d[i - 2][j - 2] + 1)
    return d[la][lb]


# ── The catalog: what a kitchen has ──────────────────────────────────────


@dataclass
class CatalogProvider:
    """A model provider as the checks see it. ``healthy`` is the result of its last connectivity test (None: never tested)."""

    name: str
    kind: str = ""
    models: List[str] = field(default_factory=list)
    healthy: Optional[bool] = None

    def open(self) -> bool:
        """True when the provider may serve a model id that is not in its list."""
        return self.kind in OPEN_MODEL_KINDS or len(self.models) == 0

    def has(self, model: str) -> bool:
        return model in self.models


@dataclass
class CatalogTool:
    name: str
    enabled: bool = True
    capabilities: List[str] = field(default_factory=list)


@dataclass
class CatalogSkill:
    """``status`` is ``accepted`` for the only state that may run."""

    name: str
    status: str = ""


@dataclass
class CatalogAgent:
    name: str
    status: str = ""


@dataclass
class Names:
    """A set of names an edition may or may not be able to list. The community edition has no scenarios and no
    roles, so ``known`` is False there and a reference to one is not checked rather than refused."""

    known: bool = False
    names: List[str] = field(default_factory=list)


@dataclass
class Catalog:
    """A snapshot of what one kitchen has: the names a configuration may refer to.

    ``unknown`` lists the parts (of :data:`CATALOG_PARTS`) that could not be read, for instance because the caller
    may not list them; references into an unknown part are not checked, never judged against an empty list.
    """

    providers: List[CatalogProvider] = field(default_factory=list)
    #: a prompt's name -> its latest version
    prompts: Dict[str, int] = field(default_factory=dict)
    tools: List[CatalogTool] = field(default_factory=list)
    skills: List[CatalogSkill] = field(default_factory=list)
    agents: List[CatalogAgent] = field(default_factory=list)
    recipes: List[str] = field(default_factory=list)
    channels: List[str] = field(default_factory=list)
    environments: List[str] = field(default_factory=list)
    credentials: List[str] = field(default_factory=list)
    scenarios: Names = field(default_factory=Names)
    roles: Names = field(default_factory=Names)
    unknown: frozenset = frozenset()

    @classmethod
    def from_dict(cls, d: Mapping[str, Any]) -> "Catalog":
        """Build a catalog from the JSON shape of the shared fixture (``catalogs.*`` in refcheck-cases.json)."""

        def names(key: str) -> Names:
            n = d.get(key) or {}
            return Names(bool(n.get("known")), [str(x) for x in (n.get("names") or [])])

        return cls(
            providers=[
                CatalogProvider(p["name"], p.get("kind", ""), list(p.get("models") or []), p.get("healthy"))
                for p in d.get("providers") or []
            ],
            prompts={str(k): int(v) for k, v in (d.get("prompts") or {}).items()},
            tools=[CatalogTool(t["name"], bool(t.get("enabled")), list(t.get("capabilities") or [])) for t in d.get("tools") or []],
            skills=[CatalogSkill(s["name"], s.get("status", "")) for s in d.get("skills") or []],
            agents=[CatalogAgent(a["name"], a.get("status", "")) for a in d.get("agents") or []],
            recipes=[str(x) for x in d.get("recipes") or []],
            channels=[str(x) for x in d.get("channels") or []],
            environments=[str(x) for x in d.get("environments") or []],
            credentials=[str(x) for x in d.get("credentials") or []],
            scenarios=names("scenarios"),
            roles=names("roles"),
            unknown=frozenset(d.get("unknown") or ()),
        )

    # lookups

    def is_unknown(self, part: str) -> bool:
        return part in self.unknown

    def provider(self, name: str) -> Optional[CatalogProvider]:
        return next((p for p in self.providers if p.name == name), None)

    def provider_names(self) -> List[str]:
        return sorted(p.name for p in self.providers)

    def all_models(self) -> List[str]:
        seen = set()
        for p in self.providers:
            seen.update(p.models)
        return sorted(seen)

    def tool(self, name: str) -> Optional[CatalogTool]:
        return next((t for t in self.tools if t.name == name), None)

    def tool_names(self, capability: str) -> List[str]:
        return sorted(t.name for t in self.tools if not capability or capability in t.capabilities)

    def skill(self, name: str) -> Optional[CatalogSkill]:
        return next((s for s in self.skills if s.name == name), None)

    def skill_names(self) -> List[str]:
        return sorted(s.name for s in self.skills)

    def agent(self, name: str) -> Optional[CatalogAgent]:
        return next((a for a in self.agents if a.name == name), None)

    def agent_names(self) -> List[str]:
        # a retired agent is not offered as something to pick
        return sorted(a.name for a in self.agents if a.status != "retired")

    def prompt_names(self) -> List[str]:
        return sorted(self.prompts)


# ── Entry points ─────────────────────────────────────────────────────────


def check_agent(agent: Any, catalog: Catalog, bake: bool = False, environment: str = "") -> List[Problem]:
    """Check an agent's configuration against what the kitchen has.

    ``agent`` is the JSON the API takes (a dict), an :class:`agentoven.agents.AgentBuilder`, or a native
    ``Agent``. It is judged as given: pass the stored agent with its draft applied (:func:`with_draft`) to judge
    what the next bake would run. ``bake=True`` is the check at the moment the agent is about to run, when a
    draft's gaps (a provider with no model, a managed agent with no model at all) are errors and not warnings.
    """
    from agentoven.agents import agent_to_wire

    ck = _Checker(catalog, bake, environment)
    ck.agent(agent_to_wire(agent))
    return ck.out


def check_recipe(recipe: Any, catalog: Catalog, bake: bool = False, environment: str = "") -> List[Problem]:
    """Check a recipe against what the kitchen has: the agents its steps run, the steps they wait for, the
    recipes it nests, the channels it notifies, the credentials it signs in with, the roles that may approve its
    gates, the environment it runs in. ``recipe`` is a dict, a :class:`agentoven.flow.Flow` or a native ``Recipe``.
    ``bake=True`` is the check before a run, ``environment`` the one the run asked for."""
    from agentoven.agents import recipe_to_wire

    ck = _Checker(catalog, bake, environment)
    ck.recipe(recipe_to_wire(recipe))
    return ck.out


# ── The checker ──────────────────────────────────────────────────────────


def _quote(s: str) -> str:
    """A value in a message, quoted the way the server's messages quote it."""
    out = json.dumps(s, ensure_ascii=False)
    # Go's %q writes control characters as \x00; JSON as \u0000
    for i in range(0x20):
        j = "\\u%04x" % i
        if j in out:
            out = out.replace(j, "\\x%02x" % i)
    return out


def _join_quoted(ss: Sequence[str]) -> str:
    return ", ".join(_quote(s) for s in ss)


def _join_or(ss: Sequence[str]) -> str:
    q = [_quote(s) for s in ss]
    if len(q) == 1:
        return q[0]
    return ", ".join(q[:-1]) + " or " + q[-1]


def _is_num(v: Any) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def _trim_float(f: float) -> str:
    if math.isfinite(f) and f == int(f):
        return str(int(f))
    return repr(f)


def _describe(v: Any) -> str:
    if isinstance(v, str):
        return v
    if isinstance(v, float) and math.isfinite(v) and v == int(v):
        v = int(v)
    try:
        b = json.dumps(v, separators=(",", ":"), ensure_ascii=False)
    except (TypeError, ValueError):
        b = str(v)
    return b[:57] + "..." if len(b) > 60 else b


def _json_type_name(v: Any) -> str:
    if v is None:
        return "null"
    if isinstance(v, str):
        return "a string"
    if isinstance(v, bool):
        return "a boolean"
    if isinstance(v, Mapping):
        return "an object"
    if isinstance(v, (list, tuple)):
        return "a list"
    if _is_num(v):
        return "a number"
    return "another type"


def _contains(items: Sequence[str], s: str) -> bool:
    return s in items


def _contains_fold(items: Sequence[str], s: str) -> bool:
    want = s.strip().casefold()
    return any(x.strip().casefold() == want for x in items)


def _uniq(ss: Iterable[str]) -> List[str]:
    seen = set()
    out = []
    for s in ss:
        if s and s not in seen:
            seen.add(s)
            out.append(s)
    return out


def _has_control(s: str) -> bool:
    return any(ord(ch) < 0x20 or ord(ch) == 0x7F for ch in s)


class _Checker:
    def __init__(self, catalog: Catalog, bake: bool, environment: str) -> None:
        self.c = catalog
        self.bake = bake
        self.environment = environment
        self.out: List[Problem] = []

    # ── collecting ──

    def add(self, p: Problem) -> None:
        self.out.append(p)

    def gap_severity(self) -> str:
        """A gap that only matters when the configuration runs: a warning on a write, an error at a bake."""
        return SEVERITY_ERROR if self.bake else SEVERITY_WARNING

    def required(self, path: str, what: str) -> None:
        self.add(Problem(path, CODE_REQUIRED, f"{path}: {what}"))

    def invalid(self, path: str, value: str, message: str, suggestions: Optional[List[str]] = None) -> None:
        self.add(Problem(path, CODE_INVALID_VALUE, f"{path}: {message}", value=value, suggestions=suggestions or []))

    def wrong_type(self, path: str, want: str, got: Any) -> None:
        self.add(Problem(path, CODE_WRONG_TYPE, f"{path}: must be {want}, not {_json_type_name(got)}", value=_describe(got)))

    def out_of_range(self, path: str, got: float, lo: float, hi: float, whole: bool) -> None:
        what = "a whole number" if whole else "a number"
        self.add(Problem(path, CODE_OUT_OF_RANGE,
                         f"{path}: {_trim_float(got)} must be {what} from {_trim_float(lo)} to {_trim_float(hi)}",
                         value=_trim_float(got)))

    def not_found(self, path: str, kind: str, value: str, within: str, candidates: Sequence[str]) -> None:
        sugg = nearest(value, candidates, _MAX_SUGGESTIONS)
        what = f"a {kind} in this kitchen"
        if kind == KIND_MODEL and within:
            what = "a model of provider " + _quote(within)
        elif kind == KIND_MODEL:
            what = "a model of any provider in this kitchen"
        elif kind == KIND_AGENT:
            what = "an agent in this kitchen"
        elif kind == KIND_STEP:
            what = "a step of this recipe"
        elif kind == KIND_INGREDIENT_KIND:
            what = "an ingredient kind"
        elif kind == KIND_BACKEND:
            what = "a vector store backend"
        elif kind == KIND_STEP_KIND:
            what = "a step kind"
        elif kind in (KIND_EMBEDDING, KIND_VECTORSTORE):
            what = "one of this agent's " + kind + " ingredients"
        elif kind == KIND_ENVIRONMENT:
            what = "an environment of this kitchen"
        msg = f"{path}: {_quote(value)} is not {what}"
        if sugg:
            msg += f" (did you mean {_join_or(sugg)}?)"
        elif len(candidates) == 0:
            msg += " (there are none)"
        elif len(candidates) <= 8:
            msg += " (available: " + _join_quoted(candidates) + ")"
        else:
            msg += f" ({len(candidates)} available)"
        self.add(Problem(path, CODE_NOT_FOUND, msg, kind=kind, value=value, within=within, suggestions=sugg))

    def not_usable(self, path: str, kind: str, value: str, why: str, severity: str) -> None:
        self.add(Problem(path, CODE_NOT_USABLE, f"{path}: {kind} {_quote(value)} {why}", severity=severity, kind=kind, value=value))

    def dupes(self, path: str, values: Sequence[Optional[str]]) -> None:
        """Report the second and later occurrence of each value of a list."""
        seen = set()
        for i, v in enumerate(values):
            if v is None:
                continue
            if v in seen:
                self.add(Problem(f"{path}[{i}]", CODE_DUPLICATE, f"{path}[{i}]: {_quote(v)} is listed more than once", value=v))
            seen.add(v)

    def enum(self, path: str, value: str, allowed: Sequence[str]) -> None:
        """A value against a fixed set. An empty value is the default and passes."""
        if value == "" or value in allowed:
            return
        sugg = nearest(value, allowed, _MAX_SUGGESTIONS)
        if sugg:
            msg = f"{_quote(value)} is not valid (did you mean {_join_or(sugg)}?)"
        else:
            msg = f"{_quote(value)} is not valid: must be one of {_join_quoted(allowed)}"
        self.invalid(path, value, msg, sugg)

    def from_set(self, path: str, kind: str, name: str, values: Sequence[str], part: str = "") -> None:
        if part and self.c.is_unknown(part):
            return
        if name not in values:
            self.not_found(path, kind, name, "", sorted(values))

    # ── reading values from a JSON object ──

    def read_str(self, path: str, m: Mapping[str, Any], key: str) -> str:
        """A string setting; a value of another type is a problem and reads as absent."""
        v = m.get(key)
        if v is None:
            return ""
        if not isinstance(v, str):
            self.wrong_type(path, "a string", v)
            return ""
        return v

    def read_int(self, path: str, m: Mapping[str, Any], key: str, lo: int, hi: int) -> Optional[int]:
        """A whole-number setting within [lo, hi]; None when it is absent or wrong (a problem is recorded)."""
        v = m.get(key)
        if v is None:
            return None
        if not _is_num(v) or not math.isfinite(v) or v != int(v):
            self.wrong_type(path, "a whole number", v)
            return None
        n = int(v)
        if n < lo or n > hi:
            self.out_of_range(path, n, lo, hi, True)
            return None
        return n

    def read_float(self, path: str, m: Mapping[str, Any], key: str, lo: float, hi: float) -> Optional[float]:
        v = m.get(key)
        if v is None:
            return None
        if not _is_num(v) or not math.isfinite(v):
            self.wrong_type(path, "a number", v)
            return None
        if v < lo or v > hi:
            self.out_of_range(path, v, lo, hi, False)
            return None
        return float(v)

    def read_strs(self, path: str, m: Mapping[str, Any], key: str) -> List[Optional[str]]:
        """A list of names. An entry that is not a string is a problem and reads as None (it keeps its index)."""
        v = m.get(key)
        if v is None:
            return []
        if not isinstance(v, (list, tuple)):
            self.wrong_type(path, "a list", v)
            return []
        out: List[Optional[str]] = []
        for i, x in enumerate(v):
            if isinstance(x, str):
                out.append(x)
            else:
                self.wrong_type(f"{path}[{i}]", "a string", x)
                out.append(None)
        return out

    # ── references ──

    def provider(self, path: str, name: str) -> Tuple[Optional[CatalogProvider], bool]:
        """Check a provider name; returns it when it exists."""
        if self.c.is_unknown("providers"):
            return None, False
        p = self.c.provider(name)
        if p is None:
            self.not_found(path, KIND_PROVIDER, name, "", self.c.provider_names())
            return None, False
        if p.healthy is not None and not p.healthy:
            self.not_usable(path, KIND_PROVIDER, name, "failed its last connectivity test", SEVERITY_WARNING)
        return p, True

    def model(self, path: str, p: CatalogProvider, model: str) -> None:
        """A model id against a provider that exists."""
        if p.has(model):
            return
        if not p.open():
            self.not_found(path, KIND_MODEL, model, p.name, sorted(p.models))
        elif len(p.models) > 0:
            self.add(Problem(path, CODE_UNLISTED, f"{path}: {_quote(model)} is not in provider {_quote(p.name)}'s list; "
                             f"accepted because a {p.kind} provider serves models its list does not name",
                             severity=SEVERITY_WARNING, kind=KIND_MODEL, value=model, within=p.name))

    def model_pair(self, provider_path: str, model_path: str, provider: str, model: str,
                   provider_required: bool, model_required: bool) -> None:
        """A provider/model pair. ``provider_required`` says whether a model without a provider is a problem (the
        primary model) or is looked up among every provider's models (the summarizer, whose provider is
        optional); ``model_required`` whether a provider without a model is a gap."""
        if provider == "" and model == "":
            return
        if self.c.is_unknown("providers"):
            return
        if provider == "":
            if provider_required:
                # Nothing reads a model name without its provider (an external agent may carry one), so this is a
                # note, not a refusal; a managed agent with no model at all is refused at its bake.
                self.add(Problem(provider_path, CODE_REQUIRED, f"{provider_path}: {model_path} has no effect without the provider that serves it",
                                 severity=SEVERITY_WARNING))
                return
            if any(p.has(model) for p in self.c.providers):
                return
            if any(p.open() for p in self.c.providers):
                return  # some provider serves any id; the model cannot be shown absent
            self.not_found(model_path, KIND_MODEL, model, "", self.c.all_models())
            return
        p, ok = self.provider(provider_path, provider)
        if not ok or p is None:
            return
        if model == "":
            if model_required:
                self.add(Problem(model_path, CODE_REQUIRED, f"{model_path}: provider {_quote(provider)} is set and needs a model",
                                 severity=self.gap_severity(), within=provider))
            return
        self.model(model_path, p, model)

    def tool(self, path: str, name: str, needs: str, required: bool) -> None:
        """A tool name. ``needs`` is the capability the use requires ("tool" to call it, "notify" to send to it);
        ``required`` says whether a tool that exists but cannot be used is an error or a warning."""
        if self.c.is_unknown("tools"):
            return
        t = self.c.tool(name)
        if t is None:
            self.not_found(path, KIND_TOOL, name, "", self.c.tool_names(needs))
            return
        sev = SEVERITY_ERROR if required else SEVERITY_WARNING
        if not t.enabled:
            self.not_usable(path, KIND_TOOL, name, "is disabled", sev)
        elif needs and needs not in t.capabilities:
            self.not_usable(path, KIND_TOOL, name, f"does not have the {_quote(needs)} capability", sev)

    def skill(self, path: str, name: str, required: bool) -> None:
        if self.c.is_unknown("skills"):
            return
        s = self.c.skill(name)
        if s is None:
            self.not_found(path, KIND_SKILL, name, "", self.c.skill_names())
            return
        if s.status != "accepted":
            sev = SEVERITY_ERROR if required else SEVERITY_WARNING
            self.not_usable(path, KIND_SKILL, name, f"is not accepted (status: {s.status})", sev)

    def agent_ref(self, path: str, name: str) -> None:
        """An agent a subagent list or a recipe step names."""
        if self.c.is_unknown("agents"):
            return
        a = self.c.agent(name)
        if a is None:
            self.not_found(path, KIND_AGENT, name, "", self.c.agent_names())
            return
        if a.status == "retired":
            self.not_usable(path, KIND_AGENT, name, "is retired: register it again to bring it back", SEVERITY_ERROR)
        elif a.status in ("burnt", "cooled"):
            self.not_usable(path, KIND_AGENT, name, f"is not running (status: {a.status})", SEVERITY_WARNING)

    # ── an agent ──

    def agent(self, a: Mapping[str, Any]) -> None:
        name = self.read_str("name", a, "name")
        if name:
            if len(name.encode("utf-8")) > _MAX_NAME_LENGTH:
                self.invalid("name", name, f"is longer than {_MAX_NAME_LENGTH} characters")
            elif any(ch in name for ch in '/\\?#%') or _has_control(name):
                self.invalid("name", name, 'must not contain "/", "\\", "?", "#", "%" or control characters: it is part of the agent\'s URL')
        mode = self.read_str("mode", a, "mode")
        self.enum("mode", mode, _AGENT_MODES)
        self.enum("runtime", self.read_str("runtime", a, "runtime"), _AGENT_RUNTIMES)
        self.enum("execution_mode", self.read_str("execution_mode", a, "execution_mode"), _EXEC_MODES)
        self.enum("behavior", self.read_str("behavior", a, "behavior"), _BEHAVIORS)
        self.enum("reasoning_strategy", self.read_str("reasoning_strategy", a, "reasoning_strategy"), _STRATEGIES)
        self.read_int("max_turns", a, "max_turns", 0, _MAX_TURNS)
        if a.get("context_budget") not in (None, 0):
            self.read_int("context_budget", a, "context_budget", _MIN_CONTEXT_BUDGET, _MAX_CONTEXT_BUDGET)

        # The model the agent runs on, the model it falls back to, the model that summarizes its history.
        model_provider = self.read_str("model_provider", a, "model_provider")
        model_name = self.read_str("model_name", a, "model_name")
        self.model_pair("model_provider", "model_name", model_provider, model_name, True, True)
        self.model_pair("backup_provider", "backup_model", self.read_str("backup_provider", a, "backup_provider"),
                        self.read_str("backup_model", a, "backup_model"), True, True)
        self.model_pair("summary_provider", "summary_model", self.read_str("summary_provider", a, "summary_provider"),
                        self.read_str("summary_model", a, "summary_model"), False, True)

        skills = self.read_strs("skills", a, "skills")
        for i, s in enumerate(skills):
            if s is not None:
                self.skill(f"skills[{i}]", s, False)
        self.dupes("skills", skills)
        approval = self.read_strs("approval_tools", a, "approval_tools")
        for i, t in enumerate(approval):
            if t is not None:
                self.tool(f"approval_tools[{i}]", t, "tool", False)
        self.dupes("approval_tools", approval)
        subagents = self.read_strs("subagents", a, "subagents")
        for i, s in enumerate(subagents):
            if s is None:
                continue
            path = f"subagents[{i}]"
            if s == name and name != "":
                self.invalid(path, s, "an agent cannot delegate to itself")
                continue
            self.agent_ref(path, s)
        self.dupes("subagents", subagents)

        budget = a.get("budget")
        if isinstance(budget, Mapping):
            self._budget(budget)
        bot = a.get("bot")
        if isinstance(bot, Mapping):
            tools = self.read_strs("bot.tools", bot, "tools")
            for i, t in enumerate(tools):
                if t is not None:
                    self.tool(f"bot.tools[{i}]", t, "tool", False)
            self.dupes("bot.tools", tools)

        guardrails = a.get("guardrails")
        if isinstance(guardrails, (list, tuple)):
            for i, g in enumerate(guardrails):
                if not isinstance(g, Mapping) or not g.get("enabled"):
                    continue  # a rule that is switched off is never evaluated, whatever it says
                path = f"guardrails[{i}]"
                self.enum(path + ".kind", self.read_str(path + ".kind", g, "kind"), _GUARDRAIL_KINDS)
                self.enum(path + ".stage", self.read_str(path + ".stage", g, "stage"), _GUARDRAIL_STAGES)
                self.enum(path + ".action", self.read_str(path + ".action", g, "action"), _GUARDRAIL_ACTIONS)

        ingredients = a.get("ingredients")
        ings = [i for i in ingredients if isinstance(i, Mapping)] if isinstance(ingredients, (list, tuple)) else []
        self.ingredients(ingredients if isinstance(ingredients, (list, tuple)) else [])

        # A managed agent that is about to run needs a model somewhere.
        if self.bake and mode in ("", "managed") and not (model_provider or any(i.get("kind") == "model" for i in ings)):
            self.add(Problem("model_provider", CODE_REQUIRED,
                             "model_provider: a managed agent needs a model: set model_provider and model_name, or add a model ingredient",
                             kind=KIND_MODEL))

    def _budget(self, b: Mapping[str, Any]) -> None:
        cost = b.get("max_cost_usd")
        if _is_num(cost) and cost < 0:
            self.out_of_range("budget.max_cost_usd", cost, 0, 1e9, False)
        for key in ("max_tokens", "max_active_seconds", "max_turns"):
            v = b.get(key)
            if _is_num(v) and v < 0:
                self.out_of_range("budget." + key, v, 0, 1e12, True)
        share = b.get("child_share")
        if _is_num(share) and (share < 0 or share > 1):
            self.out_of_range("budget.child_share", share, 0, 1, False)

    def ingredients(self, items: Sequence[Any]) -> None:
        seen = set()
        kind_count: Dict[str, int] = {}
        embeddings: List[Tuple[str, str, str]] = []  # provider name, provider kind, model
        vectorstores: List[Tuple[str, str]] = []  # backend, index
        retrievers: List[Tuple[str, Mapping[str, Any]]] = []

        for i, ing in enumerate(items):
            path = f"ingredients[{i}]"
            if not isinstance(ing, Mapping):
                self.wrong_type(path, "an object", ing)
                continue
            kind = self.read_str(path + ".kind", ing, "kind")
            if kind not in _INGREDIENT_KINDS:
                if kind == "":
                    self.required(path + ".kind", "an ingredient needs a kind")
                else:
                    self.not_found(path + ".kind", KIND_INGREDIENT_KIND, kind, "", _INGREDIENT_KINDS)
                continue
            kind_count[kind] = kind_count.get(kind, 0) + 1
            name = self.read_str(path + ".name", ing, "name")
            if name:
                key = kind + "\x00" + name
                if key in seen:
                    self.add(Problem(path + ".name", CODE_DUPLICATE, f"{path}.name: there is already a {kind} ingredient named {_quote(name)}", value=name))
                seen.add(key)
            if kind in ("model", "prompt") and kind_count[kind] == 2:
                self.add(Problem(path + ".kind", CODE_DUPLICATE, f"{path}.kind: an agent has one {kind} ingredient; this is the second", value=kind))
            cfg = ing.get("config")
            if cfg is None:
                cfg = {}
            elif not isinstance(cfg, Mapping):
                self.wrong_type(path + ".config", "an object", cfg)
                cfg = {}
            required = bool(ing.get("required"))
            if kind == "model":
                self.model_ingredient(path, cfg)
            elif kind == "tool":
                tool_name = name
                n = self.read_str(path + ".config.tool_name", cfg, "tool_name")
                if n:
                    tool_name = n
                if tool_name == "":
                    self.required(path + ".name", "a tool ingredient needs the name of a tool")
                else:
                    self.tool(_tool_path(path, cfg), tool_name, "tool", required)
            elif kind == "observability":
                tool_name = name
                n = self.read_str(path + ".config.tool_name", cfg, "tool_name")
                if n:
                    tool_name = n
                if tool_name:
                    self.tool(_tool_path(path, cfg), tool_name, "tool", False)
            elif kind == "prompt":
                self.prompt_ingredient(path, name, cfg)
            elif kind == "data":
                uri = cfg.get("uri")
                if uri is not None and not isinstance(uri, str):
                    self.wrong_type(path + ".config.uri", "a string", uri)
                elif (uri or "").strip() == "":
                    self.required(path + ".config.uri", "a data ingredient needs a uri")
            elif kind == "embedding":
                embeddings.append(self.embedding_ingredient(path, cfg))
            elif kind == "vectorstore":
                vectorstores.append(self.vectorstore_ingredient(path, cfg))
            elif kind == "retriever":
                self.retriever_ingredient(path, cfg)
                retrievers.append((path, cfg))
            elif kind == "scenario":
                self.scenario_ingredient(path, name, cfg)
            elif kind == "skill":
                if name == "":
                    self.required(path + ".name", "a skill ingredient needs the name of a skill")
                else:
                    self.skill(path + ".name", name, required)

        # A built-in retriever names an embedding and a vector store among the agent's own ingredients.
        for path, cfg in retrievers:
            provider = _str_of(cfg, "provider")
            if provider not in ("", "built-in"):
                continue  # an external RAG provider keeps its own
            ref = _str_of(cfg, "embedding_ref")
            if ref:
                found = any(pn == ref or k + "/" + m == ref or k == ref for pn, k, m in embeddings)
                if not found:
                    self.not_found(path + ".config.embedding_ref", KIND_EMBEDDING, ref, "", sorted(pn for pn, _, _ in embeddings))
            ref = _str_of(cfg, "vectorstore_ref")
            if ref:
                found = any(backend == ref or index == ref for backend, index in vectorstores)
                if not found:
                    names = _uniq(n for backend, index in vectorstores for n in (backend, index))
                    self.not_found(path + ".config.vectorstore_ref", KIND_VECTORSTORE, ref, "", sorted(names))

    def model_ingredient(self, path: str, cfg: Mapping[str, Any]) -> None:
        cpath = path + ".config"
        provider = self.read_str(cpath + ".provider", cfg, "provider")
        model = self.read_str(cpath + ".model", cfg, "model")
        if provider == "":
            self.add(Problem(cpath + ".provider", CODE_REQUIRED, cpath + ".provider: a model ingredient needs the provider that serves it",
                             severity=self.gap_severity()))
            return
        p, ok = self.provider(cpath + ".provider", provider)
        if not ok and not self.c.is_unknown("providers"):
            return
        if model == "":
            self.add(Problem(cpath + ".model", CODE_REQUIRED, f"{cpath}.model: provider {_quote(provider)} is set and needs a model",
                             severity=self.gap_severity(), within=provider))
            return
        if ok and p is not None:
            self.model(cpath + ".model", p, model)
        self.read_float(cpath + ".temperature", cfg, "temperature", 0, 2)
        self.read_int(cpath + ".max_tokens", cfg, "max_tokens", 1, 10_000_000)

    def prompt_ingredient(self, path: str, ing_name: str, cfg: Mapping[str, Any]) -> None:
        text = self.read_str(path + ".config.text", cfg, "text")
        template = self.read_str(path + ".config.template", cfg, "template")
        if text or template:
            return  # the prompt is written here, there is nothing to look up
        name = ing_name
        n = self.read_str(path + ".config.prompt_name", cfg, "prompt_name")
        if n:
            name = n
        field_path = path + ".name"
        if "prompt_name" in cfg:
            field_path = path + ".config.prompt_name"
        if name == "":
            self.required(path + ".name", "a prompt ingredient needs the prompt's name, or the prompt written in config.text")
            return
        if self.c.is_unknown("prompts"):
            return
        latest = self.c.prompts.get(name)
        if latest is None:
            self.not_found(field_path, KIND_PROMPT, name, "", self.c.prompt_names())
            return
        v = cfg.get("version")
        if v is not None:
            if not _is_num(v) or not math.isfinite(v) or v != int(v):
                self.wrong_type(path + ".config.version", "a whole number", v)
            elif int(v) < 1 or int(v) > latest:
                self.add(Problem(path + ".config.version", CODE_NOT_FOUND,
                                 f"{path}.config.version: prompt {_quote(name)} has versions 1 to {latest}, not {_trim_float(v)}",
                                 kind=KIND_PROMPT, value=_trim_float(v), within=name))

    def embedding_ingredient(self, path: str, cfg: Mapping[str, Any]) -> Tuple[str, str, str]:
        cpath = path + ".config"
        provider = self.read_str(cpath + ".provider", cfg, "provider")
        model = self.read_str(cpath + ".model", cfg, "model")
        kind = ""
        self.read_int(cpath + ".dimensions", cfg, "dimensions", 1, _MAX_EMBEDDING_DIMS)
        self.read_int(cpath + ".batch_size", cfg, "batch_size", 1, _MAX_EMBEDDING_BATCH)
        if provider == "":
            self.add(Problem(cpath + ".provider", CODE_REQUIRED, cpath + ".provider: an embedding ingredient needs the provider that serves it"))
            return provider, kind, model
        p, ok = self.provider(cpath + ".provider", provider)
        if ok and p is not None:
            kind = p.kind
        return provider, kind, model

    def vectorstore_ingredient(self, path: str, cfg: Mapping[str, Any]) -> Tuple[str, str]:
        cpath = path + ".config"
        backend = self.read_str(cpath + ".backend", cfg, "backend")
        index = self.read_str(cpath + ".index", cfg, "index")
        self.read_int(cpath + ".dimensions", cfg, "dimensions", 1, _MAX_EMBEDDING_DIMS)
        if backend == "":
            backend = "embedded"
        if backend not in _VECTOR_BACKENDS:
            self.not_found(cpath + ".backend", KIND_BACKEND, backend, "", _VECTOR_BACKENDS)
        if index == "" and backend != "embedded":
            self.required(cpath + ".index", "a vector store other than the embedded one needs an index")
        if index == "":
            index = "default"
        return backend, index

    def retriever_ingredient(self, path: str, cfg: Mapping[str, Any]) -> None:
        cpath = path + ".config"
        self.read_int(cpath + ".top_k", cfg, "top_k", 1, _MAX_TOP_K)
        self.read_float(cpath + ".score_threshold", cfg, "score_threshold", 0, 1)
        v = cfg.get("hybrid_search")
        if v is not None and not isinstance(v, bool):
            self.wrong_type(cpath + ".hybrid_search", "true or false", v)

    def scenario_ingredient(self, path: str, ing_name: str, cfg: Mapping[str, Any]) -> None:
        cpath = path + ".config"
        scenario = ing_name
        s = self.read_str(cpath + ".scenario", cfg, "scenario")
        if s:
            scenario = s
        self.read_int(cpath + ".rollouts", cfg, "rollouts", 1, _MAX_SCENARIO_ROLLOUT)
        self.read_float(cpath + ".min_pass_rate", cfg, "min_pass_rate", 0, 1)
        if scenario == "":
            self.required(cpath + ".scenario", "a scenario ingredient needs the id of a scenario")
            return
        if self.c.scenarios.known and scenario not in self.c.scenarios.names:
            field_path = cpath + ".scenario" if "scenario" in cfg else path + ".name"
            self.not_found(field_path, KIND_SCENARIO, scenario, "", sorted(self.c.scenarios.names))

    # ── a recipe ──

    def recipe(self, r: Mapping[str, Any]) -> None:
        default_env = self.read_str("default_environment", r, "default_environment")
        if default_env:
            self.from_set("default_environment", KIND_ENVIRONMENT, default_env, self.c.environments, "environments")
        if self.environment:
            self.from_set("environment", KIND_ENVIRONMENT, self.environment, self.c.environments, "environments")

        raw_steps = r.get("steps")
        steps: List[Mapping[str, Any]] = []
        if isinstance(raw_steps, (list, tuple)):
            for i, s in enumerate(raw_steps):
                if isinstance(s, Mapping):
                    steps.append(s)
                else:
                    self.wrong_type(f"steps[{i}]", "an object", s)
                    steps.append({})
        elif raw_steps is not None:
            self.wrong_type("steps", "a list", raw_steps)

        names: List[str] = []
        index: Dict[str, int] = {}
        for i, s in enumerate(steps):
            path = f"steps[{i}]"
            sname = self.read_str(path + ".name", s, "name")
            if sname == "":
                self.required(path + ".name", "a step needs a name")
            elif sname in index:
                self.add(Problem(path + ".name", CODE_DUPLICATE, f"{path}.name: there is already a step named {_quote(sname)}", value=sname))
            else:
                index[sname] = i
                names.append(sname)
        sorted_names = sorted(names)
        recipe_name = self.read_str("name", r, "name")

        for i, s in enumerate(steps):
            self.step(f"steps[{i}]", s, recipe_name, sorted_names)
        self.cycles(steps, index)

    def step(self, path: str, s: Mapping[str, Any], recipe_name: str, step_names: List[str]) -> None:
        kind = self.read_str(path + ".kind", s, "kind")
        sname = self.read_str(path + ".name", s, "name")
        if kind == "":
            self.required(path + ".kind", "a step needs a kind")
        elif kind not in _STEP_KINDS:
            self.not_found(path + ".kind", KIND_STEP_KIND, kind, "", _STEP_KINDS)

        # The agent a step runs. "agent" is the client-facing alias of "agent_ref": when both are given they
        # must agree, or one of them is the typo.
        agent_ref = self.read_str(path + ".agent_ref", s, "agent_ref")
        agent_alias = self.read_str(path + ".agent", s, "agent")
        ref = agent_ref
        if agent_alias and agent_ref and agent_alias != agent_ref:
            self.invalid(path + ".agent", agent_alias, f"disagrees with agent_ref {_quote(agent_ref)}")
        if ref == "":
            ref = agent_alias
        ref_path = path + ".agent_ref" if agent_ref else path + ".agent"
        if ref != "":
            self.agent_ref(ref_path, ref)
        elif kind in ("agent", "map"):
            self.required(path + ".agent_ref", f"a {kind} step needs the agent to run")

        if kind == "sub_recipe":
            recipe_ref = self.read_str(path + ".recipe_ref", s, "recipe_ref")
            if recipe_ref == "":
                self.required(path + ".recipe_ref", "a sub_recipe step needs the recipe to run")
            elif recipe_ref == recipe_name and recipe_name != "":
                # A recipe nesting itself exists once it is saved, and the server refuses the loop with the path
                # it makes; it is not "missing" here.
                pass
            else:
                self.from_set(path + ".recipe_ref", KIND_RECIPE, recipe_ref, self.c.recipes, "recipes")
        if kind == "map" and self.read_str(path + ".source_path", s, "source_path") == "":
            self.required(path + ".source_path", "a map step needs the path of the list it runs over")

        depends = self.read_strs(path + ".depends_on", s, "depends_on")
        for j, dep in enumerate(depends):
            if dep is None:
                continue
            p = f"{path}.depends_on[{j}]"
            if dep == sname and sname != "":
                self.invalid(p, dep, "a step cannot wait for itself")
            elif dep not in step_names:
                self.not_found(p, KIND_STEP, dep, "", step_names)
        self.dupes(path + ".depends_on", depends)
        branches = s.get("branches")
        if isinstance(branches, (list, tuple)):
            for j, b in enumerate(branches):
                nxt = self.read_str(f"{path}.branches[{j}].next_step", b, "next_step") if isinstance(b, Mapping) else ""
                if nxt and nxt not in step_names:
                    self.not_found(f"{path}.branches[{j}].next_step", KIND_STEP, nxt, "", step_names)
        default_next = self.read_str(path + ".default_next", s, "default_next")
        if default_next and default_next not in step_names:
            self.not_found(path + ".default_next", KIND_STEP, default_next, "", step_names)
        config = s.get("config")
        config = config if isinstance(config, Mapping) else {}
        qf = _str_of(config, "question_from")
        if qf and qf not in step_names:
            self.not_found(path + ".config.question_from", KIND_STEP, qf, "", step_names)

        self.read_int(path + ".max_retries", s, "max_retries", 0, _MAX_STEP_RETRIES)
        self.read_int(path + ".timeout_secs", s, "timeout_secs", 0, _MAX_STEP_TIMEOUT)
        max_iterations = self.read_int(path + ".max_iterations", s, "max_iterations", 0, _MAX_STEP_ITERATIONS)
        self.read_int(path + ".max_concurrency", s, "max_concurrency", 0, _MAX_STEP_CONCURRENT)
        if self.read_str(path + ".loop_condition", s, "loop_condition") != "" and not (max_iterations or 0) > 0:
            self.required(path + ".max_iterations", "a loop_condition needs max_iterations, the hard cap on the loop")

        # Who is told, and who may decide.
        notify = self.read_strs(path + ".notify_tools", s, "notify_tools")
        for j, t in enumerate(notify):
            if t is not None:
                self.tool(f"{path}.notify_tools[{j}]", t, "notify", True)
        self.dupes(path + ".notify_tools", notify)
        if kind == "notify":
            self.channels(path, config)
        auth_key_ref = self.read_str(path + ".auth_key_ref", s, "auth_key_ref")
        if auth_key_ref:
            self.from_set(path + ".auth_key_ref", KIND_CREDENTIAL, auth_key_ref, self.c.credentials, "credentials")
        roles = self.read_strs(path + ".approver_roles", s, "approver_roles")
        for j, role in enumerate(roles):
            if role is None:
                continue
            if self.c.roles.known and not _contains_fold(self.c.roles.names, role) and role.casefold() != "admin":
                self.not_found(f"{path}.approver_roles[{j}]", KIND_ROLE, role, "", sorted(self.c.roles.names))
        self.dupes(path + ".approver_roles", [None if r is None else r.strip().lower() for r in roles])
        emails = self.read_strs(path + ".approver_emails", s, "approver_emails")
        for j, e in enumerate(emails):
            if e is None:
                continue
            at = e.find("@")
            if at < 1 or at == len(e) - 1 or any(ch in e for ch in " ,;"):
                self.invalid(f"{path}.approver_emails[{j}]", e, "is not an e-mail address")
        self.dupes(path + ".approver_emails", emails)
        domain = self.read_str(path + ".approver_domain", s, "approver_domain")
        if "@" in domain:
            self.invalid(path + ".approver_domain", domain, 'is a domain such as "acme.com", without "@"')

    def channels(self, path: str, config: Mapping[str, Any]) -> None:
        """The channels a notify step sends to (config.channels)."""
        raw = config.get("channels")
        if raw is None:
            return
        if not isinstance(raw, (list, tuple)):
            self.wrong_type(path + ".config.channels", "a list of channel names", raw)
            return
        names: List[Optional[str]] = []
        for j, c in enumerate(raw):
            if not isinstance(c, str):
                self.wrong_type(f"{path}.config.channels[{j}]", "a channel name", c)
                names.append(None)
                continue
            names.append(c)
        for j, name in enumerate(names):
            if name is None or name.strip() == "":
                continue
            self.from_set(f"{path}.config.channels[{j}]", KIND_CHANNEL, name.strip(), self.c.channels, "channels")
        self.dupes(path + ".config.channels", names)

    def cycles(self, steps: Sequence[Mapping[str, Any]], index: Mapping[str, int]) -> None:
        """A loop in the steps' dependencies, reported once, at the step where the loop is found."""
        unseen, walking, done = 0, 1, 2
        state = [unseen] * len(steps)
        stack: List[str] = []
        reported = [False]
        deps_of = [[d for d in (s.get("depends_on") or []) if isinstance(d, str)] if isinstance(s.get("depends_on"), (list, tuple)) else [] for s in steps]
        names = [s.get("name") if isinstance(s.get("name"), str) else "" for s in steps]

        def visit(i: int) -> None:
            if reported[0]:
                return
            state[i] = walking
            stack.append(names[i])
            for dep in deps_of[i]:
                j = index.get(dep)
                if j is None or dep == names[i]:
                    continue
                if state[j] == walking:
                    loop = list(stack)
                    while loop and loop[0] != dep:
                        loop.pop(0)
                    loop.append(dep)
                    self.add(Problem(f"steps[{i}].depends_on", CODE_INVALID_VALUE,
                                     f"steps[{i}].depends_on: the steps wait for each other in a circle: {' -> '.join(loop)}", value=dep))
                    reported[0] = True
                    return
                if state[j] == unseen:
                    visit(j)
            stack.pop()
            state[i] = done

        for i in range(len(steps)):
            if state[i] == unseen and names[i] != "":
                visit(i)


def _tool_path(path: str, cfg: Mapping[str, Any]) -> str:
    n = cfg.get("tool_name")
    if isinstance(n, str) and n:
        return path + ".config.tool_name"
    return path + ".name"


def _str_of(m: Mapping[str, Any], key: str) -> str:
    v = m.get(key)
    return v if isinstance(v, str) else ""


# ── Drafts and edits (what a write leaves, what the next bake runs) ──────


def apply_edit(agent: Mapping[str, Any], edit: Mapping[str, Any]) -> Dict[str, Any]:
    """The agent with an edit written onto it, field by field, leaving alone what the edit leaves out.

    This is the server's ``AgentEdit.Apply``: an empty string, a zero and an empty list leave a field as it was;
    ``guardrails``, ``approval_tools`` and ``subagents`` (a list, ``[]`` clears, absent is unchanged) and
    ``summary_provider`` / ``summary_model`` (a string, ``""`` clears) are applied whenever they are present. The
    inputs are not changed.
    """
    out = dict(agent)
    for key in ("description", "framework", "model_provider", "model_name", "backup_provider", "backup_model",
                "mode", "execution_mode", "behavior", "runtime", "reasoning_strategy"):
        if edit.get(key):
            out[key] = edit[key]
    for key in ("max_turns", "context_budget"):
        v = edit.get(key)
        if _is_num(v) and v > 0:
            out[key] = v
    if edit.get("bot") is not None:
        out["bot"] = edit["bot"]
    for key in ("ingredients", "skills"):
        if edit.get(key):
            out[key] = edit[key]
    for key in ("guardrails", "approval_tools", "subagents", "summary_provider", "summary_model"):
        if edit.get(key) is not None:
            out[key] = edit[key]
    return out


def with_draft(agent: Mapping[str, Any]) -> Dict[str, Any]:
    """The stored agent as it will be once its draft is baked: what the next bake would run (the server's
    ``Agent.WithDraft``). An agent without a draft comes back as it is."""
    base = {k: v for k, v in agent.items() if k != "draft"}
    draft = agent.get("draft")
    return apply_edit(base, draft) if isinstance(draft, Mapping) else base


# ── Reading the kitchen ──────────────────────────────────────────────────

# A reply of these statuses means "this caller cannot list that here" (not allowed, or the edition has no such
# route): the part stays unknown. Any other failure (a server error, no connection) is raised: it is not an
# answer, and "nothing exists" must never be made up from it.
_UNREADABLE = (403, 404)


def _read(client: Any, path: str) -> Optional[List[Any]]:
    try:
        data = client._get(path)
    except Exception as exc:  # AgentOvenAPIError, without importing the client module here
        if getattr(exc, "status_code", None) in _UNREADABLE:
            return None
        raise
    if isinstance(data, Mapping):  # {"skills": [...]} and the like
        for v in data.values():
            if isinstance(v, list):
                return v
        return []
    return data if isinstance(data, list) else []


def _dicts(items: Optional[List[Any]]) -> List[Mapping[str, Any]]:
    return [i for i in items or [] if isinstance(i, Mapping)]


def load_catalog(client: Any) -> Catalog:
    """Read what the client's kitchen has, through the lists the API already serves.

    A list the caller may not read (HTTP 403 or 404, a route the edition does not have) leaves that part of the
    catalog unknown (:attr:`Catalog.unknown`): its references are not checked, never judged against an empty
    list. Scenarios are known only when the server lists them (Pro); roles have no list route, so a recipe's
    ``approver_roles`` are not checked from the SDK. Any other failure is raised.
    """
    cat = Catalog()
    unknown = set()

    def part(name: str, path: str) -> List[Mapping[str, Any]]:
        items = _read(client, path)
        if items is None:
            unknown.add(name)
        return _dicts(items)

    for p in part("providers", "/api/v1/models/providers"):
        models = p.get("models")
        healthy = p.get("last_test_healthy")
        cat.providers.append(CatalogProvider(str(p.get("name", "")), str(p.get("kind", "")),
                                             [str(m) for m in models] if isinstance(models, list) else [],
                                             healthy if isinstance(healthy, bool) else None))
    for p in part("prompts", "/api/v1/prompts"):
        name, version = str(p.get("name", "")), p.get("version")
        v = int(version) if _is_num(version) else 1
        cat.prompts[name] = max(cat.prompts.get(name, 0), v)
    for t in part("tools", "/api/v1/tools"):
        caps = t.get("capabilities")
        cat.tools.append(CatalogTool(str(t.get("name", "")), t.get("enabled", True) is not False,
                                     [str(c) for c in caps] if isinstance(caps, list) else ["tool"]))
    for s in part("skills", "/api/v1/skills"):
        cat.skills.append(CatalogSkill(str(s.get("name", "")), str(s.get("status", ""))))
    for a in part("agents", "/api/v1/agents"):
        cat.agents.append(CatalogAgent(str(a.get("name", "")), str(a.get("status", ""))))
    for r in part("recipes", "/api/v1/recipes"):
        if not r.get("archived_at"):  # an archived recipe cannot run
            cat.recipes.append(str(r.get("name", "")))
    for c in part("channels", "/api/v1/channels"):
        cat.channels.append(str(c.get("name", "")))
    for e in part("environments", "/api/v1/environments"):
        cat.environments.append(str(e.get("slug") or e.get("name") or ""))
    for c in part("credentials", "/api/v1/credentials"):
        cat.credentials.append(str(c.get("name", "")))
    scenarios = _read(client, "/api/v1/scenarios")
    if scenarios is not None:
        cat.scenarios = Names(True, [str(s.get("id") or s.get("name") or "") for s in _dicts(scenarios)])
    cat.unknown = frozenset(unknown)
    return cat

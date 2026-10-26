"""
agentoven.flow — build a recipe the way the dashboard's recipe builder does.

A recipe is a list of steps in order. A decision has a yes list and a no list, a person's approval can have
its own approved and rejected lists, and steps can run side by side. ``Flow.to_wire()`` writes the steps the
control plane stores (each step names the steps it waits for), in exactly the shape the dashboard builder
writes, so a recipe made here opens in the builder and one made there can be rebuilt here.

Example::

    from agentoven import AgentOvenClient
    from agentoven.flow import Flow, agent, ask, check, decide, mentions, together

    flow = Flow("refund-review", "Triage a ticket, ask a person about refunds", [
        agent("Triage", "triage-agent"),
        decide("Needs a person?", mentions("refund", "chargeback"),
            yes=[ask("Approve the refund", within="1 day",
                     approved=[agent("Issue the refund", "refunder")],
                     rejected=[agent("Explain the decision", "writer")])],
            no=[together([agent("Reply", "writer")], [check("Tone check", "reviewer")])]),
        agent("Log the outcome", "logger"),
    ])

    client = AgentOvenClient("http://localhost:8080", api_key="...")
    client.create_recipe(flow)
    run = client.run_recipe("refund-review", {"text": "I want a refund"})

Everything here is plain Python: no native module is needed to build or check a flow.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, Iterable, Iterator, List, Optional, Sequence, Union

__all__ = [
    "Flow",
    "FlowError",
    "Node",
    "Problem",
    "agent",
    "ask",
    "check",
    "decide",
    "each",
    "lookup",
    "mentions",
    "notify",
    "sub_recipe",
    "together",
    "wait",
]

#: What a decision tests: the condition step's own result says whether its expression held.
YES_CONDITION = "condition_met == true"
#: What an approval's first path tests: the gate's own result says whether it was approved.
APPROVED_CONDITION = "approved == true"
#: Marks the step standing in for an empty path, so the engine still skips the other path.
CARRY_ON = "carry_on"

#: The longest a wait may last, in seconds.
WAIT_MAX_SECONDS = 30 * 24 * 3600
_WAIT_UNITS = {"s": 1, "m": 60, "h": 3600, "d": 86400}
_WAIT_RE = re.compile(r"^\s*(\d+(?:\.\d+)?)\s*([smhd]?)\s*$", re.IGNORECASE)

#: How long an approval waits, in the words the dashboard uses.
WITHIN = {"1 hour": 60, "4 hours": 240, "1 day": 1440, "3 days": 4320, "no limit": 0}

MAX_LANES = 4


class Problem:
    """One thing that would stop a flow from running."""

    def __init__(self, title: str, message: str) -> None:
        self.title = title
        self.message = message

    def __repr__(self) -> str:
        return f"Problem({self.message!r})"

    def __str__(self) -> str:
        return self.message


class FlowError(ValueError):
    """A flow that cannot run as written. ``problems`` lists every reason."""

    def __init__(self, problems: Sequence[Problem]) -> None:
        self.problems = list(problems)
        super().__init__("; ".join(p.message for p in self.problems))


@dataclass
class Node:
    """One step of a flow, or a group of steps run side by side. Build them with the functions below."""

    kind: str  # agent | evaluator | rag | condition | human_gate | map | sub_recipe | parallel
    title: str = ""
    config: Dict[str, Any] = field(default_factory=dict)
    fields: Dict[str, Any] = field(default_factory=dict)  # step fields outside config: agent_ref, max_retries, ...
    yes: List["Node"] = field(default_factory=list)  # Yes, or Approved
    no: List["Node"] = field(default_factory=list)  # No, or Rejected
    lanes: List[List["Node"]] = field(default_factory=list)
    paths: bool = False  # a person's approval with approved and rejected paths
    name: str = ""

    @property
    def has_paths(self) -> bool:
        return self.kind == "condition" or (self.kind == "human_gate" and self.paths)


def _nodes(items: Optional[Iterable[Node]], what: str) -> List[Node]:
    out = list(items or [])
    for n in out:
        if not isinstance(n, Node):
            raise TypeError(f"{what} takes steps made with agent(), ask(), decide() and the like, got {type(n).__name__}")
    return out


# ── Steps ─────────────────────────────────────────────────────


def _agent_fields(agent_name: str, retries: int, timeout_secs: int, loop_while: Optional[str], max_iterations: int) -> Dict[str, Any]:
    f: Dict[str, Any] = {"agent_ref": agent_name, "agent": agent_name}
    if retries:
        f["max_retries"] = retries
    if timeout_secs:
        f["timeout_secs"] = timeout_secs
    if loop_while:
        f["loop_condition"] = loop_while
        f["max_iterations"] = max_iterations or 3
    return f


def agent(
    title: str,
    agent: str,
    *,
    retries: int = 0,
    timeout_secs: int = 0,
    loop_while: Optional[str] = None,
    max_iterations: int = 0,
) -> Node:
    """Hand a task to one of your agents. It receives what the recipe started with and what earlier steps produced.

    ``retries`` is how many more times to try after a failure. ``loop_while`` repeats the step while the
    expression holds for its output, at most ``max_iterations`` times (3 when not given).
    """
    return Node("agent", title, fields=_agent_fields(agent, retries, timeout_secs, loop_while, max_iterations))


def check(title: str, agent: str, *, retries: int = 0, timeout_secs: int = 0) -> Node:
    """Score a result with an agent before it moves on."""
    return Node("evaluator", title, fields=_agent_fields(agent, retries, timeout_secs, None, 0))


def lookup(title: str, top_k: int = 3) -> Node:
    """Search your knowledge for what the recipe started with, or the step before it."""
    return Node("rag", title, config={"top_k": top_k})


def mentions(*words: str) -> str:
    """The condition "the answer mentions any of these words". Capital letters do not matter."""
    clean = [w.strip().lower().replace('"', "").replace("\\", "") for w in words]
    return " or ".join(f'lower(text) contains "{w}"' for w in clean if w)


def decide(title: str, when: str, *, yes: Optional[Sequence[Node]] = None, no: Optional[Sequence[Node]] = None) -> Node:
    """Take one way or another. ``when`` is a condition: use ``mentions(...)`` or write one.

    In an expression, ``text`` is the words of the step before and its output fields are in scope, for
    example ``output.score >= 4``. An empty path means "carry on" for that answer.
    """
    return Node("condition", title, config={"expression": when}, yes=_nodes(yes, "yes"), no=_nodes(no, "no"))


def ask(
    title: str,
    *,
    within: str = "1 day",
    minutes: Optional[int] = None,
    notify: Optional[str] = None,
    domain: Optional[str] = None,
    emails: Optional[Sequence[str]] = None,
    approved: Optional[Sequence[Node]] = None,
    rejected: Optional[Sequence[Node]] = None,
    if_nobody_answers: str = "stop",
) -> Node:
    """Wait for a person to approve.

    With neither ``approved`` nor ``rejected``, a "no" stops the recipe. Give either (an empty list is fine) and
    each answer follows its own path: ``rejected=[]`` means a rejection ends the recipe as finished, so put
    the steps that should only follow an approval inside ``approved``. ``if_nobody_answers="reject"`` makes a
    timeout follow the Rejected path too.

    ``within`` is one of ``"1 hour"``, ``"4 hours"``, ``"1 day"``, ``"3 days"`` or ``"no limit"``, or pass
    ``minutes``. ``notify`` names a tool that can send a message. ``domain`` and ``emails`` limit who may answer.
    """
    if minutes is None:
        if within not in WITHIN:
            raise ValueError(f"within must be one of {sorted(WITHIN)} (or pass minutes), got {within!r}")
        minutes = WITHIN[within]
    if if_nobody_answers not in ("stop", "reject"):
        raise ValueError('if_nobody_answers must be "stop" or "reject"')
    paths = approved is not None or rejected is not None
    if if_nobody_answers == "reject" and not paths:
        raise ValueError("if_nobody_answers=\"reject\" needs a Rejected path: give approved= or rejected=")
    config: Dict[str, Any] = {"max_wait_minutes": minutes}
    if paths:
        config["on_reject"] = "branch"
        if if_nobody_answers == "reject":
            config["on_timeout"] = "branch"
    fields: Dict[str, Any] = {}
    if notify:
        fields["notify_tools"] = [notify]
    if domain:
        fields["approver_domain"] = domain
    if emails:
        fields["approver_emails"] = list(emails)
    return Node(
        "human_gate", title, config=config, fields=fields, paths=paths,
        yes=_nodes(approved, "approved"), no=_nodes(rejected, "rejected"),
    )


def together(*lanes: Sequence[Node]) -> Node:
    """Start several runs of steps at the same time. The next step waits until every one has finished.

    Each argument is a list of steps (one lane). Two to four lanes.
    """
    if not MAX_LANES >= len(lanes) >= 2:
        raise ValueError(f"together() needs 2 to {MAX_LANES} lanes, got {len(lanes)}")
    return Node("parallel", lanes=[_nodes(lane, "a lane") for lane in lanes])


def _wait_seconds(text: str) -> Optional[float]:
    m = _WAIT_RE.match(text)
    return float(m.group(1)) * _WAIT_UNITS[(m.group(2) or "s").lower()] if m else None


def _wait_text(duration: Union[str, int, float, timedelta]) -> str:
    """A length of time as the engine reads it: "30s", "5m", "2h" or "3d"."""
    if isinstance(duration, timedelta):
        seconds: float = duration.total_seconds()
    elif isinstance(duration, (int, float)) and not isinstance(duration, bool):
        seconds = float(duration)
    else:
        m = _WAIT_RE.match(str(duration))
        if not m:
            raise ValueError(f'a wait must be a length of time like "30s", "5m", "2h" or "3d", got {duration!r}')
        return f"{m.group(1)}{(m.group(2) or 's').lower()}"
    if seconds < 0:
        raise ValueError("a wait cannot be negative")
    for unit, size in (("d", 86400), ("h", 3600), ("m", 60)):
        if seconds >= size and seconds % size == 0:
            return f"{int(seconds // size)}{unit}"
    return f"{seconds:g}s"


def wait(title: str, duration: Union[str, int, float, timedelta, None] = None, *, until: Union[str, datetime, None] = None) -> Node:
    """Pause the recipe for a length of time, or until a time, then carry on.

    ``duration`` is ``"30s"``, ``"5m"``, ``"2h"``, ``"3d"``, a number of seconds, or a ``timedelta``. ``until`` is a
    timezone-aware ``datetime`` or an ISO 8601 text such as ``"2026-11-02T09:00:00Z"``; if that time has
    passed when the step starts, the recipe carries on at once. A wait lasts at most 30 days, can be ended
    early with ``client.release_wait``, and is held in the control plane's memory, so it does not survive a restart.
    """
    if duration is not None and until is not None:
        raise ValueError("give a duration or an until time, not both")
    config: Dict[str, Any] = {}
    if duration is not None:
        config["duration"] = _wait_text(duration)
    if until is not None:
        if isinstance(until, datetime):
            if until.tzinfo is None:
                raise ValueError("until needs a time zone: pass an aware datetime, such as datetime(..., tzinfo=timezone.utc)")
            until = until.astimezone(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")
        else:
            try:
                datetime.fromisoformat(str(until).replace("Z", "+00:00"))
            except ValueError:
                raise ValueError(f'until must be an ISO 8601 time like "2026-11-02T09:00:00Z", got {until!r}') from None
        config["until"] = until
    return Node("wait", title, config=config)


def notify(
    title: str,
    message: str,
    *,
    channels: Optional[Sequence[str]] = None,
    tools: Optional[Sequence[str]] = None,
    subject: Optional[str] = None,
    payload: Optional[Dict[str, Any]] = None,
    on_failure: Optional[str] = None,
) -> Node:
    """Push a message out when the recipe reaches this step, then carry on.

    It is how a recipe tells someone something at a chosen point: that a person is needed just before an
    ``ask(...)`` approval, that a stage is done, that something went the unusual way. ``channels`` are names of
    notification channels (a ``websocket`` channel is a live feed that listeners connect to; see
    ``client.listen``); ``tools`` are tools that can send a message. ``message`` and ``subject`` may use
    ``{{run_id}}``, ``{{recipe}}``, ``{{step}}``, ``{{text}}`` (what the steps before said), ``{{input.field}}`` and
    ``{{step-name.field}}``. ``payload`` adds fields to what listeners receive.

    The step fails if a target cannot be reached, unless ``on_failure="carry_on"``. A live feed with nobody
    connected is not a failure.
    """
    if on_failure not in (None, "stop", "carry_on"):
        raise ValueError('on_failure must be "stop" or "carry_on"')
    config: Dict[str, Any] = {"message": message}
    if subject:
        config["subject"] = subject
    if channels:
        config["channels"] = list(channels)
    if on_failure:
        config["on_failure"] = on_failure
    if payload:
        config["payload"] = dict(payload)
    fields: Dict[str, Any] = {"notify_tools": list(tools)} if tools else {}
    return Node("notify", title, config=config, fields=fields)


def each(title: str, agent: str, over: str, *, max_concurrency: int = 0) -> Node:
    """Run an agent once for each item of a list an earlier step produced. ``over`` is the path to the
    list, such as ``"extract.items"``. Not drawn by the dashboard builder yet: a recipe with one opens read-only there."""
    return Node("map", title, fields={"agent_ref": agent, "agent": agent, "source_path": over, **({"max_concurrency": max_concurrency} if max_concurrency else {})})


def sub_recipe(
    title: str,
    recipe: str,
    *,
    input_mapping: Optional[Dict[str, str]] = None,
    output_mapping: Optional[Dict[str, str]] = None,
) -> Node:
    """Run another recipe as one step. Not drawn by the dashboard builder yet."""
    fields: Dict[str, Any] = {"recipe_ref": recipe}
    if input_mapping:
        fields["input_mapping"] = dict(input_mapping)
    if output_mapping:
        fields["output_mapping"] = dict(output_mapping)
    return Node("sub_recipe", title, fields=fields)


# ── The recipe ────────────────────────────────────────────────


def _slug(text: str) -> str:
    s = re.sub(r"[^a-z0-9]+", "-", text.lower()).strip("-")[:48]
    return s or "step"


def _walk(nodes: Sequence[Node]) -> Iterator[Node]:
    for n in nodes:
        yield n
        yield from _walk(n.yes)
        yield from _walk(n.no)
        for lane in n.lanes:
            yield from _walk(lane)


def _real(nodes: Sequence[Node]) -> List[Node]:
    return [n for n in _walk(nodes) if n.kind != "parallel"]


class Flow:
    """A recipe: a name and the steps in order."""

    def __init__(self, name: str, description: str = "", steps: Optional[Sequence[Node]] = None, version: Optional[str] = None) -> None:
        self.name = name
        self.description = description
        self.version = version
        self.steps: List[Node] = _nodes(steps, "steps")

    def add(self, *nodes: Node) -> "Flow":
        """Append steps. Returns the flow, so calls chain."""
        self.steps.extend(_nodes(nodes, "add"))
        return self

    def validate(self, agents: Optional[Iterable[str]] = None) -> List[Problem]:
        """What would stop this recipe from running. Pass the names of the agents that exist to check those too."""
        known = None if agents is None else set(agents)
        out: List[Problem] = []
        real = _real(self.steps)
        if not real:
            return [Problem("", "Add at least one step.")]
        for g in (n for n in _walk(self.steps) if n.kind == "parallel"):
            if sum(len(_real(lane)) for lane in g.lanes) < 2:
                out.append(Problem("", "Add at least two steps to run side by side."))

        def lists(items: Sequence[Node]) -> Iterator[Sequence[Node]]:
            yield items
            for n in items:
                yield from lists(n.yes)
                yield from lists(n.no)
                for lane in n.lanes:
                    yield from lists(lane)

        for items in lists(self.steps):
            for i, n in enumerate(items):
                if n.kind == "human_gate" and n.paths and not n.no and i < len(items) - 1:
                    out.append(Problem(n.title, f'Steps after "{n.title or "Step"}" would also run after a rejection. Move them under Approved.'))
        for n in real:
            t = n.title or "Step"
            if n.kind in ("agent", "evaluator", "map"):
                ref = n.fields.get("agent_ref", "")
                if not ref:
                    out.append(Problem(n.title, f'"{t}" needs an agent.'))
                elif known is not None and ref not in known:
                    out.append(Problem(n.title, f'"{t}" uses {ref}, which does not exist.'))
            if n.kind == "wait":
                text = str(n.config.get("duration", ""))
                if not text and not n.config.get("until"):
                    out.append(Problem(n.title, f'"{t}" needs a time to wait.'))
                elif text and (_wait_seconds(text) or 0) > WAIT_MAX_SECONDS:
                    out.append(Problem(n.title, f'"{t}" would wait longer than 30 days.'))
            if n.kind == "notify":
                if not str(n.config.get("message", "")).strip():
                    out.append(Problem(n.title, f'"{t}" needs a message.'))
                if not n.config.get("channels") and not n.fields.get("notify_tools"):
                    out.append(Problem(n.title, f'"{t}" needs a channel or a tool to send to.'))
            if n.kind == "map" and not n.fields.get("source_path"):
                out.append(Problem(n.title, f'"{t}" needs the list to go over.'))
            if n.kind == "sub_recipe" and not n.fields.get("recipe_ref"):
                out.append(Problem(n.title, f'"{t}" needs a recipe to run.'))
            if n.kind == "condition" and not str(n.config.get("expression", "")).strip():
                out.append(Problem(n.title, f'"{t}" needs a condition to test.'))
        return out

    def to_steps(self) -> List[Dict[str, Any]]:
        """The steps as the control plane stores them. Does not check the flow; see ``validate``."""
        return _compile(self.steps)

    def to_wire(self, *, agents: Optional[Iterable[str]] = None, validate: bool = True) -> Dict[str, Any]:
        """The body to create the recipe with. Raises ``FlowError`` listing what is wrong, unless ``validate=False``."""
        if validate:
            problems = self.validate(agents)
            if problems:
                raise FlowError(problems)
        body: Dict[str, Any] = {"name": self.name, "description": self.description, "steps": self.to_steps()}
        if self.version:
            body["version"] = self.version
        return body

    def __repr__(self) -> str:
        return f"Flow(name={self.name!r}, steps={len(_real(self.steps))})"


# ── Flow to steps ─────────────────────────────────────────────
# The same algorithm as the dashboard's recipeFlow.ts compile(); sdk/fixtures/recipe-flows.json pins them together.


def _compile(nodes: Sequence[Node]) -> List[Dict[str, Any]]:
    taken: set = set()

    def unique(base: str) -> str:
        stem = _slug(base)
        name, i = stem, 2
        while name in taken:
            name, i = f"{stem}-{i}", i + 1
        return name

    # Names come from titles, in the order the steps are read, and stay distinct.
    for n in _walk(nodes):
        if n.kind != "parallel":
            n.name = unique(n.title)
            taken.add(n.name)

    def carry_on(parent: str, label: str, deps: List[str]) -> Dict[str, Any]:
        name = unique(f"{parent}-{label}")
        taken.add(name)
        return {"name": name, "kind": "fan_out", "depends_on": deps, "config": {"title": "Carry on", "ui": CARRY_ON}}

    def build(items: Sequence[Node], deps: List[str]):
        steps: List[Dict[str, Any]] = []
        tails = deps
        for n in items:
            if n.kind == "parallel":
                # Every lane waits for what came before, and whatever follows waits for the end of every lane.
                ends: List[str] = []
                for lane in n.lanes:
                    lane_steps, lane_tails = build(lane, tails)
                    steps += lane_steps
                    ends += lane_tails if lane_steps else []
                if ends:
                    tails = ends
                continue
            step: Dict[str, Any] = {
                "name": n.name,
                "kind": n.kind,
                "depends_on": list(tails),
                **n.fields,
                "config": {"title": n.title, **n.config},
            }
            steps.append(step)
            if not n.has_paths:
                tails = [n.name]
                continue
            gate = n.kind == "human_gate"
            paths = []
            for label, side in (("yes", n.yes), ("no", n.no)):
                # A path must open with one step: the branch names it, and the engine skips its sibling.
                # So a path that is empty, or opens with steps side by side, starts with a carry-on step.
                opener = None
                if not side or side[0].kind == "parallel":
                    word = ("approved" if label == "yes" else "rejected") if gate else ("then" if label == "yes" else "otherwise")
                    opener = carry_on(n.name, word, [n.name])
                path_steps, path_tails = build(side, [opener["name"] if opener else n.name])
                every = ([opener] if opener else []) + path_steps
                paths.append((every[0]["name"], every, [opener["name"]] if (not path_steps and opener) else path_tails))
            step["branches"] = [{"condition": APPROVED_CONDITION if gate else YES_CONDITION, "next_step": paths[0][0]}]
            step["default_next"] = paths[1][0]
            steps += paths[0][1] + paths[1][1]
            tails = paths[0][2] + paths[1][2]
        return steps, tails

    return build(nodes, [])[0]

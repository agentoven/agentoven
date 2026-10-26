// The recipe builder works on a simple shape: a list of steps in order, where a "decide" step has a Yes list
// and a No list. The engine runs a graph (each step names the steps it waits for). This module converts
// between the two and checks a flow before it is saved. Everything here is pure, so it is tested without a DOM.

export interface WireBranch { condition: string; next_step: string }

/** A step as the control plane stores it. Fields the builder does not manage are kept untouched. */
export interface WireStep {
  name: string;
  kind: string;
  agent_ref?: string;
  agent?: string;
  depends_on?: string[];
  config?: Record<string, unknown>;
  branches?: WireBranch[];
  default_next?: string;
  [extra: string]: unknown;
}

/** A node of the flow. A node whose step is a condition is a "decide" and carries both paths. */
export interface FlowNode {
  id: string;
  step: WireStep;
  yes?: FlowNode[];
  no?: FlowNode[];
  /** For "do at the same time": each lane is a run of steps. A parallel node is not a step itself. */
  lanes?: FlowNode[][];
}

export type StepType = 'agent' | 'lookup' | 'person' | 'check' | 'decide' | 'parallel';

export const isDecide = (n: FlowNode): boolean => n.step.kind === 'condition';
export const isParallel = (n: FlowNode): boolean => n.step.kind === 'parallel';
export const MAX_LANES = 4;

/** The kinds the builder can draw. Anything else makes a recipe read-only here. */
const SUPPORTED_KINDS = new Set(['agent', 'evaluator', 'rag', 'human_gate', 'condition']);

/** What a decide step tests: the condition step's own result says whether its expression held. */
export const YES_CONDITION = 'condition_met == true';

/** Marks the step standing in for an empty path, so the engine still skips the other path. */
export const CARRY_ON = 'carry_on';

// ── Names and titles ──────────────────────────────────────────

export function slug(text: string): string {
  const s = text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 48);
  return s || 'step';
}

export function uniqueName(base: string, taken: Set<string>): string {
  let name = slug(base);
  let i = 2;
  const stem = name;
  while (taken.has(name)) name = `${stem}-${i++}`;
  return name;
}

export const humanize = (name: string): string => {
  const s = name.replace(/[-_]+/g, ' ').trim();
  return s ? s[0].toUpperCase() + s.slice(1) : name;
};

/** What the builder shows for a step: the title it was given, else its name made readable. */
export function titleOf(step: WireStep): string {
  const t = step.config?.title;
  return typeof t === 'string' && t.trim() ? t : humanize(step.name);
}

// ── Reading a flow ────────────────────────────────────────────

export function allNodes(flow: FlowNode[]): FlowNode[] {
  const out: FlowNode[] = [];
  const walk = (list: FlowNode[]) => { for (const n of list) { out.push(n); walk(n.yes ?? []); walk(n.no ?? []); for (const lane of n.lanes ?? []) walk(lane); } };
  walk(flow);
  return out;
}

/** The nodes that are real steps (a "do at the same time" group is only a way of arranging them). */
export const realNodes = (flow: FlowNode[]): FlowNode[] => allNodes(flow).filter((n) => !isParallel(n));

export function findNode(flow: FlowNode[], id: string): FlowNode | undefined {
  return allNodes(flow).find((n) => n.id === id);
}

export function stepNames(flow: FlowNode[]): Set<string> {
  return new Set(realNodes(flow).map((n) => n.step.name));
}

// ── Building a flow ───────────────────────────────────────────

let counter = 0;
export const newId = (): string => `n${Date.now().toString(36)}${(counter++).toString(36)}`;

export function newStep(type: StepType, flow: FlowNode[], agent = ''): FlowNode {
  const taken = stepNames(flow);
  const make = (title: string, kind: string, extra: Partial<WireStep> = {}, config: Record<string, unknown> = {}): FlowNode => ({
    id: newId(),
    step: { name: uniqueName(title, taken), kind, ...extra, config: { title, ...config } },
    ...(kind === 'condition' ? { yes: [], no: [] } : {}),
  });
  switch (type) {
    case 'parallel': return { id: newId(), step: { name: '', kind: 'parallel' }, lanes: [[], []] };
    case 'agent': return make('Agent step', 'agent', { agent_ref: agent });
    case 'check': return make('Check quality', 'evaluator', { agent_ref: agent });
    case 'lookup': return make('Look something up', 'rag', {}, { top_k: 3 });
    case 'person': return make('Ask a person', 'human_gate', {}, { max_wait_minutes: 1440 });
    case 'decide': return make('Decide', 'condition', {}, { expression: '' });
  }
}

export type Where = { parent: string | null; path: 'yes' | 'no' | number | null; index: number };

const clone = (flow: FlowNode[]): FlowNode[] => JSON.parse(JSON.stringify(flow)) as FlowNode[];

function listAt(flow: FlowNode[], where: Where): FlowNode[] | undefined {
  if (where.parent === null) return flow;
  const parent = findNode(flow, where.parent);
  if (!parent || where.path === null) return undefined;
  if (typeof where.path === 'number') return parent.lanes?.[where.path];
  return where.path === 'yes' ? (parent.yes ??= []) : (parent.no ??= []);
}

export function insertNode(flow: FlowNode[], where: Where, node: FlowNode): FlowNode[] {
  const next = clone(flow);
  const list = listAt(next, where);
  if (!list) return flow;
  list.splice(Math.min(Math.max(where.index, 0), list.length), 0, node);
  return next;
}

export function removeNode(flow: FlowNode[], id: string): FlowNode[] {
  const next = clone(flow);
  const drop = (list: FlowNode[]): boolean => {
    const i = list.findIndex((n) => n.id === id);
    if (i >= 0) { list.splice(i, 1); return true; }
    return list.some((n) => drop(n.yes ?? []) || drop(n.no ?? []) || (n.lanes ?? []).some((lane) => drop(lane)));
  };
  drop(next);
  return next;
}

export function updateStep(flow: FlowNode[], id: string, patch: Partial<WireStep>, config?: Record<string, unknown>): FlowNode[] {
  const next = clone(flow);
  const node = findNode(next, id);
  if (!node) return flow;
  Object.assign(node.step, patch);
  if (config) node.step.config = { ...(node.step.config ?? {}), ...config };
  return next;
}

/** Where a click on a "+" lands: right after the node. */
export function whereAfter(flow: FlowNode[], id: string): Where | undefined {
  const find = (list: FlowNode[], parent: string | null, path: 'yes' | 'no' | number | null): Where | undefined => {
    const i = list.findIndex((n) => n.id === id);
    if (i >= 0) return { parent, path, index: i + 1 };
    for (const n of list) {
      const w = find(n.yes ?? [], n.id, 'yes') ?? find(n.no ?? [], n.id, 'no') ?? (n.lanes ?? []).map((lane, li) => find(lane, n.id, li)).find(Boolean);
      if (w) return w;
    }
    return undefined;
  };
  return find(flow, null, null);
}

/** Adds a lane to, or removes the last lane from, a "do at the same time" group. */
export function resizeLanes(flow: FlowNode[], id: string, count: number): FlowNode[] {
  const next = clone(flow);
  const node = findNode(next, id);
  if (!node || !node.lanes) return flow;
  const n = Math.min(Math.max(count, 2), MAX_LANES);
  while (node.lanes.length < n) node.lanes.push([]);
  node.lanes.length = n;
  return next;
}

// ── Flow to steps ─────────────────────────────────────────────

const MANAGED = ['depends_on', 'branches', 'default_next'] as const;

function base(step: WireStep): WireStep {
  const copy: WireStep = { ...step, config: step.config ? { ...step.config } : undefined };
  for (const k of MANAGED) delete copy[k];
  if (!copy.config) delete copy.config;
  // The engine reads agent_ref; the older alias is kept in step.
  if (copy.agent_ref && !copy.agent) copy.agent = copy.agent_ref;
  return copy;
}

function carryOn(decide: string, label: string, depends: string[], taken: Set<string>): WireStep {
  const name = uniqueName(`${decide}-${label}`, taken);
  taken.add(name);
  return { name, kind: 'fan_out', depends_on: depends, config: { title: 'Carry on', ui: CARRY_ON } };
}

function build(nodes: FlowNode[], deps: string[], taken: Set<string>): { steps: WireStep[]; tails: string[] } {
  const steps: WireStep[] = [];
  let tails = deps;
  for (const node of nodes) {
    if (isParallel(node)) {
      // Every lane waits for what came before, and whatever follows waits for the end of every lane.
      const ends: string[] = [];
      for (const lane of node.lanes ?? []) {
        const built = build(lane, tails, taken);
        steps.push(...built.steps);
        ends.push(...(built.steps.length === 0 ? [] : built.tails));
      }
      if (ends.length > 0) tails = ends;
      continue;
    }
    const head = base(node.step);
    head.depends_on = [...tails];
    steps.push(head);
    if (!isDecide(node)) { tails = [head.name]; continue; }

    const paths: { first: string; steps: WireStep[]; tails: string[] }[] = [];
    for (const [label, list] of [['yes', node.yes ?? []], ['no', node.no ?? []]] as const) {
      // A path must open with one step: the decision's branch names it, and the engine skips its siblings.
      // So a path that is empty, or opens with steps side by side, starts with a carry-on step.
      const needsOpener = list.length === 0 || isParallel(list[0]);
      const opener = needsOpener ? carryOn(head.name, label === 'yes' ? 'then' : 'otherwise', [head.name], taken) : null;
      const built = build(list, [opener ? opener.name : head.name], taken);
      const all = opener ? [opener, ...built.steps] : built.steps;
      paths.push({ first: all[0].name, steps: all, tails: built.steps.length === 0 && opener ? [opener.name] : built.tails });
    }
    head.branches = [{ condition: YES_CONDITION, next_step: paths[0].first }];
    head.default_next = paths[1].first;
    steps.push(...paths[0].steps, ...paths[1].steps);
    tails = [...paths[0].tails, ...paths[1].tails];
  }
  return { steps, tails };
}

export function compile(flow: FlowNode[]): WireStep[] {
  const taken = stepNames(flow);
  return build(flow, [], taken).steps;
}

// ── Steps to flow ─────────────────────────────────────────────

function fail(reason: string): never {
  throw new Error(reason);
}

export type Decompiled = { ok: true; flow: FlowNode[] } | { ok: false; reason: string };

const dependsOf = (s: WireStep): string[] => s.depends_on ?? [];
const sameSet = (a: string[], b: string[]): boolean => a.length === b.length && a.every((x) => b.includes(x));

/**
 * Reads stored steps back into a flow. It only accepts shapes the builder can draw and write back
 * unchanged; for any other shape it says so and the recipe stays read-only here, so nothing is lost.
 */
export function decompile(wire: WireStep[]): Decompiled {
  if (wire.length === 0) return { ok: true, flow: [] };
  const byName = new Map(wire.map((s) => [s.name, s]));
  if (byName.size !== wire.length) return { ok: false, reason: 'Two steps share a name.' };
  for (const s of wire) {
    if (!SUPPORTED_KINDS.has(s.kind) && !(s.kind === 'fan_out' && s.config?.ui === CARRY_ON)) {
      return { ok: false, reason: `It uses a "${s.kind}" step, which the builder cannot draw yet.` };
    }
    for (const d of dependsOf(s)) if (!byName.has(d)) return { ok: false, reason: `Step "${s.name}" waits for "${d}", which does not exist.` };
  }
  const dependents = new Map<string, WireStep[]>();
  for (const s of wire) for (const d of dependsOf(s)) dependents.set(d, [...(dependents.get(d) ?? []), s]);
  const roots = wire.filter((s) => dependsOf(s).length === 0);
  if (roots.length === 0) return { ok: false, reason: 'No step can start: every step waits for another.' };

  const used = new Set<string>();

  // Reads a run of steps from `first`, stopping at a step that waits for more than one step (a join).
  // `tails` are the last steps of the run: two or more when the run ends in a decision whose paths do not meet yet.
  type Out = { nodes: FlowNode[]; tails: string[]; join: WireStep[] };

  // Steps that all start from the same place and run side by side, until they meet again (or end).
  // Returns the join to carry on from, or the finished run when the steps never meet inside this run.
  const side = (starts: WireStep[], nodes: FlowNode[], what: string): { cont: WireStep } | { out: Out } => {
    if (starts.length > MAX_LANES) fail(`${what} starts more than ${MAX_LANES} steps at once, which the builder cannot draw yet.`);
    const lanes = starts.map((k) => readPath(k));
    nodes.push({ id: newId(), step: { name: '', kind: 'parallel' }, lanes: lanes.map((l) => l.nodes) });
    const innerTails = lanes.flatMap((l) => l.tails);
    const joins = new Map(lanes.flatMap((l) => l.join).map((j) => [j.name, j]));
    if (joins.size === 0) return { out: { nodes, tails: innerTails, join: [] } };
    if (joins.size !== 1) fail(`The steps that start together after ${what} do not meet again in one place.`);
    const next = [...joins.values()][0];
    if (sameSet(dependsOf(next), innerTails)) return { cont: next };
    // The join also waits for steps outside this group, so it belongs to an enclosing group.
    if (innerTails.every((t) => dependsOf(next).includes(t))) return { out: { nodes, tails: innerTails, join: [next] } };
    fail(`"${next.name}" waits for more than the steps that start together after ${what}.`);
  };

  const chain = (first: WireStep, nodes: FlowNode[] = []): Out => {
    let cur: WireStep = first;
    for (;;) {
      if (used.has(cur.name)) fail('Steps loop back on each other.');
      used.add(cur.name);
      const kids = dependents.get(cur.name) ?? [];
      if (cur.kind === 'condition') {
        const yesName = cur.branches?.[0]?.next_step;
        const noName = cur.default_next;
        if (cur.branches?.length !== 1 || cur.branches[0].condition !== YES_CONDITION || !yesName || !noName) fail(`The decision "${cur.name}" is written in a way the builder cannot draw yet.`);
        const yesFirst = byName.get(yesName!), noFirst = byName.get(noName!);
        if (!yesFirst || !noFirst || !sameSet(kids.map((k) => k.name), [yesName!, noName!])) fail(`The decision "${cur.name}" leads to more than its two paths.`);
        const y = readPath(yesFirst!), n = readPath(noFirst!);
        nodes.push({ id: newId(), step: stripManaged(cur), yes: y.nodes, no: n.nodes });
        const innerTails = [...y.tails, ...n.tails];
        const joinNames = new Set([...y.join, ...n.join].map((j) => j.name));
        if (joinNames.size === 0) return { nodes, tails: innerTails, join: [] };
        if (joinNames.size !== 1) fail(`The paths after "${cur.name}" do not come back together in one place.`);
        const next = [...y.join, ...n.join][0];
        if (sameSet(dependsOf(next), innerTails)) { cur = next; continue; }
        // The join also waits for steps outside this decision, so it belongs to an enclosing decision.
        if (innerTails.every((t) => dependsOf(next).includes(t))) return { nodes, tails: innerTails, join: [next] };
        fail(`"${next.name}" waits for more than the two paths of "${cur.name}".`);
      }
      nodes.push({ id: newId(), step: stripManaged(cur) });
      if (kids.length === 0) return { nodes, tails: [cur.name], join: [] };
      if (kids.length > 1) {
        if (!kids.every((k) => sameSet(dependsOf(k), [cur.name]))) fail(`"${cur.name}" starts several steps, and some of them also wait for other steps; the builder cannot draw that yet.`);
        const g = side(kids, nodes, `"${cur.name}"`);
        if ('out' in g) return g.out;
        cur = g.cont;
        continue;
      }
      const next = kids[0];
      if (dependsOf(next).length > 1) return { nodes, tails: [cur.name], join: [next] };
      if (!sameSet(dependsOf(next), [cur.name])) fail(`"${next.name}" does not follow "${cur.name}" directly.`);
      cur = next;
    }
  };

  // A path is a chain that ends at a join (or at the end of the recipe). A carry-on step stands for an empty path.
  const readPath = (first: WireStep): { nodes: FlowNode[]; join: WireStep[]; tails: string[] } => {
    const c = chain(first);
    // A leading carry-on step only opens the path (see compile); it is not something the person added.
    const nodes = c.nodes[0]?.step.config?.ui === CARRY_ON ? c.nodes.slice(1) : c.nodes;
    return { nodes, join: c.join, tails: c.tails };
  };

  try {
    let top: Out;
    if (roots.length === 1) {
      top = chain(roots[0]);
    } else {
      // Several steps with nothing before them start together.
      const pre: FlowNode[] = [];
      const g = side(roots, pre, 'the start');
      top = 'out' in g ? g.out : chain(g.cont, pre);
    }
    if (top.join.length) fail(`"${top.join[0].name}" waits for steps from different paths.`);
    if (used.size !== wire.length) fail('Some steps are not connected to the start.');
    const flow = top.nodes;
    // The flow must write back exactly as stored, or the builder would change the recipe on save.
    if (!sameWire(compile(flow), wire)) fail('The steps are connected in a way the builder would change when saving.');
    return { ok: true, flow };
  } catch (e) {
    return { ok: false, reason: e instanceof Error ? e.message : 'The shape is not supported.' };
  }
}

function stripManaged(step: WireStep): WireStep {
  const copy: WireStep = { ...step, config: step.config ? { ...step.config } : undefined };
  for (const k of MANAGED) delete copy[k];
  if (!copy.config) delete copy.config;
  return copy;
}

/** Equal as recipes: same steps, same waits and branches, in any order. */
export function sameWire(a: WireStep[], b: WireStep[]): boolean {
  const norm = (list: WireStep[]) => list
    .map((s) => ({ ...s, depends_on: [...(s.depends_on ?? [])].sort(), agent: s.agent ?? s.agent_ref }))
    .sort((x, y) => x.name.localeCompare(y.name));
  const strip = (v: unknown): unknown => {
    if (Array.isArray(v)) return v.map(strip);
    if (v && typeof v === 'object') {
      const o: Record<string, unknown> = {};
      for (const k of Object.keys(v).sort()) {
        const val = (v as Record<string, unknown>)[k];
        if (val === undefined || val === '' || (Array.isArray(val) && val.length === 0)) continue;
        o[k] = strip(val);
      }
      return o;
    }
    return v;
  };
  return JSON.stringify(strip(norm(a))) === JSON.stringify(strip(norm(b)));
}

// ── Checking a flow ───────────────────────────────────────────

export interface Problem { id: string; message: string }

/** What would stop this recipe from running. `agents` are the names that exist. */
export function validate(flow: FlowNode[], agents: Set<string>): Problem[] {
  const out: Problem[] = [];
  const nodes = realNodes(flow);
  if (nodes.length === 0) return [{ id: '', message: 'Add at least one step.' }];
  for (const g of allNodes(flow).filter(isParallel)) {
    const inside = (g.lanes ?? []).reduce((n, lane) => n + realNodes(lane).length, 0);
    if (inside < 2) out.push({ id: g.id, message: 'Add at least two steps to run side by side.' });
  }
  for (const n of nodes) {
    const s = n.step;
    const t = titleOf(s);
    if ((s.kind === 'agent' || s.kind === 'evaluator') && !s.agent_ref) out.push({ id: n.id, message: `"${t}" needs an agent.` });
    else if ((s.kind === 'agent' || s.kind === 'evaluator') && !agents.has(s.agent_ref!)) out.push({ id: n.id, message: `"${t}" uses ${s.agent_ref}, which no longer exists.` });
    if (s.kind === 'condition' && !String(s.config?.expression ?? '').trim()) out.push({ id: n.id, message: `"${t}" needs a condition to test.` });
  }
  return out;
}

/** How many steps a flow has, counting the ones inside decisions. */
export const stepCount = (flow: FlowNode[]): number => realNodes(flow).length;

// ── What a decision tests ─────────────────────────────────────

/** `text` is what the steps before said. A decision that "mentions words" tests that text for any of them. */
export function mentionsExpression(words: string[]): string {
  const clean = words.map((w) => w.trim().toLowerCase().replace(/["\\]/g, '')).filter(Boolean);
  return clean.map((w) => `lower(text) contains "${w}"`).join(' or ');
}

/** Reads a "mentions" expression back into its words, or null when the expression was written by hand. */
export function wordsFromExpression(expr: string): string[] | null {
  const parts = expr.split(' or ').map((p) => p.trim()).filter(Boolean);
  if (parts.length === 0) return null;
  const words: string[] = [];
  for (const p of parts) {
    const m = /^lower\(text\) contains "([^"\\]+)"$/.exec(p);
    if (!m) return null;
    words.push(m[1]);
  }
  return words;
}

// ── Starting points ───────────────────────────────────────────

export interface Template { id: string; name: string; body: string; build: (agent: string) => FlowNode[] }

function chainOf(types: StepType[], agent: string, titles: string[]): FlowNode[] {
  let flow: FlowNode[] = [];
  types.forEach((t, i) => {
    const n = newStep(t, flow, agent);
    n.step.config = { ...(n.step.config ?? {}), title: titles[i] };
    n.step.name = uniqueName(titles[i], stepNames(flow));
    flow = [...flow, n];
  });
  return flow;
}

export const TEMPLATES: Template[] = [
  {
    id: 'review', name: 'Review and approve',
    body: 'An agent drafts something, a quality check reads it, then a person approves before it goes out.',
    build: (agent) => chainOf(['agent', 'check', 'person'], agent, ['Draft', 'Check the draft', 'Ask a person to approve']),
  },
  {
    id: 'research', name: 'Research, then write',
    body: 'Look things up in your knowledge, summarize what matters, write the result.',
    build: (agent) => chainOf(['lookup', 'agent', 'agent'], agent, ['Look it up', 'Summarize', 'Write the result']),
  },
  {
    id: 'triage', name: 'Triage and route',
    body: 'Read an incoming request and send it down the right path, with a person for the risky ones.',
    build: (agent) => {
      const flow = chainOf(['agent', 'decide', 'agent'], agent, ['Triage', 'Needs a person?', 'Write the reply']);
      const decide = flow[1];
      decide.step.config = { ...decide.step.config, expression: 'lower(text) contains "refund"' };
      const gate = newStep('person', flow, agent);
      gate.step.config = { ...(gate.step.config ?? {}), title: 'Ask a person to approve' };
      gate.step.name = uniqueName('Ask a person to approve', stepNames(flow));
      decide.yes = [gate];
      return flow;
    },
  },
];

export const BLANK: () => FlowNode[] = () => [];

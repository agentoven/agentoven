/**
 * Recipe flows — build a recipe the way the dashboard's recipe builder does.
 *
 * A recipe is a list of steps in order. A decision has a yes list and a no list, a person's approval can have
 * its own approved and rejected lists, and steps can run side by side. `Flow.toWire()` writes the steps the
 * control plane stores (each step names the steps it waits for), in exactly the shape the dashboard builder
 * writes, so a recipe made here opens in the builder and one made there can be rebuilt here.
 *
 * ```ts
 * import { Flow, flow, ProClient } from '@agentoven/sdk';
 * const { agent, ask, check, decide, mentions, together } = flow;
 *
 * const refunds = new Flow('refund-review', 'Triage a ticket, ask a person about refunds', [
 *   agent('Triage', 'triage-agent'),
 *   decide('Needs a person?', mentions('refund', 'chargeback'), {
 *     yes: [ask('Approve the refund', {
 *       within: '1 day',
 *       approved: [agent('Issue the refund', 'refunder')],
 *       rejected: [agent('Explain the decision', 'writer')],
 *     })],
 *     no: [together([agent('Reply', 'writer')], [check('Tone check', 'reviewer')])],
 *   }),
 *   agent('Log the outcome', 'logger'),
 * ]);
 *
 * const pro = new ProClient({ url: 'http://localhost:8080', apiKey: '...' });
 * await pro.createRecipe(refunds);
 * const run = await pro.runRecipe('refund-review', { text: 'I want a refund' });
 * ```
 *
 * Plain TypeScript: no native module is needed to build or check a flow.
 */

/** A step as the control plane stores it. */
export interface WireStep {
  name: string;
  kind: string;
  agent_ref?: string;
  agent?: string;
  depends_on?: string[];
  config?: Record<string, unknown>;
  branches?: { condition: string; next_step: string }[];
  default_next?: string;
  [extra: string]: unknown;
}

/** What a decision tests: the condition step's own result says whether its expression held. */
export const YES_CONDITION = 'condition_met == true';
/** What an approval's first path tests: the gate's own result says whether it was approved. */
export const APPROVED_CONDITION = 'approved == true';
/** Marks the step standing in for an empty path, so the engine still skips the other path. */
export const CARRY_ON = 'carry_on';

/** How long an approval waits, in the words the dashboard uses. */
export const WITHIN = { '1 hour': 60, '4 hours': 240, '1 day': 1440, '3 days': 4320, 'no limit': 0 } as const;
export type Within = keyof typeof WITHIN;

export const MAX_LANES = 4;

/** One thing that would stop a flow from running. */
export interface Problem {
  title: string;
  message: string;
}

/** A flow that cannot run as written. `problems` lists every reason. */
export class FlowError extends Error {
  readonly problems: Problem[];
  constructor(problems: Problem[]) {
    super(problems.map((p) => p.message).join('; '));
    this.name = 'FlowError';
    this.problems = problems;
  }
}

/** One step of a flow, or a group of steps run side by side. Build them with the functions below. */
export interface Node {
  readonly kind: string; // agent | evaluator | rag | condition | human_gate | map | sub_recipe | parallel
  readonly title: string;
  readonly config: Record<string, unknown>;
  readonly fields: Record<string, unknown>; // step fields outside config: agent_ref, max_retries, ...
  readonly yes: Node[]; // Yes, or Approved
  readonly no: Node[]; // No, or Rejected
  readonly lanes: Node[][];
  readonly paths: boolean; // a person's approval with approved and rejected paths
  /** Set when the flow is compiled. */
  name: string;
}

const node = (kind: string, title: string, extra: Partial<Node> = {}): Node => ({
  kind, title, config: {}, fields: {}, yes: [], no: [], lanes: [], paths: false, name: '', ...extra,
});

const hasPaths = (n: Node): boolean => n.kind === 'condition' || (n.kind === 'human_gate' && n.paths);

function nodes(items: readonly Node[] | undefined, what: string): Node[] {
  const out = [...(items ?? [])];
  for (const n of out) {
    if (!n || typeof n !== 'object' || typeof n.kind !== 'string') {
      throw new TypeError(`${what} takes steps made with agent(), ask(), decide() and the like`);
    }
  }
  return out;
}

// ── Steps ─────────────────────────────────────────────────────

export interface AgentOptions {
  /** How many more times to try after a failure. */
  retries?: number;
  timeoutSecs?: number;
  /** Repeat the step while this expression holds for its output, at most `maxIterations` times (3 when not given). */
  loopWhile?: string;
  maxIterations?: number;
}

function agentFields(agentName: string, o: AgentOptions): Record<string, unknown> {
  const f: Record<string, unknown> = { agent_ref: agentName, agent: agentName };
  if (o.retries) f.max_retries = o.retries;
  if (o.timeoutSecs) f.timeout_secs = o.timeoutSecs;
  if (o.loopWhile) {
    f.loop_condition = o.loopWhile;
    f.max_iterations = o.maxIterations || 3;
  }
  return f;
}

/** Hand a task to one of your agents. It receives what the recipe started with and what earlier steps produced. */
export function agent(title: string, agentName: string, options: AgentOptions = {}): Node {
  return node('agent', title, { fields: agentFields(agentName, options) });
}

/** Score a result with an agent before it moves on. */
export function check(title: string, agentName: string, options: Pick<AgentOptions, 'retries' | 'timeoutSecs'> = {}): Node {
  return node('evaluator', title, { fields: agentFields(agentName, options) });
}

/** Search your knowledge for what the recipe started with, or the step before it. */
export function lookup(title: string, topK = 3): Node {
  return node('rag', title, { config: { top_k: topK } });
}

/** The condition "the answer mentions any of these words". Capital letters do not matter. */
export function mentions(...words: string[]): string {
  return words
    .map((w) => w.trim().toLowerCase().replace(/["\\]/g, ''))
    .filter(Boolean)
    .map((w) => `lower(text) contains "${w}"`)
    .join(' or ');
}

/**
 * Take one way or another. `when` is a condition: use `mentions(...)` or write one. In an expression, `text`
 * is the words of the step before and its output fields are in scope, for example `output.score >= 4`.
 * An empty path means "carry on" for that answer.
 */
export function decide(title: string, when: string, paths: { yes?: readonly Node[]; no?: readonly Node[] } = {}): Node {
  return node('condition', title, { config: { expression: when }, yes: nodes(paths.yes, 'yes'), no: nodes(paths.no, 'no') });
}

export interface AskOptions {
  /** One of '1 hour', '4 hours', '1 day', '3 days' or 'no limit'. Default '1 day'. */
  within?: Within;
  /** Overrides `within`. */
  minutes?: number;
  /** A tool that can send a message. */
  notify?: string;
  /** Only people from this email domain may answer. */
  domain?: string;
  emails?: readonly string[];
  /** Give `approved` or `rejected` (an empty list is fine) and each answer follows its own path. */
  approved?: readonly Node[];
  rejected?: readonly Node[];
  /** 'reject' makes a timeout follow the Rejected path too. Default 'stop'. */
  ifNobodyAnswers?: 'stop' | 'reject';
}

/**
 * Wait for a person to approve. With neither `approved` nor `rejected`, a "no" stops the recipe. With either,
 * each answer follows its own path; `rejected: []` means a rejection ends the recipe as finished, so put the
 * steps that should only follow an approval inside `approved`.
 */
export function ask(title: string, options: AskOptions = {}): Node {
  const o = options;
  let minutes = o.minutes;
  if (minutes === undefined) {
    const within = o.within ?? '1 day';
    if (!(within in WITHIN)) throw new RangeError(`within must be one of ${Object.keys(WITHIN).join(', ')} (or pass minutes), got ${JSON.stringify(within)}`);
    minutes = WITHIN[within];
  }
  const ifNobody = o.ifNobodyAnswers ?? 'stop';
  if (ifNobody !== 'stop' && ifNobody !== 'reject') throw new RangeError('ifNobodyAnswers must be "stop" or "reject"');
  const paths = o.approved !== undefined || o.rejected !== undefined;
  if (ifNobody === 'reject' && !paths) throw new RangeError('ifNobodyAnswers: "reject" needs a Rejected path: give approved or rejected');
  const config: Record<string, unknown> = { max_wait_minutes: minutes };
  if (paths) {
    config.on_reject = 'branch';
    if (ifNobody === 'reject') config.on_timeout = 'branch';
  }
  const fields: Record<string, unknown> = {};
  if (o.notify) fields.notify_tools = [o.notify];
  if (o.domain) fields.approver_domain = o.domain;
  if (o.emails?.length) fields.approver_emails = [...o.emails];
  return node('human_gate', title, { config, fields, paths, yes: nodes(o.approved, 'approved'), no: nodes(o.rejected, 'rejected') });
}

/**
 * Start several runs of steps at the same time. The next step waits until every one has finished. Each argument
 * is a list of steps (one lane). Two to four lanes.
 */
export function together(...lanes: (readonly Node[])[]): Node {
  if (lanes.length < 2 || lanes.length > MAX_LANES) throw new RangeError(`together() needs 2 to ${MAX_LANES} lanes, got ${lanes.length}`);
  return node('parallel', '', { lanes: lanes.map((l) => nodes(l, 'a lane')) });
}

/** The longest a wait may last, in seconds. */
export const WAIT_MAX_SECONDS = 30 * 24 * 3600;
const WAIT_RE = /^\s*(\d+(?:\.\d+)?)\s*([smhd]?)\s*$/i;
const WAIT_UNITS: Record<string, number> = { s: 1, m: 60, h: 3600, d: 86400 };

/** A length of time as the engine reads it: "30s", "5m", "2h" or "3d". */
function waitText(duration: string | number): string {
  if (typeof duration === 'string') {
    const m = WAIT_RE.exec(duration);
    if (!m) throw new RangeError(`a wait must be a length of time like "30s", "5m", "2h" or "3d", got ${JSON.stringify(duration)}`);
    return `${m[1]}${(m[2] || 's').toLowerCase()}`;
  }
  if (!(duration >= 0)) throw new RangeError('a wait cannot be negative');
  for (const [unit, size] of [['d', 86400], ['h', 3600], ['m', 60]] as const) {
    if (duration >= size && duration % size === 0) return `${duration / size}${unit}`;
  }
  return `${duration}s`;
}

/**
 * Pause the recipe for a length of time, or until a time, then carry on. `duration` is `'30s'`, `'5m'`, `'2h'`,
 * `'3d'`, or a number of seconds. `until` is a `Date` or ISO 8601 text such as `'2026-11-02T09:00:00Z'`; if that
 * time has passed when the step starts, the recipe carries on at once. A wait lasts at most 30 days, can be
 * ended early with `pro.releaseWait`, and is held in the control plane's memory, so it does not survive a restart.
 */
export function wait(title: string, options: { duration?: string | number; until?: string | Date } = {}): Node {
  if (options.duration !== undefined && options.until !== undefined) throw new RangeError('give a duration or an until time, not both');
  const config: Record<string, unknown> = {};
  if (options.duration !== undefined) config.duration = waitText(options.duration);
  if (options.until !== undefined) {
    const at = options.until instanceof Date ? options.until : new Date(options.until);
    if (Number.isNaN(at.getTime())) throw new RangeError(`until must be an ISO 8601 time like "2026-11-02T09:00:00Z", got ${JSON.stringify(options.until)}`);
    config.until = options.until instanceof Date ? at.toISOString().replace(/\.\d{3}Z$/, 'Z') : options.until;
  }
  return node('wait', title, { config });
}

/**
 * Run an agent once for each item of a list an earlier step produced. `over` is the path to the list, such as
 * `extract.items`. Not drawn by the dashboard builder yet: a recipe with one opens read-only there.
 */
export function each(title: string, agentName: string, over: string, options: { maxConcurrency?: number } = {}): Node {
  const fields: Record<string, unknown> = { agent_ref: agentName, agent: agentName, source_path: over };
  if (options.maxConcurrency) fields.max_concurrency = options.maxConcurrency;
  return node('map', title, { fields });
}

/** Run another recipe as one step. Not drawn by the dashboard builder yet. */
export function subRecipe(
  title: string,
  recipe: string,
  options: { inputMapping?: Record<string, string>; outputMapping?: Record<string, string> } = {},
): Node {
  const fields: Record<string, unknown> = { recipe_ref: recipe };
  if (options.inputMapping && Object.keys(options.inputMapping).length) fields.input_mapping = { ...options.inputMapping };
  if (options.outputMapping && Object.keys(options.outputMapping).length) fields.output_mapping = { ...options.outputMapping };
  return node('sub_recipe', title, { fields });
}

// ── The recipe ────────────────────────────────────────────────

export function slug(text: string): string {
  const s = text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 48);
  return s || 'step';
}

function* walk(items: readonly Node[]): Generator<Node> {
  for (const n of items) {
    yield n;
    yield* walk(n.yes);
    yield* walk(n.no);
    for (const lane of n.lanes) yield* walk(lane);
  }
}

const real = (items: readonly Node[]): Node[] => [...walk(items)].filter((n) => n.kind !== 'parallel');

export interface WireRecipe {
  name: string;
  description: string;
  version?: string;
  steps: WireStep[];
}

/** A recipe: a name and the steps in order. */
export class Flow {
  readonly steps: Node[];

  constructor(readonly name: string, readonly description = '', steps: readonly Node[] = [], readonly version?: string) {
    this.steps = nodes(steps, 'steps');
  }

  /** Append steps. Returns the flow, so calls chain. */
  add(...items: Node[]): this {
    this.steps.push(...nodes(items, 'add'));
    return this;
  }

  /** What would stop this recipe from running. Pass the names of the agents that exist to check those too. */
  validate(agents?: Iterable<string>): Problem[] {
    const known = agents ? new Set(agents) : undefined;
    const out: Problem[] = [];
    if (real(this.steps).length === 0) return [{ title: '', message: 'Add at least one step.' }];
    for (const g of [...walk(this.steps)].filter((n) => n.kind === 'parallel')) {
      if (g.lanes.reduce((n, lane) => n + real(lane).length, 0) < 2) out.push({ title: '', message: 'Add at least two steps to run side by side.' });
    }
    const lists = (items: readonly Node[]): (readonly Node[])[] => [items, ...items.flatMap((n) => [...lists(n.yes), ...lists(n.no), ...n.lanes.flatMap(lists)])];
    for (const items of lists(this.steps)) {
      items.forEach((n, i) => {
        if (n.kind === 'human_gate' && n.paths && n.no.length === 0 && i < items.length - 1) {
          out.push({ title: n.title, message: `Steps after "${n.title || 'Step'}" would also run after a rejection. Move them under Approved.` });
        }
      });
    }
    for (const n of real(this.steps)) {
      const t = n.title || 'Step';
      if (n.kind === 'agent' || n.kind === 'evaluator' || n.kind === 'map') {
        const ref = String(n.fields.agent_ref ?? '');
        if (!ref) out.push({ title: n.title, message: `"${t}" needs an agent.` });
        else if (known && !known.has(ref)) out.push({ title: n.title, message: `"${t}" uses ${ref}, which does not exist.` });
      }
      if (n.kind === 'wait') {
        const text = String(n.config.duration ?? '');
        if (!text && !n.config.until) out.push({ title: n.title, message: `"${t}" needs a time to wait.` });
        else if (text) {
          const m = WAIT_RE.exec(text);
          if (m && Number(m[1]) * WAIT_UNITS[(m[2] || 's').toLowerCase()] > WAIT_MAX_SECONDS) out.push({ title: n.title, message: `"${t}" would wait longer than 30 days.` });
        }
      }
      if (n.kind === 'map' && !n.fields.source_path) out.push({ title: n.title, message: `"${t}" needs the list to go over.` });
      if (n.kind === 'sub_recipe' && !n.fields.recipe_ref) out.push({ title: n.title, message: `"${t}" needs a recipe to run.` });
      if (n.kind === 'condition' && !String(n.config.expression ?? '').trim()) out.push({ title: n.title, message: `"${t}" needs a condition to test.` });
    }
    return out;
  }

  /** The steps as the control plane stores them. Does not check the flow; see `validate`. */
  toSteps(): WireStep[] {
    return compile(this.steps);
  }

  /** The body to create the recipe with. Throws `FlowError` listing what is wrong, unless `validate: false`. */
  toWire(options: { agents?: Iterable<string>; validate?: boolean } = {}): WireRecipe {
    if (options.validate !== false) {
      const problems = this.validate(options.agents);
      if (problems.length) throw new FlowError(problems);
    }
    return { name: this.name, description: this.description, ...(this.version ? { version: this.version } : {}), steps: this.toSteps() };
  }
}

// ── Flow to steps ─────────────────────────────────────────────
// The same algorithm as the dashboard's recipeFlow.ts compile(); sdk/fixtures/recipe-flows.json pins them together.

function compile(flowNodes: readonly Node[]): WireStep[] {
  const taken = new Set<string>();
  const unique = (base: string): string => {
    const stem = slug(base);
    let name = stem;
    let i = 2;
    while (taken.has(name)) name = `${stem}-${i++}`;
    return name;
  };

  // Names come from titles, in the order the steps are read, and stay distinct.
  for (const n of walk(flowNodes)) {
    if (n.kind !== 'parallel') {
      n.name = unique(n.title);
      taken.add(n.name);
    }
  }

  const carryOn = (parent: string, label: string, deps: string[]): WireStep => {
    const name = unique(`${parent}-${label}`);
    taken.add(name);
    return { name, kind: 'fan_out', depends_on: deps, config: { title: 'Carry on', ui: CARRY_ON } };
  };

  const build = (items: readonly Node[], deps: string[]): { steps: WireStep[]; tails: string[] } => {
    const steps: WireStep[] = [];
    let tails = deps;
    for (const n of items) {
      if (n.kind === 'parallel') {
        // Every lane waits for what came before, and whatever follows waits for the end of every lane.
        const ends: string[] = [];
        for (const lane of n.lanes) {
          const built = build(lane, tails);
          steps.push(...built.steps);
          ends.push(...(built.steps.length === 0 ? [] : built.tails));
        }
        if (ends.length > 0) tails = ends;
        continue;
      }
      const step: WireStep = { name: n.name, kind: n.kind, depends_on: [...tails], ...n.fields, config: { title: n.title, ...n.config } };
      steps.push(step);
      if (!hasPaths(n)) {
        tails = [n.name];
        continue;
      }
      const gate = n.kind === 'human_gate';
      const paths: { first: string; steps: WireStep[]; tails: string[] }[] = [];
      for (const [label, side] of [['yes', n.yes], ['no', n.no]] as const) {
        // A path must open with one step: the branch names it, and the engine skips its sibling.
        // So a path that is empty, or opens with steps side by side, starts with a carry-on step.
        const needsOpener = side.length === 0 || side[0].kind === 'parallel';
        const word = gate ? (label === 'yes' ? 'approved' : 'rejected') : label === 'yes' ? 'then' : 'otherwise';
        const opener = needsOpener ? carryOn(n.name, word, [n.name]) : null;
        const built = build(side, [opener ? opener.name : n.name]);
        const all = opener ? [opener, ...built.steps] : built.steps;
        paths.push({ first: all[0].name, steps: all, tails: built.steps.length === 0 && opener ? [opener.name] : built.tails });
      }
      step.branches = [{ condition: gate ? APPROVED_CONDITION : YES_CONDITION, next_step: paths[0].first }];
      step.default_next = paths[1].first;
      steps.push(...paths[0].steps, ...paths[1].steps);
      tails = [...paths[0].tails, ...paths[1].tails];
    }
    return { steps, tails };
  };

  return build(flowNodes, []).steps;
}

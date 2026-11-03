/**
 * Reference checks: does what an agent or a recipe names exist in the kitchen that holds it?
 *
 * An agent's configuration is full of names: the provider and model it runs on, the model that summarizes its
 * history, its tools, prompts and skills, the agents it may delegate to. A recipe names agents, steps, channels,
 * credentials, approver roles. A typo in any of them used to surface at bake time, or on the first run, as a string
 * to decode. `checkAgent` and `checkRecipe` read a configuration against a `Catalog` (a snapshot of what the kitchen
 * has) and return every problem at once, each with the field it is in, the value that is wrong and the nearest values
 * that are right.
 *
 * This is the same check the control plane runs (Go: control-plane/pkg/refcheck), pinned to it by
 * sdk/fixtures/refcheck-cases.json, which the tests of every SDK run. It is pure: no network, no clock. The server
 * stays the authority and refuses a write the same way (HTTP 422); a check here only saves the round trip, and every
 * entry point that runs it can be skipped.
 *
 * A configuration is read as the wire JSON the API takes (`model_provider`, `summary_model`, `steps[].agent_ref`).
 * Values of the wrong JSON type are the server's to report (`wrong_type`, `unknown_field`): the server reads the
 * request body, this reads a configuration that was already built, so a value of the wrong type is left out here
 * (except inside an ingredient's free-form `config`, which the server checks the same way).
 */

/** Only an `error` refuses a write or a bake; a `warning` is advice. */
export type Severity = 'error' | 'warning';

/** What is wrong. Stable: clients switch on these. */
export type ProblemCode =
  | 'not_found'
  | 'not_usable'
  | 'unlisted'
  | 'unknown_field'
  | 'wrong_type'
  | 'out_of_range'
  | 'duplicate'
  | 'required'
  | 'invalid_value';

/** What a reference should have pointed at. */
export type ProblemKind =
  | 'provider' | 'model' | 'prompt' | 'tool' | 'skill' | 'agent' | 'recipe' | 'scenario' | 'channel'
  | 'environment' | 'credential' | 'role' | 'step' | 'ingredient_kind' | 'step_kind' | 'embedding'
  | 'vectorstore' | 'vectorstore_backend';

/** One thing wrong with a configuration. */
export interface Problem {
  /** The field, in the JSON the API takes: `summary_model`, `ingredients[2].config.provider`, `steps[1].agent_ref`. */
  path: string;
  code: ProblemCode;
  severity: Severity;
  /** What the value should have named (absent for problems that are not references). */
  kind?: ProblemKind;
  /** What was given, as text. */
  value?: string;
  /** What `value` should have been found in: the provider whose model list was searched. */
  within?: string;
  /** The sentence for a person. */
  message: string;
  /** The nearest valid values, nearest first (at most three). */
  suggestions?: string[];
}

/** A model provider as the checks see it. */
export interface CatalogProvider {
  name: string;
  kind: string;
  models?: string[] | null;
  /** The result of the provider's last connectivity test; absent when it was never tested. */
  healthy?: boolean | null;
}

export interface CatalogTool {
  name: string;
  enabled: boolean;
  capabilities?: string[] | null;
}

/** `status` is `accepted` for the only state that may run. */
export interface CatalogSkill {
  name: string;
  status: string;
}

export interface CatalogAgent {
  name: string;
  status: string;
}

/** A set of names an edition may or may not be able to list; `known: false` means "not checked", never "empty". */
export interface CatalogNames {
  known: boolean;
  names?: string[] | null;
}

/**
 * A snapshot of what one kitchen has: the names a configuration may refer to. A part that is left out (`undefined`)
 * is unknown and is not checked (the caller could not read that list); an empty list means the kitchen has none, so
 * a reference to one is refused. `scenarios` and `roles` are unknown unless `known` is true.
 */
export interface Catalog {
  providers?: CatalogProvider[];
  /** A prompt's name and its latest version. */
  prompts?: Record<string, number>;
  tools?: CatalogTool[];
  skills?: CatalogSkill[];
  agents?: CatalogAgent[];
  recipes?: string[];
  channels?: string[];
  /** Environment slugs. */
  environments?: string[];
  /** Names of the kitchen's credentials. */
  credentials?: string[];
  scenarios?: CatalogNames;
  roles?: CatalogNames;
}

/** What the configuration is being checked for. */
export interface CheckOptions {
  /**
   * Set when the configuration is about to run (a bake, a re-cook, a run of a recipe). A draft may be incomplete, so on a
   * write the gaps that only matter at run time (a provider with no model, a managed agent with no model) are
   * warnings; at a bake they are errors.
   */
  bake?: boolean;
  /** The environment a run was asked for, checked against the kitchen's. */
  environment?: string;
}

/** An agent as the API takes it. Only the fields the checks read are named. */
export interface AgentConfig {
  name?: string;
  mode?: string;
  runtime?: string;
  execution_mode?: string;
  behavior?: string;
  reasoning_strategy?: string;
  max_turns?: number;
  context_budget?: number;
  model_provider?: string;
  model_name?: string;
  backup_provider?: string;
  backup_model?: string;
  /** The provider that serves `summary_model`, the compaction model. Empty: any provider that lists the model. */
  summary_provider?: string;
  summary_model?: string;
  skills?: string[];
  approval_tools?: string[];
  subagents?: string[];
  budget?: { max_cost_usd?: number; max_tokens?: number; max_active_seconds?: number; max_turns?: number; child_share?: number; [k: string]: unknown };
  bot?: { tools?: string[]; [k: string]: unknown };
  guardrails?: { kind?: string; stage?: string; action?: string; enabled?: boolean; [k: string]: unknown }[];
  ingredients?: { name?: string; kind?: string; required?: boolean; config?: Record<string, unknown>; [k: string]: unknown }[];
  [extra: string]: unknown;
}

/** A recipe as the API takes it. Only the fields the checks read are named. */
export interface RecipeConfig {
  name?: string;
  default_environment?: string;
  steps?: Record<string, unknown>[];
  [extra: string]: unknown;
}

/**
 * What a write may change about an agent (the body of `PUT /agents/{name}`): a field left out is unchanged, `[]` clears
 * a list, `""` clears `summary_provider` and `summary_model`, and `null` clears an optional setting (the ones typed
 * `| null` below; the model and the mode cannot be cleared, the server refuses `null` for them).
 */
export interface AgentEdit {
  description?: string | null;
  framework?: string | null;
  model_provider?: string;
  model_name?: string;
  backup_provider?: string | null;
  backup_model?: string | null;
  mode?: string;
  execution_mode?: string | null;
  max_turns?: number | null;
  behavior?: string;
  runtime?: string;
  bot?: AgentConfig['bot'] | null;
  ingredients?: AgentConfig['ingredients'] | null;
  skills?: string[];
  guardrails?: AgentConfig['guardrails'];
  context_budget?: number | null;
  reasoning_strategy?: string | null;
  summary_provider?: string | null;
  summary_model?: string | null;
  approval_tools?: string[];
  subagents?: string[];
  [extra: string]: unknown;
}

// ── Limits of the numeric settings (generous: they refuse a value that cannot be meant) ──

const MAX_TURNS_LIMIT = 1000;
const MIN_CONTEXT_BUDGET = 256;
const MAX_CONTEXT_BUDGET = 10_000_000;
const MAX_NAME_LENGTH = 128;
const MAX_TOP_K = 1000;
const MAX_EMBEDDING_DIMS = 65536;
const MAX_EMBEDDING_BATCH = 100_000;
const MAX_SCENARIO_ROLLOUT = 1000;
const MAX_STEP_RETRIES = 20;
const MAX_STEP_TIMEOUT = 7 * 24 * 3600;
const MAX_STEP_ITERATIONS = 10_000;
const MAX_STEP_CONCURRENT = 1000;
const MAX_SUGGESTIONS = 3;

const AGENT_MODES = ['managed', 'external'];
const AGENT_RUNTIMES = ['agentoven', 'langchain', 'langgraph', 'deepagents', 'crewai', 'custom', 'agenticbot'];
const EXEC_MODES = ['local', 'docker', 'k8s'];
const BEHAVIORS = ['reactive', 'agentic'];
const STRATEGIES = ['react', 'plan-and-execute', 'reflexion'];
const INGREDIENT_KINDS = ['model', 'tool', 'prompt', 'data', 'observability', 'embedding', 'vectorstore', 'retriever', 'scenario', 'skill'];
const VECTOR_BACKENDS = ['embedded', 'pgvector', 'pinecone', 'qdrant', 'cosmosdb', 'chroma', 'snowflake', 'databricks'];
const GUARDRAIL_KINDS = ['content_filter', 'pii_detection', 'topic_restriction', 'max_length', 'regex_filter', 'prompt_injection', 'llamaguard', 'custom'];
const GUARDRAIL_STAGES = ['input', 'output', 'both', 'retrieval', 'all'];
const GUARDRAIL_ACTIONS = ['block', 'redact', 'mask', 'warn', 'require_approval'];
const STEP_KINDS = ['agent', 'human_gate', 'evaluator', 'condition', 'fan_out', 'fan_in', 'rag', 'router', 'map', 'sub_recipe', 'wait', 'notify'];

/** Provider kinds that serve models their list does not name: a model id outside the list is accepted, with a warning. */
const OPEN_MODEL_KINDS = new Set(['openrouter', 'litellm', 'ollama']);

// ── did you mean ──

/** Letters and digits only. */
function squash(s: string): string {
  return s.replace(/[^\p{L}\p{Nd}]/gu, '');
}

/** Edit distance with an adjacent swap counted as one edit (optimal string alignment): "dokcer" is one slip. */
function distance(a: string, b: string): number {
  const ra = [...a];
  const rb = [...b];
  const d: number[][] = Array.from({ length: ra.length + 1 }, (_, i) => {
    const row = new Array<number>(rb.length + 1).fill(0);
    row[0] = i;
    return row;
  });
  for (let j = 0; j <= rb.length; j++) d[0][j] = j;
  for (let i = 1; i <= ra.length; i++) {
    for (let j = 1; j <= rb.length; j++) {
      const cost = ra[i - 1] === rb[j - 1] ? 0 : 1;
      d[i][j] = Math.min(d[i][j - 1] + 1, d[i - 1][j] + 1, d[i - 1][j - 1] + cost);
      if (i > 1 && j > 1 && ra[i - 1] === rb[j - 2] && ra[i - 2] === rb[j - 1]) d[i][j] = Math.min(d[i][j], d[i - 2][j - 2] + 1);
    }
  }
  return d[ra.length][rb.length];
}

/** Go sorts strings by UTF-8 bytes, JavaScript by UTF-16 units; both agree below U+10000, which is every name in use. */
function byName(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

function sorted(list: readonly string[]): string[] {
  return [...list].sort(byName);
}

/**
 * The candidates closest to `value`, nearest first, at most `limit` (default 3). A candidate is close when the two differ
 * by a few characters (a typo), when they are the same once case and punctuation are ignored (`gpt4o` and `gpt-4o`),
 * or when one holds the other (`haiku` in `claude-haiku-4-5`). This is the rule behind every "did you mean".
 */
export function nearest(value: string, candidates: readonly string[], limit = MAX_SUGGESTIONS): string[] {
  const v = value.toLowerCase();
  const vn = squash(v);
  if (vn === '') return [];
  const vnLen = [...vn].length;
  const allowed = Math.min(Math.max(Math.floor(vnLen / 4), 1), 3);
  const close: { name: string; dist: number }[] = [];
  const seen = new Set<string>();
  for (const c of candidates) {
    if (seen.has(c)) continue;
    seen.add(c);
    const cl = c.toLowerCase();
    const cn = squash(cl);
    if (cn === '') continue;
    let d = Math.min(distance(v, cl), distance(vn, cn));
    if (d > allowed && vnLen >= 3 && [...cn].length >= 3 && (cn.includes(vn) || vn.includes(cn))) d = allowed; // contained: close, ranked after nearer typos
    if (d <= allowed) close.push({ name: c, dist: d });
  }
  close.sort((a, b) => a.dist - b.dist || byName(a.name, b.name));
  const n = limit <= 0 || limit > close.length ? close.length : limit;
  return close.slice(0, n).map((c) => c.name);
}

// ── problems ──

/** Whether a problem refuses a write. */
export function isBlocking(p: Problem): boolean {
  return p.severity !== 'warning';
}

/** Separate the problems that refuse a write from the warnings. */
export function split(problems: readonly Problem[]): { errors: Problem[]; warnings: Problem[] } {
  return { errors: problems.filter(isBlocking), warnings: problems.filter((p) => !isBlocking(p)) };
}

function sameKey(p: Problem): string {
  return `${p.code}\0${p.kind ?? ''}\0${p.value ?? ''}\0${p.within ?? ''}`;
}

/**
 * The blocking problems of `after` that `before` did not already have. A write that does not make anything worse is not
 * refused for what the configuration already held (an agent saved before the checks existed can still be edited); a
 * bake takes no such leave. Two problems are the same when they agree on code, kind and value, wherever they sit.
 */
export function introduced(before: readonly Problem[], after: readonly Problem[]): Problem[] {
  const held = new Map<string, number>();
  for (const p of before) if (isBlocking(p)) held.set(sameKey(p), (held.get(sameKey(p)) ?? 0) + 1);
  const out: Problem[] = [];
  for (const p of after) {
    if (!isBlocking(p)) continue;
    const k = sameKey(p);
    const n = held.get(k) ?? 0;
    if (n > 0) held.set(k, n - 1);
    else out.push(p);
  }
  return out;
}

const quote = (s: string): string => JSON.stringify(s);
const joinQuoted = (ss: readonly string[]): string => ss.map(quote).join(', ');

function joinOr(ss: readonly string[]): string {
  const q = ss.map(quote);
  return q.length === 1 ? q[0] : `${q.slice(0, -1).join(', ')} or ${q[q.length - 1]}`;
}

// ── reading a configuration (JSON values) ──

type Obj = Record<string, unknown>;
const isObj = (v: unknown): v is Obj => typeof v === 'object' && v !== null && !Array.isArray(v);
const present = (v: unknown): boolean => v !== undefined && v !== null;
const strOf = (v: unknown): string => (typeof v === 'string' ? v : '');
const numOf = (v: unknown): number => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
const strList = (v: unknown): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : []);
const objList = (v: unknown): Obj[] => (Array.isArray(v) ? v.filter(isObj) : []);
const has = (o: Obj, k: string): boolean => Object.prototype.hasOwnProperty.call(o, k);

function describe(v: unknown): string {
  if (typeof v === 'string') return v;
  const b = JSON.stringify(v) ?? String(v);
  return b.length > 60 ? `${b.slice(0, 57)}...` : b;
}

function jsonTypeName(v: unknown): string {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return 'a string';
  if (typeof v === 'boolean') return 'a boolean';
  if (typeof v === 'number') return 'a number';
  if (Array.isArray(v)) return 'a list';
  if (typeof v === 'object') return 'an object';
  return 'another type';
}

const trimFloat = (f: number): string => String(f);

function hasControl(s: string): boolean {
  for (const ch of s) {
    const c = ch.codePointAt(0)!;
    if (c < 0x20 || c === 0x7f) return true;
  }
  return false;
}

const utf8Length = (s: string): number => new TextEncoder().encode(s).length;

// ── the checker ──

interface ProviderFacts {
  name: string;
  kind: string;
  models: string[];
  healthy?: boolean | null;
}

class Checker {
  readonly out: Problem[] = [];

  constructor(
    private readonly c: Catalog,
    private readonly o: CheckOptions,
  ) {}

  add(p: Omit<Problem, 'severity'> & { severity?: Severity }): void {
    const problem: Problem = { ...p, severity: p.severity ?? 'error' };
    for (const k of ['kind', 'value', 'within'] as const) if (problem[k] === undefined || problem[k] === '') delete problem[k];
    if (!problem.suggestions || problem.suggestions.length === 0) delete problem.suggestions;
    this.out.push(problem);
  }

  /** A problem that only matters when the configuration runs. */
  gapSeverity(): Severity {
    return this.o.bake ? 'error' : 'warning';
  }

  required(path: string, what: string): void {
    this.add({ path, code: 'required', message: `${path}: ${what}` });
  }

  invalid(path: string, value: string, message: string, suggestions?: string[]): void {
    this.add({ path, code: 'invalid_value', value, message: `${path}: ${message}`, suggestions });
  }

  wrongType(path: string, want: string, got: unknown): void {
    this.add({ path, code: 'wrong_type', value: describe(got), message: `${path}: must be ${want}, not ${jsonTypeName(got)}` });
  }

  outOfRange(path: string, got: number, lo: number, hi: number, whole: boolean): void {
    const what = whole ? 'a whole number' : 'a number';
    this.add({ path, code: 'out_of_range', value: trimFloat(got), message: `${path}: ${trimFloat(got)} must be ${what} from ${trimFloat(lo)} to ${trimFloat(hi)}` });
  }

  /** A value that is not among the candidates, with the nearest ones. */
  notFound(path: string, kind: ProblemKind, value: string, within: string, candidates: string[]): void {
    const sugg = nearest(value, candidates, MAX_SUGGESTIONS);
    let what = `a ${kind} in this kitchen`;
    if (kind === 'model' && within !== '') what = `a model of provider ${quote(within)}`;
    else if (kind === 'model') what = 'a model of any provider in this kitchen';
    else if (kind === 'agent') what = 'an agent in this kitchen';
    else if (kind === 'step') what = 'a step of this recipe';
    else if (kind === 'ingredient_kind') what = 'an ingredient kind';
    else if (kind === 'vectorstore_backend') what = 'a vector store backend';
    else if (kind === 'step_kind') what = 'a step kind';
    else if (kind === 'embedding' || kind === 'vectorstore') what = `one of this agent's ${kind} ingredients`;
    else if (kind === 'environment') what = 'an environment of this kitchen';
    let msg = `${path}: ${quote(value)} is not ${what}`;
    if (sugg.length > 0) msg += ` (did you mean ${joinOr(sugg)}?)`;
    else if (candidates.length === 0) msg += ' (there are none)';
    else if (candidates.length <= 8) msg += ` (available: ${joinQuoted(candidates)})`;
    else msg += ` (${candidates.length} available)`;
    this.add({ path, code: 'not_found', kind, value, within, message: msg, suggestions: sugg });
  }

  notUsable(path: string, kind: ProblemKind, value: string, why: string, severity: Severity): void {
    this.add({ path, code: 'not_usable', severity, kind, value, message: `${path}: ${kind} ${quote(value)} ${why}` });
  }

  // ── what the kitchen has ──

  private providers(): ProviderFacts[] | undefined {
    return this.c.providers?.map((p) => ({ name: p.name, kind: p.kind, models: p.models ?? [], healthy: p.healthy }));
  }

  private providerIsOpen(p: ProviderFacts): boolean {
    return OPEN_MODEL_KINDS.has(p.kind) || p.models.length === 0;
  }

  /** A provider name, checked; the provider when it exists. Unknown providers: nothing to check against. */
  provider(path: string, name: string): ProviderFacts | undefined {
    const list = this.providers();
    if (!list) return undefined;
    const p = list.find((x) => x.name === name);
    if (!p) {
      this.notFound(path, 'provider', name, '', sorted(list.map((x) => x.name)));
      return undefined;
    }
    if (p.healthy === false) this.notUsable(path, 'provider', name, 'failed its last connectivity test', 'warning');
    return p;
  }

  private model(path: string, p: ProviderFacts, model: string): void {
    if (p.models.includes(model)) return;
    if (!this.providerIsOpen(p)) {
      this.notFound(path, 'model', model, p.name, sorted(p.models));
    } else if (p.models.length > 0) {
      this.add({
        path, code: 'unlisted', severity: 'warning', kind: 'model', value: model, within: p.name,
        message: `${path}: ${quote(model)} is not in provider ${quote(p.name)}'s list; accepted because a ${p.kind} provider serves models its list does not name`,
      });
    }
  }

  /**
   * A provider/model pair. `providerRequired` says whether a model without a provider is noted (the primary model) or
   * looked up among every provider's models (the summarizer, whose provider is optional). `modelRequired` is whether a
   * provider without a model is a gap.
   */
  modelPair(providerPath: string, modelPath: string, provider: string, model: string, providerRequired: boolean, modelRequired: boolean): void {
    if (provider === '' && model === '') return;
    if (provider === '') {
      if (providerRequired) {
        // Nothing reads a model name without its provider, so this is a note, not a refusal.
        this.add({ path: providerPath, code: 'required', severity: 'warning', message: `${providerPath}: ${modelPath} has no effect without the provider that serves it` });
        return;
      }
      const list = this.providers();
      if (!list) return;
      if (list.some((p) => p.models.includes(model))) return;
      if (list.some((p) => this.providerIsOpen(p))) return; // some provider serves any id; the model cannot be shown absent
      const all = new Set<string>();
      for (const p of list) for (const m of p.models) all.add(m);
      this.notFound(modelPath, 'model', model, '', sorted([...all]));
      return;
    }
    const p = this.provider(providerPath, provider);
    if (!p) return;
    if (model === '') {
      if (modelRequired) {
        this.add({ path: modelPath, code: 'required', severity: this.gapSeverity(), within: provider, message: `${modelPath}: provider ${quote(provider)} is set and needs a model` });
      }
      return;
    }
    this.model(modelPath, p, model);
  }

  /** A tool name. `needs` is the capability the use requires ("tool" to call it, "notify" to send to it). */
  tool(path: string, name: string, needs: string, required: boolean): void {
    const tools = this.c.tools;
    if (!tools) return;
    const t = tools.find((x) => x.name === name);
    if (!t) {
      this.notFound(path, 'tool', name, '', sorted(tools.filter((x) => needs === '' || (x.capabilities ?? []).includes(needs)).map((x) => x.name)));
      return;
    }
    const sev: Severity = required ? 'error' : 'warning';
    if (!t.enabled) this.notUsable(path, 'tool', name, 'is disabled', sev);
    else if (needs !== '' && !(t.capabilities ?? []).includes(needs)) this.notUsable(path, 'tool', name, `does not have the ${quote(needs)} capability`, sev);
  }

  skill(path: string, name: string, required: boolean): void {
    const skills = this.c.skills;
    if (!skills) return;
    const s = skills.find((x) => x.name === name);
    if (!s) {
      this.notFound(path, 'skill', name, '', sorted(skills.map((x) => x.name)));
      return;
    }
    if (s.status !== 'accepted') this.notUsable(path, 'skill', name, `is not accepted (status: ${s.status})`, required ? 'error' : 'warning');
  }

  /** An agent a subagent list or a recipe step names. */
  agentRef(path: string, name: string): void {
    const agents = this.c.agents;
    if (!agents) return;
    const a = agents.find((x) => x.name === name);
    if (!a) {
      this.notFound(path, 'agent', name, '', sorted(agents.filter((x) => x.status !== 'retired').map((x) => x.name)));
      return;
    }
    if (a.status === 'retired') this.notUsable(path, 'agent', name, 'is retired: register it again to bring it back', 'error');
    else if (a.status === 'burnt' || a.status === 'cooled') this.notUsable(path, 'agent', name, `is not running (status: ${a.status})`, 'warning');
  }

  /** A name that must be one of `set`; an unknown set (undefined) is not checked. */
  fromSet(path: string, kind: ProblemKind, name: string, set: readonly string[] | undefined): void {
    if (set && !set.includes(name)) this.notFound(path, kind, name, '', sorted(set));
  }

  /** The second and later occurrence of each value of a list. */
  dupes(path: string, values: readonly string[]): void {
    const seen = new Set<string>();
    values.forEach((v, i) => {
      if (seen.has(v)) this.add({ path: `${path}[${i}]`, code: 'duplicate', value: v, message: `${path}[${i}]: ${quote(v)} is listed more than once` });
      seen.add(v);
    });
  }

  /** A value against a fixed set. An empty value is the default and passes. */
  enum(path: string, value: string, allowed: readonly string[]): void {
    if (value === '' || allowed.includes(value)) return;
    const sugg = nearest(value, allowed, MAX_SUGGESTIONS);
    const msg = sugg.length > 0 ? `${quote(value)} is not valid (did you mean ${joinOr(sugg)}?)` : `${quote(value)} is not valid: must be one of ${joinQuoted(allowed)}`;
    this.invalid(path, value, msg, sugg);
  }

  rangeInt(path: string, v: number, lo: number, hi: number): void {
    if (v < lo || v > hi) this.outOfRange(path, v, lo, hi, true);
  }

  // ── values read from a free-form config map ──

  cfgString(path: string, m: Obj, key: string): { value: string; ok: boolean } {
    const v = m[key];
    if (!present(v)) return { value: '', ok: false };
    if (typeof v !== 'string') {
      this.wrongType(`${path}.${key}`, 'a string', v);
      return { value: '', ok: false };
    }
    return { value: v, ok: true };
  }

  cfgInt(path: string, m: Obj, key: string, lo: number, hi: number): void {
    const v = m[key];
    if (!present(v)) return;
    if (typeof v !== 'number' || !Number.isFinite(v) || !Number.isInteger(v)) {
      this.wrongType(`${path}.${key}`, 'a whole number', v);
      return;
    }
    if (v < lo || v > hi) this.outOfRange(`${path}.${key}`, v, lo, hi, true);
  }

  cfgFloat(path: string, m: Obj, key: string, lo: number, hi: number): void {
    const v = m[key];
    if (!present(v)) return;
    if (typeof v !== 'number' || !Number.isFinite(v)) {
      this.wrongType(`${path}.${key}`, 'a number', v);
      return;
    }
    if (v < lo || v > hi) this.outOfRange(`${path}.${key}`, v, lo, hi, false);
  }

  // ── an agent ──

  agent(a: AgentConfig): void {
    const name = strOf(a.name);
    if (name !== '') {
      if (utf8Length(name) > MAX_NAME_LENGTH) this.invalid('name', name, `is longer than ${MAX_NAME_LENGTH} characters`);
      else if (/[/\\?#%]/.test(name) || hasControl(name)) this.invalid('name', name, `must not contain "/", "\\", "?", "#", "%" or control characters: it is part of the agent's URL`);
    }
    this.enum('mode', strOf(a.mode), AGENT_MODES);
    this.enum('runtime', strOf(a.runtime), AGENT_RUNTIMES);
    this.enum('execution_mode', strOf(a.execution_mode), EXEC_MODES);
    this.enum('behavior', strOf(a.behavior), BEHAVIORS);
    this.enum('reasoning_strategy', strOf(a.reasoning_strategy), STRATEGIES);
    this.rangeInt('max_turns', numOf(a.max_turns), 0, MAX_TURNS_LIMIT);
    if (numOf(a.context_budget) !== 0) this.rangeInt('context_budget', numOf(a.context_budget), MIN_CONTEXT_BUDGET, MAX_CONTEXT_BUDGET);

    // The model the agent runs on, the model it falls back to, the model that summarizes its history.
    this.modelPair('model_provider', 'model_name', strOf(a.model_provider), strOf(a.model_name), true, true);
    this.modelPair('backup_provider', 'backup_model', strOf(a.backup_provider), strOf(a.backup_model), true, true);
    this.modelPair('summary_provider', 'summary_model', strOf(a.summary_provider), strOf(a.summary_model), false, true);

    const skills = strList(a.skills);
    skills.forEach((n, i) => this.skill(`skills[${i}]`, n, false));
    this.dupes('skills', skills);
    const approval = strList(a.approval_tools);
    approval.forEach((n, i) => this.tool(`approval_tools[${i}]`, n, 'tool', false));
    this.dupes('approval_tools', approval);
    const subagents = strList(a.subagents);
    subagents.forEach((n, i) => {
      const path = `subagents[${i}]`;
      if (n === name && name !== '') this.invalid(path, n, 'an agent cannot delegate to itself');
      else this.agentRef(path, n);
    });
    this.dupes('subagents', subagents);

    if (isObj(a.budget)) {
      const b = a.budget;
      if (numOf(b.max_cost_usd) < 0) this.outOfRange('budget.max_cost_usd', numOf(b.max_cost_usd), 0, 1e9, false);
      for (const f of ['max_tokens', 'max_active_seconds', 'max_turns']) {
        if (numOf(b[f]) < 0) this.outOfRange(`budget.${f}`, numOf(b[f]), 0, 1e12, true);
      }
      const share = numOf(b.child_share);
      if (share < 0 || share > 1) this.outOfRange('budget.child_share', share, 0, 1, false);
    }
    if (isObj(a.bot)) {
      const tools = strList(a.bot.tools);
      tools.forEach((n, i) => this.tool(`bot.tools[${i}]`, n, 'tool', false));
      this.dupes('bot.tools', tools);
    }

    objList(a.guardrails).forEach((g, i) => {
      if (g.enabled !== true) return; // a rule that is switched off is never evaluated, whatever it says
      const path = `guardrails[${i}]`;
      this.enum(`${path}.kind`, strOf(g.kind), GUARDRAIL_KINDS);
      this.enum(`${path}.stage`, strOf(g.stage), GUARDRAIL_STAGES);
      this.enum(`${path}.action`, strOf(g.action), GUARDRAIL_ACTIONS);
    });

    this.ingredients(a);

    // A managed agent that is about to run needs a model somewhere.
    const mode = strOf(a.mode);
    const managed = mode === 'managed' || mode === '';
    if (this.o.bake && managed && !hasModel(a)) {
      this.add({ path: 'model_provider', code: 'required', kind: 'model', message: 'model_provider: a managed agent needs a model: set model_provider and model_name, or add a model ingredient' });
    }
  }

  private ingredients(a: AgentConfig): void {
    const seen = new Set<string>();
    const kindCount = new Map<string, number>();
    const embeddings: EmbeddingFacts[] = [];
    const vectorstores: { backend: string; index: string }[] = [];
    const retrievers: { path: string; cfg: Obj }[] = [];

    (Array.isArray(a.ingredients) ? a.ingredients : []).forEach((ing, i) => {
      if (!isObj(ing)) return;
      const path = `ingredients[${i}]`;
      const kind = strOf(ing.kind);
      if (!INGREDIENT_KINDS.includes(kind)) {
        if (kind === '') this.required(`${path}.kind`, 'an ingredient needs a kind');
        else this.notFound(`${path}.kind`, 'ingredient_kind', kind, '', INGREDIENT_KINDS);
        return;
      }
      const count = (kindCount.get(kind) ?? 0) + 1;
      kindCount.set(kind, count);
      const ingName = strOf(ing.name);
      if (ingName !== '') {
        const key = `${kind}\0${ingName}`;
        if (seen.has(key)) this.add({ path: `${path}.name`, code: 'duplicate', value: ingName, message: `${path}.name: there is already a ${kind} ingredient named ${quote(ingName)}` });
        seen.add(key);
      }
      if ((kind === 'model' || kind === 'prompt') && count === 2) {
        this.add({ path: `${path}.kind`, code: 'duplicate', value: kind, message: `${path}.kind: an agent has one ${kind} ingredient; this is the second` });
      }
      const cfg: Obj = isObj(ing.config) ? ing.config : {};
      const required = ing.required === true;
      switch (kind) {
        case 'model':
          this.modelIngredient(path, cfg);
          break;
        case 'tool': {
          let name = ingName;
          const n = this.cfgString(`${path}.config`, cfg, 'tool_name');
          if (n.ok && n.value !== '') name = n.value;
          if (name === '') {
            this.required(`${path}.name`, 'a tool ingredient needs the name of a tool');
            break;
          }
          this.tool(toolPath(path, cfg), name, 'tool', required);
          break;
        }
        case 'observability': {
          let name = ingName;
          const n = this.cfgString(`${path}.config`, cfg, 'tool_name');
          if (n.ok && n.value !== '') name = n.value;
          if (name !== '') this.tool(toolPath(path, cfg), name, 'tool', false);
          break;
        }
        case 'prompt':
          this.promptIngredient(path, ingName, cfg);
          break;
        case 'data': {
          const uri = this.cfgString(`${path}.config`, cfg, 'uri');
          const wrongType = present(cfg.uri) && !uri.ok;
          if (!wrongType && uri.value.trim() === '') this.required(`${path}.config.uri`, 'a data ingredient needs a uri');
          break;
        }
        case 'embedding':
          embeddings.push(this.embeddingIngredient(path, cfg));
          break;
        case 'vectorstore':
          vectorstores.push(this.vectorStoreIngredient(path, cfg));
          break;
        case 'retriever':
          this.retrieverIngredient(path, cfg);
          retrievers.push({ path, cfg });
          break;
        case 'scenario':
          this.scenarioIngredient(path, ingName, cfg);
          break;
        case 'skill':
          if (ingName === '') {
            this.required(`${path}.name`, 'a skill ingredient needs the name of a skill');
            break;
          }
          this.skill(`${path}.name`, ingName, required);
          break;
      }
    });

    // A built-in retriever names an embedding and a vector store among the agent's own ingredients.
    for (const r of retrievers) {
      const p = strOf(r.cfg.provider);
      if (p !== '' && p !== 'built-in') continue; // an external RAG provider keeps its own
      const eref = strOf(r.cfg.embedding_ref);
      if (eref !== '') {
        const names = embeddings.map((e) => e.providerName);
        // An embedding whose provider's kind could not be looked up (the providers are unknown) may be the one meant.
        const found = embeddings.some((e) => e.providerName === eref || e.kind === undefined || `${e.kind}/${e.model}` === eref || e.kind === eref);
        if (!found) this.notFound(`${r.path}.config.embedding_ref`, 'embedding', eref, '', sorted(names));
      }
      const vref = strOf(r.cfg.vectorstore_ref);
      if (vref !== '') {
        const names = vectorstores.flatMap((v) => [v.backend, v.index]);
        const found = vectorstores.some((v) => v.backend === vref || v.index === vref);
        if (!found) this.notFound(`${r.path}.config.vectorstore_ref`, 'vectorstore', vref, '', sorted([...new Set(names.filter((n) => n !== ''))]));
      }
    }
  }

  private modelIngredient(path: string, cfg: Obj): void {
    const cpath = `${path}.config`;
    const provider = this.cfgString(cpath, cfg, 'provider').value;
    const model = this.cfgString(cpath, cfg, 'model').value;
    if (provider === '') {
      this.add({ path: `${cpath}.provider`, code: 'required', severity: this.gapSeverity(), message: `${cpath}.provider: a model ingredient needs the provider that serves it` });
      return;
    }
    const p = this.provider(`${cpath}.provider`, provider);
    if (!p) return;
    if (model === '') {
      this.add({ path: `${cpath}.model`, code: 'required', severity: this.gapSeverity(), within: provider, message: `${cpath}.model: provider ${quote(provider)} is set and needs a model` });
      return;
    }
    this.model(`${cpath}.model`, p, model);
    this.cfgFloat(cpath, cfg, 'temperature', 0, 2);
    this.cfgInt(cpath, cfg, 'max_tokens', 1, 10_000_000);
  }

  private promptIngredient(path: string, ingName: string, cfg: Obj): void {
    const text = this.cfgString(`${path}.config`, cfg, 'text').value;
    const tmpl = this.cfgString(`${path}.config`, cfg, 'template').value;
    if (text !== '' || tmpl !== '') return; // the prompt is written here, there is nothing to look up
    let name = ingName;
    const n = this.cfgString(`${path}.config`, cfg, 'prompt_name');
    if (n.ok && n.value !== '') name = n.value;
    const field = has(cfg, 'prompt_name') ? `${path}.config.prompt_name` : `${path}.name`;
    if (name === '') {
      this.required(`${path}.name`, "a prompt ingredient needs the prompt's name, or the prompt written in config.text");
      return;
    }
    const prompts = this.c.prompts;
    if (!prompts) return;
    if (!has(prompts, name)) {
      this.notFound(field, 'prompt', name, '', sorted(Object.keys(prompts)));
      return;
    }
    const latest = prompts[name];
    const v = cfg.version;
    if (present(v)) {
      if (typeof v !== 'number' || !Number.isInteger(v)) {
        this.wrongType(`${path}.config.version`, 'a whole number', v);
      } else if (v < 1 || v > latest) {
        this.add({ path: `${path}.config.version`, code: 'not_found', kind: 'prompt', value: trimFloat(v), within: name, message: `${path}.config.version: prompt ${quote(name)} has versions 1 to ${latest}, not ${trimFloat(v)}` });
      }
    }
  }

  private embeddingIngredient(path: string, cfg: Obj): EmbeddingFacts {
    const cpath = `${path}.config`;
    const provider = this.cfgString(cpath, cfg, 'provider').value;
    const model = this.cfgString(cpath, cfg, 'model').value;
    const facts: EmbeddingFacts = { providerName: provider, model, kind: this.c.providers ? '' : undefined };
    this.cfgInt(cpath, cfg, 'dimensions', 1, MAX_EMBEDDING_DIMS);
    this.cfgInt(cpath, cfg, 'batch_size', 1, MAX_EMBEDDING_BATCH);
    if (provider === '') {
      this.add({ path: `${cpath}.provider`, code: 'required', message: `${cpath}.provider: an embedding ingredient needs the provider that serves it` });
      return facts;
    }
    const p = this.provider(`${cpath}.provider`, provider);
    if (p) facts.kind = p.kind;
    return facts;
  }

  private vectorStoreIngredient(path: string, cfg: Obj): { backend: string; index: string } {
    const cpath = `${path}.config`;
    let backend = this.cfgString(cpath, cfg, 'backend').value;
    let index = this.cfgString(cpath, cfg, 'index').value;
    this.cfgInt(cpath, cfg, 'dimensions', 1, MAX_EMBEDDING_DIMS);
    if (backend === '') backend = 'embedded';
    if (!VECTOR_BACKENDS.includes(backend)) this.notFound(`${cpath}.backend`, 'vectorstore_backend', backend, '', VECTOR_BACKENDS);
    if (index === '' && backend !== 'embedded') this.required(`${cpath}.index`, 'a vector store other than the embedded one needs an index');
    if (index === '') index = 'default';
    return { backend, index };
  }

  private retrieverIngredient(path: string, cfg: Obj): void {
    const cpath = `${path}.config`;
    this.cfgInt(cpath, cfg, 'top_k', 1, MAX_TOP_K);
    this.cfgFloat(cpath, cfg, 'score_threshold', 0, 1);
    if (present(cfg.hybrid_search) && typeof cfg.hybrid_search !== 'boolean') this.wrongType(`${cpath}.hybrid_search`, 'true or false', cfg.hybrid_search);
  }

  private scenarioIngredient(path: string, ingName: string, cfg: Obj): void {
    const cpath = `${path}.config`;
    let scenario = ingName;
    const s = this.cfgString(cpath, cfg, 'scenario');
    if (s.ok && s.value !== '') scenario = s.value;
    this.cfgInt(cpath, cfg, 'rollouts', 1, MAX_SCENARIO_ROLLOUT);
    this.cfgFloat(cpath, cfg, 'min_pass_rate', 0, 1);
    if (scenario === '') {
      this.required(`${cpath}.scenario`, 'a scenario ingredient needs the id of a scenario');
      return;
    }
    const known = this.c.scenarios;
    if (known?.known && !(known.names ?? []).includes(scenario)) {
      this.notFound(has(cfg, 'scenario') ? `${cpath}.scenario` : `${path}.name`, 'scenario', scenario, '', sorted(known.names ?? []));
    }
  }

  // ── a recipe ──

  recipe(r: RecipeConfig): void {
    if (strOf(r.default_environment) !== '') this.fromSet('default_environment', 'environment', strOf(r.default_environment), this.c.environments);
    if (this.o.environment) this.fromSet('environment', 'environment', this.o.environment, this.c.environments);

    const steps = objList(r.steps);
    const names: string[] = [];
    const index = new Map<string, number>();
    steps.forEach((s, i) => {
      const path = `steps[${i}]`;
      const name = strOf(s.name);
      if (name === '') this.required(`${path}.name`, 'a step needs a name');
      else if (index.has(name)) this.add({ path: `${path}.name`, code: 'duplicate', value: name, message: `${path}.name: there is already a step named ${quote(name)}` });
      else {
        index.set(name, i);
        names.push(name);
      }
    });
    const sortedNames = sorted(names);
    steps.forEach((s, i) => this.step(`steps[${i}]`, s, strOf(r.name), sortedNames));
    this.cycles(steps, index);
  }

  private step(path: string, s: Obj, recipeName: string, stepNames: string[]): void {
    const kind = strOf(s.kind);
    const name = strOf(s.name);
    if (kind === '') this.required(`${path}.kind`, 'a step needs a kind');
    else if (!STEP_KINDS.includes(kind)) this.notFound(`${path}.kind`, 'step_kind', kind, '', STEP_KINDS);

    // The agent a step runs. "agent" is the client-facing alias of "agent_ref": when both are given they must agree,
    // or one of them is the typo.
    const agentRef = strOf(s.agent_ref);
    const alias = strOf(s.agent);
    if (alias !== '' && agentRef !== '' && alias !== agentRef) this.invalid(`${path}.agent`, alias, `disagrees with agent_ref ${quote(agentRef)}`);
    const ref = agentRef !== '' ? agentRef : alias;
    const refPath = agentRef === '' ? `${path}.agent` : `${path}.agent_ref`;
    if (ref !== '') this.agentRef(refPath, ref);
    else if (kind === 'agent' || kind === 'map') this.required(`${path}.agent_ref`, `a ${kind} step needs the agent to run`);

    if (kind === 'sub_recipe') {
      const rr = strOf(s.recipe_ref);
      if (rr === '') this.required(`${path}.recipe_ref`, 'a sub_recipe step needs the recipe to run');
      else if (!(rr === recipeName && recipeName !== '')) this.fromSet(`${path}.recipe_ref`, 'recipe', rr, this.c.recipes);
      // A recipe nesting itself exists once it is saved, and the server refuses the loop with the path it makes.
    }
    if (kind === 'map' && strOf(s.source_path) === '') this.required(`${path}.source_path`, 'a map step needs the path of the list it runs over');

    const depends = strList(s.depends_on);
    depends.forEach((dep, j) => {
      const p = `${path}.depends_on[${j}]`;
      if (dep === name && name !== '') this.invalid(p, dep, 'a step cannot wait for itself');
      else if (!stepNames.includes(dep)) this.notFound(p, 'step', dep, '', stepNames);
    });
    this.dupes(`${path}.depends_on`, depends);
    objList(s.branches).forEach((b, j) => {
      const next = strOf(b.next_step);
      if (next !== '' && !stepNames.includes(next)) this.notFound(`${path}.branches[${j}].next_step`, 'step', next, '', stepNames);
    });
    const defaultNext = strOf(s.default_next);
    if (defaultNext !== '' && !stepNames.includes(defaultNext)) this.notFound(`${path}.default_next`, 'step', defaultNext, '', stepNames);
    const config: Obj = isObj(s.config) ? s.config : {};
    const qf = strOf(config.question_from);
    if (qf !== '' && !stepNames.includes(qf)) this.notFound(`${path}.config.question_from`, 'step', qf, '', stepNames);

    this.rangeInt(`${path}.max_retries`, numOf(s.max_retries), 0, MAX_STEP_RETRIES);
    this.rangeInt(`${path}.timeout_secs`, numOf(s.timeout_secs), 0, MAX_STEP_TIMEOUT);
    this.rangeInt(`${path}.max_iterations`, numOf(s.max_iterations), 0, MAX_STEP_ITERATIONS);
    this.rangeInt(`${path}.max_concurrency`, numOf(s.max_concurrency), 0, MAX_STEP_CONCURRENT);
    if (strOf(s.loop_condition) !== '' && numOf(s.max_iterations) <= 0) this.required(`${path}.max_iterations`, 'a loop_condition needs max_iterations, the hard cap on the loop');

    // Who is told, and who may decide.
    const notify = strList(s.notify_tools);
    notify.forEach((t, j) => this.tool(`${path}.notify_tools[${j}]`, t, 'notify', true));
    this.dupes(`${path}.notify_tools`, notify);
    if (kind === 'notify') this.channels(path, config);
    const authKeyRef = strOf(s.auth_key_ref);
    if (authKeyRef !== '') this.fromSet(`${path}.auth_key_ref`, 'credential', authKeyRef, this.c.credentials);
    const approverRoles = strList(s.approver_roles);
    const known = this.c.roles;
    approverRoles.forEach((role, j) => {
      if (known?.known && !containsFold(known.names ?? [], role) && role.toLowerCase() !== 'admin') {
        this.notFound(`${path}.approver_roles[${j}]`, 'role', role, '', sorted(known.names ?? []));
      }
    });
    this.dupes(`${path}.approver_roles`, approverRoles.map((r) => r.trim().toLowerCase())); // a role is the same role in any case
    const emails = strList(s.approver_emails);
    emails.forEach((e, j) => {
      const at = e.indexOf('@');
      if (at < 1 || at === e.length - 1 || /[ ,;]/.test(e)) this.invalid(`${path}.approver_emails[${j}]`, e, 'is not an e-mail address');
    });
    this.dupes(`${path}.approver_emails`, emails);
    const domain = strOf(s.approver_domain);
    if (domain.includes('@')) this.invalid(`${path}.approver_domain`, domain, 'is a domain such as "acme.com", without "@"');
  }

  /** The channels a notify step sends to (config.channels). */
  private channels(path: string, config: Obj): void {
    const raw = config.channels;
    if (!present(raw)) return;
    if (!Array.isArray(raw)) {
      this.wrongType(`${path}.config.channels`, 'a list of channel names', raw);
      return;
    }
    const list: string[] = [];
    raw.forEach((c, j) => {
      if (typeof c !== 'string') this.wrongType(`${path}.config.channels[${j}]`, 'a channel name', c);
      else list.push(c);
    });
    list.forEach((name, j) => {
      if (name.trim() !== '') this.fromSet(`${path}.config.channels[${j}]`, 'channel', name.trim(), this.c.channels);
    });
    this.dupes(`${path}.config.channels`, list);
  }

  /** A loop in the steps' dependencies, reported once at the step where it is found. */
  private cycles(steps: Obj[], index: Map<string, number>): void {
    const UNSEEN = 0;
    const WALKING = 1;
    const DONE = 2;
    const state = new Array<number>(steps.length).fill(UNSEEN);
    const stack: string[] = [];
    let reported = false;
    const visit = (i: number): void => {
      if (reported) return;
      state[i] = WALKING;
      stack.push(strOf(steps[i].name));
      for (const dep of strList(steps[i].depends_on)) {
        const j = index.get(dep);
        if (j === undefined || dep === strOf(steps[i].name)) continue;
        if (state[j] === WALKING) {
          let loop = [...stack];
          while (loop.length > 0 && loop[0] !== dep) loop = loop.slice(1);
          loop.push(dep);
          this.add({ path: `steps[${i}].depends_on`, code: 'invalid_value', value: dep, message: `steps[${i}].depends_on: the steps wait for each other in a circle: ${loop.join(' -> ')}` });
          reported = true;
          return;
        }
        if (state[j] === UNSEEN) visit(j);
      }
      stack.pop();
      state[i] = DONE;
    };
    steps.forEach((s, i) => {
      if (state[i] === UNSEEN && strOf(s.name) !== '') visit(i);
    });
  }
}

interface EmbeddingFacts {
  providerName: string;
  model: string;
  /** The provider's kind; '' when the provider is not in the catalog; undefined when the providers are unknown. */
  kind: string | undefined;
}

function toolPath(path: string, cfg: Obj): string {
  return strOf(cfg.tool_name) !== '' ? `${path}.config.tool_name` : `${path}.name`;
}

function hasModel(a: AgentConfig): boolean {
  if (strOf(a.model_provider) !== '') return true;
  return objList(a.ingredients).some((i) => i.kind === 'model');
}

function containsFold(list: readonly string[], s: string): boolean {
  return list.some((x) => x.trim().toLowerCase() === s.trim().toLowerCase());
}

/**
 * Check an agent's configuration against what the kitchen has. The agent is judged as it is given: to judge what the
 * next bake would run, pass it with its draft applied (`withDraft`) and `bake: true`.
 */
export function checkAgent(agent: AgentConfig, catalog: Catalog, opts: CheckOptions = {}): Problem[] {
  const ck = new Checker(catalog, opts);
  ck.agent(agent);
  return ck.out;
}

/**
 * Check a recipe against what the kitchen has: the agents its steps run, the steps they wait for, the recipes it nests,
 * the channels it notifies, the credentials it signs in with, the roles that may approve its gates, the environment it
 * runs in.
 */
export function checkRecipe(recipe: RecipeConfig, catalog: Catalog, opts: CheckOptions = {}): Problem[] {
  const ck = new Checker(catalog, opts);
  ck.recipe(recipe);
  return ck.out;
}

// ── an agent's edits and draft (what a PUT or a bake would leave) ──

// The optional settings an explicit null clears (the server's clearableFields). The lists that clear with `[]` are not
// here: a null list is "no opinion" there, as it always was.
const CLEARABLE_BY_NULL = ['description', 'framework', 'backup_provider', 'backup_model', 'execution_mode', 'max_turns', 'bot',
  'ingredients', 'context_budget', 'reasoning_strategy', 'summary_provider', 'summary_model'] as const;

/**
 * The agent with `edit` laid over it, the way the server applies an edit: what the edit sets wins, what it leaves out
 * stays. `""` is "not part of this edit" for every field except `summary_provider` and `summary_model`, where it clears
 * the setting; `[]` clears `skills`, `approval_tools`, `subagents` and `guardrails`, but not `ingredients`. A field sent
 * as `null` is cleared, for the optional settings (`backup_provider`, `backup_model`, `description`, `max_turns`, ...).
 */
export function applyEdit(agent: AgentConfig, edit: AgentEdit): AgentConfig {
  const out: AgentConfig = { ...agent };
  for (const f of ['description', 'framework', 'model_provider', 'model_name', 'backup_provider', 'backup_model', 'mode', 'execution_mode', 'behavior', 'runtime', 'reasoning_strategy'] as const) {
    if (strOf(edit[f]) !== '') out[f] = edit[f] as string;
  }
  if (numOf(edit.max_turns) > 0) out.max_turns = edit.max_turns as number;
  if (numOf(edit.context_budget) > 0) out.context_budget = edit.context_budget as number;
  if (isObj(edit.bot)) out.bot = edit.bot;
  if (Array.isArray(edit.ingredients) && edit.ingredients.length > 0) out.ingredients = edit.ingredients;
  if (Array.isArray(edit.skills)) out.skills = edit.skills;
  if (Array.isArray(edit.guardrails)) out.guardrails = edit.guardrails;
  if (typeof edit.summary_provider === 'string') out.summary_provider = edit.summary_provider;
  if (typeof edit.summary_model === 'string') out.summary_model = edit.summary_model;
  if (Array.isArray(edit.approval_tools)) out.approval_tools = edit.approval_tools;
  if (Array.isArray(edit.subagents)) out.subagents = edit.subagents;
  for (const f of CLEARABLE_BY_NULL) {
    if (edit[f] === null) delete out[f];
  }
  return out;
}

/** The agent as it will be once its draft is baked (a baked agent keeps edits in `draft`). */
export function withDraft(agent: AgentConfig): AgentConfig {
  const { draft, ...rest } = agent;
  return isObj(draft) ? applyEdit(rest, draft as AgentEdit) : rest;
}

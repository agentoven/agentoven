// What a configuration may refer to, and what to do when it refers to something that is not there.
//
// An agent names a provider and a model, skills, tools, other agents; a recipe names agents, recipes,
// channels, credentials, environments. Every such field is a dropdown fed from the kitchen's own lists, so
// this file holds the pure part: turning a list into options with their state (a disabled tool, an
// unapproved skill, a retired agent), finding the value that no longer names anything, and the
// "did you mean" the server uses (a port of control-plane/pkg/refcheck/nearest.go, pinned to it by the cases
// in sdk/fixtures/refcheck-cases.json). It imports only types, so it runs under node:test.
//
// The server stays the authority: nothing here refuses a write, it only keeps the form from offering or
// keeping what the kitchen does not have, and a list that could not be read is "unknown", never "empty".

import type { Problem } from '../api.ts';

// ── Options ───────────────────────────────────────────────────

/** Why an option needs a word beside it. */
export type OptionState = 'disabled' | 'unapproved' | 'unhealthy' | 'retired' | 'cooled' | 'burnt' | 'pending' | 'inactive';

export const STATE_TEXT: Record<OptionState, string> = {
  disabled: 'disabled',
  unapproved: 'not approved',
  unhealthy: 'last test failed',
  retired: 'retired',
  cooled: 'cooled',
  burnt: 'failed to bake',
  pending: 'pending',
  inactive: 'inactive',
};

export interface RefOption {
  value: string;
  label: string;
  group?: string;
  state?: OptionState;
  detail?: string;
  /** It exists but cannot be picked (a retired agent, a tool without the capability the field needs). */
  disabled?: boolean;
}

/** What a list of options is known to hold. `undefined` options means the list could not be read: nothing is checked against it. */
export type Options = RefOption[] | undefined;

/** Provider kinds that serve model ids their list does not name; the same set as refcheck.OpenModelKinds. */
export const OPEN_MODEL_KINDS = ['openrouter', 'litellm', 'ollama'];

export interface ProviderLike {
  name: string;
  kind: string;
  models?: string[] | null;
  last_test_healthy?: boolean;
  modalities?: string[];
}

/** Whether a provider may serve a model id that is not in its list (refcheck Provider.Open). */
export function isOpenProvider(p: Pick<ProviderLike, 'kind' | 'models'>): boolean {
  return OPEN_MODEL_KINDS.includes(p.kind) || !p.models || p.models.length === 0;
}

export function providerOptions(providers: ProviderLike[]): RefOption[] {
  return providers.map((p) => ({
    value: p.name,
    label: p.name,
    detail: p.kind,
    state: p.last_test_healthy === false ? 'unhealthy' : undefined,
  }));
}

/** The models one provider lists, as options. */
export function modelOptions(p: ProviderLike | undefined): RefOption[] {
  return (p?.models ?? []).map((m) => ({ value: m, label: m }));
}

export interface ToolLike { name: string; enabled: boolean; capabilities?: string[] | null; description?: string }

/** `need` is the capability the field uses the tool for: "tool" to call it, "notify" to send through it. */
export function toolOptions(tools: ToolLike[], need: 'tool' | 'notify' | '' = 'tool'): RefOption[] {
  return tools.map((t) => {
    const capable = !need || (t.capabilities ?? []).includes(need);
    return {
      value: t.name,
      label: t.name,
      state: !t.enabled ? 'disabled' : undefined,
      detail: capable ? t.description : `no ${need} capability`,
      disabled: !capable,
    };
  });
}

export interface SkillLike { name: string; status: string; manifest?: { description?: string } | null }

export function skillOptions(skills: SkillLike[]): RefOption[] {
  return skills.map((s) => ({
    value: s.name,
    label: s.name,
    state: s.status === 'accepted' ? undefined : s.status === 'pending' ? 'pending' : 'unapproved',
    detail: s.status === 'accepted' ? s.manifest?.description : s.status.replace(/_/g, ' '),
  }));
}

export interface AgentLike { name: string; status: string }

/** The agents a field may name. `self` is left out (an agent cannot delegate to itself); retired ones are shown, not pickable. */
export function agentOptions(agents: AgentLike[], self = ''): RefOption[] {
  return agents.filter((a) => a.name !== self).map((a) => ({
    value: a.name,
    label: a.name,
    state: a.status === 'retired' ? 'retired' : a.status === 'cooled' ? 'cooled' : a.status === 'burnt' ? 'burnt' : undefined,
    disabled: a.status === 'retired',
  }));
}

export interface PromptLike { name: string; version: number }

/** One option per prompt name, at its latest version (the list may hold every version). */
export function promptOptions(prompts: PromptLike[]): RefOption[] {
  const latest = new Map<string, number>();
  for (const p of prompts) latest.set(p.name, Math.max(latest.get(p.name) ?? 0, p.version));
  return [...latest].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)).map(([name, v]) => ({ value: name, label: name, detail: `v${v}` }));
}

/** The versions of a prompt, newest first, from its latest. */
export function promptVersions(options: Options, name: string): number[] {
  const latest = Number((options?.find((o) => o.value === name)?.detail ?? '').replace(/^v/, ''));
  return Number.isInteger(latest) && latest > 0 ? Array.from({ length: latest }, (_, i) => latest - i) : [];
}

export interface RecipeLike { name: string; description?: string; archived_at?: string }

/** The recipes a step may run. An archived recipe cannot run, so it is not offered; `self` is left out. */
export function recipeOptions(recipes: RecipeLike[], self = ''): RefOption[] {
  return recipes.filter((r) => r.name !== self && !r.archived_at).map((r) => ({ value: r.name, label: r.name, detail: r.description || undefined }));
}

export interface ChannelLike { name: string; kind: string; active: boolean }

export function channelOptions(channels: ChannelLike[]): RefOption[] {
  return channels.map((c) => ({
    value: c.name,
    label: c.name,
    detail: c.kind === 'websocket' ? 'live feed' : c.kind,
    state: c.active ? undefined : 'inactive',
    disabled: !c.active, // an inactive channel delivers nothing, so a step is not pointed at it
  }));
}

export function environmentOptions(envs: { slug: string; name?: string; locked?: boolean }[]): RefOption[] {
  return envs.map((e) => ({ value: e.slug, label: e.slug, detail: e.name && e.name !== e.slug ? e.name : undefined }));
}

export function credentialOptions(creds: { name: string }[]): RefOption[] {
  return creds.map((c) => ({ value: c.name, label: c.name }));
}

export function scenarioOptions(scenarios: { id: string; name?: string }[]): RefOption[] {
  return scenarios.map((s) => ({ value: s.id, label: s.name || s.id, detail: s.name && s.name !== s.id ? s.id : undefined }));
}

/** Roles a recipe's approval may name (Pro). `admin` is the retired name of a Head Chef and the server still accepts it. */
export const APPROVER_ROLES = ['head_chef', 'chef', 'baker', 'taster', 'auditor', 'legal', 'finance'];

export function roleOptions(roles: string[] = APPROVER_ROLES): RefOption[] {
  return roles.map((r) => ({ value: r, label: r.replace(/_/g, ' ').replace(/^./, (c) => c.toUpperCase()), detail: r }));
}

/** Vector store backends the server accepts (refcheck vectorBackends). */
export const VECTOR_BACKENDS = ['embedded', 'pgvector', 'pinecone', 'qdrant', 'cosmosdb', 'chroma', 'snowflake', 'databricks'];

export const backendOptions = (): RefOption[] => VECTOR_BACKENDS.map((b) => ({ value: b, label: b }));

// ── Searching and keyboard movement ───────────────────────────

/** The options a search shows: every word typed must be in the label, the value or the detail. */
export function filterOptions(options: RefOption[], query: string): RefOption[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return options;
  return options.filter((o) => {
    const hay = `${o.label} ${o.value} ${o.detail ?? ''} ${o.group ?? ''}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

/** Options in the order of their groups (first appearance), with the group of each. Options with no group come first. */
export function groupOptions(options: RefOption[]): { group: string; options: RefOption[] }[] {
  const out: { group: string; options: RefOption[] }[] = [];
  for (const o of options) {
    const g = o.group ?? '';
    let bucket = out.find((b) => b.group === g);
    if (!bucket) { bucket = { group: g, options: [] }; out.push(bucket); }
    bucket.options.push(o);
  }
  return out.sort((a, b) => (a.group === '' ? -1 : b.group === '' ? 1 : 0));
}

/** The next active row for an arrow key, skipping rows that cannot be picked; -1 when none can. */
export function stepActive(options: RefOption[], from: number, dir: 1 | -1 | 'first' | 'last'): number {
  const ok = (i: number) => i >= 0 && i < options.length && !options[i].disabled;
  if (dir === 'first') return options.findIndex((o) => !o.disabled);
  if (dir === 'last') { for (let i = options.length - 1; i >= 0; i--) if (ok(i)) return i; return -1; }
  for (let i = from + dir; i >= 0 && i < options.length; i += dir) if (ok(i)) return i;
  return ok(from) ? from : -1;
}

// ── Did you mean ──────────────────────────────────────────────

const MAX_SUGGESTIONS = 3;

const squash = (s: string) => Array.from(s).filter((c) => /[\p{L}\p{Nd}]/u.test(c)).join('');

/** Edit distance where swapping two neighbours is one edit (optimal string alignment), as the server counts it. */
function distance(a: string, b: string): number {
  const ra = Array.from(a);
  const rb = Array.from(b);
  const d: number[][] = Array.from({ length: ra.length + 1 }, (_, i) => [i, ...new Array<number>(rb.length).fill(0)]);
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

/**
 * The candidates closest to `value`, nearest first, at most `limit`. Close means: a few characters apart (a
 * typo), the same once case and punctuation are ignored ("gpt4o" and "gpt-4o"), or one holds the other
 * ("haiku" in "claude-haiku-4-5"). Same rule, same ranking as the server's.
 */
export function nearest(value: string, candidates: string[], limit = MAX_SUGGESTIONS): string[] {
  const v = value.toLowerCase();
  const vn = squash(v);
  if (!vn) return [];
  const allowed = Math.min(3, Math.max(1, Math.floor(Array.from(vn).length / 4)));
  const seen = new Set<string>();
  const close: { name: string; dist: number }[] = [];
  for (const c of candidates) {
    if (seen.has(c)) continue;
    seen.add(c);
    const cl = c.toLowerCase();
    const cn = squash(cl);
    if (!cn) continue;
    let d = Math.min(distance(v, cl), distance(vn, cn));
    if (d > allowed && Array.from(vn).length >= 3 && Array.from(cn).length >= 3 && (cn.includes(vn) || vn.includes(cn))) d = allowed;
    if (d <= allowed) close.push({ name: c, dist: d });
  }
  close.sort((a, b) => a.dist - b.dist || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0));
  return close.slice(0, limit > 0 ? limit : undefined).map((c) => c.name);
}

// ── A value that names nothing ────────────────────────────────

/** A value the kitchen does not have, with what it was probably meant to be. */
export interface Unresolved {
  value: string;
  suggestions: string[];
  /** What is wrong with it, in words: "not found" for something that does not exist, "retired" for one that cannot be used. */
  why: string;
}

/**
 * The reason a chosen value is not a valid choice, or null when it is fine or cannot be told (the list is
 * unknown). An empty value is "nothing chosen", which is not an error here: whether the field needs one is
 * the form's rule.
 */
export function unresolved(value: string, options: Options): Unresolved | null {
  if (!value || !options) return null;
  const found = options.find((o) => o.value === value);
  if (!found) return { value, why: 'not found', suggestions: nearest(value, options.filter((o) => !o.disabled).map((o) => o.value)) };
  if (found.disabled) return { value, why: found.state ? STATE_TEXT[found.state] : 'cannot be used', suggestions: nearest(value, options.filter((o) => !o.disabled && o.value !== value).map((o) => o.value)) };
  return null;
}

export function unresolvedMany(values: string[] | undefined, options: Options): Unresolved[] {
  return (values ?? []).map((v) => unresolved(v, options)).filter((u): u is Unresolved => u !== null);
}

/** Drops repeats, keeping the first of each (a list field holds each name once). */
export function dedupe(values: string[]): string[] {
  return [...new Set(values)];
}

/** Adds a value to a list field if it is not there. */
export function addValue(values: string[], v: string): string[] {
  return v && !values.includes(v) ? [...values, v] : values;
}

// ── A provider and a model together ───────────────────────────

export interface ModelChoice { provider: string; model: string }

/** Which half of a provider/model pair is wrong. */
export interface ModelUnresolved { field: 'provider' | 'model'; value: string; suggestions: string[]; why: string }

/**
 * What is wrong with a provider/model pair, or null. A provider the kitchen does not have is wrong; so is a
 * model its list does not hold, unless the provider is open (a custom id is then allowed, and the server only
 * warns). `providers` undefined means they could not be read. With `providerOptional` a model that names no provider
 * is looked up among all of them.
 */
export function unresolvedModel(choice: ModelChoice, providers: ProviderLike[] | undefined, providerOptional = false): ModelUnresolved | null {
  if (!providers) return null;
  if (!choice.provider) {
    // A model with no provider (the summarizer's, which may leave it out) is looked up among every provider's list.
    if (!providerOptional || !choice.model || providers.some((x) => isOpenProvider(x) || x.models?.includes(choice.model))) return null;
    return { field: 'model', value: choice.model, why: 'not found', suggestions: nearest(choice.model, providers.flatMap((x) => x.models ?? [])) };
  }
  const p = providers.find((x) => x.name === choice.provider);
  if (!p) return { field: 'provider', value: choice.provider, why: 'not found', suggestions: nearest(choice.provider, providers.map((x) => x.name)) };
  if (!choice.model || isOpenProvider(p) || p.models?.includes(choice.model)) return null;
  return { field: 'model', value: choice.model, why: 'not found', suggestions: nearest(choice.model, p.models ?? []) };
}

// ── Messages from the server ──────────────────────────────────

/** What a refused configuration (422 `invalid_configuration`) carries. */
export interface ConfigurationErrors { problems: Problem[]; warnings: Problem[] }

const isProblem = (p: unknown): p is Problem =>
  typeof p === 'object' && p !== null && typeof (p as Problem).path === 'string' && typeof (p as Problem).message === 'string';

/** The problems in an error body, or null when the body is not the server's `invalid_configuration` answer. */
export function configurationErrors(body: unknown): ConfigurationErrors | null {
  if (typeof body !== 'object' || body === null || (body as { code?: unknown }).code !== 'invalid_configuration') return null;
  const { problems, warnings } = body as { problems?: unknown; warnings?: unknown };
  const list = (v: unknown) => (Array.isArray(v) ? v.filter(isProblem) : []);
  return { problems: list(problems), warnings: list(warnings) };
}

/** Problems whose path is exactly `path`. */
export function problemsAt(problems: Problem[] | undefined, path: string): Problem[] {
  return (problems ?? []).filter((p) => p.path === path);
}

/** Problems at `path` or below it (`ingredients[2]` holds `ingredients[2].config.model`). */
export function problemsUnder(problems: Problem[] | undefined, path: string): Problem[] {
  return (problems ?? []).filter((p) => p.path === path || p.path.startsWith(`${path}.`) || p.path.startsWith(`${path}[`));
}

/** One line for a problem, with the nearest values the server found. */
export function describeProblem(p: Problem): string {
  const tail = p.suggestions?.length && !/did you mean/.test(p.message) ? ` (did you mean ${p.suggestions.map((s) => `"${s}"`).join(', ')}?)` : '';
  return `${p.message}${tail}`;
}

/**
 * Where a problem's path points in a list the form has built: `ingredients[3]` is the row whose index in the
 * submitted list was 3. `rows` maps a row to the index it was sent at (rows left out of the payload are -1).
 */
export function rowOfPath(path: string, field: string, rows: number[]): number {
  const m = new RegExp(`^${field}\\[(\\d+)\\]`).exec(path);
  return m ? rows.indexOf(Number(m[1])) : -1;
}

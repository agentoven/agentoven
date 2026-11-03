/**
 * AgentOven Pro REST client — TypeScript.
 *
 * Covers all Pro-only API surfaces: kitchens, users, guardrails, environments,
 * promotions, sessions, service accounts, schedules, test suites, workloads,
 * audit events, and the traceability matrix.
 *
 * The native napi-rs `AgentOvenClient` handles the core OSS operations (agent
 * register/bake/cool, recipes, providers). This class wraps it and adds the
 * missing Pro surface as plain `fetch` calls, so both can be used together.
 *
 * @example
 * ```ts
 * import { ProClient } from '@agentoven/sdk';
 *
 * const client = new ProClient({
 *   url: 'https://agentoven.example.com',
 *   apiKey: process.env.AGENTOVEN_API_KEY,
 *   kitchen: 'payments',
 * });
 *
 * const info = await client.serverInfo();
 * const guardrails = await client.listGuardrails();
 * await client.createSchedule({ recipe_name: 'daily-report', cron: '0 8 * * MON-FRI' });
 * ```
 */

import { Blobs } from './blobs.js';
import { Tasks } from './tasks.js';
import { Flow } from './flow.js';
import { audioClipFromWire, audioFromBytes } from './media.js';
import { resolveModalities, supportsModality, type AnyModality } from './modalities.js';
import { connectRealtime, type RealtimeSession, type WebSocketFactory } from './realtime.js';
import type {
  AgentCard,
  AgentEnvironment,
  AgentOvenClientOptions,
  AuditEvent,
  CreateGuardrailRequest,
  CreateProviderRequest,
  CreateScheduleRequest,
  CreateServiceAccountResponse,
  Deployment,
  Environment,
  EpisodeRecord,
  Guardrail,
  GuardrailException,
  GuardrailKinds,
  GuardrailTestResult,
  TestGuardrailsOptions,
  InvokeOptions,
  InvokeResult,
  Kitchen,
  KitchenMember,
  NotificationChannel,
  Promotion,
  Provider,
  RealtimeToken,
  Recipe,
  RegisterToolRequest,
  RecipeRun,
  Schedule,
  Scenario,
  ScenarioRun,
  ScenarioRunRequest,
  ScopedAPIKey,
  SendSessionMessageOptions,
  ServerInfo,
  ServiceAccount,
  Session,
  SessionMessageResponse,
  TestRun,
  TestSuite,
  Tool,
  TraceabilityMatrix,
  UpdateProviderRequest,
  UpdateProviderResponse,
  UpsertAgentEnvironmentRequest,
  User,
  UserRole,
  KitchenRole,
  Workload,
  WorldSchema,
} from './types.js';

export class AgentOvenAPIError extends Error {
  constructor(
    public readonly statusCode: number,
    public readonly detail: unknown,
  ) {
    super(`AgentOven API error ${statusCode}: ${JSON.stringify(detail)}`);
    this.name = 'AgentOvenAPIError';
  }
}

/**
 * The agent's provider does not offer the `audio` modality, so it cannot take
 * or give speech (HTTP 400). Enable audio on the provider, or move the agent to
 * one that supports it — see `agentCard(name).modalities`.
 */
export class AudioModalityError extends AgentOvenAPIError {
  constructor(statusCode: number, detail: unknown) {
    super(statusCode, detail);
    this.name = 'AudioModalityError';
  }

  /** The server's explanation. */
  get reason(): string {
    return errorText(this.detail);
  }
}

/**
 * This account signs through the identity provider (OIDC), which re-asks for the password in a browser. The SDK
 * can sign with a local account's password only; sign in the dashboard, or use a local account.
 */
export class OidcSigningRequired extends AgentOvenAPIError {
  constructor(statusCode: number, detail: unknown) {
    super(statusCode, detail);
    this.name = 'OidcSigningRequired';
  }

  /** The server's explanation. */
  get reason(): string {
    const d = this.detail as { message?: unknown } | null;
    return d && typeof d === 'object' && d.message ? String(d.message) : errorText(this.detail);
  }
}

/** The fields of a refusal, from either shape the server uses: the API's flat object or the gateway's `{ error: {...} }`. */
function refusalFields(detail: unknown): Record<string, unknown> {
  if (detail && typeof detail === 'object') {
    const inner = (detail as { error?: unknown }).error;
    return inner && typeof inner === 'object' ? (inner as Record<string, unknown>) : (detail as Record<string, unknown>);
  }
  return {};
}

/**
 * A rate limit refused the request (HTTP 429, Pro ADR-0051): nothing was done, and the same request is expected to
 * be admitted after `retryAfter` seconds (the `Retry-After` header). `scope` is what is limited (`kitchen`, `agent`,
 * `api_key` or `provider`) and `limit` the dimension that ran out. The SDK does not retry for you.
 */
export class RateLimitedError extends AgentOvenAPIError {
  /** Seconds to wait: the `Retry-After` header, else the body's `retry_after`. */
  readonly retryAfter?: number;
  constructor(statusCode: number, detail: unknown, retryAfterHeader?: number) {
    super(statusCode, detail);
    this.name = 'RateLimitedError';
    const body = refusalFields(detail).retry_after;
    this.retryAfter = retryAfterHeader ?? (typeof body === 'number' ? body : undefined);
  }
  get scope(): string | undefined { return refusalFields(this.detail).scope as string | undefined; }
  get limit(): string | undefined { return refusalFields(this.detail).limit as string | undefined; }
  get reason(): string { return String(refusalFields(this.detail).message ?? errorText(this.detail)); }
}

/**
 * A budget is used up (HTTP 402): the call was not sent. Raised for the gateway's `budget_exceeded` and for a run
 * that stopped at a budget (`run_budget_exhausted`). `scope` names the budget (`run`, `kitchen`, `provider`, `agent`,
 * `api_key`), `unit` is `usd` or `tokens`, and `period` with `resetsAt` say when a period budget starts again (a
 * person can raise it sooner: `setBudget`).
 */
export class BudgetExceededError extends AgentOvenAPIError {
  constructor(statusCode: number, detail: unknown) {
    super(statusCode, detail);
    this.name = 'BudgetExceededError';
  }
  get scope(): string | undefined { return refusalFields(this.detail).scope as string | undefined; }
  get unit(): string | undefined {
    const f = refusalFields(this.detail);
    if (f.unit) return String(f.unit);
    return ({ cost: 'usd', tokens: 'tokens' } as Record<string, string>)[String(f.dimension ?? '')];
  }
  get period(): string | undefined { return refusalFields(this.detail).period as string | undefined; }
  get resetsAt(): string | undefined { return refusalFields(this.detail).resets_at as string | undefined; }
  get reason(): string { return String(refusalFields(this.detail).message ?? errorText(this.detail)); }
}

/** The error for a non-2xx reply: a typed one for a rate limit and for a budget that is used up. */
function apiError(status: number, detail: unknown, headers: Headers): AgentOvenAPIError {
  const f = refusalFields(detail);
  const retryAfter = headers.get('retry-after');
  if (status === 429 && (f.error === 'rate_limited' || f.code === 'rate_limit_exceeded' || retryAfter !== null)) {
    const n = retryAfter === null ? NaN : Number.parseInt(retryAfter, 10);
    return new RateLimitedError(status, detail, Number.isNaN(n) ? undefined : n);
  }
  if (status === 402 && (f.type === 'budget_exceeded' || f.code === 'budget_exceeded' || f.error === 'budget_exceeded' || f.error === 'run_budget_exhausted')) {
    return new BudgetExceededError(status, detail);
  }
  return new AgentOvenAPIError(status, detail);
}

/** The header that carries a signing token on a decision call (the control plane's `signatures.Header`). */
export const SIGNING_TOKEN_HEADER = 'X-Signing-Token';

/**
 * The roles a person or credential can hold inside one kitchen. `restaurateur` is platform-wide, so it is a role of
 * a person in the user directory, never of a kitchen member, binding or credential. The retired `admin` is no role:
 * it was a Head Chef inside a kitchen and a Restaurateur across the installation.
 */
export const KITCHEN_ROLES: readonly KitchenRole[] = ['head_chef', 'chef', 'baker', 'taster', 'auditor', 'legal', 'finance', 'viewer'];
/** The roles of a person in the user directory: the kitchen roles and the platform-wide `restaurateur`. */
export const PERSON_ROLES: readonly UserRole[] = ['restaurateur', ...KITCHEN_ROLES];

/**
 * The gates a decision can be signed at (Pro, ADR-0041). `legal_hold_release` lifts a legal hold (a dataset's or the
 * audit trail's) and is signed by the legal team; `approval_skip` runs a recipe on an environment that requires
 * approval, without that approval.
 */
export const SIGNING_GATES = [
  'tool_approval', 'budget_extension', 'resume_guidance', 'recipe_gate', 'wait_release',
  'environment_promotion', 'skill_approval', 'bot_action', 'live_tool_replay',
  'dataset_publish', 'dataset_purge', 'legal_hold_release', 'approval_skip',
  'secret_binding', 'secret_migrate', 'budget_raise',
] as const;

/** What a budget is measured in, the periods it resets on, what a rate limit counts, and the cache's modes (Pro, ADR-0051). */
export const BUDGET_UNITS = ['usd', 'tokens'] as const;
export const BUDGET_PERIODS = ['daily', 'weekly', 'monthly', 'lifetime'] as const;
export const LIMIT_SCOPES = ['kitchen', 'agent', 'api_key', 'provider'] as const;
export const CACHE_MODES = ['off', 'deterministic', 'always'] as const;

const RETENTION_MODES = ['expire', 'persist', 'regulated'];
const REPLAY_TOOL_MODES: ReplayToolMode[] = ['frozen', 'blocked', 'live'];

/** Refuse a role the control plane does not have. */
function checkRole(role: string, allowed: readonly string[], what: string): void {
  if (role === 'admin') {
    throw new Error(
      `role 'admin' is retired (${what}): use 'head_chef' for a kitchen role` +
        (allowed.includes('restaurateur') ? " or 'restaurateur' for a person across the installation" : ''),
    );
  }
  if (!allowed.includes(role)) {
    const hint = role === 'restaurateur' ? " ('restaurateur' is platform-wide: assign it in the user directory)" : '';
    throw new Error(`unknown role ${JSON.stringify(role)} for ${what}: one of ${allowed.join(', ')}${hint}`);
  }
}

function checkRetentionMode(mode: string | undefined): void {
  if (mode !== undefined && !RETENTION_MODES.includes(mode)) throw new Error(`retention_mode is one of ${RETENTION_MODES.join(', ')}, not ${JSON.stringify(mode)}`);
}

function signingRequest(gate: string, recordId: string, meaning: string, payload?: Record<string, unknown>) {
  if (meaning !== 'approved' && meaning !== 'rejected') throw new Error(`meaning is 'approved' or 'rejected', not ${JSON.stringify(meaning)}`);
  return { gate, record_id: recordId, meaning, payload: payload ?? {} };
}

/** A query string (with its `?`) of the parameters that are set, or an empty one. */
function queryString(params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== '') q.set(k, String(v));
  const qs = q.toString();
  return qs ? `?${qs}` : '';
}

/** The fields that say which budget: at most one of provider, agent and apiKey, and the unit. */
function budgetRef(o: BudgetScope): Record<string, string> {
  const named: Record<string, string> = {};
  if (o.provider) named.provider = o.provider;
  if (o.agent) named.agent = o.agent;
  if (o.apiKey) named.api_key = o.apiKey;
  if (Object.keys(named).length > 1) throw new Error('a budget counts the kitchen or one provider, one agent or one API key, not a mix');
  const unit = o.unit ?? 'usd';
  if (!(BUDGET_UNITS as readonly string[]).includes(unit)) throw new Error(`unit must be one of ${BUDGET_UNITS.join(', ')}`);
  return { ...named, unit };
}

/**
 * The record id a signature on a budget change covers: `<scope>:<name>:<unit>`, for example `provider:openai:usd`,
 * `agent:triage:tokens` or `kitchen::usd` (the name is empty for the kitchen).
 */
export function budgetRecordId(o: BudgetScope = {}): string {
  const ref = budgetRef(o);
  for (const scope of ['provider', 'agent', 'api_key']) if (ref[scope]) return `${scope}:${ref[scope]}:${ref.unit}`;
  return `kitchen::${ref.unit}`;
}

const NO_AUDIO = 'does not offer the audio modality';

function errorText(detail: unknown): string {
  if (typeof detail === 'object' && detail !== null) {
    const d = detail as { error?: unknown; message?: unknown };
    return String(d.error ?? d.message ?? JSON.stringify(detail));
  }
  return String(detail);
}

const seg = encodeURIComponent;

/** How long one request may take before it is abandoned (`timeoutMs` in the client options changes it). */
export const DEFAULT_TIMEOUT_MS = 120_000;

export class ProClient {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly kitchen: string;
  private readonly timeoutMs: number;

  constructor(options: AgentOvenClientOptions = {}) {
    this.baseUrl = (options.url ?? 'http://localhost:8080').replace(/\/$/, '');
    this.apiKey = options.apiKey;
    this.kitchen = options.kitchen ?? 'default';
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  }

  /** Returns a cloned client bound to a specific kitchen. */
  withKitchen(kitchen: string): ProClient {
    return new ProClient({
      url: this.baseUrl,
      apiKey: this.apiKey,
      kitchen,
      timeoutMs: this.timeoutMs,
    });
  }

  /**
   * Resolve which kitchen contains the recipe by probing candidate kitchens.
   * Returns the first kitchen where GET /recipes/{name} succeeds, else null.
   */
  async resolveRecipeKitchen(recipeName: string, candidateKitchens: string[]): Promise<string | null> {
    for (const kitchen of candidateKitchens) {
      try {
        await this.getRecipe(recipeName, { kitchen });
        return kitchen;
      } catch (err) {
        if (err instanceof AgentOvenAPIError && err.statusCode === 404) {
          continue;
        }
        throw err;
      }
    }
    return null;
  }

  // ── Server info ────────────────────────────────────────────────────────────

  async serverInfo(): Promise<ServerInfo> {
    return this.get('/api/v1/info');
  }

  // ── Kitchen management ─────────────────────────────────────────────────────

  async listKitchens(): Promise<Kitchen[]> {
    return this.get('/api/v1/kitchens');
  }

  async getKitchen(kitchenId: string): Promise<Kitchen> {
    return this.get(`/api/v1/kitchens/${seg(kitchenId)}`);
  }

  async createKitchen(name: string, displayName?: string): Promise<Kitchen> {
    return this.post('/api/v1/kitchens', { name, display_name: displayName ?? name });
  }

  async deleteKitchen(kitchenId: string): Promise<void> {
    await this.delete(`/api/v1/kitchens/${seg(kitchenId)}`);
  }

  async listKitchenMembers(kitchenId: string): Promise<KitchenMember[]> {
    return this.get(`/api/v1/kitchens/${seg(kitchenId)}/members`);
  }

  async addKitchenMember(kitchenId: string, userId: string, role: KitchenRole): Promise<KitchenMember> {
    checkRole(role, KITCHEN_ROLES, 'a kitchen member');
    return this.post(`/api/v1/kitchens/${seg(kitchenId)}/members`, { user_id: userId, role });
  }

  // ── User directory ─────────────────────────────────────────────────────────

  async listUsers(): Promise<User[]> {
    return this.get('/api/v1/users');
  }

  async getUser(userId: string): Promise<User> {
    return this.get(`/api/v1/users/${seg(userId)}`);
  }

  /** `role` is one of `PERSON_ROLES`: `restaurateur` (the installation's owner) or a kitchen role. The retired `admin` is refused. */
  async createUser(email: string, role: UserRole, name?: string): Promise<User> {
    checkRole(role, PERSON_ROLES, 'a user');
    return this.post('/api/v1/users', { email, role, name: name ?? '' });
  }

  async updateUser(userId: string, fields: Partial<Pick<User, 'name' | 'role'>>): Promise<User> {
    return this.put(`/api/v1/users/${seg(userId)}`, fields);
  }

  async deleteUser(userId: string): Promise<void> {
    await this.delete(`/api/v1/users/${seg(userId)}`);
  }

  // ── Guardrails ─────────────────────────────────────────────────────────────

  async listGuardrails(): Promise<Guardrail[]> {
    return this.get('/api/v1/guardrails');
  }

  /** The kinds, stages, actions and PII/secret detectors a rule can use. */
  async guardrailKinds(): Promise<GuardrailKinds> {
    return this.get('/api/v1/guardrails/kinds');
  }

  /** One rule; rejects when the kitchen has no such rule. */
  async getGuardrail(guardrailId: string): Promise<Guardrail> {
    const rule = (await this.listGuardrails()).find((g) => g.id === guardrailId);
    if (!rule) throw new Error(`no guardrail ${guardrailId} in this kitchen`);
    return rule;
  }

  /**
   * Create a rule. `action` says what happens when it fires (default `block`); the rule is checked when it is saved and one
   * that could not work is refused. Example: `{ name: 'no-pii-out', kind: 'pii_detection', stage: 'all', action: 'redact',
   * config: { patterns: ['pii', 'secrets'] } }`.
   */
  async createGuardrail(req: CreateGuardrailRequest): Promise<Guardrail> {
    return this.post('/api/v1/guardrails', req);
  }

  async updateGuardrail(guardrailId: string, req: Partial<CreateGuardrailRequest>): Promise<Guardrail> {
    return this.put(`/api/v1/guardrails/${seg(guardrailId)}`, req);
  }

  /** Switch a rule on or off: set to `enabled` when given, flipped otherwise. */
  async toggleGuardrail(guardrailId: string, enabled?: boolean): Promise<Guardrail> {
    if (enabled !== undefined) return this.updateGuardrail(guardrailId, { enabled });
    return this.request('PATCH', `/api/v1/guardrails/${seg(guardrailId)}/toggle`, {});
  }

  async deleteGuardrail(guardrailId: string): Promise<void> {
    await this.delete(`/api/v1/guardrails/${seg(guardrailId)}`);
  }

  /**
   * What rules would do to `text`, without running an agent or storing anything. With no `rules` the kitchen's workspace
   * rules are used (disabled ones too); custom endpoint rules are skipped unless `includeCustom`.
   */
  async testGuardrails(text: string, opts: TestGuardrailsOptions = {}): Promise<GuardrailTestResult> {
    const body: Record<string, unknown> = { text, stage: opts.stage ?? 'input' };
    if (opts.rules?.length) body.rules = opts.rules;
    if (opts.includeCustom) body.include_custom = true;
    return this.post('/api/v1/guardrails/test', body);
  }

  async listGuardrailExceptions(guardrailId: string): Promise<GuardrailException[]> {
    return this.get(`/api/v1/guardrails/${seg(guardrailId)}/exceptions`);
  }

  /** Exempt one agent from a rule; `expiresAt` (RFC 3339) makes the exception expire. */
  async addGuardrailException(guardrailId: string, agentName: string, reason = '', expiresAt?: string): Promise<GuardrailException> {
    return this.post(`/api/v1/guardrails/${seg(guardrailId)}/exceptions`, {
      agent_name: agentName,
      reason,
      ...(expiresAt ? { expires_at: expiresAt } : {}),
    });
  }

  /** Remove an exception by its own id, as `listGuardrailExceptions` returns it. */
  async removeGuardrailException(guardrailId: string, exceptionId: string): Promise<void> {
    await this.delete(`/api/v1/guardrails/${seg(guardrailId)}/exceptions/${seg(exceptionId)}`);
  }

  // ── Environments & promotions ──────────────────────────────────────────────

  async listEnvironments(): Promise<Environment[]> {
    return this.get('/api/v1/environments');
  }

  async createEnvironment(name: string, kind: Environment['kind'] = 'dev'): Promise<Environment> {
    return this.post('/api/v1/environments', { name, kind });
  }

  async promoteAgent(
    agentName: string,
    fromEnv: string,
    toEnv: string,
    version?: string,
  ): Promise<Promotion> {
    return this.post('/api/v1/promotions', {
      agent_name: agentName,
      from_env: fromEnv,
      to_env: toEnv,
      version,
    });
  }

  async listDeployments(agentName?: string): Promise<Deployment[]> {
    const qs = agentName ? `?agent=${encodeURIComponent(agentName)}` : '';
    return this.get(`/api/v1/deployments${qs}`);
  }

  async getTraceabilityMatrix(): Promise<TraceabilityMatrix> {
    return this.get(`/api/v1/traces/matrix?kitchen=${encodeURIComponent(this.kitchen)}`);
  }

  // ── Traces ────────────────────────────────────────────────────────────────

  /**
   * Traces, newest first. `filters` are the list endpoint's own; `view` is the id of a saved view, whose
   * filters apply unless named as well.
   */
  async listTraces(filters: TraceListFilters = {}): Promise<Record<string, unknown>[]> {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(filters)) if (v !== undefined && v !== '' && v !== null) q.set(k, String(v));
    const qs = q.toString();
    return this.get(`/api/v1/traces${qs ? `?${qs}` : ''}`);
  }

  async getTrace(traceId: string): Promise<Record<string, unknown>> {
    return this.get(`/api/v1/traces/${encodeURIComponent(traceId)}`);
  }

  /**
   * The trace laid out on a clock, computed by the server: each span's depth, offset and duration, the critical
   * path, retries and fallbacks, tokens and cost by model, and the governance events. A large trace comes in
   * pages; pass the previous page's `cursor` for the next.
   */
  async traceTimeline(traceId: string, opts: { limit?: number; cursor?: string } = {}): Promise<TraceTimeline> {
    const q = new URLSearchParams();
    if (opts.limit) q.set('limit', String(opts.limit));
    if (opts.cursor) q.set('cursor', opts.cursor);
    const qs = q.toString();
    return this.get(`/api/v1/traces/${encodeURIComponent(traceId)}/timeline${qs ? `?${qs}` : ''}`);
  }

  /**
   * The audit events recorded for a trace (and, for a recipe run, every trace under it), in order, with each
   * event's sequence number and hash where the audit log is chained, and a `coverage` note saying from when
   * events can be found by trace.
   */
  async traceAudit(traceId: string): Promise<TraceAudit> {
    return this.get(`/api/v1/traces/${encodeURIComponent(traceId)}/audit`);
  }

  /** Your saved views of the trace list and the ones shared in this kitchen. */
  async listTraceViews(): Promise<TraceView[]> {
    return this.get('/api/v1/trace-views');
  }

  async saveTraceView(name: string, filter: TraceListFilters = {}, shared = false): Promise<TraceView> {
    return this.post('/api/v1/trace-views', { name, filter, shared });
  }

  async updateTraceView(viewId: string, name: string, filter: TraceListFilters = {}, shared = false): Promise<TraceView> {
    return this.put(`/api/v1/trace-views/${encodeURIComponent(viewId)}`, { name, filter, shared });
  }

  async deleteTraceView(viewId: string): Promise<void> {
    return this.delete(`/api/v1/trace-views/${encodeURIComponent(viewId)}`);
  }

  // ── Recipes ───────────────────────────────────────────────────────────────

  async listRecipes(opts?: { kitchen?: string }): Promise<Recipe[]> {
    return this.get('/api/v1/recipes', opts?.kitchen);
  }

  async getRecipe(name: string, opts?: { kitchen?: string }): Promise<Recipe> {
    return this.get(`/api/v1/recipes/${encodeURIComponent(name)}`, opts?.kitchen);
  }

  /**
   * Create a recipe. Pass a `Flow` to build one the way the dashboard does: it is checked first and a `FlowError`
   * lists what is wrong. With `checkAgents: true` every agent it names must also exist in the kitchen.
   */
  async createRecipe(recipe: Partial<Recipe> | Flow, opts?: { kitchen?: string; checkAgents?: boolean }): Promise<Recipe> {
    if (recipe instanceof Flow) {
      const agents = opts?.checkAgents ? await this.agentNames(opts.kitchen) : undefined;
      return this.post('/api/v1/recipes', recipe.toWire({ agents }), opts?.kitchen);
    }
    return this.post('/api/v1/recipes', recipe, opts?.kitchen);
  }

  /** Replace a recipe's description and steps with those of `flow` (a Flow with the same name). */
  async updateRecipe(flow: Flow, opts?: { kitchen?: string; checkAgents?: boolean }): Promise<Recipe> {
    const agents = opts?.checkAgents ? await this.agentNames(opts.kitchen) : undefined;
    const { description, steps } = flow.toWire({ agents });
    return this.put(`/api/v1/recipes/${encodeURIComponent(flow.name)}`, { description, steps }, opts?.kitchen);
  }

  private async agentNames(kitchen?: string): Promise<string[]> {
    const agents = await this.get<{ name: string }[]>('/api/v1/agents', kitchen);
    return agents.map((a) => a.name);
  }

  /**
   * Archive a recipe. It is hidden and can no longer be run or edited, but it and its runs are kept (a run is a
   * record of what models did); `restoreRecipe` brings it back.
   */
  async deleteRecipe(name: string, opts?: { kitchen?: string }): Promise<void> {
    await this.delete(`/api/v1/recipes/${encodeURIComponent(name)}`, opts?.kitchen);
  }

  /** Bring an archived recipe back. */
  async restoreRecipe(name: string, opts?: { kitchen?: string }): Promise<Recipe> {
    return this.post(`/api/v1/recipes/${encodeURIComponent(name)}/restore`, {}, opts?.kitchen);
  }

  /**
   * Run a recipe again with the input (and environment) of an earlier run, or with `input` instead. Returns at
   * once with the new `run_id` and the `rerun_of` it repeats.
   */
  async rerunRecipeRun(
    name: string,
    runId: string,
    input?: Record<string, unknown>,
    opts?: { kitchen?: string },
  ): Promise<{ run_id: string; rerun_of: string; status: string }> {
    return this.post(
      `/api/v1/recipes/${encodeURIComponent(name)}/runs/${encodeURIComponent(runId)}/rerun`,
      input ? { input } : {},
      opts?.kitchen,
    );
  }

  async bakeRecipe(
    name: string,
    input: Record<string, unknown> = {},
    environment?: string,
    opts?: { kitchen?: string },
  ): Promise<Record<string, unknown>> {
    return this.post(
      `/api/v1/recipes/${encodeURIComponent(name)}/bake`,
      { input, ...(environment ? { environment } : {}) },
      opts?.kitchen,
    );
  }

  /**
   * Start a run. Returns at once with `run_id` and `status`; see `waitForRecipeRun`.
   *
   * An environment can require approval before a recipe runs on it. A Head Chef or Restaurateur may skip that
   * approval (never in a GxP kitchen), and on Pro the skip is a signed decision on the `approval_skip` gate: record
   * id `<recipe>@<environment>`, payload `{ input }`, so a token for one run cannot start another. Pass `password`
   * (the SDK signs it, `sign`) or a `signingToken` signed for exactly this recipe, environment and input; it needs
   * `environment`. Leave both out when the environment needs no approval. A scheduled or automated run cannot skip
   * an approval.
   */
  async runRecipe(
    name: string,
    input: Record<string, unknown> = {},
    opts?: { environment?: string; kitchen?: string } & Signing,
  ): Promise<Record<string, unknown>> {
    if (opts?.password === undefined && !opts?.signingToken) {
      return this.bakeRecipe(name, input, opts?.environment, { kitchen: opts?.kitchen });
    }
    if (!opts?.environment) throw new Error('skipping an approval is signed for one environment: pass environment');
    const extra = await this.signingHeaders(opts, 'approval_skip', `${name}@${opts.environment}`, { input });
    return this.request(
      'POST',
      `/api/v1/recipes/${encodeURIComponent(name)}/bake`,
      { input, environment: opts.environment },
      opts.kitchen,
      extra,
    );
  }

  /**
   * Wait for a run to finish and return it (`status`, `step_results`, `output`...). A run waiting for a person to
   * approve a step is `paused`. With `stopAtGate` (the default) this returns then, with `pending_gates` naming the
   * steps, so you can `approveGate` or `rejectGate`; with `false` it keeps waiting. Rejects after `timeoutMs`.
   */
  async waitForRecipeRun(
    name: string,
    runId: string,
    opts: { timeoutMs?: number; pollMs?: number; stopAtGate?: boolean; kitchen?: string } = {},
  ): Promise<RecipeRun> {
    const { timeoutMs = 300_000, pollMs = 1_000, stopAtGate = true } = opts;
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const body = await this.get<{ run?: RecipeRun; pending_gates?: string[] | null } & RecipeRun>(
        `/api/v1/recipes/${encodeURIComponent(name)}/runs/${encodeURIComponent(runId)}`,
        opts.kitchen,
      );
      const run: RecipeRun = { ...(body.run ?? body) };
      if ('pending_gates' in body) run.pending_gates = body.pending_gates ?? [];
      if (['completed', 'failed', 'canceled'].includes(run.status) || (stopAtGate && run.status === 'paused')) return run;
      if (Date.now() >= deadline) throw new Error(`run ${runId} of ${name} is still '${run.status}' after ${timeoutMs / 1000}s`);
      await new Promise((resolve) => setTimeout(resolve, pollMs));
    }
  }

  // ── Notification channels ─────────────────────────────────────────────────

  /** The notification channels of the kitchen. */
  async listChannels(opts?: { kitchen?: string }): Promise<NotificationChannel[]> {
    return this.get('/api/v1/channels', opts?.kitchen);
  }

  /**
   * Create a channel. `kind: 'websocket'` (the default) is a live feed: listeners connect to it with
   * `listenToChannel`, and a recipe's `notify` step names it. A `webhook` channel needs `url` (and may sign with
   * `secret`). `events` limits which platform events (`gate_waiting`, `step_failed`...) it also receives on their
   * own; a `notify` step reaches a channel it names whatever it subscribes to.
   */
  async createChannel(
    channel: { name: string; kind?: string; events?: string[]; url?: string; secret?: string; config?: Record<string, unknown> },
    opts?: { kitchen?: string },
  ): Promise<NotificationChannel> {
    return this.post('/api/v1/channels', { kind: 'websocket', events: [], ...channel }, opts?.kitchen);
  }

  async deleteChannel(name: string, opts?: { kitchen?: string }): Promise<void> {
    await this.delete(`/api/v1/channels/${encodeURIComponent(name)}`, opts?.kitchen);
  }

  /**
   * A ticket for listening to one websocket channel without headers, so a browser can: hand `token` to the page
   * and connect with `listenToChannel({ url, channel, token })`. Good for a minute.
   */
  async channelTicket(name: string, opts?: { kitchen?: string }): Promise<{ token: string; expires_in: number; subprotocol: string }> {
    return this.post(`/api/v1/channels/${encodeURIComponent(name)}/ws-ticket`, {}, opts?.kitchen);
  }

  // ── Signed decisions (Pro, ADR-0041) ───────────────────────────────────────
  // A decision at a gate is signed: you prove who you are again and the control plane gives you a single-use
  // token bound to exactly that decision, which you send in the `X-Signing-Token` header (`SIGNING_TOKEN_HEADER`).
  // `sign` does both steps. Every method below that takes a decision accepts `password` (the SDK signs it for
  // you) or `signingToken` (a token you already hold), never both.

  /**
   * Sign one decision and return its single-use signing token: asks for the challenge, proves who you are with
   * `password`, and returns the token (good for a minute and for exactly this decision).
   *
   * `gate` is the kind of decision (see `SIGNING_GATES`), `recordId` what is decided, `meaning` is `approved` or
   * `rejected`, and `payload` is the content of the decision that is signed with it (a reason, an input). Send the
   * token in the `X-Signing-Token` header, or pass it as `signingToken` to the method that takes the decision.
   *
   * Two gates are new with the roles. `legal_hold_release` (`recordId` `dataset:<id>` or `audit:<kitchen>`,
   * payload `{ reason }`) lifts a legal hold; `liftDatasetLegalHold` and `liftAuditLegalHold` sign it for you.
   * `approval_skip` (`recordId` `<recipe>@<environment>`, payload `{ input }`) runs a recipe on an environment that
   * requires approval; `runRecipe` signs it for you when you pass `password`.
   *
   * An API key or service account cannot sign: it gets `signature_required`. An account that signs in through an
   * identity provider (OIDC) signs in the browser, not here: this rejects with `OidcSigningRequired` before the
   * password is sent anywhere.
   */
  async sign(gate: string, recordId: string, meaning: 'approved' | 'rejected', payload: Record<string, unknown> | undefined, password: string): Promise<string> {
    const challenge = await this.signatureChallenge(gate, recordId, meaning, payload);
    if (challenge.reauth === 'oidc') {
      throw new OidcSigningRequired(400, {
        error: 'use_oidc',
        message: 'this account signs through the identity provider: sign in the dashboard, or sign in with a local account (password) to sign from the SDK',
      });
    }
    try {
      const reply = await this.requestSigningToken(gate, recordId, meaning, challenge.subject.digest, password, payload);
      return reply.signing_token;
    } catch (err) {
      if (err instanceof AgentOvenAPIError && (err.detail as { error?: string } | null)?.error === 'use_oidc') {
        throw new OidcSigningRequired(err.statusCode, err.detail);
      }
      throw err;
    }
  }

  /** Same as `sign` with the password before the payload (the older argument order). */
  signDecision(gate: string, recordId: string, meaning: 'approved' | 'rejected', password: string, payload?: Record<string, unknown>): Promise<string> {
    return this.sign(gate, recordId, meaning, payload, password);
  }

  /**
   * The first half of signing: what would be signed (`subject`, with its `digest`, `title` and `display`), how you
   * would prove who you are (`reauth`: `password` or `oidc`) and how long the token lives. Rejects with 403 if you
   * may not sign this decision.
   */
  signatureChallenge(gate: string, recordId: string, meaning: 'approved' | 'rejected', payload?: Record<string, unknown>): Promise<SigningChallenge> {
    return this.post('/api/v1/signatures/challenge', signingRequest(gate, recordId, meaning, payload));
  }

  /**
   * The second half of signing: `password` re-authenticates a local account and, if the record is still what
   * `digest` (from `signatureChallenge`) was computed over, returns the token. Prefer `sign`.
   */
  requestSigningToken(gate: string, recordId: string, meaning: 'approved' | 'rejected', digest: string, password: string, payload?: Record<string, unknown>): Promise<SigningTokenReply> {
    return this.post('/api/v1/signatures/token', { ...signingRequest(gate, recordId, meaning, payload), digest, password });
  }

  /** Send any request with a signing token in the `X-Signing-Token` header, for a decision this SDK has no method for. */
  signedRequest<T = unknown>(method: string, path: string, body: unknown, signingToken: string): Promise<T> {
    return this.request<T>(method, path, body, undefined, { [SIGNING_TOKEN_HEADER]: signingToken });
  }

  /** Signed decisions of the kitchen: who signed (name and role), when, the meaning, and what became of the decision. */
  listSignatures(gate = '', recordId = ''): Promise<SignatureRecord[]> {
    return this.get(`/api/v1/signatures?gate=${encodeURIComponent(gate)}&record_id=${encodeURIComponent(recordId)}`);
  }

  /** One signed decision. */
  getSignature(signatureId: string): Promise<SignatureRecord> {
    return this.get(`/api/v1/signatures/${seg(signatureId)}`);
  }

  /** Check the kitchen's signature chain: `ok` is false, with where it breaks, if one was changed or removed. */
  verifySignatures(): Promise<{ kitchen: string; count: number; ok: boolean; broken_at?: number; reason?: string; head_hash?: string }> {
    return this.get('/api/v1/signatures/verify');
  }

  /**
   * Who may approve what in this kitchen: `policy` maps `<gate>` or `<gate>:<scope>` to `{ roles, users }`, and
   * `gates` lists the gates a rule can name.
   */
  getApproverPolicy(): Promise<{ policy: ApproverPolicy; gates: string[] }> {
    return this.get('/api/v1/approver-policy');
  }

  /**
   * Replace the kitchen's approver rules (a Head Chef's; the whole policy is replaced, so send every rule). Keys are
   * `<gate>` or `<gate>:<scope>` (an environment slug or an agent name, e.g. `environment_promotion:prod`). Roles are
   * checked here against `PERSON_ROLES`; the retired `admin` is refused (name `head_chef` or `restaurateur`).
   */
  async setApproverPolicy(policy: Record<string, { roles?: string[]; users?: string[] }>): Promise<{ policy: ApproverPolicy }> {
    const rules: ApproverPolicy = {};
    for (const [key, rule] of Object.entries(policy)) {
      const roles = [...(rule.roles ?? [])];
      for (const role of roles) checkRole(role, PERSON_ROLES, `approver policy ${JSON.stringify(key)}`);
      rules[key] = { roles, users: [...(rule.users ?? [])] };
    }
    return this.put('/api/v1/approver-policy', { policy: rules });
  }

  // ── Approvals where people work (Pro, ADR-0050). None of these decides anything: a decision is signed. ──

  /**
   * Decisions that were announced to people through the kitchen's channels, newest first. `status` is `open`
   * (default), `closed` or `all`; `gate` and `recordId` narrow them. Each says how many messages went out, the
   * reminders sent, the escalation stage and who claimed it.
   */
  async listApprovalRequests(opts: { status?: 'open' | 'closed' | 'all'; gate?: string; recordId?: string } = {}): Promise<ApprovalRequestSummary[]> {
    const q = new URLSearchParams({ status: opts.status ?? 'open' });
    if (opts.gate) q.set('gate', opts.gate);
    if (opts.recordId) q.set('record_id', opts.recordId);
    return (await this.get<{ requests: ApprovalRequestSummary[] }>(`/api/v1/approval-requests?${q}`)).requests;
  }

  /** One request: its timeline (who was messaged where, reminders, escalations, claims) and, while open, the chain of people to be asked. */
  getApprovalRequest(id: string): Promise<ApprovalRequestDetail> {
    return this.get(`/api/v1/approval-requests/${seg(id)}`);
  }

  /** The latest request for a decision (its gate and record id) with its timeline, or `null` if it was never announced. */
  async approvalNotifyStatus(gate: string, recordId: string): Promise<ApprovalRequestDetail | null> {
    const [latest] = await this.listApprovalRequests({ status: 'all', gate, recordId });
    return latest ? this.getApprovalRequest(latest.id) : null;
  }

  /** Say you are looking at it: reminders are quiet for a while. It does not decide anything. */
  async claimApprovalRequest(id: string): Promise<ApprovalRequestSummary> {
    return (await this.post<{ request: ApprovalRequestSummary }>(`/api/v1/approval-requests/${seg(id)}/claim`, {})).request;
  }

  /** Let go of a claim. */
  async unclaimApprovalRequest(id: string): Promise<ApprovalRequestSummary> {
    return (await this.post<{ request: ApprovalRequestSummary }>(`/api/v1/approval-requests/${seg(id)}/unclaim`, {})).request;
  }

  /** Say it is not yours: it goes to the next person up at once. */
  async declineApprovalRequest(id: string): Promise<ApprovalRequestSummary> {
    return (await this.post<{ request: ApprovalRequestSummary }>(`/api/v1/approval-requests/${seg(id)}/decline`, {})).request;
  }

  /** Quiet reminders and escalation for `minutes` (1 to 1440). The deadline does not move. */
  async snoozeApprovalRequest(id: string, minutes = 60): Promise<ApprovalRequestSummary> {
    if (!Number.isInteger(minutes) || minutes < 1 || minutes > 1440) throw new RangeError('a snooze is between 1 and 1440 minutes');
    return (await this.post<{ request: ApprovalRequestSummary }>(`/api/v1/approval-requests/${seg(id)}/snooze`, { minutes })).request;
  }

  /** How the kitchen chases what waits for a person: the rules by gate, the built-in default, the gates a rule can name. */
  getApprovalEscalation(): Promise<{ policy: Record<string, ApprovalEscalationRule>; default: ApprovalEscalationRule; gates: string[]; min_interval_seconds: number }> {
    return this.get('/api/v1/approval-escalation');
  }

  /**
   * Replace the kitchen's rules (a Head Chef's; send every rule). Expiring refuses a request after the last wait; it
   * never approves it.
   */
  setApprovalEscalation(policy: Record<string, ApprovalEscalationRule>): Promise<{ policy: Record<string, ApprovalEscalationRule> }> {
    return this.put('/api/v1/approval-escalation', { policy });
  }

  /** The header for one decision: the token passed, or one signed now with `password`, or none. */
  private async signingHeaders(
    opts: Signing | undefined,
    gate: string,
    recordId: string,
    payload?: Record<string, unknown>,
    meaning: 'approved' | 'rejected' = 'approved',
  ): Promise<Record<string, string> | undefined> {
    let token = opts?.signingToken;
    if (opts?.password !== undefined && token) throw new Error('pass password or signingToken, not both');
    if (opts?.password !== undefined) token = await this.sign(gate, recordId, meaning, payload, opts.password);
    return token ? { [SIGNING_TOKEN_HEADER]: token } : undefined;
  }

  /**
   * End a `wait` step early, so the run carries on now. Rejects with a 404 if it is not waiting. On Pro the
   * decision is signed: pass `password`, the password of your local account, or a `signingToken` you hold.
   */
  async releaseWait(name: string, runId: string, step: string, opts?: { kitchen?: string } & Signing): Promise<unknown> {
    const extra = await this.signingHeaders(opts, 'wait_release', `${runId}:${step}`);
    return this.request(
      'POST',
      `/api/v1/recipes/${encodeURIComponent(name)}/runs/${encodeURIComponent(runId)}/steps/${encodeURIComponent(step)}/release`,
      {},
      opts?.kitchen,
      extra,
    );
  }

  /**
   * Approve a step that is waiting for a person. The decision is recorded with who made it. On Pro every decision
   * is signed: pass `opts.password`, the password of your local account, and the SDK signs it (`sign`), or a
   * `signingToken` you hold. Without either the call is unsigned, which Pro refuses with `signature_required`.
   */
  approveGate(runId: string, step: string, comments?: string, opts?: Signing): Promise<unknown> {
    return this.decideGate(runId, step, true, comments, opts);
  }

  /** Reject a waiting step. `reason` is recorded and passed to the step that follows the Rejected path. */
  rejectGate(runId: string, step: string, reason?: string, opts?: Signing): Promise<unknown> {
    return this.decideGate(runId, step, false, reason, opts);
  }

  private async decideGate(runId: string, step: string, approved: boolean, comments?: string, opts?: Signing): Promise<unknown> {
    const extra = await this.signingHeaders(opts, 'recipe_gate', `${runId}:${step}`, undefined, approved ? 'approved' : 'rejected');
    return this.request(
      'POST',
      `/api/v1/approvals/${encodeURIComponent(runId)}/${encodeURIComponent(step)}`,
      { approved, ...(comments ? { comments } : {}) },
      undefined,
      extra,
    );
  }

  async listRecipeRuns(recipeName: string, opts?: { kitchen?: string }): Promise<RecipeRun[]> {
    return this.get(`/api/v1/recipes/${encodeURIComponent(recipeName)}/runs`, opts?.kitchen);
  }

  async getRecipeRun(recipeName: string, runId: string, opts?: { kitchen?: string }): Promise<RecipeRun> {
    return this.get(
      `/api/v1/recipes/${encodeURIComponent(recipeName)}/runs/${encodeURIComponent(runId)}`,
      opts?.kitchen,
    );
  }

  // ── Sessions ───────────────────────────────────────────────────────────────

  async listSessions(agentName?: string): Promise<Session[]> {
    const qs = agentName ? `?agent=${encodeURIComponent(agentName)}` : '';
    return this.get(`/api/v1/sessions${qs}`);
  }

  async getSession(sessionId: string): Promise<Session> {
    return this.get(`/api/v1/sessions/${seg(sessionId)}`);
  }

  async createSession(agentName: string, data?: unknown): Promise<Session> {
    return this.post('/api/v1/sessions', { agent_name: agentName, data });
  }

  async deleteSession(sessionId: string): Promise<void> {
    await this.delete(`/api/v1/sessions/${seg(sessionId)}`);
  }

  // ── Service accounts ───────────────────────────────────────────────────────

  async listServiceAccounts(): Promise<ServiceAccount[]> {
    return this.get('/api/v1/service-accounts');
  }

  /**
   * Create a machine identity bound to this kitchen. The token is returned only once — store it securely
   * immediately. `role` is a kitchen role (one of `KITCHEN_ROLES`; the retired `admin` is refused). Every credential
   * belongs to a person (`ownerEmail`, you if left out): a member of this kitchen at a rank that covers the role.
   * Creating one on someone else's behalf is a Head Chef's act. When the owner leaves the kitchen the credential
   * stops; give it to someone with `setServiceAccountOwner`.
   */
  async createServiceAccount(name: string, role: KitchenRole, opts?: { ownerEmail?: string }): Promise<CreateServiceAccountResponse> {
    checkRole(role, KITCHEN_ROLES, 'a service account');
    return this.post('/api/v1/service-accounts', { name, role, ...(opts?.ownerEmail ? { owner_email: opts.ownerEmail } : {}) });
  }

  async deleteServiceAccount(saId: string): Promise<void> {
    await this.delete(`/api/v1/service-accounts/${seg(saId)}`);
  }

  // ── Schedules ──────────────────────────────────────────────────────────────

  async listSchedules(): Promise<Schedule[]> {
    return this.get('/api/v1/schedules');
  }

  async getSchedule(scheduleId: string): Promise<Schedule> {
    return this.get(`/api/v1/schedules/${seg(scheduleId)}`);
  }

  async createSchedule(req: CreateScheduleRequest): Promise<Schedule> {
    return this.post('/api/v1/schedules', { timezone: 'UTC', enabled: true, ...req });
  }

  async updateSchedule(scheduleId: string, fields: Partial<CreateScheduleRequest>): Promise<Schedule> {
    return this.put(`/api/v1/schedules/${seg(scheduleId)}`, fields);
  }

  async deleteSchedule(scheduleId: string): Promise<void> {
    await this.delete(`/api/v1/schedules/${seg(scheduleId)}`);
  }

  // ── Inbound triggers (ADR-0047) ────────────────────────────────────────────
  // A trigger starts a recipe or an agent from an event that arrives from outside, as a service account
  // that belongs to a person. It cannot start what the account could not, and it cannot skip an approval.

  async listTriggers(): Promise<Trigger[]> {
    return (await this.get<{ triggers?: Trigger[] }>('/api/v1/triggers')).triggers ?? [];
  }

  async getTrigger(triggerId: string): Promise<Trigger> {
    return this.get(`/api/v1/triggers/${seg(triggerId)}`);
  }

  /**
   * Create a trigger. `kind` is `webhook` (the default), `email` or `file`; `target_kind` is `recipe` (the
   * default) or `agent`. `input_mapping` turns the payload into the run's input: `"$.a.b"` takes a value and
   * keeps its type, `"text {{ $.a.b }}"` puts one into text, nothing else is evaluated. Secrets in `config`
   * are references (`vault://...`), never values. A webhook trigger given no `secret_ref` gets a signing
   * secret generated, returned once as `secret`; sign deliveries with `signWebhook`.
   */
  async createTrigger(req: CreateTriggerRequest): Promise<{ trigger: Trigger; secret?: string }> {
    const kind = req.kind ?? 'webhook';
    const targetKind = req.target_kind ?? 'recipe';
    if (!TRIGGER_KINDS.includes(kind)) throw new Error(`kind is one of ${TRIGGER_KINDS.join(', ')}, not ${JSON.stringify(kind)}`);
    if (targetKind !== 'recipe' && targetKind !== 'agent') throw new Error('target_kind is recipe or agent');
    return this.post('/api/v1/triggers', { enabled: true, ...req, kind, target_kind: targetKind });
  }

  /** Change a trigger. Switching one off needs only the right to manage triggers; anything else needs the authority of the account it runs as. */
  async updateTrigger(triggerId: string, fields: Partial<Omit<CreateTriggerRequest, 'name' | 'kind' | 'secret_ref'>>): Promise<Trigger> {
    return this.put(`/api/v1/triggers/${seg(triggerId)}`, fields);
  }

  async setTriggerEnabled(triggerId: string, enabled: boolean): Promise<Trigger> {
    return this.updateTrigger(triggerId, { enabled });
  }

  async deleteTrigger(triggerId: string): Promise<void> {
    await this.delete(`/api/v1/triggers/${seg(triggerId)}`);
  }

  /** Replace the signing secret AgentOven generated. The new one is returned once; the old one stops working at once. */
  async rotateTriggerSecret(triggerId: string): Promise<{ secret: string }> {
    return this.post(`/api/v1/triggers/${seg(triggerId)}/rotate-secret`, {});
  }

  /**
   * Try a trigger with a sample payload. A dry run (the default) shows the input the run would get and
   * starts nothing; `dryRun: false` starts a real run as the trigger's account, recorded as a test.
   */
  async fireTrigger(triggerId: string, payload: unknown = {}, opts: { dryRun?: boolean } = {}): Promise<TriggerFireResult> {
    return this.post(`/api/v1/triggers/${seg(triggerId)}/fire`, { payload, dry_run: opts.dryRun ?? true });
  }

  /** Look at an e-mail or file trigger's source now, instead of at its next interval. */
  async pollTrigger(triggerId: string): Promise<{ handled: number }> {
    return this.post(`/api/v1/triggers/${seg(triggerId)}/poll`, {});
  }

  /** What happened to deliveries, newest first. */
  async listTriggerDeliveries(filter: { trigger_id?: string; status?: TriggerDeliveryStatus; run_id?: string; limit?: number; before?: string } = {}): Promise<TriggerDelivery[]> {
    if (filter.status !== undefined && !TRIGGER_DELIVERY_STATUSES.includes(filter.status)) {
      throw new Error(`status is one of ${TRIGGER_DELIVERY_STATUSES.join(', ')}, not ${JSON.stringify(filter.status)}`);
    }
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(filter)) if (v !== undefined && v !== '') q.set(k, String(v));
    const qs = q.toString();
    return (await this.get<{ deliveries?: TriggerDelivery[] }>(`/api/v1/triggers/deliveries${qs ? `?${qs}` : ''}`)).deliveries ?? [];
  }

  // ── Test suites ────────────────────────────────────────────────────────────
  // The server routes every one of these by id, never by name — despite the
  // early `name`-labelled params below (kept only so `getTestSuite(x)` /
  // `runTestSuite(x)` don't break existing callers passing an id under that
  // name; pass the suite's `id` field, not its display name).

  async listTestSuites(): Promise<TestSuite[]> {
    return this.get('/api/v1/test-suites');
  }

  async getTestSuite(id: string): Promise<TestSuite> {
    return this.get(`/api/v1/test-suites/${seg(id)}`);
  }

  async createTestSuite(suite: Omit<TestSuite, 'id' | 'created_at'>): Promise<TestSuite> {
    return this.post('/api/v1/test-suites', suite);
  }

  async updateTestSuite(id: string, suite: Partial<TestSuite>): Promise<TestSuite> {
    return this.put(`/api/v1/test-suites/${seg(id)}`, suite);
  }

  async deleteTestSuite(id: string): Promise<void> {
    return this.delete(`/api/v1/test-suites/${seg(id)}`);
  }

  async runTestSuite(id: string, trigger = 'manual'): Promise<{ run_id: string; status: string }> {
    return this.post(`/api/v1/test-suites/${seg(id)}/run`, { trigger });
  }

  /**
   * Lists runs for one suite. The server has no top-level `GET
   * /api/v1/test-runs` — only this nested route — so, unlike the earlier
   * version of this method, `suiteId` is required, not optional.
   */
  async listTestRuns(suiteId: string): Promise<TestRun[]> {
    return this.get(`/api/v1/test-suites/${seg(suiteId)}/runs`);
  }

  async getTestRun(suiteId: string, runId: string): Promise<TestRun> {
    return this.get(`/api/v1/test-suites/${seg(suiteId)}/runs/${seg(runId)}`);
  }

  async cancelTestRun(runId: string): Promise<void> {
    return this.post(`/api/v1/test-runs/${seg(runId)}/cancel`, {});
  }

  // ── World schemas (Pro, ADR-0032) ────────────────────────────────────────────

  async listWorldSchemas(): Promise<WorldSchema[]> {
    return this.get('/api/v1/world-schemas');
  }

  async getWorldSchema(id: string): Promise<WorldSchema> {
    return this.get(`/api/v1/world-schemas/${seg(id)}`);
  }

  async createWorldSchema(schema: WorldSchema): Promise<{ schema: WorldSchema; issues: unknown[] }> {
    return this.post('/api/v1/world-schemas', schema);
  }

  async saveWorldSchema(id: string, schema: WorldSchema): Promise<{ schema: WorldSchema; issues: unknown[] }> {
    return this.put(`/api/v1/world-schemas/${seg(id)}`, schema);
  }

  // ── Scenarios (Pro, ADR-0032) ─────────────────────────────────────────────────

  async listScenarios(): Promise<Scenario[]> {
    return this.get('/api/v1/scenarios');
  }

  async getScenario(id: string): Promise<Scenario> {
    return this.get(`/api/v1/scenarios/${seg(id)}`);
  }

  async createScenario(scenario: Scenario): Promise<Scenario> {
    return this.post('/api/v1/scenarios', scenario);
  }

  async saveScenario(id: string, scenario: Scenario): Promise<Scenario> {
    return this.put(`/api/v1/scenarios/${seg(id)}`, scenario);
  }

  async deleteScenario(id: string): Promise<void> {
    return this.delete(`/api/v1/scenarios/${seg(id)}`);
  }

  /**
   * Submits a scenario run. Leave `scenario_ids` unset to auto-pick: the
   * agent's own scenario ingredients decide what runs and how many episodes
   * (each ingredient's own `rollouts`), the same path a scenario auto-pick
   * test case uses.
   */
  async runScenarios(req: ScenarioRunRequest): Promise<ScenarioRun> {
    return this.post('/api/v1/scenario-runs', req);
  }

  async getScenarioRun(runId: string): Promise<ScenarioRun> {
    return this.get(`/api/v1/scenario-runs/${seg(runId)}`);
  }

  async listScenarioRuns(): Promise<ScenarioRun[]> {
    return this.get('/api/v1/scenario-runs');
  }

  /** The per-episode detail behind a scenario run's aggregate pass_rate —
   *  one entry per rollout, each with its own graded verdict. */
  async getScenarioRunEpisodes(runId: string): Promise<EpisodeRecord[]> {
    return this.get(`/api/v1/scenario-runs/${seg(runId)}/episodes`);
  }

  async cancelScenarioRun(runId: string): Promise<void> {
    return this.post(`/api/v1/scenario-runs/${seg(runId)}/cancel`, {});
  }

  // ── Workloads (K8s) ────────────────────────────────────────────────────────

  async listWorkloads(): Promise<Workload[]> {
    return this.get('/api/v1/workloads');
  }

  async getWorkload(workloadId: string): Promise<Workload> {
    return this.get(`/api/v1/workloads/${seg(workloadId)}`);
  }

  // ── Agent environments (Pro) ───────────────────────────────────────────────

  /** List all environment deployments for an agent. */
  async listAgentEnvironments(agentName: string): Promise<AgentEnvironment[]> {
    return this.get(`/api/v1/agents/${seg(agentName)}/environments`);
  }

  /** Get a specific environment deployment. */
  async getAgentEnvironment(agentName: string, envSlug: string): Promise<AgentEnvironment> {
    return this.get(`/api/v1/agents/${seg(agentName)}/environments/${seg(envSlug)}`);
  }

  /** Create or update an agent environment deployment. */
  async upsertAgentEnvironment(agentName: string, envSlug: string, req: UpsertAgentEnvironmentRequest): Promise<AgentEnvironment> {
    return this.put(`/api/v1/agents/${seg(agentName)}/environments/${seg(envSlug)}`, req);
  }

  /** Initiate cool-down / removal of an agent environment deployment. */
  async deleteAgentEnvironment(agentName: string, envSlug: string): Promise<void> {
    return this.delete(`/api/v1/agents/${seg(agentName)}/environments/${seg(envSlug)}`);
  }

  // ── Scoped API keys (Pro) ─────────────────────────────────────────────────

  async listScopedKeys(): Promise<ScopedAPIKey[]> {
    return this.get('/api/v1/keys');
  }

  /**
   * Create a scoped API key. Like a service account it belongs to a person (`owner_email`, you if left out), who
   * must be a member of this kitchen; see `setScopedKeyOwner`.
   */
  async createScopedKey(req: {
    label: string;
    agent_names: string[];
    max_calls?: number;
    expires_in?: string;
    owner_email?: string;
  }): Promise<Record<string, unknown>> {
    return this.post('/api/v1/keys', req);
  }

  async getScopedKey(keyId: string): Promise<ScopedAPIKey> {
    return this.get(`/api/v1/keys/${seg(keyId)}`);
  }

  async revokeScopedKey(keyId: string): Promise<Record<string, unknown>> {
    return this.post(`/api/v1/keys/${seg(keyId)}/revoke`, {});
  }

  async getScopedKeyUsage(keyId: string): Promise<Record<string, unknown>> {
    return this.get(`/api/v1/keys/${seg(keyId)}/usage`);
  }

  async deleteScopedKey(keyId: string): Promise<void> {
    await this.delete(`/api/v1/keys/${seg(keyId)}`);
  }

  // ── Credentials (Pro) ────────────────────────────────────────────────────

  async listCredentials(): Promise<Record<string, unknown>[]> {
    return this.get('/api/v1/credentials');
  }

  async createCredential(name: string, value: string, label = ''): Promise<Record<string, unknown>> {
    return this.post('/api/v1/credentials', { name, value, label });
  }

  async deleteCredential(credentialName: string): Promise<void> {
    await this.delete(`/api/v1/credentials/${seg(credentialName)}`);
  }

  // ── License (Pro) ────────────────────────────────────────────────────────

  async licenseStatus(): Promise<Record<string, unknown>> {
    return this.get('/api/v1/license/status');
  }

  async licensePhoneHome(): Promise<Record<string, unknown>> {
    return this.get('/api/v1/license/phone-home');
  }

  // ── Providers & modalities ─────────────────────────────────────────────────
  // Modalities are a property of the provider: an agent has exactly the
  // modalities of its own provider. See ./modalities.ts.

  async listProviders(): Promise<Provider[]> {
    return this.get('/api/v1/models/providers');
  }

  async getProvider(name: string): Promise<Provider> {
    return this.get(`/api/v1/models/providers/${seg(name)}`);
  }

  /**
   * Register a model provider. `modalities` takes the builder or a wire-format
   * object and is validated before the request is sent.
   */
  async createProvider(req: CreateProviderRequest): Promise<Provider> {
    const { api_key, modalities, config, ...rest } = req;
    const cfg = providerConfig(config, api_key, modalities, false);
    return this.post('/api/v1/models/providers', { models: [], is_default: false, ...rest, config: cfg });
  }

  /**
   * Update a provider; only what you pass changes. `config` keys are merged,
   * `modalities` is merged per modality, and `null` deletes a key or a whole
   * modality (back to its default).
   */
  async updateProvider(name: string, req: UpdateProviderRequest = {}): Promise<UpdateProviderResponse> {
    const { api_key, modalities, config, is_default, ...rest } = req;
    const body: Record<string, unknown> = { ...rest };
    const cfg = providerConfig(config, api_key, modalities, true);
    if (Object.keys(cfg).length > 0) body.config = cfg;
    // The server overwrites is_default on every update; keep the current value.
    body.is_default = is_default ?? (await this.getProvider(name)).is_default;
    return this.put(`/api/v1/models/providers/${seg(name)}`, body);
  }

  // ── Tools (MCP gateway) ────────────────────────────────────────────────────

  /** The tools registered in this kitchen. A tool's `auth_config` is never returned. */
  async listTools(): Promise<Tool[]> {
    return (await this.get<Tool[] | null>('/api/v1/tools')) ?? [];
  }

  /** One tool by name. Rejects with an `AgentOvenAPIError` (404) if there is none. */
  async getTool(name: string): Promise<Tool> {
    return this.get(`/api/v1/tools/${seg(name)}`);
  }

  /**
   * Register a tool agents can call, or update it if the name is taken.
   *
   * ```ts
   * await pro.registerTool({
   *   name: 'refunds',
   *   endpoint: 'https://payments.internal/refund',
   *   description: 'Pay a refund',
   *   schema: { type: 'object', properties: { amount: { type: 'number' } } },
   * });
   * ```
   *
   * `transport` is `http` (default), `sse` or `stdio`. Registering an existing
   * name updates that tool; the server's update replaces `enabled` every time,
   * so it is always sent (`true` unless you pass `false`). The lookup is one
   * extra GET. Resolves to the stored tool.
   */
  async registerTool(req: RegisterToolRequest): Promise<Tool> {
    const { name, endpoint, description = '', transport = 'http', enabled = true, ...rest } = req;
    const body = { name, endpoint, description, transport, enabled, ...rest };
    try {
      await this.getTool(name);
    } catch (err) {
      if (err instanceof AgentOvenAPIError && err.statusCode === 404) {
        return this.post('/api/v1/tools', body);
      }
      throw err;
    }
    return this.put(`/api/v1/tools/${seg(name)}`, body);
  }

  /** Remove a tool from the kitchen. */
  async deleteTool(name: string): Promise<void> {
    await this.delete(`/api/v1/tools/${seg(name)}`);
  }

  // ── Tool packs (Pro, ADR-0048) ─────────────────────────────────────────────
  // A pack is a named, versioned set of governed tools for one kind of system (read-only PostgreSQL, email and Slack,
  // or any REST API imported from an OpenAPI document). An installer supplies only secret references (`vault://name`);
  // a pack's write tools wait for a person's approval by default.

  /** The packs this server offers, with the secrets, settings and tools of each (`toolpack:read`). */
  async listToolPackCatalog(): Promise<ToolPackManifest[]> {
    return (await this.get<{ packs: ToolPackManifest[] }>('/api/v1/tool-packs/catalog')).packs ?? [];
  }
  /** The packs installed in this kitchen, each with its tools, their switches and its approval status. */
  async listToolPacks(): Promise<ToolPackInstall[]> {
    return (await this.get<{ installs: ToolPackInstall[] }>('/api/v1/tool-packs')).installs ?? [];
  }
  getToolPack(name: string): Promise<ToolPackInstall> {
    return this.get(`/api/v1/tool-packs/${seg(name)}`);
  }
  /**
   * Install a catalog pack (`toolpack:install`). `secrets` binds each secret role to a `vault://` reference. `tools` maps a
   * tool's local name to its switches; switching a tool's approval off also needs `toolpack:govern`. With skill approval
   * required the install waits for people (`status` is `awaiting_approval`).
   */
  installToolPack(req: InstallToolPackRequest): Promise<ToolPackInstall> {
    return this.post('/api/v1/tool-packs', req);
  }
  /** Move an install to the catalog's current version; per-tool switches carry over. */
  upgradeToolPack(name: string, req: { secrets?: Record<string, string>; settings?: Record<string, unknown> } = {}): Promise<ToolPackInstall> {
    return this.post(`/api/v1/tool-packs/${seg(name)}/upgrade`, req);
  }
  /** Point an install at other `vault://` references. */
  rebindToolPackSecrets(name: string, secrets: Record<string, string>): Promise<ToolPackInstall> {
    return this.put(`/api/v1/tool-packs/${seg(name)}/secrets`, { secrets });
  }
  /** A connectivity check where one is possible. */
  checkToolPack(name: string): Promise<{ ok: boolean; message?: string }> {
    return this.post(`/api/v1/tool-packs/${seg(name)}/check`, {});
  }
  /** Switch one tool of an install on or off, or its approval on or off (`toolpack:govern`). */
  setToolPackTool(pack: string, tool: string, sw: ToolPackSwitch): Promise<ToolPackTool> {
    return this.request('PATCH', `/api/v1/tool-packs/${seg(pack)}/tools/${seg(tool)}`, sw);
  }
  /** Remove an install, its tools and its skill (`toolpack:remove`). */
  async uninstallToolPack(name: string): Promise<void> {
    await this.delete(`/api/v1/tool-packs/${seg(name)}`);
  }
  /**
   * List the operations of an OpenAPI 3 document, each classified read or write, without importing. Give the document's
   * `url` (the server fetches it, subject to the kitchen's egress allowlist) or its text as `spec`.
   */
  previewOpenApi(src: { url?: string; spec?: string }): Promise<OpenApiPreview> {
    return this.post('/api/v1/tool-packs/openapi/preview', src);
  }
  /**
   * Import an OpenAPI 3 document as a pack of governed tools, one per operation in `operations` (default: every read
   * operation; a write is imported only when named, and waits for approval). `base_url` pins the API's address (default: the
   * document's first server).
   */
  importOpenApi(req: ImportOpenApiRequest): Promise<ToolPackInstall> {
    return this.post('/api/v1/tool-packs/openapi/import', req);
  }
  /** Hosts the kitchen allowlisted for tool packs although they resolve to private addresses. */
  listEgressHosts(): Promise<{ hosts: Array<{ host: string; added_by: string; added_at: string }>; platform: string[] }> {
    return this.get('/api/v1/tool-packs/egress');
  }
  /** Allowlist a host for the kitchen's packs (`toolpack:govern`). Cloud metadata addresses stay refused. */
  async allowEgressHost(host: string): Promise<void> {
    await this.post('/api/v1/tool-packs/egress', { host });
  }
  async removeEgressHost(host: string): Promise<void> {
    await this.delete(`/api/v1/tool-packs/egress/${seg(host)}`);
  }

  // ── Agent card ─────────────────────────────────────────────────────────────

  /** The agent's A2A card; `card.modalities` lists what its provider offers. */
  async agentCard(name: string): Promise<AgentCard> {
    return this.get(`/api/v1/agents/${seg(name)}/card`);
  }

  /** Whether the agent can use `modality` (`text` is always true). */
  async supports(agent: string, modality: AnyModality): Promise<boolean> {
    return supportsModality(await this.agentCard(agent), modality);
  }

  // ── Invoke (text and cascaded voice) ───────────────────────────────────────

  /**
   * Run a managed agent once. Cascaded voice needs the `audio` modality on the
   * agent's provider:
   *
   * ```ts
   * const r = await pro.invoke('support-agent', {
   *   audio: await audioFromPath('question.wav'),
   *   voiceOutput: true,
   *   voice: 'alloy',
   * });
   * console.log(r.transcript, r.response);
   * await saveAudio(r.audio!, 'answer'); // answer.mp3
   * ```
   *
   * Rejects with {@link AudioModalityError} when the provider does not offer audio.
   */
  async invoke(agent: string, options: InvokeOptions): Promise<InvokeResult> {
    const attachments = options.attachments ?? [];
    if (!options.message && options.audio === undefined && attachments.length === 0) {
      throw new Error('invoke needs a message, audio, or both (or attachments)');
    }
    if (options.voice && !options.voiceOutput) throw new Error('voice only applies with voiceOutput: true');
    const body: Record<string, unknown> = {};
    if (options.message) body.message = options.message;
    if (options.audio !== undefined) {
      const a =
        options.audio instanceof Uint8Array || options.audio instanceof ArrayBuffer
          ? audioFromBytes(options.audio, options.audioMimeType)
          : options.audio;
      body.audio = { data: a.data, mime_type: a.mimeType };
    }
    if (attachments.length > 0) body.content_parts = attachments;
    if (options.voiceOutput) body.voice_output = true;
    if (options.voice) body.voice = options.voice;
    if (options.variables) body.variables = options.variables;
    if (options.sessionId) body.session_id = options.sessionId;
    if (options.thinkingEnabled) body.thinking_enabled = true;

    const raw = await this.post<Record<string, any>>(`/api/v1/agents/${seg(agent)}/invoke`, body);
    return {
      agent: raw.agent ?? '',
      response: raw.response ?? '',
      traceId: raw.trace_id ?? '',
      sessionId: raw.session_id ?? '',
      turns: raw.turns ?? 0,
      usage: raw.usage ?? {},
      latencyMs: raw.latency_ms ?? 0,
      transcript: raw.transcript ?? undefined,
      audio: raw.audio ? audioClipFromWire(raw.audio) : undefined,
      raw,
    };
  }

  // ── Agent sessions & attachments ───────────────────────────────────────────
  // Multi-turn conversations on an agent (POST /agents/{name}/sessions). Not to
  // be confused with the Pro session directory behind createSession().

  /** Start a conversation with a ready agent. */
  async createAgentSession(
    agent: string,
    opts: { maxTurns?: number; metadata?: Record<string, unknown> } = {},
  ): Promise<{ id: string; status: string; [key: string]: unknown }> {
    const body: Record<string, unknown> = {};
    if (opts.maxTurns) body.max_turns = opts.maxTurns;
    if (opts.metadata) body.metadata = opts.metadata;
    return this.post(`/api/v1/agents/${seg(agent)}/sessions`, body);
  }

  /**
   * Send a turn, optionally with images, PDFs, audio or video. Audio
   * attachments are transcribed first and need the `audio` modality; images,
   * PDFs and video need `image` / `pdf` / `video`.
   */
  async sendSessionMessage(
    agent: string,
    sessionId: string,
    options: SendSessionMessageOptions,
  ): Promise<SessionMessageResponse> {
    const attachments = options.attachments ?? [];
    if (!options.content && attachments.length === 0) {
      throw new Error('a session message needs content, attachments, or both');
    }
    const body: Record<string, unknown> = { content: options.content ?? '' };
    if (attachments.length > 0) body.content_parts = attachments;
    if (options.promptVars) body.prompt_vars = options.promptVars;
    if (options.metadata) body.metadata = options.metadata;
    return this.post(`/api/v1/agents/${seg(agent)}/sessions/${seg(sessionId)}/messages`, body);
  }

  // ── Replay (Pro, ADR-0039) ─────────────────────────────────────────────────
  // Re-run a captured call or run with edits. A replay is an investigation, not a reproduction: models are not
  // deterministic and the world has moved on, so a replay is a new trace named `replay:<agent>` and never
  // evidence of what the original run did. The original is never changed. Needs `trace:replay` and
  // `trace:read:content`.

  /**
   * Re-run one captured model call as up to five variants (the server's limit). Runs no tool: the tool calls the
   * model asked for come back, unexecuted. Each variant is `{ label?, edits }`; the server refuses an edit it does
   * not understand. With no variants the call is re-run as captured, which shows how much of the original answer
   * was chance. `reason` is recorded, and is required for an archived agent's trace. The edited prompt goes
   * through the agent's input guardrails and the reply through the output ones; the kitchen's spend gate counts the
   * call. Each variant ends `completed`, `blocked`, `refused` or `failed`.
   */
  replayCall(traceId: string, spanId: string, opts: { variants?: ReplayVariant[]; reason?: string } = {}): Promise<ReplayCallResult> {
    return this.post(`/api/v1/traces/${seg(traceId)}/spans/${seg(spanId)}/replay`, { variants: opts.variants ?? [], reason: opts.reason ?? '' });
  }

  /**
   * Re-run an agent run from one of its model calls (`fromSpan`) with `edits` (the same keys as `replayCall`).
   * `tools` says what the run's tool calls do:
   *
   * - `frozen` (the default): the captured result is returned for as long as the model asks for the same tool call
   *   with the same arguments; when it asks for one with no captured result the replay stops, `diverged`, and shows
   *   both. Nothing is invented and no tool is run.
   * - `blocked`: the call is returned, not run, and the replay ends there.
   * - `live`: only the tools named in `liveTools` run, and only if the agent marked them idempotent (tools with side
   *   effects, the `agentoven_*` tools and calls to other agents never run live). It needs `trace:replay:live-tools`
   *   and is a signed decision on the `live_tool_replay` gate, bound to the trace, the step, the tool list and the
   *   edits: pass `password` (the SDK asks `prepareLiveReplay` what to sign and signs it) or a `signingToken`
   *   signed from that preparation. Without either the server refuses with `signature_required`.
   */
  async replayRun(
    traceId: string,
    fromSpan: string,
    opts: { edits?: ReplayEdits; tools?: ReplayToolMode; liveTools?: string[]; reason?: string } & Signing = {},
  ): Promise<ReplayRunResult> {
    const tools = opts.tools ?? 'frozen';
    if (!REPLAY_TOOL_MODES.includes(tools)) throw new Error(`tools must be one of ${REPLAY_TOOL_MODES.join(', ')}, not ${JSON.stringify(tools)}`);
    const liveTools = [...(opts.liveTools ?? [])];
    if (tools === 'live' && liveTools.length === 0) throw new Error('tools "live" needs liveTools naming the tools that may run live');
    if (tools !== 'live' && (liveTools.length > 0 || opts.password !== undefined || opts.signingToken)) {
      throw new Error('liveTools, password and signingToken only go with tools "live"');
    }
    if (opts.password !== undefined && opts.signingToken) throw new Error('pass password or signingToken, not both');
    let token = opts.signingToken;
    if (opts.password !== undefined) {
      const prep = await this.prepareLiveReplay(traceId, fromSpan, liveTools, { edits: opts.edits, reason: opts.reason });
      token = await this.sign(prep.gate, prep.record_id, 'approved', prep.payload, opts.password);
    }
    return this.request(
      'POST',
      `/api/v1/traces/${seg(traceId)}/replay`,
      { from_span: fromSpan, edits: opts.edits ?? {}, tools, live_tools: liveTools, reason: opts.reason ?? '' },
      undefined,
      token ? { [SIGNING_TOKEN_HEADER]: token } : undefined,
    );
  }

  /**
   * Check a live replay as far as it can be checked, and return what to sign: `{ gate, record_id, payload }`,
   * computed by the server so you never reproduce the digest of the edits. Pass them to `sign`, then the token to
   * `replayRun`. Needs `trace:replay:live-tools`.
   */
  prepareLiveReplay(traceId: string, fromSpan: string, liveTools: string[], opts: { edits?: ReplayEdits; reason?: string } = {}): Promise<PreparedLiveReplay> {
    return this.post(`/api/v1/traces/${seg(traceId)}/replay/prepare`, {
      from_span: fromSpan, edits: opts.edits ?? {}, live_tools: [...liveTools], reason: opts.reason ?? '',
    });
  }

  /**
   * A model call as it was captured, whole: the request (with the tools it offered) and the response. It is the
   * most sensitive thing a trace holds, so reading it needs `trace:read:content` and is audited. A call recorded
   * before complete capture existed answers 404 `not_captured`.
   */
  getSpanCapture(traceId: string, spanId: string): Promise<SpanCapture> {
    return this.get(`/api/v1/traces/${seg(traceId)}/spans/${seg(spanId)}/capture`);
  }

  // ── Secrets (Pro, ADR-0035) ────────────────────────────────────────────────
  // A secret is a name, a reference and metadata; its value lives in a provider (the built-in encrypted vault, or
  // Vault and the like) and is write-only: no method here returns one. Approving or revoking a binding and a real
  // migration are signed decisions: pass `password` (the SDK signs it) or a `signingToken`.

  /** The built-in provider and the configured ones, with what each can do and whether it is healthy (`secret-provider:read`). */
  listSecretProviders(): Promise<SecretProvider[]> {
    return this.get('/api/v1/secret-providers');
  }
  /** Add a provider of a driver kind the platform enabled (`secret-provider:manage`). `config` can only narrow the platform's settings. */
  createSecretProvider(req: { name: string; kind: string; config?: Record<string, unknown>; is_default?: boolean; platform?: boolean }): Promise<SecretProvider> {
    return this.post('/api/v1/secret-providers', req);
  }
  /** Make a provider the default for new secrets (`dbvault` goes back to the built-in one). */
  async setDefaultSecretProvider(name: string, opts: { platform?: boolean } = {}): Promise<void> {
    await this.put(`/api/v1/secret-providers/${seg(name)}/default${queryString({ platform: opts.platform ? 'true' : undefined })}`, {});
  }
  /** Remove a provider no secret lives in (409 `in_use` otherwise). */
  async deleteSecretProvider(name: string, opts: { platform?: boolean } = {}): Promise<void> {
    await this.delete(`/api/v1/secret-providers/${seg(name)}${queryString({ platform: opts.platform ? 'true' : undefined })}`);
  }

  /** The kitchen's secrets: metadata, never values (`secret:read`). */
  listSecrets(): Promise<Secret[]> {
    return this.get('/api/v1/secrets');
  }
  getSecret(name: string): Promise<Secret> {
    return this.get(`/api/v1/secrets/${seg(name)}`);
  }
  /**
   * Create a secret (`secret:write`). With `value` it is written to the provider (which must be writable); with
   * `ref` and no value it links a secret somebody else owns; with neither it adopts a credential made before
   * secrets existed. The value is never returned.
   */
  createSecret(req: CreateSecretRequest): Promise<Secret> {
    return this.post('/api/v1/secrets', req);
  }
  /** Change a secret's metadata; what you leave out stays as it is. */
  updateSecret(name: string, patch: UpdateSecretRequest): Promise<Secret> {
    return this.put(`/api/v1/secrets/${seg(name)}`, patch);
  }
  async deleteSecret(name: string): Promise<void> {
    await this.delete(`/api/v1/secrets/${seg(name)}`);
  }
  /**
   * Rotate to a new value. With `probe` the control plane checks the new value against an upstream first (a host the
   * secret may go to); a value it rejects is not rotated in (422 `probe_rejected`). The previous version stays valid
   * for the grace window.
   */
  rotateSecret(name: string, value: string, opts: { probe?: SecretProbe; grace_seconds?: number } = {}): Promise<RotateSecretResult> {
    return this.put(`/api/v1/secrets/${seg(name)}/value`, { value, ...opts });
  }
  /** Pick up a rotation the secret's owner made (a linked secret). */
  refreshSecret(name: string): Promise<{ changed: boolean; secret: Secret }> {
    return this.post(`/api/v1/secrets/${seg(name)}/refresh`, {});
  }
  /** A secret's versions, without values. */
  secretVersions(name: string): Promise<Array<{ Version: string; State: string; Length: number; CreatedBy: string; CreatedAt: string }>> {
    return this.get(`/api/v1/secrets/${seg(name)}/versions`);
  }
  /** Bindings in the kitchen, or of one secret. */
  listSecretBindings(secret?: string): Promise<SecretBinding[]> {
    return this.get(`/api/v1/secret-bindings${queryString({ secret })}`);
  }
  /** Propose that `secret` be sent for `skill` to `server` (`secret:bind`). `skill: '-'` is a tool registered by hand. Nothing happens until a person approves. */
  proposeSecretBinding(secret: string, req: { skill: string; server?: string; header_scheme?: string }): Promise<SecretBinding> {
    return this.post(`/api/v1/secrets/${seg(secret)}/bindings`, req);
  }
  /**
   * Approve (`approved`) or reject/revoke (`rejected`) a binding: a signed decision on the `secret_binding` gate, by
   * someone other than the person who proposed it.
   */
  async decideSecretBinding(id: string, meaning: 'approved' | 'rejected', opts?: Signing): Promise<SecretBinding> {
    const extra = await this.signingHeaders(opts, 'secret_binding', id, undefined, meaning);
    return this.request('POST', `/api/v1/secret-bindings/${seg(id)}/decision`, { meaning }, undefined, extra);
  }
  /** Withdraw a binding that is still pending. */
  async withdrawSecretBinding(id: string): Promise<void> {
    await this.delete(`/api/v1/secret-bindings/${seg(id)}`);
  }
  /** What moving the kitchen's secrets from one provider to another would do. Nothing changes. */
  planSecretMigration(from: string, to: string): Promise<SecretMigrationPlan> {
    return this.post('/api/v1/secrets/migrate', { from, to, dry_run: true });
  }
  /**
   * Move the kitchen's secrets from one provider to another (`secret:migrate`): copied, read back and compared, then
   * repointed in one step; the source is untouched. A signed decision on the `secret_migrate` gate, record
   * `<from>:<to>`, bound to the digest of the plan.
   */
  async migrateSecrets(from: string, to: string, opts?: Signing): Promise<SecretMigrationPlan & { moved: number }> {
    const extra = await this.signingHeaders(opts, 'secret_migrate', `${from}:${to}`);
    return this.request('POST', '/api/v1/secrets/migrate', { from, to }, undefined, extra);
  }

  // ── Governed datasets (Pro, ADR-0039) ──────────────────────────────────────
  // A dataset is a named set of cases; a case is a snapshot with provenance and a keyed content hash; a version is
  // an append-only manifest of case hashes. Retention is `expire` (with a TTL), `persist` (kept until purged,
  // reviewed on a date) or `regulated` (kept for at least `min_retention_days`; a purge and any lowering of the
  // minimum are signed).

  /** The kitchen's datasets (`dataset:read`). */
  listDatasets(): Promise<Dataset[]> {
    return this.get('/api/v1/datasets');
  }

  /**
   * Create a dataset (`dataset:write`). `name` is 1 to 120 characters. `retention_mode` is `expire`, `persist` or
   * `regulated` (the server's default if left out); `ttl_days` is for `expire`, `review_by` (an RFC 3339 time) for
   * `persist`, `min_retention_days` for `regulated` (default 90, never below 7). `purpose` is required for what
   * keeps data beyond a TTL.
   */
  async createDataset(req: DatasetSettings & { name: string }): Promise<Dataset> {
    checkRetentionMode(req.retention_mode);
    return this.post('/api/v1/datasets', req);
  }

  getDataset(datasetId: string): Promise<Dataset> {
    return this.get(`/api/v1/datasets/${seg(datasetId)}`);
  }

  /**
   * Change a dataset's settings (`dataset:write`). The server takes the settings whole: what you leave out is
   * cleared, so read `getDataset` and send back what should stay. Raising a regulated minimum is allowed; lowering
   * it is `lowerDatasetMinimum` (signed); a regulated dataset cannot leave regulated mode; a change that makes a
   * dataset expire sooner needs `dataset:purge`.
   */
  async updateDataset(datasetId: string, settings: DatasetSettings): Promise<Dataset> {
    checkRetentionMode(settings.retention_mode);
    return this.put(`/api/v1/datasets/${seg(datasetId)}`, settings);
  }

  /** The cases of a dataset's draft. `retired: true` shows the retired ones. */
  listDatasetCases(datasetId: string, opts: { retired?: boolean; limit?: number; offset?: number } = {}): Promise<unknown> {
    return this.get(`/api/v1/datasets/${seg(datasetId)}/cases${queryString({ retired: opts.retired ? 'true' : undefined, limit: opts.limit, offset: opts.offset })}`);
  }

  /**
   * Add a case to the draft (`dataset:write`). `input` is required (any JSON); `metadata` must be an object.
   * `provenance` may carry `source_trace_id`, `span_id`, `agent`, `agent_version`, `environment`, `provider`, `model`.
   */
  addDatasetCase(
    datasetId: string,
    c: { input: unknown; expected_output?: unknown; metadata?: Record<string, unknown>; provenance?: Record<string, unknown> },
  ): Promise<DatasetCase> {
    return this.post(`/api/v1/datasets/${seg(datasetId)}/cases`, {
      input: c.input, expected_output: c.expected_output ?? null, metadata: c.metadata ?? null, ...(c.provenance ? { provenance: c.provenance } : {}),
    });
  }

  /**
   * Copy a span of a trace of this kitchen into a case, with the provenance the trace tells (agent, version,
   * environment, model). The case is a copy: the trace is purged on its own schedule. The span's own output is the
   * expected output unless `expected_output` is present (`null` included, which records none). To add a replay's
   * result, pass the replay's trace and span here.
   */
  addDatasetCaseFromTrace(
    datasetId: string,
    c: { trace_id: string; span_id: string; expected_output?: unknown; metadata?: Record<string, unknown> },
  ): Promise<DatasetCase> {
    const body: Record<string, unknown> = { trace_id: c.trace_id, span_id: c.span_id };
    if ('expected_output' in c) body.expected_output = c.expected_output ?? null;
    if (c.metadata !== undefined) body.metadata = c.metadata;
    return this.post(`/api/v1/datasets/${seg(datasetId)}/cases/from-trace`, body);
  }

  /** Take a case out of the draft. Published versions still list it. */
  async retireDatasetCase(datasetId: string, caseId: string): Promise<void> {
    await this.delete(`/api/v1/datasets/${seg(datasetId)}/cases/${seg(caseId)}`);
  }

  /**
   * Erase a case's payload (`dataset:purge`): input, expected output and metadata go, the content hash and the
   * version manifests stay. A legal hold refuses it.
   */
  eraseDatasetCase(datasetId: string, caseId: string, reason: string): Promise<{ case: DatasetCase; already_erased: boolean }> {
    return this.post(`/api/v1/datasets/${seg(datasetId)}/cases/${seg(caseId)}/erase`, { reason });
  }

  listDatasetVersions(datasetId: string): Promise<Record<string, unknown>[]> {
    return this.get(`/api/v1/datasets/${seg(datasetId)}/versions`);
  }

  /** One version with its manifest. */
  getDatasetVersion(datasetId: string, version: number): Promise<Record<string, unknown>> {
    return this.get(`/api/v1/datasets/${seg(datasetId)}/versions/${Math.trunc(version)}`);
  }

  /** Recompute a version: its manifest digest, the chain to the version before and the content hashes. */
  verifyDatasetVersion(datasetId: string, version: number): Promise<Record<string, unknown>> {
    return this.get(`/api/v1/datasets/${seg(datasetId)}/versions/${Math.trunc(version)}/verify`);
  }

  /**
   * Publish the draft as the next version (`dataset:publish`). A regulated dataset's version is a signed decision
   * (gate `dataset_publish`, record id the dataset's id): pass `password` or a `signingToken`; other datasets ignore
   * it. Refused when the draft is empty (422 `empty_version`) or unchanged (409 `no_changes`).
   */
  async publishDataset(datasetId: string, opts?: Signing): Promise<{ version: Record<string, unknown>; signed: boolean }> {
    const extra = await this.signingHeaders(opts, 'dataset_publish', datasetId);
    return this.request('POST', `/api/v1/datasets/${seg(datasetId)}/versions`, {}, undefined, extra);
  }

  /**
   * Export a version (the latest by default) as JSONL (`dataset:export`): a manifest line with its digests and the
   * result of verifying it, then one line per case in manifest order. An erased case is exported as erased, with no
   * payload. The export is audited. `text` is as received: write that to a file to keep it byte for byte.
   */
  async exportDataset(datasetId: string, version?: number): Promise<DatasetExport> {
    const { text, headers } = await this.requestText('GET', `/api/v1/datasets/${seg(datasetId)}/export${queryString({ version })}`);
    const lines = text.split('\n').filter((l) => l.trim()).map((l) => JSON.parse(l) as Record<string, unknown>);
    const manifest = lines[0] ?? {};
    return {
      version: Number(manifest.version ?? 0),
      manifest_digest: headers.get('x-manifest-digest') ?? String(manifest.manifest_digest ?? ''),
      manifest,
      cases: lines.slice(1),
      text,
    };
  }

  /**
   * Lower a regulated dataset's minimum retention (`dataset:purge`). A signed decision on the `dataset_purge` gate
   * (record id the dataset's id, payload `{ action: 'lower_minimum', new_minimum_days, reason }`): pass `password`
   * or `signingToken`. The server refuses a minimum below its floor (7 days) or a missing reason before anyone is
   * asked to sign.
   */
  async lowerDatasetMinimum(datasetId: string, minRetentionDays: number, reason: string, opts?: Signing): Promise<Dataset> {
    const extra = await this.signingHeaders(opts, 'dataset_purge', datasetId, { action: 'lower_minimum', new_minimum_days: minRetentionDays, reason });
    return this.request('POST', `/api/v1/datasets/${seg(datasetId)}/retention/lower-minimum`, { min_retention_days: minRetentionDays, reason }, undefined, extra);
  }

  /** Place a legal hold on a dataset. Placing only protects, so it takes no signature. While held, purge and erasure are refused. */
  placeDatasetLegalHold(datasetId: string, reason: string): Promise<Dataset> {
    return this.put(`/api/v1/datasets/${seg(datasetId)}/legal-hold`, { held: true, reason });
  }

  /**
   * Lift a legal hold on a dataset. Only the Legal role may, with a stated reason, and the lift is a signed decision
   * on the `legal_hold_release` gate: record id `dataset:<id>`, payload `{ reason }`. Pass `password` (the SDK signs
   * it, `sign`) or a `signingToken` signed for exactly this.
   */
  async liftDatasetLegalHold(datasetId: string, reason: string, opts?: Signing): Promise<Dataset> {
    const extra = await this.signingHeaders(opts, 'legal_hold_release', `dataset:${datasetId}`, { reason });
    return this.request('PUT', `/api/v1/datasets/${seg(datasetId)}/legal-hold`, { held: false, reason }, undefined, extra);
  }

  /**
   * Archive a dataset, then delete it, leaving a tombstone (`dataset:purge`). A regulated dataset's purge is a signed
   * decision on the `dataset_purge` gate (record id the dataset's id, payload `{ action: 'purge', reason }`), refused
   * inside its minimum before anyone is asked to sign; pass `password` or `signingToken` for it. Other datasets
   * ignore them.
   */
  async purgeDataset(datasetId: string, reason: string, opts?: Signing): Promise<{ tombstone: Record<string, unknown>; signed: boolean }> {
    const extra = await this.signingHeaders(opts, 'dataset_purge', datasetId, { action: 'purge', reason });
    return this.request('POST', `/api/v1/datasets/${seg(datasetId)}/purge`, { reason }, undefined, extra);
  }

  /** The datasets the retention janitor removes within `days` (30 by default). */
  listExpiringDatasets(days?: number): Promise<Dataset[]> {
    return this.get(`/api/v1/datasets/expiring${queryString({ days })}`);
  }

  /** The `persist` datasets due for review within `days` (30 by default). */
  listDatasetReviews(days?: number): Promise<Dataset[]> {
    return this.get(`/api/v1/datasets/reviews${queryString({ days })}`);
  }

  /** What stays of purged datasets: who purged, when, why, and the final manifest digest. */
  listDatasetTombstones(): Promise<Record<string, unknown>[]> {
    return this.get('/api/v1/datasets/tombstones');
  }

  // ── Legal hold on the audit trail (Pro, ADR-0043) ──────────────────────────

  /** Place a legal hold on the kitchen's audit trail (`audit:legalhold`). It only protects, so it takes no signature. */
  placeAuditLegalHold(reason = ''): Promise<Record<string, unknown>> {
    return this.post('/api/v1/audit/legal-hold', { hold: true, reason });
  }

  /**
   * Lift the legal hold on the kitchen's audit trail. Only the Legal role may, with a stated reason, and the lift is
   * a signed decision on the `legal_hold_release` gate: record id `audit:<kitchen>` (this client's kitchen), payload
   * `{ reason }`. Pass `password` or a `signingToken`.
   */
  async liftAuditLegalHold(reason: string, opts?: Signing): Promise<Record<string, unknown>> {
    const extra = await this.signingHeaders(opts, 'legal_hold_release', `audit:${this.kitchen}`, { reason });
    return this.request('POST', '/api/v1/audit/legal-hold', { hold: false, reason }, undefined, extra);
  }

  // ── Budgets, rate limits and the response cache (Pro, ADR-0051) ────────────

  /**
   * The kitchen's period budgets with what is left: `scope` (`kitchen`, `provider`, `agent`, `api_key`), `unit`
   * (`usd`, `tokens`), `amount`, `period`, `consumed`, `reserved`, `remaining`, `used_pct`, `exhausted`, `reset_at`
   * and the `thresholds`. Needs `cost:read`.
   */
  async listBudgets(): Promise<Budget[]> {
    return (await this.get<{ budgets: Budget[] }>('/api/v1/spend/budgets')).budgets;
  }

  /**
   * Create or change a period budget (`budget:extend`, the finance decision). With none of `provider`, `agent` and
   * `apiKey` it caps the whole kitchen; with one, only that provider's, agent's or credential's calls (`apiKey` is the
   * credential's subject, for example `scopedkey:<id>`). `unit` is `usd` (the default) or `tokens` (a whole number),
   * `period` one of `BUDGET_PERIODS` (monthly when left out). `thresholds` is a list of `{ pct, action: 'alert' |
   * 'block' }`; the default warns at 50 and 80 percent and stops at 100.
   *
   * Creating a budget or lowering one is not a decision. **Raising** one (a higher amount, a different period, a stop
   * that moves up or goes away) is a signed decision on the `budget_raise` gate: record id `<scope>:<name>:<unit>`
   * (see `budgetRecordId`), payload this request's body. Pass `password` (the SDK signs it, `sign`) or a
   * `signingToken` signed for exactly this; without one the server answers `signature_required` when signing is on.
   */
  async setBudget(amount: number, opts: SetBudgetOptions & Signing = {}): Promise<Budget> {
    const ref = budgetRef(opts);
    if (opts.period !== undefined && !(BUDGET_PERIODS as readonly string[]).includes(opts.period)) {
      throw new Error(`period must be one of ${BUDGET_PERIODS.join(', ')}`);
    }
    const body: Record<string, unknown> = { ...ref, amount };
    if (opts.period !== undefined) body.period = opts.period;
    if (opts.thresholds !== undefined) {
      for (const t of opts.thresholds) if (t.action !== 'alert' && t.action !== 'block') throw new Error("a threshold's action is 'alert' or 'block'");
      body.thresholds = opts.thresholds.map((t) => ({ pct: t.pct, action: t.action }));
    }
    const extra = await this.signingHeaders(opts, 'budget_raise', budgetRecordId(opts), body);
    return this.request('PUT', '/api/v1/spend/budgets', body, undefined, extra);
  }

  /**
   * Remove a budget (`budget:extend`). Removing lifts its limit, so it is signed like a raise: the `budget_raise`
   * gate, payload `{ delete: true }`; pass `password` or `signingToken`. Its ledger entries stay. Rejects with a 404
   * if there is no such budget.
   */
  async removeBudget(opts: BudgetScope & Signing = {}): Promise<void> {
    const ref = budgetRef(opts);
    const extra = await this.signingHeaders(opts, 'budget_raise', budgetRecordId(opts), { delete: true });
    await this.request('DELETE', `/api/v1/spend/budgets${queryString(ref)}`, undefined, undefined, extra);
  }

  /**
   * The ledger, newest first: one entry per model call per budget it was charged to (agent, API key, provider, model,
   * tokens, cost). `since` is an RFC 3339 time, `before` an entry id to page from.
   */
  async listBudgetLedger(opts: { budgetId?: string; since?: string; before?: number; limit?: number } = {}): Promise<BudgetLedgerEntry[]> {
    const q = queryString({ budget_id: opts.budgetId, since: opts.since, before: opts.before, limit: opts.limit });
    return (await this.get<{ entries: BudgetLedgerEntry[] }>(`/api/v1/spend/ledger${q}`)).entries;
  }

  /**
   * The thresholds the kitchen's budgets have crossed, newest first. `notified` is false for an alert the
   * notification channels were not told about (the kitchen was throttled); it is on record all the same.
   */
  async listBudgetAlerts(limit?: number): Promise<BudgetAlert[]> {
    return (await this.get<{ alerts: BudgetAlert[] }>(`/api/v1/spend/alerts${queryString({ limit })}`)).alerts;
  }

  /**
   * What the kitchen's model calls amounted to over the last `days` (30 by default), by `group` (`agent`, `model`,
   * `provider` or `day`): calls, tokens, `cost_usd` and the response cache's `cache_hits`, `cache_misses` and
   * `cache_saved_usd`. Biggest spender first.
   */
  async budgetUsage(group: UsageGroup = 'agent', opts: { days?: number; limit?: number } = {}): Promise<UsageRow[]> {
    if (!['agent', 'model', 'provider', 'day'].includes(group)) throw new Error("group is 'agent', 'model', 'provider' or 'day'");
    return (await this.get<{ usage: UsageRow[] }>(`/api/v1/spend/usage${queryString({ group, days: opts.days, limit: opts.limit })}`)).usage;
  }

  /**
   * The kitchen's rate limits: `scope`, `ref`, `requests_per_minute`, `tokens_per_minute`, `invocations_per_minute`
   * (0 is not limited) and `rejected` (requests it refused since the server process started). Needs `cost:read`.
   */
  async listLimits(): Promise<RateLimit[]> {
    return (await this.get<{ limits: RateLimit[] }>('/api/v1/limits')).limits;
  }

  /**
   * Create or replace a rate limit (`limit:manage`). `scope` is one of `LIMIT_SCOPES`; `ref` names the agent,
   * credential subject or provider (`'*'` gives each its own bucket) and is empty for the kitchen.
   * `requestsPerMinute` counts model calls, `tokensPerMinute` the tokens they use and `invocationsPerMinute` agent
   * invocations at the API (not for a provider). A caller over a limit gets 429 with `Retry-After`
   * (`RateLimitedError`).
   */
  async setLimit(scope: LimitScope, ref: string, rates: { requestsPerMinute?: number; tokensPerMinute?: number; invocationsPerMinute?: number }): Promise<RateLimit> {
    if (!(LIMIT_SCOPES as readonly string[]).includes(scope)) throw new Error(`scope must be one of ${LIMIT_SCOPES.join(', ')}`);
    const { requestsPerMinute = 0, tokensPerMinute = 0, invocationsPerMinute = 0 } = rates;
    if (!requestsPerMinute && !tokensPerMinute && !invocationsPerMinute) {
      throw new Error('a limit sets at least one of requestsPerMinute, tokensPerMinute and invocationsPerMinute (removeLimit removes it)');
    }
    return this.put('/api/v1/limits', {
      scope, ref, requests_per_minute: requestsPerMinute, tokens_per_minute: tokensPerMinute, invocations_per_minute: invocationsPerMinute,
    });
  }

  /** Remove a rate limit (`limit:manage`). Rejects with a 404 if there is none. */
  removeLimit(scope: LimitScope, ref = ''): Promise<void> {
    return this.delete(`/api/v1/limits${queryString({ scope, ref })}`);
  }

  /**
   * The response cache of this kitchen: `policies` (the kitchen's own has an empty `agent`) and `stats` (entries,
   * bytes, hits, misses, stores and why requests were not cached, for the server process you reached). The hit rate
   * over time is in `budgetUsage`.
   */
  getCache(): Promise<CacheState> {
    return this.get('/api/v1/cache');
  }

  /**
   * Turn the response cache on, off or deterministic for the kitchen (`agent` empty) or one agent (`cache:manage`).
   * `mode` is one of `CACHE_MODES`: `deterministic` caches requests that ask for `temperature` 0, `always` any
   * eligible request. An agent's policy replaces the kitchen's for that agent. Entries are kept per caller unless
   * `shareAcrossPrincipals` is true. Setting a policy forgets what was stored under the old one.
   */
  async setCachePolicy(mode: CacheMode, opts: { agent?: string; ttlSeconds?: number; shareAcrossPrincipals?: boolean } = {}): Promise<CachePolicy> {
    if (!(CACHE_MODES as readonly string[]).includes(mode)) throw new Error(`mode must be one of ${CACHE_MODES.join(', ')}`);
    const body: Record<string, unknown> = { agent: opts.agent ?? '', mode, share_across_principals: opts.shareAcrossPrincipals ?? false };
    if (opts.ttlSeconds !== undefined) body.ttl_seconds = opts.ttlSeconds;
    return this.put('/api/v1/cache/policy', body);
  }

  /** Remove a cache policy (back to the kitchen's, or to no caching) (`cache:manage`). */
  removeCachePolicy(agent = ''): Promise<void> {
    return this.delete(`/api/v1/cache/policy${queryString({ agent })}`);
  }

  /** Forget everything stored for the kitchen, or for one agent, on every replica (`cache:manage`). */
  purgeCache(agent = ''): Promise<{ purged_at: string }> {
    return this.post('/api/v1/cache/purge', agent ? { agent } : {});
  }

  // ── Identity-provider bindings, credential owners, my kitchens (Pro, ADR-0042) ──

  /**
   * Which group or app role of the identity provider is which role in this kitchen. A binding takes effect at a
   * person's next login. Only the kitchen's Head Chef or a Restaurateur.
   */
  listKitchenBindings(kitchenId: string): Promise<KitchenBinding[]> {
    return this.get(`/api/v1/kitchens/${seg(kitchenId)}/bindings`);
  }

  /**
   * Bind an identity-provider group (its object id) or app role value to a kitchen `role` (one of `KITCHEN_ROLES`;
   * `restaurateur` is platform-wide and `admin` is retired). Binding a group as `head_chef` is a Restaurateur's decision.
   */
  async createKitchenBinding(kitchenId: string, claimValue: string, role: KitchenRole, label = ''): Promise<KitchenBinding> {
    checkRole(role, KITCHEN_ROLES, 'a kitchen binding');
    if (!claimValue.trim()) throw new Error('claimValue is the group object id or app role value the identity provider puts in the token');
    return this.post(`/api/v1/kitchens/${seg(kitchenId)}/bindings`, { claim_value: claimValue, role, label });
  }

  async deleteKitchenBinding(kitchenId: string, bindingId: string): Promise<void> {
    await this.delete(`/api/v1/kitchens/${seg(kitchenId)}/bindings/${seg(bindingId)}`);
  }

  /**
   * Give a service account to another person: a member of the kitchen at a rank that covers the account's role.
   * Handing it to someone else is a Head Chef's act. It is how a credential whose owner left works again.
   */
  setServiceAccountOwner(saId: string, ownerEmail: string): Promise<ServiceAccount> {
    return this.post(`/api/v1/service-accounts/${seg(saId)}/owner`, { owner_email: ownerEmail });
  }

  /** Give a scoped API key to another person (same rules as `setServiceAccountOwner`). */
  setScopedKeyOwner(keyId: string, ownerEmail: string): Promise<ScopedAPIKey> {
    return this.post(`/api/v1/keys/${seg(keyId)}/owner`, { owner_email: ownerEmail });
  }

  /** The kitchens you can enter and your role in each. */
  myKitchens(): Promise<{ enforced: boolean; kitchens: { id: string; name: string; description?: string; role: UserRole }[] }> {
    return this.get('/api/v1/me/kitchens');
  }

  // ── Agent versions ─────────────────────────────────────────────────────────
  // An agent's version moves only on a bake whose configuration differs from the last bake's: the first bake keeps
  // the registered version (0.1.0), a bake that changes nothing keeps its version, and a changed one takes the next
  // minor version and keeps the old one. Editing a registered agent (`registerAgent`, `PUT`) does not bump it.
  // Naming a `version` on `bake` sets that version as given.

  /** The versions an agent has been baked at, each as the agent was configured then. */
  listAgentVersions(name: string): Promise<Record<string, unknown>[]> {
    return this.get(`/api/v1/agents/${seg(name)}/versions`);
  }

  /** One baked version of an agent (`0.1.0` is the first bake). */
  getAgentVersion(name: string, version: string): Promise<Record<string, unknown>> {
    return this.get(`/api/v1/agents/${seg(name)}/versions/${seg(version)}`);
  }

  // ── Audit events ───────────────────────────────────────────────────────────

  async listAuditEvents(opts: { limit?: number; action?: string; agent?: string; environment?: string } = {}): Promise<AuditEvent[]> {
    const params = new URLSearchParams();
    if (opts.limit) params.set('limit', String(opts.limit));
    if (opts.action) params.set('action', opts.action);
    if (opts.agent) params.set('agent', opts.agent);
    if (opts.environment) params.set('environment', opts.environment);
    const qs = params.toString() ? `?${params.toString()}` : '';
    return this.get(`/api/v1/audit${qs}`);
  }

  // ── Internal helpers ───────────────────────────────────────────────────────

  /**
   * Mint a short-lived token a *browser* can use to open the realtime socket.
   *
   * Browsers cannot set headers on a WebSocket, so a backend that holds the API
   * key calls this and hands the token to the page, which connects with
   * `connectRealtime({ url, agent, token })`. The token is bound to the agent
   * and kitchen, is only checked when the socket opens, and expires in
   * `ttlSeconds` (1-300).
   */
  async realtimeToken(agent: string, ttlSeconds = 60): Promise<RealtimeToken> {
    if (!Number.isInteger(ttlSeconds) || ttlSeconds < 1 || ttlSeconds > 300) {
      throw new Error('ttlSeconds must be a whole number of seconds between 1 and 300');
    }
    return this.post(`/api/v1/realtime/${seg(agent)}/token`, { ttl_seconds: ttlSeconds });
  }

  /**
   * Open a realtime voice session with `agent`. See {@link connectRealtime}.
   * Node with a global WebSocket (verified on Node 24), or pass `webSocket` to supply your own.
   *
   * The agent always runs on its own provider, which must offer the `realtime`
   * modality. Pass `token` (from {@link realtimeToken}) to connect the way a
   * browser does — no auth headers, the token as a WebSocket subprotocol.
   */
  realtime(
    agent: string,
    options: { voice?: string; model?: string; token?: string; webSocket?: WebSocketFactory } = {},
  ): Promise<RealtimeSession> {
    return connectRealtime({ url: this.baseUrl, apiKey: this.apiKey, kitchen: this.kitchen, agent, ...options });
  }

  /** Long-running tasks: start, watch, resume and cancel (`pro.tasks.start(agent, input)`). */
  get tasks(): Tasks {
    return new Tasks({ url: this.baseUrl, apiKey: this.apiKey, kitchen: this.kitchen, timeoutMs: this.timeoutMs });
  }

  /** Upload a file once and name it in messages by `blob_ref` (`pro.blobs.upload(bytes)`). */
  get blobs(): Blobs {
    return new Blobs({ url: this.baseUrl, apiKey: this.apiKey, kitchen: this.kitchen, timeoutMs: this.timeoutMs });
  }

  private headers(kitchenOverride?: string): Record<string, string> {
    const kitchen = kitchenOverride ?? this.kitchen;
    const h: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Kitchen': kitchen,
      'X-Kitchen-Id': kitchen,
    };
    if (this.apiKey) {
      h['Authorization'] = `Bearer ${this.apiKey}`;
    }
    return h;
  }

  private async send(method: string, path: string, body: unknown, kitchenOverride?: string, extraHeaders?: Record<string, string>): Promise<Response> {
    const url = `${this.baseUrl}${path}`;
    const res = await fetch(url, {
      method,
      headers: { ...this.headers(kitchenOverride), ...extraHeaders },
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(this.timeoutMs),
    });

    if (!res.ok) {
      // The body is read once: a failed res.json() has consumed it, and reading it again throws
      // "Body is unusable", which hid every proxy's HTML 502 page behind a TypeError.
      const raw = await res.text().catch(() => '');
      let detail: unknown = raw;
      try {
        detail = JSON.parse(raw);
      } catch {
        /* not JSON (a gateway's error page): keep the text */
      }
      if (res.status === 400 && errorText(detail).includes(NO_AUDIO)) {
        throw new AudioModalityError(res.status, detail);
      }
      throw apiError(res.status, detail, res.headers);
    }
    return res;
  }

  private async request<T>(method: string, path: string, body?: unknown, kitchenOverride?: string, extraHeaders?: Record<string, string>): Promise<T> {
    const res = await this.send(method, path, body, kitchenOverride, extraHeaders);
    const text = await res.text();
    return text ? (JSON.parse(text) as T) : ({} as T);
  }

  /** A response that is not one JSON document (an export): its text and its headers. */
  private async requestText(method: string, path: string): Promise<{ text: string; headers: Headers }> {
    const res = await this.send(method, path, undefined);
    return { text: await res.text(), headers: res.headers };
  }

  private get<T>(path: string, kitchenOverride?: string): Promise<T> {
    return this.request<T>('GET', path, undefined, kitchenOverride);
  }

  private post<T>(path: string, body: unknown, kitchenOverride?: string): Promise<T> {
    return this.request<T>('POST', path, body, kitchenOverride);
  }

  private put<T>(path: string, body: unknown, kitchenOverride?: string): Promise<T> {
    return this.request<T>('PUT', path, body, kitchenOverride);
  }

  private async delete(path: string, kitchenOverride?: string): Promise<void> {
    await this.request<void>('DELETE', path, undefined, kitchenOverride);
  }
}

function providerConfig(
  config: Record<string, unknown> | undefined,
  apiKey: string | undefined,
  modalities: Parameters<typeof resolveModalities>[0] | undefined,
  allowClear: boolean,
): Record<string, unknown> {
  const cfg: Record<string, unknown> = { ...(config ?? {}) };
  if (apiKey !== undefined) cfg.api_key = apiKey;
  if (modalities !== undefined) {
    if ('modalities' in cfg) throw new Error("pass modalities or config.modalities, not both");
    cfg.modalities = modalities;
  }
  if ('modalities' in cfg) {
    cfg.modalities = resolveModalities(cfg.modalities as Parameters<typeof resolveModalities>[0], { allowClear });
  }
  return cfg;
}

/** One signed decision: who signed, when, with what meaning, and what became of the decision. */
/** What the trace list can be narrowed by; the same keys a saved view's filter holds. */
export interface TraceListFilters {
  agent?: string; recipe?: string; status?: string; model?: string; provider?: string; tag?: string; session?: string;
  environment?: string; agent_version?: string; q?: string;
  min_cost?: number; max_cost?: number; min_duration?: number; max_duration?: number;
  range?: '1h' | '24h' | '7d' | '30d'; sort?: 'created_at' | 'cost' | 'duration' | 'tokens'; order?: 'asc' | 'desc';
  roots?: boolean; include_archived?: boolean; limit?: number; offset?: number;
  /** The id of a saved view; its filters apply unless named as well. */
  view?: string;
}

export interface TraceView {
  id: string; kitchen: string; owner: string; owner_name?: string; name: string;
  filter: TraceListFilters; shared: boolean; created_at: string; updated_at: string;
}

export interface TimelineSpan {
  id: string; parent_id?: string; depth: number; kind: string; name: string; status: string;
  offset_ms: number; duration_ms: number; critical?: boolean; skew?: boolean; orphan?: boolean;
  attempt?: number; retry_of?: string; model?: string; provider?: string; error?: string;
  cost_usd?: number; price_source?: string;
}

export interface TraceTimeline {
  schema_version: number; trace_id: string; environment?: string; agent_version?: string;
  started_at: string; total_ms: number; critical_path_ms: number; span_count: number;
  truncated: boolean; cursor?: string; spans: TimelineSpan[];
  models: { model: string; provider?: string; calls: number; input_tokens: number; output_tokens: number; total_tokens: number; cost_usd: number }[];
  lanes: { name: string; events: { at: string; offset_ms: number; action: string; resource?: string; actor?: string; status?: number; seq?: number; entry_hash?: string }[] }[];
}

export interface TraceAudit {
  trace_id: string; count: number; events: Record<string, unknown>[];
  coverage: { from: string | null; complete: boolean; note: string };
}

export interface SignatureRecord {
  id: string;
  kitchen: string;
  seq: number;
  gate: string;
  record_id: string;
  meaning: string;
  digest: string;
  signer_id: string;
  signer_email: string;
  printed_name: string;
  signer_role: string;
  auth_method: string;
  auth_time: string;
  signed_at: string;
  outcome?: string;
  outcome_detail?: string;
}

/** A decision that was announced to people, and where its chasing stands (Pro ADR-0050). */
export interface ApprovalRequestSummary {
  id: string; kitchen: string; gate: string; record_id: string; title: string;
  status: 'open' | 'resolved' | 'expired' | 'gone';
  close_reason?: string; requested_at: string; expires_at?: string;
  stage: number; stage_count: number; notified_at?: string; reminders_sent: number; last_reminder_at?: string; held_until?: string;
  claimed_by?: string; claimed_via?: string; last_error?: string; closed_at?: string;
  /** Messages sent, and sends that failed (in a list). */
  delivered?: number; failed?: number;
}

/** One request with its timeline and, while open, the chain of people who will be asked, in order. */
export interface ApprovalRequestDetail {
  request: ApprovalRequestSummary;
  events: { id: number; at: string; kind: string; stage: number; channel?: string; recipient?: string; actor?: string; detail?: string }[];
  chain?: { stage: number; state: 'done' | 'current' | 'next'; people: { email: string; name: string; role: string }[]; channels: string[] }[];
}

/** How a kind of decision is chased. Every time is in seconds, and 0 means never. */
export interface ApprovalEscalationRule {
  channels?: string[];
  remind_after_seconds: number; remind_every_seconds: number; max_reminders: number;
  escalate_after_seconds: number;
  escalation?: { roles?: string[]; users?: string[]; channels?: string[] }[];
  expire_after_seconds: number; claim_hold_seconds?: number;
}

/** Which budget: the kitchen's (none of the three), or one provider's, agent's or credential's, in dollars or tokens. */
export interface BudgetScope {
  provider?: string;
  agent?: string;
  /** The credential's subject, for example `scopedkey:<id>`. */
  apiKey?: string;
  unit?: BudgetUnit;
}
export type BudgetUnit = (typeof BUDGET_UNITS)[number];
export type BudgetPeriod = (typeof BUDGET_PERIODS)[number];
export type LimitScope = (typeof LIMIT_SCOPES)[number];
export type CacheMode = (typeof CACHE_MODES)[number];
export type UsageGroup = 'agent' | 'model' | 'provider' | 'day';

export interface SetBudgetOptions extends BudgetScope {
  period?: BudgetPeriod;
  thresholds?: { pct: number; action: 'alert' | 'block' }[];
}

/** A period budget as the server reports it. Amounts are in `unit`. */
export interface Budget {
  id: string; kitchen: string; scope: 'kitchen' | 'provider' | 'agent' | 'api_key'; unit: BudgetUnit;
  provider?: string; agent?: string; api_key?: string;
  amount: number; period: BudgetPeriod; period_start: string; reset_at?: string;
  consumed: number; reserved: number; remaining: number; used_pct: number; exhausted: boolean;
  thresholds: { pct: number; action: 'alert' | 'block' }[]; block_pct: number; updated_by?: string; updated_at: string;
}

export interface BudgetLedgerEntry {
  id: number; budget_id: string; kitchen: string; period_start: string; at: string;
  agent?: string; api_key?: string; provider?: string; model?: string; input_tokens: number; output_tokens: number; cost_usd: number;
}

export interface BudgetAlert {
  id: number; budget_id: string; kitchen: string; provider?: string; agent?: string; api_key?: string; unit: BudgetUnit; period?: string;
  period_start: string; threshold_pct: number; action: 'alert' | 'block'; consumed: number; amount: number; fired_at: string;
  /** False when the notification channels were not told (the kitchen was throttled). */
  notified: boolean;
}

export interface UsageRow {
  group: string; calls: number; input_tokens: number; output_tokens: number; cost_usd: number;
  cache_hits: number; cache_misses: number; cache_saved_usd: number;
}

export interface RateLimit {
  id: string; kitchen: string; scope: LimitScope; ref: string;
  requests_per_minute: number; tokens_per_minute: number; invocations_per_minute: number;
  updated_by?: string; updated_at: string;
  /** Requests it refused since the server process started (each replica counts alone). */
  rejected?: number;
}

export interface CachePolicy {
  kitchen: string; agent: string; mode: CacheMode; ttl_seconds: number; share_across_principals: boolean; updated_by?: string; updated_at: string;
}

export interface CacheState {
  policies: CachePolicy[];
  stats: { entries: number; bytes: number; max_bytes: number; hits: number; misses: number; stores: number; bypassed: Record<string, number> };
}

/** How a decision is signed: `password` of your local account (the SDK signs it), or a `signingToken` you already hold. */
export interface Signing {
  password?: string;
  signingToken?: string;
}

export interface SigningChallenge {
  subject: { gate: string; record_id: string; kitchen: string; digest: string; title: string; display: Record<string, unknown>; meanings: string[]; scope?: string; requester?: string };
  meaning: string;
  /** How the person proves who they are: `password` (a local user) or `oidc` (their identity provider). */
  reauth: 'password' | 'oidc';
  token_valid_for_seconds: number;
  printed_name: string;
}

export interface SigningTokenReply {
  signing_token: string;
  expires_at: string;
  digest: string;
  meaning: string;
}

export type ApproverPolicy = Record<string, { roles: string[]; users: string[] }>;

export type ReplayToolMode = 'frozen' | 'blocked' | 'live';

/** The changes a variant makes to the captured request; anything not named is as it was. The server refuses an edit it does not know. */
export interface ReplayEdits {
  model?: string; provider?: string; temperature?: number; max_tokens?: number; top_p?: number; stop?: string[];
  thinking_enabled?: boolean; thinking_budget?: number; tool_choice?: 'auto' | 'none' | 'required' | Record<string, unknown>;
  response_format?: Record<string, unknown>; messages?: { index: number; content: string }[];
}

export interface ReplayVariant {
  label?: string;
  edits: ReplayEdits;
}

export interface ReplayCallResult {
  notice?: string;
  variants: { index: number; label?: string; status: 'completed' | 'blocked' | 'refused' | 'failed'; reason?: string; trace_id?: string; content?: string; [k: string]: unknown }[];
  [k: string]: unknown;
}

export interface ReplayRunResult {
  notice?: string;
  status?: string;
  steps?: Record<string, unknown>[];
  [k: string]: unknown;
}

export interface PreparedLiveReplay {
  gate: string;
  record_id: string;
  payload: Record<string, unknown>;
}

export interface SpanCapture {
  trace_id: string; span_id: string; agent: string; turn: number;
  request: Record<string, unknown> | null; labels?: Record<string, string>; response?: Record<string, unknown> | null; error?: string;
}

export interface DatasetSettings {
  description?: string;
  retention_mode?: 'expire' | 'persist' | 'regulated';
  ttl_days?: number;
  review_by?: string;
  min_retention_days?: number;
  purpose?: string;
}

export interface Dataset extends DatasetSettings {
  id: string;
  name: string;
  legal_hold?: boolean;
  latest_version?: number;
  retention?: Record<string, unknown>;
  [k: string]: unknown;
}

export interface DatasetCase {
  id: string;
  content_hash?: string;
  erased?: boolean;
  [k: string]: unknown;
}

/** A dataset version exported as JSONL; `text` is as received. */
export interface DatasetExport {
  version: number;
  manifest_digest: string;
  manifest: Record<string, unknown>;
  cases: Record<string, unknown>[];
  text: string;
}

export interface KitchenBinding {
  id: string;
  kitchen_id: string;
  kitchen_name?: string;
  claim_value: string;
  role: KitchenRole;
  label: string;
  created_by?: string;
  created_at: string;
}


// ── Secrets (ADR-0035) ──────────────────────────────────────────────────────

export interface SecretProvider {
  name: string;
  kind: string;
  scope: 'builtin' | 'kitchen' | 'platform';
  is_default: boolean;
  writable: boolean;
  healthy: boolean;
  error?: string;
  features: string[];
}

export interface Secret {
  name: string;
  provider: string;
  ref?: string;
  current_version: string;
  label?: string;
  owner?: string;
  linked: boolean;
  /** False for a credential made before secrets existed: it has no metadata yet. */
  managed: boolean;
  allowed_hosts: string[];
  require_binding: boolean;
  expires_at?: string;
  expired?: boolean;
  rotated_at?: string;
  last_used_at?: string;
  previous_version?: string;
  previous_valid_until?: string;
  grace_seconds: number;
}

export interface CreateSecretRequest {
  name: string;
  provider?: string;
  /** Written to the provider. Never returned, logged or audited. */
  value?: string;
  /** Where the value lives in the provider; with no value it links a secret somebody else owns. */
  ref?: string;
  label?: string;
  owner?: string;
  allowed_hosts?: string[];
  expires_at?: string;
  require_binding?: boolean;
  grace_seconds?: number;
}

export interface UpdateSecretRequest {
  label?: string;
  owner?: string;
  allowed_hosts?: string[];
  expires_at?: string;
  clear_expiry?: boolean;
  require_binding?: boolean;
  grace_seconds?: number;
}

export interface SecretProbe {
  url: string;
  method?: 'GET' | 'HEAD' | 'POST';
  /** `bearer` (default) or `api-key:<Header-Name>`. */
  header_scheme?: string;
  expect_status?: number;
}

export interface RotateSecretResult {
  secret: Secret;
  version: string;
  previous_version?: string;
  previous_valid_until?: string;
  probed: boolean;
}

export interface SecretBinding {
  id: string;
  secret: string;
  skill: string;
  server: string;
  header_scheme: string;
  status: 'pending' | 'approved' | 'revoked';
  requested_by: string;
  decided_by?: string;
  signature_id?: string;
}

export interface SecretMigrationPlan {
  kitchen: string;
  from: string;
  to: string;
  secrets: Array<{ name: string; ref: string; version: string; linked: boolean }>;
  conflicts: string[];
  digest: string;
}

// ── Tool packs (ADR-0048) ───────────────────────────────────────────────────────

export interface ToolPackSwitch { enabled?: boolean; require_approval?: boolean }

export interface ToolPackManifest {
  name: string; version: string; title: string; description: string; kind: string;
  secrets: Array<{ role: string; description: string; required: boolean; hint?: string }>;
  settings?: Array<{ key: string; description: string; type: string; default?: unknown; min?: number; max?: number }>;
  tools: Array<{ name: string; description: string; access: 'read' | 'write'; needs?: string[] }>;
  policy: { writes_require_approval: boolean };
}

export interface ToolPackTool {
  name: string; local_name: string; description: string; access: 'read' | 'write'; enabled: boolean; require_approval: boolean;
  schema?: Record<string, unknown>; method?: string; path?: string;
}

export interface ToolPackInstall {
  name: string; pack: string; version: string; kind: string; title: string; description: string;
  /** `awaiting_approval` while skill approval holds the tools; `skill_missing` and `out_of_sync` mean it needs repair. */
  status: 'active' | 'awaiting_approval' | 'rejected' | 'suspended' | 'skill_missing' | string;
  out_of_sync?: boolean; skill: string; settings: Record<string, unknown>; secrets: Record<string, string>;
  source?: Record<string, unknown>; digest: string; installed_by: string; installed_at: string; updated_at: string;
  tools: ToolPackTool[];
}

export interface InstallToolPackRequest {
  pack: string; secrets: Record<string, string>; name?: string; settings?: Record<string, unknown>; tools?: Record<string, ToolPackSwitch>;
}

export interface OpenApiPreview {
  title: string; version: string; description?: string; servers: string[]; sha256: string;
  auth: { type: string; name?: string; token_url?: string; scopes?: string };
  operations: Array<{ id: string; method: string; path: string; summary?: string; tags?: string[]; deprecated?: boolean; access: 'read' | 'write'; unsupported?: string }>;
}

export interface ImportOpenApiRequest {
  name: string; url?: string; spec?: string; base_url?: string;
  auth?: { type?: 'none' | 'bearer' | 'header' | 'query' | 'basic' | 'oauth2_client_credentials'; name?: string; token_url?: string; scopes?: string };
  operations?: string[]; secrets?: Record<string, string>; tools?: Record<string, ToolPackSwitch>; replace?: boolean;
}

export const TRIGGER_KINDS: readonly TriggerKind[] = ['webhook', 'email', 'file'];
export const TRIGGER_DELIVERY_STATUSES: readonly TriggerDeliveryStatus[] = ['accepted', 'rejected', 'duplicate', 'failed'];

export type TriggerKind = 'webhook' | 'email' | 'file';
export type TriggerDeliveryStatus = 'accepted' | 'rejected' | 'duplicate' | 'failed';

export interface Trigger {
  id: string; kitchen: string; name: string; kind: TriggerKind; description?: string;
  target_kind: 'recipe' | 'agent'; target: string; environment?: string; service_account_id: string;
  input_mapping: unknown; required_paths: string[]; config: Record<string, unknown>;
  secret_ref?: string; secret_managed: boolean; rate_limit_per_min: number; enabled: boolean;
  created_by?: string; created_at: string; updated_at: string;
  last_delivery_at?: string; last_polled_at?: string; last_error?: string;
  /** Where a webhook trigger receives, relative to the server. */
  url_path?: string;
}

export interface CreateTriggerRequest {
  name: string;
  /** The service account the work runs as: it belongs to a person, and its role is the most the trigger can do. */
  service_account_id: string;
  target: string;
  kind?: TriggerKind;
  target_kind?: 'recipe' | 'agent';
  environment?: string;
  description?: string;
  input_mapping?: Record<string, unknown>;
  required_paths?: string[];
  config?: Record<string, unknown>;
  secret_ref?: string;
  rate_limit_per_min?: number;
  enabled?: boolean;
}

export interface TriggerDelivery {
  id: string; trigger_id: string; trigger_name: string; source: 'webhook' | 'email' | 'file' | 'test';
  delivery_key?: string; status: TriggerDeliveryStatus; code?: string; reason?: string;
  run_kind?: 'recipe' | 'task'; run_id?: string; test?: boolean;
  payload_bytes: number; payload_sha256?: string; remote_addr?: string; suppressed?: number; received_at: string;
}

export interface TriggerFireResult {
  status: string; dry_run: boolean; code?: string; reason?: string;
  run_id?: string; run_kind?: string; delivery_id?: string; preview?: unknown;
}

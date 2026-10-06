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
  InvokeOptions,
  InvokeResult,
  Kitchen,
  KitchenMember,
  Promotion,
  Provider,
  RealtimeToken,
  Recipe,
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
  TraceabilityMatrix,
  UpdateProviderRequest,
  UpdateProviderResponse,
  UpsertAgentEnvironmentRequest,
  User,
  UserRole,
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

const NO_AUDIO = 'does not offer the audio modality';

function errorText(detail: unknown): string {
  if (typeof detail === 'object' && detail !== null) {
    const d = detail as { error?: unknown; message?: unknown };
    return String(d.error ?? d.message ?? JSON.stringify(detail));
  }
  return String(detail);
}

const seg = encodeURIComponent;

export class ProClient {
  private readonly baseUrl: string;
  private readonly apiKey?: string;
  private readonly kitchen: string;

  constructor(options: AgentOvenClientOptions = {}) {
    this.baseUrl = (options.url ?? 'http://localhost:8080').replace(/\/$/, '');
    this.apiKey = options.apiKey;
    this.kitchen = options.kitchen ?? 'default';
  }

  /** Returns a cloned client bound to a specific kitchen. */
  withKitchen(kitchen: string): ProClient {
    return new ProClient({
      url: this.baseUrl,
      apiKey: this.apiKey,
      kitchen,
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
    return this.get(`/api/v1/kitchens/${kitchenId}`);
  }

  async createKitchen(name: string, displayName?: string): Promise<Kitchen> {
    return this.post('/api/v1/kitchens', { name, display_name: displayName ?? name });
  }

  async deleteKitchen(kitchenId: string): Promise<void> {
    await this.delete(`/api/v1/kitchens/${kitchenId}`);
  }

  async listKitchenMembers(kitchenId: string): Promise<KitchenMember[]> {
    return this.get(`/api/v1/kitchens/${kitchenId}/members`);
  }

  async addKitchenMember(kitchenId: string, userId: string, role: UserRole): Promise<KitchenMember> {
    return this.post(`/api/v1/kitchens/${kitchenId}/members`, { user_id: userId, role });
  }

  // ── User directory ─────────────────────────────────────────────────────────

  async listUsers(): Promise<User[]> {
    return this.get('/api/v1/users');
  }

  async getUser(userId: string): Promise<User> {
    return this.get(`/api/v1/users/${userId}`);
  }

  async createUser(email: string, role: UserRole, name?: string): Promise<User> {
    return this.post('/api/v1/users', { email, role, name: name ?? '' });
  }

  async updateUser(userId: string, fields: Partial<Pick<User, 'name' | 'role'>>): Promise<User> {
    return this.put(`/api/v1/users/${userId}`, fields);
  }

  async deleteUser(userId: string): Promise<void> {
    await this.delete(`/api/v1/users/${userId}`);
  }

  // ── Guardrails ─────────────────────────────────────────────────────────────

  async listGuardrails(): Promise<Guardrail[]> {
    return this.get('/api/v1/guardrails');
  }

  async getGuardrail(guardrailId: string): Promise<Guardrail> {
    return this.get(`/api/v1/guardrails/${guardrailId}`);
  }

  async createGuardrail(req: CreateGuardrailRequest): Promise<Guardrail> {
    return this.post('/api/v1/guardrails', req);
  }

  async updateGuardrail(guardrailId: string, req: Partial<CreateGuardrailRequest>): Promise<Guardrail> {
    return this.put(`/api/v1/guardrails/${guardrailId}`, req);
  }

  async toggleGuardrail(guardrailId: string, enabled: boolean): Promise<Guardrail> {
    return this.put(`/api/v1/guardrails/${guardrailId}/toggle`, { enabled });
  }

  async deleteGuardrail(guardrailId: string): Promise<void> {
    await this.delete(`/api/v1/guardrails/${guardrailId}`);
  }

  async listGuardrailExceptions(guardrailId: string): Promise<GuardrailException[]> {
    return this.get(`/api/v1/guardrails/${guardrailId}/exceptions`);
  }

  async addGuardrailException(guardrailId: string, agentName: string): Promise<GuardrailException> {
    return this.post(`/api/v1/guardrails/${guardrailId}/exceptions`, { agent_name: agentName });
  }

  async removeGuardrailException(guardrailId: string, agentName: string): Promise<void> {
    await this.delete(`/api/v1/guardrails/${guardrailId}/exceptions/${agentName}`);
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

  // ── Recipes ───────────────────────────────────────────────────────────────

  async listRecipes(opts?: { kitchen?: string }): Promise<Recipe[]> {
    return this.get('/api/v1/recipes', opts?.kitchen);
  }

  async getRecipe(name: string, opts?: { kitchen?: string }): Promise<Recipe> {
    return this.get(`/api/v1/recipes/${encodeURIComponent(name)}`, opts?.kitchen);
  }

  async createRecipe(recipe: Partial<Recipe>, opts?: { kitchen?: string }): Promise<Recipe> {
    return this.post('/api/v1/recipes', recipe, opts?.kitchen);
  }

  async deleteRecipe(name: string, opts?: { kitchen?: string }): Promise<void> {
    await this.delete(`/api/v1/recipes/${encodeURIComponent(name)}`, opts?.kitchen);
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
    return this.get(`/api/v1/sessions/${sessionId}`);
  }

  async createSession(agentName: string, data?: unknown): Promise<Session> {
    return this.post('/api/v1/sessions', { agent_name: agentName, data });
  }

  async deleteSession(sessionId: string): Promise<void> {
    await this.delete(`/api/v1/sessions/${sessionId}`);
  }

  // ── Service accounts ───────────────────────────────────────────────────────

  async listServiceAccounts(): Promise<ServiceAccount[]> {
    return this.get('/api/v1/service-accounts');
  }

  /** Token is returned only once — store it securely immediately. */
  async createServiceAccount(name: string, role: UserRole): Promise<CreateServiceAccountResponse> {
    return this.post('/api/v1/service-accounts', { name, role });
  }

  async deleteServiceAccount(saId: string): Promise<void> {
    await this.delete(`/api/v1/service-accounts/${saId}`);
  }

  // ── Schedules ──────────────────────────────────────────────────────────────

  async listSchedules(): Promise<Schedule[]> {
    return this.get('/api/v1/schedules');
  }

  async getSchedule(scheduleId: string): Promise<Schedule> {
    return this.get(`/api/v1/schedules/${scheduleId}`);
  }

  async createSchedule(req: CreateScheduleRequest): Promise<Schedule> {
    return this.post('/api/v1/schedules', { timezone: 'UTC', enabled: true, ...req });
  }

  async updateSchedule(scheduleId: string, fields: Partial<CreateScheduleRequest>): Promise<Schedule> {
    return this.put(`/api/v1/schedules/${scheduleId}`, fields);
  }

  async deleteSchedule(scheduleId: string): Promise<void> {
    await this.delete(`/api/v1/schedules/${scheduleId}`);
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
    return this.get(`/api/v1/test-suites/${id}`);
  }

  async createTestSuite(suite: Omit<TestSuite, 'id' | 'created_at'>): Promise<TestSuite> {
    return this.post('/api/v1/test-suites', suite);
  }

  async updateTestSuite(id: string, suite: Partial<TestSuite>): Promise<TestSuite> {
    return this.put(`/api/v1/test-suites/${id}`, suite);
  }

  async deleteTestSuite(id: string): Promise<void> {
    return this.delete(`/api/v1/test-suites/${id}`);
  }

  async runTestSuite(id: string, trigger = 'manual'): Promise<{ run_id: string; status: string }> {
    return this.post(`/api/v1/test-suites/${id}/run`, { trigger });
  }

  /**
   * Lists runs for one suite. The server has no top-level `GET
   * /api/v1/test-runs` — only this nested route — so, unlike the earlier
   * version of this method, `suiteId` is required, not optional.
   */
  async listTestRuns(suiteId: string): Promise<TestRun[]> {
    return this.get(`/api/v1/test-suites/${suiteId}/runs`);
  }

  async getTestRun(suiteId: string, runId: string): Promise<TestRun> {
    return this.get(`/api/v1/test-suites/${suiteId}/runs/${runId}`);
  }

  async cancelTestRun(runId: string): Promise<void> {
    return this.post(`/api/v1/test-runs/${runId}/cancel`, {});
  }

  // ── World schemas (Pro, ADR-0032) ────────────────────────────────────────────

  async listWorldSchemas(): Promise<WorldSchema[]> {
    return this.get('/api/v1/world-schemas');
  }

  async getWorldSchema(id: string): Promise<WorldSchema> {
    return this.get(`/api/v1/world-schemas/${id}`);
  }

  async createWorldSchema(schema: WorldSchema): Promise<{ schema: WorldSchema; issues: unknown[] }> {
    return this.post('/api/v1/world-schemas', schema);
  }

  async saveWorldSchema(id: string, schema: WorldSchema): Promise<{ schema: WorldSchema; issues: unknown[] }> {
    return this.put(`/api/v1/world-schemas/${id}`, schema);
  }

  async deleteWorldSchema(id: string): Promise<void> {
    return this.delete(`/api/v1/world-schemas/${id}`);
  }

  // ── Scenarios (Pro, ADR-0032) ─────────────────────────────────────────────────

  async listScenarios(): Promise<Scenario[]> {
    return this.get('/api/v1/scenarios');
  }

  async getScenario(id: string): Promise<Scenario> {
    return this.get(`/api/v1/scenarios/${id}`);
  }

  async createScenario(scenario: Scenario): Promise<Scenario> {
    return this.post('/api/v1/scenarios', scenario);
  }

  async saveScenario(id: string, scenario: Scenario): Promise<Scenario> {
    return this.put(`/api/v1/scenarios/${id}`, scenario);
  }

  async deleteScenario(id: string): Promise<void> {
    return this.delete(`/api/v1/scenarios/${id}`);
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
    return this.get(`/api/v1/scenario-runs/${runId}`);
  }

  async listScenarioRuns(): Promise<ScenarioRun[]> {
    return this.get('/api/v1/scenario-runs');
  }

  /** The per-episode detail behind a scenario run's aggregate pass_rate —
   *  one entry per rollout, each with its own graded verdict. */
  async getScenarioRunEpisodes(runId: string): Promise<EpisodeRecord[]> {
    return this.get(`/api/v1/scenario-runs/${runId}/episodes`);
  }

  async cancelScenarioRun(runId: string): Promise<void> {
    return this.post(`/api/v1/scenario-runs/${runId}/cancel`, {});
  }

  // ── Workloads (K8s) ────────────────────────────────────────────────────────

  async listWorkloads(): Promise<Workload[]> {
    return this.get('/api/v1/workloads');
  }

  async getWorkload(workloadId: string): Promise<Workload> {
    return this.get(`/api/v1/workloads/${workloadId}`);
  }

  // ── Agent environments (Pro) ───────────────────────────────────────────────

  /** List all environment deployments for an agent. */
  async listAgentEnvironments(agentName: string): Promise<AgentEnvironment[]> {
    return this.get(`/api/v1/agents/${agentName}/environments`);
  }

  /** Get a specific environment deployment. */
  async getAgentEnvironment(agentName: string, envSlug: string): Promise<AgentEnvironment> {
    return this.get(`/api/v1/agents/${agentName}/environments/${envSlug}`);
  }

  /** Create or update an agent environment deployment. */
  async upsertAgentEnvironment(agentName: string, envSlug: string, req: UpsertAgentEnvironmentRequest): Promise<AgentEnvironment> {
    return this.put(`/api/v1/agents/${agentName}/environments/${envSlug}`, req);
  }

  /** Initiate cool-down / removal of an agent environment deployment. */
  async deleteAgentEnvironment(agentName: string, envSlug: string): Promise<void> {
    return this.delete(`/api/v1/agents/${agentName}/environments/${envSlug}`);
  }

  // ── Scoped API keys (Pro) ─────────────────────────────────────────────────

  async listScopedKeys(): Promise<ScopedAPIKey[]> {
    return this.get('/api/v1/keys');
  }

  async createScopedKey(req: {
    label: string;
    agent_names: string[];
    max_calls?: number;
    expires_in?: string;
  }): Promise<Record<string, unknown>> {
    return this.post('/api/v1/keys', req);
  }

  async getScopedKey(keyId: string): Promise<ScopedAPIKey> {
    return this.get(`/api/v1/keys/${keyId}`);
  }

  async revokeScopedKey(keyId: string): Promise<Record<string, unknown>> {
    return this.post(`/api/v1/keys/${keyId}/revoke`, {});
  }

  async getScopedKeyUsage(keyId: string): Promise<Record<string, unknown>> {
    return this.get(`/api/v1/keys/${keyId}/usage`);
  }

  async deleteScopedKey(keyId: string): Promise<void> {
    await this.delete(`/api/v1/keys/${keyId}`);
  }

  // ── Credentials (Pro) ────────────────────────────────────────────────────

  async listCredentials(): Promise<Record<string, unknown>[]> {
    return this.get('/api/v1/credentials');
  }

  async createCredential(name: string, value: string, label = ''): Promise<Record<string, unknown>> {
    return this.post('/api/v1/credentials', { name, value, label });
  }

  async deleteCredential(credentialName: string): Promise<void> {
    await this.delete(`/api/v1/credentials/${credentialName}`);
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
    if (!options.message && options.audio === undefined) throw new Error('invoke needs a message, audio, or both');
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

  private async request<T>(method: string, path: string, body?: unknown, kitchenOverride?: string): Promise<T> {
    const url = `${this.baseUrl}${path}`;
    const res = await fetch(url, {
      method,
      headers: this.headers(kitchenOverride),
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });

    if (!res.ok) {
      let detail: unknown;
      try {
        detail = await res.json();
      } catch {
        detail = await res.text();
      }
      if (res.status === 400 && errorText(detail).includes(NO_AUDIO)) {
        throw new AudioModalityError(res.status, detail);
      }
      throw new AgentOvenAPIError(res.status, detail);
    }

    const text = await res.text();
    return text ? (JSON.parse(text) as T) : ({} as T);
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

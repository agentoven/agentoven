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

import type {
  AgentEnvironment,
  AgentOvenClientOptions,
  AuditEvent,
  CreateGuardrailRequest,
  CreateScheduleRequest,
  CreateServiceAccountResponse,
  Deployment,
  Environment,
  EpisodeRecord,
  Guardrail,
  GuardrailException,
  Kitchen,
  KitchenMember,
  Promotion,
  Recipe,
  RecipeRun,
  Schedule,
  Scenario,
  ScenarioRun,
  ScenarioRunRequest,
  ScopedAPIKey,
  ServerInfo,
  ServiceAccount,
  Session,
  TestRun,
  TestSuite,
  TraceabilityMatrix,
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

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
  AgentOvenClientOptions,
  AuditEvent,
  CreateGuardrailRequest,
  CreateScheduleRequest,
  CreateServiceAccountResponse,
  Deployment,
  Environment,
  Guardrail,
  GuardrailException,
  Kitchen,
  KitchenMember,
  Promotion,
  Schedule,
  ServerInfo,
  ServiceAccount,
  Session,
  TestRun,
  TestSuite,
  TraceabilityMatrix,
  User,
  UserRole,
  Workload,
} from './types';

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

  async listTestSuites(): Promise<TestSuite[]> {
    return this.get('/api/v1/test-suites');
  }

  async getTestSuite(name: string): Promise<TestSuite> {
    return this.get(`/api/v1/test-suites/${name}`);
  }

  async createTestSuite(suite: Omit<TestSuite, 'id' | 'created_at'>): Promise<TestSuite> {
    return this.post('/api/v1/test-suites', suite);
  }

  async runTestSuite(name: string): Promise<TestRun> {
    return this.post(`/api/v1/test-suites/${name}/run`, {});
  }

  async listTestRuns(suiteName?: string): Promise<TestRun[]> {
    const qs = suiteName ? `?suite=${encodeURIComponent(suiteName)}` : '';
    return this.get(`/api/v1/test-runs${qs}`);
  }

  // ── Workloads (K8s) ────────────────────────────────────────────────────────

  async listWorkloads(): Promise<Workload[]> {
    return this.get('/api/v1/workloads');
  }

  async getWorkload(workloadId: string): Promise<Workload> {
    return this.get(`/api/v1/workloads/${workloadId}`);
  }

  // ── Audit events ───────────────────────────────────────────────────────────

  async listAuditEvents(opts: { limit?: number; action?: string; agent?: string } = {}): Promise<AuditEvent[]> {
    const params = new URLSearchParams();
    if (opts.limit) params.set('limit', String(opts.limit));
    if (opts.action) params.set('action', opts.action);
    if (opts.agent) params.set('agent', opts.agent);
    const qs = params.toString() ? `?${params.toString()}` : '';
    return this.get(`/api/v1/audit${qs}`);
  }

  // ── Internal helpers ───────────────────────────────────────────────────────

  private headers(): Record<string, string> {
    const h: Record<string, string> = {
      'Content-Type': 'application/json',
      'X-Kitchen': this.kitchen,
    };
    if (this.apiKey) {
      h['Authorization'] = `Bearer ${this.apiKey}`;
    }
    return h;
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    const url = `${this.baseUrl}${path}`;
    const res = await fetch(url, {
      method,
      headers: this.headers(),
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

  private get<T>(path: string): Promise<T> {
    return this.request<T>('GET', path);
  }

  private post<T>(path: string, body: unknown): Promise<T> {
    return this.request<T>('POST', path, body);
  }

  private put<T>(path: string, body: unknown): Promise<T> {
    return this.request<T>('PUT', path, body);
  }

  private async delete(path: string): Promise<void> {
    await this.request<void>('DELETE', path);
  }
}

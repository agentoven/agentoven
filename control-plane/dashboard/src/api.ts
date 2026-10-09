// AgentOven API client — talks to the Go control plane at /api/v1.
// In dev, Vite proxies /api → localhost:8080.

const BASE = '/api/v1';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    headers: { 'Content-Type': 'application/json', ...init?.headers },
    ...init,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    // Normalize details: backend may return a newline-delimited string or an array
    let details: string[] | undefined;
    if (Array.isArray(body.details)) {
      details = body.details;
    } else if (typeof body.details === 'string' && body.details) {
      details = body.details
        .split('\n')
        .map((s: string) => s.replace(/^\s*-\s*/, '').trim())
        .filter(Boolean);
    }
    throw new APIError(body.error || `API error ${res.status}`, res.status, details, body);
  }
  // 204 No Content or empty body — return without parsing JSON
  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return undefined as T;
  }
  return res.json();
}

/** Structured API error with optional detail array (e.g. bake validation). */
export class APIError extends Error {
  status: number;
  details?: string[];
  /** Parsed JSON error body, for endpoints whose non-2xx responses carry data (e.g. a 422 skill verdict). */
  body?: unknown;
  constructor(message: string, status: number, details?: string[], body?: unknown) {
    super(message);
    this.status = status;
    this.details = details;
    this.body = body;
  }
}

// ── Ingredients ───────────────────────────────────────────────

export type IngredientKind = 'model' | 'tool' | 'prompt' | 'data' | 'observability' | 'embedding' | 'vectorstore' | 'retriever';

export interface Ingredient {
  id: string;
  name: string;
  kind: IngredientKind;
  config: Record<string, unknown>;
  required: boolean;
}

// ── Agents ────────────────────────────────────────────────────

export interface Guardrail {
  kind: string;
  stage: 'input' | 'output' | 'both';
  config: Record<string, unknown>;
}

export interface Agent {
  id: string;
  name: string;
  description: string;
  framework: string;
  mode: 'managed' | 'external';
  status: string;
  kitchen: string;
  version: string;
  max_turns: number;
  // Agentic behavior fields
  behavior: 'reactive' | 'agentic';
  context_budget: number;
  summary_model: string;
  reasoning_strategy: 'react' | 'plan-and-execute' | 'reflexion';
  a2a_endpoint: string;
  skills: string[];
  model_provider: string;
  model_name: string;
  backup_provider: string;
  backup_model: string;
  ingredients: Ingredient[];
  guardrails: Guardrail[];
  tags: Record<string, string>;
  // Framework-native managed agent fields
  runtime?: 'agentoven' | 'langchain' | 'langgraph' | 'crewai' | 'custom';
  entrypoint?: string;
  repo_url?: string;
  repo_branch?: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface ThinkingBlock {
  content: string;
  token_count: number;
  model: string;
  provider: string;
}

export interface TestAgentResponse {
  agent: string;
  response: string;
  provider: string;
  model: string;
  usage: TokenUsage;
  latency_ms: number;
  trace_id: string;
  thinking_blocks?: ThinkingBlock[];
}

export interface TokenUsage {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  thinking_tokens?: number;
  estimated_cost_usd: number;
}

export interface ToolCallInfo {
  id: string;
  name: string;
  arguments: Record<string, unknown>;
}

export interface ToolResultInfo {
  tool_call_id: string;
  name: string;
  content: string;
  is_error: boolean;
}

export interface ExecutionTurn {
  number: number;
  response?: string;
  tool_calls?: ToolCallInfo[];
  tool_results?: ToolResultInfo[];
  thinking_blocks?: ThinkingBlock[];
  latency_ms: number;
  usage: TokenUsage;
}

export interface ExecutionTrace {
  trace_id: string;
  agent_name: string;
  kitchen: string;
  turns: ExecutionTurn[];
  total_ms: number;
  usage: TokenUsage;
}

export interface InvokeAgentResponse {
  agent: string;
  response: string;
  trace_id: string;
  turns: number;
  usage: TokenUsage;
  latency_ms: number;
  execution_trace: ExecutionTrace;
}

export interface AgentConfig {
  agent: Agent;
  ingredients: {
    model?: { provider: string; kind: string; model: string; endpoint: string };
    tools?: { name: string; endpoint: string; transport: string }[];
    prompt?: { name: string; version: number; template: string };
    data?: { name: string; uri: string }[];
  };
}

export interface LogEntry {
  timestamp: string;
  stream: string; // "stdout" or "stderr"
  line: string;
}

export const agents = {
  list: () => request<Agent[]>('/agents'),
  get: (name: string) => request<Agent>(`/agents/${name}`),
  create: (agent: Partial<Agent>) =>
    request<Agent>('/agents', { method: 'POST', body: JSON.stringify(agent) }),
  update: (name: string, agent: Partial<Agent>) =>
    request<Agent>(`/agents/${name}`, { method: 'PUT', body: JSON.stringify(agent) }),
  delete: (name: string) =>
    request<void>(`/agents/${name}`, { method: 'DELETE' }),
  bake: (name: string) =>
    request<Agent>(`/agents/${name}/bake`, { method: 'POST' }),
  recook: (name: string, edits?: Partial<Agent>) =>
    request<Agent>(`/agents/${name}/recook`, {
      method: 'POST',
      body: edits ? JSON.stringify(edits) : undefined,
    }),
  cool: (name: string) =>
    request<Agent>(`/agents/${name}/cool`, { method: 'POST' }),
  rewarm: (name: string) =>
    request<Agent>(`/agents/${name}/rewarm`, { method: 'POST' }),
  test: (name: string, message: string, thinkingEnabled?: boolean) =>
    request<TestAgentResponse>(`/agents/${name}/test`, {
      method: 'POST',
      body: JSON.stringify({ message, thinking_enabled: thinkingEnabled }),
    }),
  invoke: (name: string, message: string, variables?: Record<string, string>, thinkingEnabled?: boolean) =>
    request<InvokeAgentResponse>(`/agents/${name}/invoke`, {
      method: 'POST',
      body: JSON.stringify({ message, variables, thinking_enabled: thinkingEnabled }),
    }),
  config: (name: string) =>
    request<AgentConfig>(`/agents/${name}/config`),
  logsRecent: (name: string) =>
    request<LogEntry[]>(`/agents/${name}/logs/recent`),
  logsStreamURL: (name: string) => `${BASE}/agents/${name}/logs`,
};

// ── Recipes ───────────────────────────────────────────────────

export interface RecipeStep {
  name: string;
  kind: string;
  agent: string;
  prompt: string;
  depends_on: string[];
  timeout_seconds: number;
  retry_count: number;
}

export interface Recipe {
  id: string;
  name: string;
  description: string;
  kitchen: string;
  steps: RecipeStep[];
  version: string;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export const recipes = {
  list: () => request<Recipe[]>('/recipes'),
  get: (name: string) => request<Recipe>(`/recipes/${name}`),
  create: (recipe: Partial<Recipe>) =>
    request<Recipe>('/recipes', { method: 'POST', body: JSON.stringify(recipe) }),
  delete: (name: string) =>
    request<void>(`/recipes/${name}`, { method: 'DELETE' }),
  bake: (name: string, input: Record<string, unknown>) =>
    request<unknown>(`/recipes/${name}/bake`, { method: 'POST', body: JSON.stringify(input) }),
};

// ── Providers ─────────────────────────────────────────────────

export interface ModelProvider {
  id: string;
  name: string;
  kind: string;
  endpoint: string;
  models: string[];
  config: Record<string, unknown>;
  is_default: boolean;
  created_at: string;
  // Health check cache
  last_tested_at?: string;
  last_test_healthy?: boolean;
  last_test_error?: string;
  last_test_latency_ms?: number;
}

export interface ProviderTestResult {
  provider: string;
  kind: string;
  healthy: boolean;
  latency_ms: number;
  model?: string;
  error?: string;
}

export interface ProviderTemplate {
  kind: string;
  display_name: string;
  description: string;
  default_endpoint: string;
  default_models: string[];
  required_config: string[];
  help_url: string;
}

export const providers = {
  list: () => request<ModelProvider[]>('/models/providers'),
  get: (name: string) => request<ModelProvider>(`/models/providers/${name}`),
  create: (provider: Partial<ModelProvider>) =>
    request<ModelProvider>('/models/providers', { method: 'POST', body: JSON.stringify(provider) }),
  update: (name: string, provider: Partial<ModelProvider>) =>
    request<{ provider: ModelProvider; agents_burnt: number }>(`/models/providers/${name}`, {
      method: 'PUT',
      body: JSON.stringify(provider),
    }),
  delete: (name: string) =>
    request<void>(`/models/providers/${name}`, { method: 'DELETE' }),
  test: (name: string) =>
    request<ProviderTestResult>(`/models/providers/${name}/test`, { method: 'POST' }),
  templates: () =>
    request<ProviderTemplate[]>('/models/providers/templates'),
};

// ── MCP Tools ─────────────────────────────────────────────────

export interface MCPTool {
  id: string;
  name: string;
  description: string;
  kitchen: string;
  endpoint: string;
  transport: string;
  schema: Record<string, unknown>;
  auth_config: Record<string, unknown>;
  capabilities: string[];
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export const tools = {
  list: () => request<MCPTool[]>('/tools'),
  get: (name: string) => request<MCPTool>(`/tools/${name}`),
  create: (tool: Partial<MCPTool>) =>
    request<MCPTool>('/tools', { method: 'POST', body: JSON.stringify(tool) }),
  update: (name: string, tool: Partial<MCPTool>) =>
    request<MCPTool>(`/tools/${name}`, { method: 'PUT', body: JSON.stringify(tool) }),
  delete: (name: string) =>
    request<void>(`/tools/${name}`, { method: 'DELETE' }),
};

// ── Traces ────────────────────────────────────────────────────

export type SpanKind = 'agent' | 'llm' | 'tool' | 'retriever' | 'chain' | 'embedding';

export interface SpanEvent {
  name: string;
  timestamp: string;
  attributes?: Record<string, unknown>;
}

export interface SpanTokenUsage {
  input_tokens: number;
  output_tokens: number;
  thinking_tokens?: number;
  total_tokens: number;
  estimated_cost_usd?: number;
  cache_hits?: number;
  cached_tokens?: number;
}

export interface Span {
  id: string;
  trace_id: string;
  parent_span_id?: string;
  name: string;
  kind: SpanKind;
  status: string;
  start_time: string;
  end_time: string;
  duration_ms: number;
  input?: unknown;
  output?: unknown;
  metadata?: Record<string, unknown>;
  usage?: SpanTokenUsage;
  model?: string;
  provider?: string;
  error?: string;
  events?: SpanEvent[];
}

export interface Trace {
  id: string;
  agent_name: string;
  recipe_name: string;
  kitchen: string;
  status: string;
  duration_ms: number;
  total_tokens: number;
  cost_usd: number;
  metadata: Record<string, unknown>;
  input_text?: string;
  output_text: string;
  parent_trace_id?: string;
  session_id?: string;
  tags?: string[];
  usage?: SpanTokenUsage;
  user_id: string;
  created_at: string;
  spans?: Span[];
}

export const traces = {
  list: (filters?: { agent?: string; recipe?: string; status?: string }) => {
    const params = new URLSearchParams();
    if (filters?.agent) params.set('agent', filters.agent);
    if (filters?.recipe) params.set('recipe', filters.recipe);
    if (filters?.status) params.set('status', filters.status);
    const qs = params.toString();
    return request<Trace[]>(`/traces${qs ? '?' + qs : ''}`);
  },
  get: (id: string) => request<Trace>(`/traces/${id}?spans=true`),
  spans: (traceId: string) => request<Span[]>(`/traces/${traceId}/spans`),
  span: (spanId: string) => request<Span>(`/spans/${spanId}`),
};

// ── Prompts ───────────────────────────────────────────────────

export interface Prompt {
  id: string;
  name: string;
  version: number;
  template: string;
  variables: string[];
  kitchen: string;
  tags: string[];
  created_at: string;
  updated_at: string;
}

export const prompts = {
  list: () => request<Prompt[]>('/prompts'),
  get: (name: string) => request<Prompt>(`/prompts/${name}`),
  create: (prompt: Partial<Prompt>) =>
    request<Prompt>('/prompts', { method: 'POST', body: JSON.stringify(prompt) }),
  update: (name: string, prompt: Partial<Prompt>) =>
    request<Prompt>(`/prompts/${name}`, { method: 'PUT', body: JSON.stringify(prompt) }),
  delete: (name: string) =>
    request<void>(`/prompts/${name}`, { method: 'DELETE' }),
  listVersions: (name: string) =>
    request<Prompt[]>(`/prompts/${name}/versions`),
  getVersion: (name: string, version: number) =>
    request<Prompt>(`/prompts/${name}/versions/${version}`),
};

// ── Recipe Runs ───────────────────────────────────────────────

export interface RecipeRun {
  id: string;
  recipe_id: string;
  kitchen: string;
  status: string;
  input: Record<string, unknown>;
  output: Record<string, unknown>;
  step_results: StepResult[];
  started_at: string;
  completed_at?: string;
  duration_ms: number;
  error?: string;
}

export interface StepResult {
  step_name: string;
  step_kind: string;
  status: string;
  output: Record<string, unknown>;
  agent_ref: string;
  started_at: string;
  duration_ms: number;
  error?: string;
  tokens: number;
  cost_usd: number;
  gate_status?: string;
  branch_taken?: string;
}

export const recipeRuns = {
  list: (recipeName: string) =>
    request<RecipeRun[]>(`/recipes/${recipeName}/runs`),
  get: (recipeName: string, runId: string) =>
    request<RecipeRun>(`/recipes/${recipeName}/runs/${runId}`),
  cancel: (recipeName: string, runId: string) =>
    request<void>(`/recipes/${recipeName}/runs/${runId}/cancel`, { method: 'POST' }),
  approveGate: (recipeName: string, runId: string, stepName: string) =>
    request<void>(`/recipes/${recipeName}/runs/${runId}/gates/${stepName}/approve`, { method: 'POST' }),
};

// ── Embeddings ────────────────────────────────────────────────

export interface EmbedResponse {
  driver: string;
  vectors: number[][];
  dimensions: number;
}

export const embeddingsAPI = {
  list: () => request<string[]>('/embeddings'),
  health: () => request<Record<string, string>>('/embeddings/health'),
  embed: (driver: string, texts: string[]) =>
    request<EmbedResponse>(`/embeddings/${driver}/embed`, {
      method: 'POST',
      body: JSON.stringify({ texts }),
    }),
};

// ── Vector Stores ─────────────────────────────────────────────

export const vectorStoresAPI = {
  list: () => request<string[]>('/vectorstores'),
  health: () => request<Record<string, string>>('/vectorstores/health'),
};

// ── RAG ───────────────────────────────────────────────────────

export interface RAGSearchResult {
  id: string;
  content: string;
  score: number;
  metadata?: Record<string, string>;
}

export interface RAGQueryResult {
  strategy: string;
  results: RAGSearchResult[];
  total_chunks: number;
}

export interface RAGIngestResult {
  documents_processed: number;
  chunks_created: number;
  vectors_stored: number;
}

export const ragAPI = {
  query: (query: string, opts?: { namespace?: string; strategy?: string; top_k?: number }) =>
    request<RAGQueryResult>('/rag/query', {
      method: 'POST',
      body: JSON.stringify({ query, kitchen_id: '', ...opts }),
    }),
  ingest: (documents: { id: string; content: string; metadata?: Record<string, string> }[], namespace?: string) =>
    request<RAGIngestResult>('/rag/ingest', {
      method: 'POST',
      body: JSON.stringify({ kitchen_id: '', namespace: namespace || 'default', documents }),
    }),
};

// ── Connectors ────────────────────────────────────────────────

export interface DataConnector {
  id: string;
  name: string;
  kind: string;
  status: string;
  config: Record<string, unknown>;
  created_at: string;
}

export const connectorsAPI = {
  // The control plane wraps the list as { kitchen, connectors, note }.
  list: async (): Promise<DataConnector[]> => {
    const res = await request<DataConnector[] | { connectors?: DataConnector[] | null }>('/connectors');
    if (Array.isArray(res)) return res;
    return res?.connectors ?? [];
  },
};

// ── Skills ────────────────────────────────────────────────────
// Agent Skills (SKILL.md bundles). OSS registers + verifies in one call; Pro adds a
// staged flow under /skills/pro whose analyze step can pin a provider. Approve/reject of a
// needs_review skill is an OSS endpoint used by both flows.

export type SkillStatus = 'pending' | 'accepted' | 'rejected' | 'needs_review';
export type SkillSource = 'inline' | 'upload' | 'path' | 'git';
export type SkillVerdict = 'accept' | 'reject' | 'needs_review';

export interface SkillMCPServer {
  name: string;
  description?: string;
  transport: string;
  endpoint: string;
  auth_type?: string;
  auth_header?: string;
  credential_ref?: string;
}

export interface SkillManifest {
  name: string;
  description: string;
  license?: string;
  allowed_tools?: string[];
  mcp_tools?: SkillMCPServer[];
  instructions: string;
}

export interface Skill {
  id: string;
  kitchen: string;
  name: string;
  source: SkillSource;
  source_ref?: string;
  manifest: SkillManifest | null;
  status: SkillStatus;
  verification_verdict?: SkillVerdict | '';
  verification_reasoning?: string;
  verified_at?: string;
  verified_by_provider?: string;
  registered_tools?: string[];
  created_at: string;
  updated_at: string;
  created_by?: string;
}

/** Where a skill bundle comes from. Only the fields for the chosen source are sent. */
export interface SkillSourceRequest {
  source: 'git' | 'path' | 'inline';
  git_url?: string;
  git_ref?: string;
  path?: string;
  skill_md?: string;
}

/**
 * Normalized result of a verification call. OSS register and Pro analyze answer with different
 * shapes and non-2xx codes for "blocked" (422) and "needs review" (202); this flattens them.
 */
export interface SkillOutcome {
  outcome: 'accepted' | 'needs_review' | 'rejected';
  reasoning: string;
  note?: string;
  skill?: Skill;
}

export interface SkillStageResult {
  skill: Skill;
  available_providers: string[];
  note?: string;
}

export interface SkillAnalyzeRequest {
  /** Optional: pin the provider; omitted means the kitchen's default. Analysis sends the bundle to that provider's model. */
  provider?: string;
}

function toSkillOutcome(body: Record<string, unknown>): SkillOutcome {
  const status = String(body.status ?? '');
  const isSkill = typeof body.manifest === 'object' && typeof body.name === 'string';
  return {
    outcome: status === 'accepted' ? 'accepted' : status === 'rejected' ? 'rejected' : 'needs_review',
    reasoning: String((isSkill ? body.verification_reasoning : body.reasoning) ?? ''),
    note: typeof body.note === 'string' ? body.note : undefined,
    skill: isSkill ? (body as unknown as Skill) : undefined,
  };
}

async function postForSkillOutcome(path: string, payload: unknown): Promise<SkillOutcome> {
  try {
    const body = await request<Record<string, unknown>>(path, { method: 'POST', body: JSON.stringify(payload) });
    return toSkillOutcome(body);
  } catch (e) {
    // 422 means "verified and blocked": the body is the verdict, not an error to surface as one.
    if (e instanceof APIError && e.status === 422 && e.body && typeof e.body === 'object') {
      return toSkillOutcome(e.body as Record<string, unknown>);
    }
    throw e;
  }
}

const skillPath = (name: string) => `/skills/${encodeURIComponent(name)}`;

export const skills = {
  list: async () => (await request<{ skills: Skill[] | null }>('/skills')).skills ?? [],
  get: (name: string) => request<Skill>(skillPath(name)),
  register: (req: SkillSourceRequest) => postForSkillOutcome('/skills/register', req),
  delete: (name: string) => request<void>(skillPath(name), { method: 'DELETE' }),
  approve: (name: string, note?: string) =>
    request<Skill>(`${skillPath(name)}/approve`, { method: 'POST', body: JSON.stringify({ note: note || undefined }) }),
  reject: (name: string, note?: string) =>
    request<Skill>(`${skillPath(name)}/reject`, { method: 'POST', body: JSON.stringify({ note: note || undefined }) }),
};

export const skillsPro = {
  /**
   * Pro-only routes are simply absent (404/405) on OSS, so probe with a request that has no side
   * effects on Pro (stage with an empty body is rejected as a 400/412 before anything is stored).
   */
  available: async (): Promise<boolean> => {
    try {
      const res = await fetch(`${BASE}/skills/pro/stage`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: '{}',
      });
      return res.status !== 404 && res.status !== 405;
    } catch {
      return false;
    }
  },
  stage: (req: SkillSourceRequest) =>
    request<SkillStageResult>('/skills/pro/stage', { method: 'POST', body: JSON.stringify(req) }),
  analyze: (name: string, req: SkillAnalyzeRequest) =>
    postForSkillOutcome(`/skills/pro/${encodeURIComponent(name)}/analyze`, req),
};

// ── Skill catalogs and plugin import ──────────────────────────
// Importing a plugin (Claude Code or Codex layout) from a git location registers its skills and
// remote MCP servers through the same verification as POST /skills/register. Browsing catalogs
// of plugins, with an allowlist, is AgentOven Pro.

/** Where a plugin lives. */
export interface PluginLocation {
  git_url: string;
  git_ref?: string;
  /** Exact commit the catalog reviewed. */
  git_sha?: string;
  /** The plugin's directory inside the repository. */
  path?: string;
  name?: string;
  skill_paths?: string[];
}

export interface PluginSkill {
  name: string;
  description: string;
  dir: string;
}

export interface ImportServer {
  name: string;
  endpoint: string;
  needs_credential?: boolean;
}

export interface ImportSkipped {
  component: string;
  name?: string;
  reason: string;
}

export type ImportStatus = 'accepted' | 'needs_review' | 'rejected' | 'error';

export interface ImportResult {
  name: string;
  status: ImportStatus;
  reasoning?: string;
  error?: string;
}

export interface ImportResponse {
  plugin: { name: string; description?: string; version?: string; license?: string; format: string };
  skills: PluginSkill[] | null;
  servers: ImportServer[] | null;
  /** Name of the skill that carries the plugin's MCP servers. */
  server_skill?: string;
  skipped: ImportSkipped[] | null;
  /** Absent on a dry run. */
  results?: ImportResult[];
}

export interface ImportRequest extends PluginLocation {
  /** Skill names to take; empty takes all. */
  only?: string[];
  dry_run?: boolean;
  /** MCP server name to kitchen credential name. */
  credentials?: Record<string, string>;
}

export const pluginImport = {
  run: (req: ImportRequest) => request<ImportResponse>('/skills/import', { method: 'POST', body: JSON.stringify(req) }),
};

// ── Model Catalog ─────────────────────────────────────────────

export interface ModelCapability {
  model_id: string;
  provider_kind: string;
  model_name: string;
  display_name?: string;
  context_window?: number;
  max_output_tokens?: number;
  input_cost_per_1k?: number;
  output_cost_per_1k?: number;
  supports_tools?: boolean;
  supports_vision?: boolean;
  supports_streaming?: boolean;
  supports_thinking?: boolean;
  supports_json?: boolean;
  token_param_name?: string;
  api_version?: string;
  modalities?: string[];
  deprecated_at?: string;
  source?: string;
}

export interface DiscoveredModel {
  id: string;
  provider: string;
  kind: string;
  owned_by?: string;
  created_at?: number;
  metadata?: Record<string, string>;
}

export const catalog = {
  list: (provider?: string) =>
    request<{ models: ModelCapability[]; count: number }>(
      `/models/catalog${provider ? `?provider=${provider}` : ''}`,
    ),
  get: (modelID: string, provider?: string) =>
    request<ModelCapability>(
      `/models/catalog/${encodeURIComponent(modelID)}${provider ? `?provider=${provider}` : ''}`,
    ),
  refresh: () =>
    request<{ status: string; message: string }>('/models/catalog/refresh', { method: 'POST' }),
  discover: (providerName: string) =>
    request<{ provider: string; discovered: DiscoveredModel[]; count: number }>(
      `/models/providers/${providerName}/discover`,
      { method: 'POST' },
    ),
  discoveryDrivers: () =>
    request<{ discovery_capable: string[] }>('/models/discovery/drivers'),
};

// ── Health ────────────────────────────────────────────────────

export const health = {
  check: () => request<{ status: string }>('/../../health'),
};

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
    throw new APIError(body.error || `API error ${res.status}`, res.status, body.details);
  }
  return res.json();
}

/** Structured API error with optional detail array (e.g. bake validation). */
export class APIError extends Error {
  status: number;
  details?: string[];
  constructor(message: string, status: number, details?: string[]) {
    super(message);
    this.status = status;
    this.details = details;
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
  a2a_endpoint: string;
  skills: string[];
  model_provider: string;
  model_name: string;
  ingredients: Ingredient[];
  tags: Record<string, string>;
  created_by: string;
  created_at: string;
  updated_at: string;
}

export interface TestAgentResponse {
  agent: string;
  response: string;
  provider: string;
  model: string;
  usage: {
    input_tokens: number;
    output_tokens: number;
    total_tokens: number;
    estimated_cost_usd: number;
  };
  latency_ms: number;
  trace_id: string;
}

export interface InvokeAgentResponse {
  agent: string;
  response: string;
  trace_id: string;
  turns: number;
  usage: {
    input_tokens: number;
    output_tokens: number;
    total_tokens: number;
    estimated_cost_usd: number;
  };
  latency_ms: number;
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
  test: (name: string, message: string) =>
    request<TestAgentResponse>(`/agents/${name}/test`, {
      method: 'POST',
      body: JSON.stringify({ message }),
    }),
  invoke: (name: string, message: string, variables?: Record<string, string>) =>
    request<InvokeAgentResponse>(`/agents/${name}/invoke`, {
      method: 'POST',
      body: JSON.stringify({ message, variables }),
    }),
  config: (name: string) =>
    request<AgentConfig>(`/agents/${name}/config`),
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

export const providers = {
  list: () => request<ModelProvider[]>('/models/providers'),
  get: (name: string) => request<ModelProvider>(`/models/providers/${name}`),
  create: (provider: Partial<ModelProvider>) =>
    request<ModelProvider>('/models/providers', { method: 'POST', body: JSON.stringify(provider) }),
  update: (name: string, provider: Partial<ModelProvider>) =>
    request<ModelProvider>(`/models/providers/${name}`, {
      method: 'PUT',
      body: JSON.stringify(provider),
    }),
  delete: (name: string) =>
    request<void>(`/models/providers/${name}`, { method: 'DELETE' }),
  test: (name: string) =>
    request<ProviderTestResult>(`/models/providers/${name}/test`, { method: 'POST' }),
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
  created_at: string;
}

export const traces = {
  list: () => request<Trace[]>('/traces'),
  get: (id: string) => request<Trace>(`/traces/${id}`),
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
  list: () => request<DataConnector[]>('/connectors'),
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

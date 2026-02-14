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
    throw new Error(body.error || `API error ${res.status}`);
  }
  return res.json();
}

// ── Agents ────────────────────────────────────────────────────

export interface Agent {
  id: string;
  name: string;
  description: string;
  framework: string;
  status: string;
  kitchen: string;
  version: string;
  a2a_endpoint: string;
  skills: string[];
  model_provider: string;
  model_name: string;
  ingredients: unknown[];
  tags: Record<string, string>;
  created_by: string;
  created_at: string;
  updated_at: string;
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
  cool: (name: string) =>
    request<Agent>(`/agents/${name}/cool`, { method: 'POST' }),
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
}

export const providers = {
  list: () => request<ModelProvider[]>('/models/providers'),
  get: (name: string) => request<ModelProvider>(`/models/providers/${name}`),
  create: (provider: Partial<ModelProvider>) =>
    request<ModelProvider>('/models/providers', { method: 'POST', body: JSON.stringify(provider) }),
  delete: (name: string) =>
    request<void>(`/models/providers/${name}`, { method: 'DELETE' }),
  health: () => request<unknown>('/models/health'),
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
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export const tools = {
  list: () => request<MCPTool[]>('/tools'),
  get: (name: string) => request<MCPTool>(`/tools/${name}`),
  create: (tool: Partial<MCPTool>) =>
    request<MCPTool>('/tools', { method: 'POST', body: JSON.stringify(tool) }),
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

// ── Health ────────────────────────────────────────────────────

export const health = {
  check: () => request<{ status: string }>('/../../health'),
};

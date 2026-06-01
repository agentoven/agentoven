/**
 * TypeScript type definitions for the AgentOven SDK.
 */

export interface AgentOvenClientOptions {
  /** Control plane URL. Default: http://localhost:8080 */
  url?: string;
  /** API key for authentication */
  apiKey?: string;
  /** Kitchen (workspace) name. Default: "default" */
  kitchen?: string;
}

export interface RegisterAgentOptions {
  /** Human-readable description */
  description?: string;
  /** Agent framework: langchain, crewai, autogen, openai, custom */
  framework?: string;
  /** Semantic version */
  version?: string;
}

// ── Core domain types ────────────────────────────────────────────────────────

export type AgentStatus = 'draft' | 'baking' | 'ready' | 'cooled' | 'burnt' | 'retired';
export type IngredientKind = 'model' | 'tool' | 'prompt' | 'data';
export type GuardrailKind = 'keyword' | 'regex' | 'prompt-injection' | 'llamaguard' | 'custom';
export type GuardrailStage = 'input' | 'output' | 'both';
export type EnvironmentKind = 'dev' | 'qa' | 'staging' | 'prod';
export type UserRole = 'admin' | 'chef' | 'baker' | 'auditor' | 'finance' | 'viewer';

export interface Ingredient {
  name: string;
  kind: IngredientKind;
  provider?: string;
  role?: string;
  protocol?: string;
  required: boolean;
  config?: unknown;
}

export interface Step {
  name: string;
  kind: string;
  agent?: string;
  parallel: boolean;
  timeout?: string;
  human_gate: boolean;
  notify: string[];
  depends_on: string[];
}

export interface Branch {
  name: string;
  condition: string;
  target: string;
}

export interface Recipe {
  name: string;
  description: string;
  version: string;
  steps: Step[];
}

export interface RecipeRun {
  id: string;
  recipe_name: string;
  status: string;
  started_at: string;
  finished_at?: string;
  output?: unknown;
}

// ── Kitchen (Pro) ─────────────────────────────────────────────────────────────

export interface Kitchen {
  id: string;
  name: string;
  display_name: string;
  created_at: string;
}

export interface KitchenMember {
  user_id: string;
  role: UserRole;
  joined_at: string;
}

// ── User (Pro) ───────────────────────────────────────────────────────────────

export interface User {
  id: string;
  email: string;
  name: string;
  role: UserRole;
  created_at: string;
}

// ── Guardrail (Pro) ───────────────────────────────────────────────────────────

export interface Guardrail {
  id: string;
  name: string;
  kind: GuardrailKind;
  stage: GuardrailStage;
  enabled: boolean;
  overridable: boolean;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface CreateGuardrailRequest {
  name: string;
  kind: GuardrailKind;
  stage: GuardrailStage;
  enabled?: boolean;
  overridable?: boolean;
  config?: Record<string, unknown>;
}

export interface GuardrailException {
  id: string;
  guardrail_id: string;
  agent_name: string;
  created_at: string;
}

// ── Environment / Promotion (Pro) ─────────────────────────────────────────────

export interface Environment {
  id: string;
  name: string;
  kind: EnvironmentKind;
  kitchen: string;
  created_at: string;
}

export interface Promotion {
  id: string;
  agent_name: string;
  from_env: string;
  to_env: string;
  version?: string;
  status: string;
  promoted_at: string;
  promoted_by: string;
}

export interface Deployment {
  id: string;
  agent_name: string;
  environment: string;
  version: string;
  status: string;
  deployed_at: string;
}

export interface TraceabilityMatrix {
  agents: string[];
  environments: string[];
  cells: Record<string, Record<string, { version: string; status: string; deployed_at: string } | null>>;
}

// ── Session (Pro) ─────────────────────────────────────────────────────────────

export interface Session {
  id: string;
  agent_name: string;
  kitchen: string;
  created_at: string;
  expires_at?: string;
  data?: unknown;
}

// ── Service Account (Pro) ─────────────────────────────────────────────────────

export interface ServiceAccount {
  id: string;
  name: string;
  role: UserRole;
  kitchen: string;
  created_at: string;
}

export interface CreateServiceAccountResponse extends ServiceAccount {
  /** Token is returned only on creation — store it securely. */
  token: string;
}

// ── Schedule (Pro) ────────────────────────────────────────────────────────────

export interface Schedule {
  id: string;
  recipe_name: string;
  cron: string;
  timezone: string;
  enabled: boolean;
  last_run_at?: string;
  next_run_at?: string;
  created_at: string;
}

export interface CreateScheduleRequest {
  recipe_name: string;
  cron: string;
  timezone?: string;
  enabled?: boolean;
}

// ── Test Suite (Pro) ──────────────────────────────────────────────────────────

export interface TestCase {
  input: string;
  expected_contains?: string;
  expected_not_contains?: string;
  timeout_secs?: number;
}

export interface TestSuite {
  id: string;
  name: string;
  agent_name: string;
  schedule?: string;
  cases: TestCase[];
  created_at: string;
}

export interface TestRun {
  id: string;
  suite_name: string;
  status: 'pending' | 'running' | 'passed' | 'failed' | 'error';
  started_at: string;
  finished_at?: string;
  passed: number;
  failed: number;
  total: number;
}

// ── Workload (Pro / K8s) ──────────────────────────────────────────────────────

export interface Workload {
  id: string;
  agent_name: string;
  kitchen: string;
  namespace: string;
  image: string;
  status: string;
  environment_slug?: string;
  created_at: string;
}

// ── Agent Environment (Pro) ───────────────────────────────────────────────────

export type AgentEnvStatus = 'baking' | 'ready' | 'error' | 'cooling';
export type GuardrailPolicy = 'inherit' | 'strict' | 'relaxed' | 'disabled';

export interface AgentEnvironment {
  id: string;
  kitchen_id: string;
  agent_name: string;
  env_slug: string;
  version: string;
  provider_name: string;
  model_name: string;
  provider_overrides?: Record<string, unknown>;
  tool_overrides?: Record<string, unknown>;
  guardrail_policy: GuardrailPolicy;
  required_guardrails: string[];
  disabled_guardrails: string[];
  status: AgentEnvStatus;
  backend_endpoint: string;
  workload_id?: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
}

export interface UpsertAgentEnvironmentRequest {
  version?: string;
  provider_name?: string;
  model_name?: string;
  provider_overrides?: Record<string, unknown>;
  tool_overrides?: Record<string, unknown>;
  guardrail_policy?: GuardrailPolicy;
  required_guardrails?: string[];
  disabled_guardrails?: string[];
  backend_endpoint?: string;
}

// ── Scoped API Key (Pro) ──────────────────────────────────────────────────────

export interface ScopedAPIKey {
  id: string;
  name: string;
  prefix: string;
  agent_name: string;
  kitchen: string;
  scopes: string[];
  /** Glob list of allowed environment slugs. "*" means all environments. */
  environment_names: string[];
  quota: number;
  call_count: number;
  expires_at?: string;
  last_used_at?: string;
  revoked: boolean;
  created_by: string;
  created_at: string;
}

// ── Audit Event ───────────────────────────────────────────────────────────────

export interface AuditEvent {
  id: string;
  action: string;
  actor: string;
  kitchen: string;
  agent?: string;
  /** Environment slug for env-scoped events (e.g. A2A proxy, guardrail blocks). */
  environment?: string;
  resource_kind: string;
  resource_id: string;
  timestamp: string;
  metadata?: Record<string, unknown>;
}

// ── Server Info ───────────────────────────────────────────────────────────────

export interface ServerInfo {
  version: string;
  edition: 'community' | 'pro' | 'enterprise';
  plan: string;
  features: string[];
  license_id?: string;
  expires_at?: string;
}

// ── API Error ─────────────────────────────────────────────────────────────────

export interface APIErrorResponse {
  error: string;
  message?: string;
  code?: string;
}


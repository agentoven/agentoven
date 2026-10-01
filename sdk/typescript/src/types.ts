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
//
// A case is exactly one of two modes, mirrored from how the server's executor
// branches on ExpectedScenarioID (internal/testsuite/executor.go) and what it
// now validates at write time (internal/testsuite/handlers.go
// validateCases): a plain input/expected-output check, or — when
// expected_scenario_id is set — a scenario auto-pick check that ignores
// input/expected_output entirely and instead asks the agent to run with no
// scenario named, verifying it resolves to this one and passes.

export interface TestCase {
  id?: string;
  name: string;
  input?: string;
  expected_output?: string;
  tags?: string[];
  variables?: Record<string, string>;
  max_latency_ms?: number;
  /** Switches this case to scenario auto-pick mode. */
  expected_scenario_id?: string;
  /** Gates the scenario's own pass rate; unset/0 requires pass_rate == 1. */
  min_pass_rate?: number;
}

export interface TestSuite {
  id: string;
  name: string;
  description?: string;
  agent_name: string;
  kitchen?: string;
  cases: TestCase[];
  schedule?: string;
  next_run_at?: string;
  enabled?: boolean;
  tags?: string[];
  tracker_ref?: string;
  created_by?: string;
  created_at: string;
  updated_at?: string;
}

export interface TestResult {
  case_id: string;
  case_name: string;
  input: string;
  expected_output: string;
  actual_output: string;
  passed: boolean;
  latency_ms: number;
  tokens_used: number;
  cost_usd: number;
  error: string;
  trace_id: string;
  /** Set for a scenario auto-pick case: the scenario the agent's own
   *  ingredients actually resolved to. */
  actual_scenario_id?: string;
}

export interface TestRunSummary {
  total_cases: number;
  passed: number;
  failed: number;
  errors: number;
  pass_rate: number;
  avg_latency_ms: number;
  total_tokens: number;
  total_cost_usd: number;
  p50_latency_ms: number;
  p95_latency_ms: number;
}

export interface TestRun {
  id: string;
  suite_id: string;
  suite_name: string;
  kitchen: string;
  agent_name: string;
  status: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled' | 'interrupted';
  results?: TestResult[];
  summary?: TestRunSummary;
  trigger: string;
  environment?: string;
  agent_version?: string;
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  created_by?: string;
}

// ── World Schema / Scenario Environment (Pro, ADR-0032) ────────────────────────
//
// Deliberately loose (`Record<string, unknown>` for nested bodies) rather than
// a full mirror of every field the dashboard's own client types — the
// server's request/response shapes for these are still evolving, and a
// hand-typed field the server renames silently breaks a real TS consumer,
// while an `unknown` field they access with a cast just needs a cast. Get the
// commonly-used top-level fields right; let callers narrow the rest.

export interface WorldSchema {
  id: string;
  version: string;
  description?: string;
  status?: string;
  classes: Record<string, unknown>[];
  relationships?: Record<string, unknown>[];
  constraints?: Record<string, unknown>[];
}

export interface Scenario {
  id: string;
  version: number;
  ontology_version: string;
  description?: string;
  tags?: string[];
  world: Record<string, unknown>;
  tools?: Record<string, unknown>[];
  actors?: Record<string, unknown>[];
  task: { agent_goal: string; agent_context?: string[] };
  assertions: Record<string, unknown>;
  reward?: Record<string, unknown>;
  rubric?: Record<string, unknown>;
  judge?: string;
}

export interface ScenarioRunRequest {
  agent_name: string;
  scenario_ids?: string[];
  rollouts?: number;
  seeds?: number[];
  environment?: string;
  trigger?: string;
  allow_production?: boolean;
}

export interface ScenarioResult {
  scenario_id: string;
  episodes: number;
  passed: number;
  failed: number;
  invalid: number;
  pass_rate: number;
  ci_low: number;
  ci_high: number;
  mean_reward: number;
  total_cost_usd: number;
  min_pass_rate?: number;
  gate?: string;
}

export interface ScenarioRun {
  id: string;
  kitchen: string;
  status: 'queued' | 'running' | 'completed' | 'failed' | 'cancelled' | 'interrupted';
  request: ScenarioRunRequest;
  results?: ScenarioResult[];
  error?: string;
  done: number;
  total: number;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

export interface AssertionResult {
  name: string;
  kind: string;
  satisfied: boolean;
  got?: unknown;
  want?: unknown;
  error?: string;
}

export interface EpisodeVerdict {
  status: string;
  reward: number;
  assertions: AssertionResult[];
  reason?: string;
}

export interface EpisodeRecord {
  run_id: string;
  scenario_id: string;
  seed: number;
  episode?: Record<string, unknown>;
  verdict?: EpisodeVerdict;
  error?: string;
  actor_cost_usd?: number;
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
  key_prefix: string;
  kitchen: string;
  agent_names: string[];
  environment_names: string[];
  max_calls: number;
  call_count: number;
  role?: string;
  expires_at?: string;
  revoked: boolean;
  created_by: string;
  created_at: string;
  updated_at?: string;
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
  service?: string;
  version: string;
  edition: 'community' | 'pro' | 'enterprise';
  plan: string;
  org?: string;
  features: {
    environments: boolean;
    test_suites: boolean;
    service_accounts: boolean;
    scoped_keys: boolean;
    sso: boolean;
    federation: boolean;
    cloud_providers: boolean;
    audit: boolean;
    rag: boolean;
    guardrails: boolean;
    promotions: boolean;
    phone_home: boolean;
  };
  limits?: Record<string, unknown>;
  auth?: {
    providers: string[];
    sso_enabled: boolean;
    require_auth: boolean;
  };
  license?: {
    valid: boolean;
    expires_at?: string;
    org?: string;
    plan: string;
    license_id?: string;
  };
}

// ── API Error ─────────────────────────────────────────────────────────────────

export interface APIErrorResponse {
  error: string;
  message?: string;
  code?: string;
}


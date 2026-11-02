/**
 * TypeScript type definitions for the AgentOven SDK.
 */

import type { AudioClip, AudioInput, ContentPart } from './media.js';
import type { ModalitiesBuilder, ModalitiesConfig, ModalitiesUpdate } from './modalities.js';

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
export type GuardrailKind =
  | 'content_filter'
  | 'pii_detection'
  | 'topic_restriction'
  | 'max_length'
  | 'regex_filter'
  | 'prompt_injection'
  | 'llamaguard'
  | 'custom';
/** `retrieval` is each retrieved chunk before it enters the prompt; `all` is input, output and retrieval. */
export type GuardrailStage = 'input' | 'output' | 'both' | 'retrieval' | 'all';
/**
 * What a rule does when it fires. `block` refuses (what a rule with no action does); `redact` replaces each match with a
 * placeholder such as `[IBAN]` and the rest of the text continues; `mask` keeps the last four characters; `warn` lets the
 * text through and records it; `require_approval` pauses a tool call for a person (anywhere else it blocks).
 */
export type GuardrailAction = 'block' | 'redact' | 'mask' | 'warn' | 'require_approval';
export type EnvironmentKind = 'dev' | 'qa' | 'staging' | 'prod';
/**
 * The roles a person or credential can hold inside one kitchen. The retired `admin` is gone: it was a Head Chef
 * inside a kitchen and a Restaurateur across the installation.
 */
export type KitchenRole = 'head_chef' | 'chef' | 'baker' | 'taster' | 'auditor' | 'legal' | 'finance' | 'viewer';
/** The role of a person in the user directory: the kitchen roles and the platform-wide `restaurateur`. */
export type UserRole = 'restaurateur' | KitchenRole;

export interface Ingredient {
  name: string;
  kind: IngredientKind;
  provider?: string;
  role?: string;
  protocol?: string;
  required: boolean;
  config?: unknown;
}

/**
 * A recipe step as the control plane stores it. Each step names the steps it waits for (`depends_on`), and a
 * step with `branches` (and `default_next`) picks which of the steps that wait for it runs. Build recipes with
 * `Flow` rather than by hand; it writes these.
 */
export interface Step {
  name: string;
  kind: string;
  agent_ref?: string;
  agent?: string;
  depends_on?: string[];
  config?: Record<string, unknown>;
  branches?: { condition: string; next_step: string }[];
  default_next?: string;
  max_retries?: number;
  timeout_secs?: number;
  notify_tools?: string[];
  [extra: string]: unknown;
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

export interface StepResult {
  step_name: string;
  step_kind: string;
  status: string;
  output?: Record<string, unknown>;
  agent_ref?: string;
  started_at?: string;
  duration_ms?: number;
  error?: string;
  tokens?: number;
  cost_usd?: number;
  /** `waiting`, `approved`, `rejected` or `timed_out` for an approval step. */
  gate_status?: string;
  branch_taken?: string;
}

/** A place notifications go. A `websocket` channel is a live feed. */
export interface NotificationChannel {
  id: string;
  name: string;
  kind: string;
  active: boolean;
  url?: string;
  events?: string[];
}

export interface RecipeRun {
  id: string;
  recipe_id?: string;
  status: string;
  started_at: string;
  completed_at?: string;
  duration_ms?: number;
  input?: Record<string, unknown>;
  output?: unknown;
  error?: string;
  step_results?: StepResult[];
  /** Steps waiting for a person, on a `paused` run, when read with `waitForRecipeRun`. */
  pending_gates?: string[];
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
  role: KitchenRole;
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
  action: GuardrailAction;
  enabled: boolean;
  overridable: boolean;
  priority?: number;
  config: Record<string, unknown>;
  created_at: string;
  updated_at: string;
}

export interface CreateGuardrailRequest {
  name: string;
  kind: GuardrailKind;
  stage: GuardrailStage;
  action?: GuardrailAction;
  enabled?: boolean;
  overridable?: boolean;
  priority?: number;
  config?: Record<string, unknown>;
}

export interface GuardrailException {
  id: string;
  guardrail_id: string;
  agent_name: string;
  reason?: string;
  approved_by?: string;
  expires_at?: string;
  created_at: string;
}

/** The kinds, stages, actions and PII/secret detectors a rule can use. */
export interface GuardrailKinds {
  kinds: Array<{ kind: GuardrailKind; label: string; description: string; config_schema: Record<string, string> }>;
  stages: GuardrailStage[];
  actions: GuardrailAction[];
  detectors: string[];
}

export interface TestGuardrailsOptions {
  /** Where the text is: a user message, an agent reply or a retrieved chunk. Default `input`. */
  stage?: 'input' | 'output' | 'retrieval';
  /** Rules to try. Without them the kitchen's workspace rules are used. */
  rules?: Array<Partial<CreateGuardrailRequest>>;
  /** Also call custom endpoint rules (needs `settings:update`). */
  includeCustom?: boolean;
}

/** What rules would do to a text. `findings` carry detector types and counts, never the matched text. */
export interface GuardrailTestResult {
  outcome: 'pass' | 'redacted' | 'warned' | 'approval_required' | 'blocked';
  passed: boolean;
  approval_required?: boolean;
  redacted: number;
  warned: number;
  /** The text that would continue: the input with every redaction applied. */
  text: string;
  skipped_custom?: number;
  results: Array<{
    passed: boolean;
    kind: string;
    stage: string;
    message?: string;
    rule_id?: string;
    rule_name?: string;
    action?: GuardrailAction;
    redacted?: number;
    findings?: Array<{ type: string; count: number }>;
  }>;
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
  role: KitchenRole;
  kitchen: string;
  /** The person the account acts on behalf of (Pro): when they leave the kitchen it stops until it has a new owner. */
  owner_email?: string;
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
  /** The person the key belongs to (Pro). */
  owner_email?: string;
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

// ── Tools (MCP gateway) ───────────────────────────────────────────────────────

/** A tool as the control plane reports it. `auth_config` is never returned. */
export interface Tool {
  id: string;
  name: string;
  description: string;
  kitchen: string;
  endpoint: string;
  /** `http`, `sse` or `stdio` when registering through the tools route. */
  transport: string;
  /** JSON Schema of the tool's arguments. */
  schema?: Record<string, unknown>;
  capabilities: string[];
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface RegisterToolRequest {
  /** The name agents list as a `tool` ingredient. */
  name: string;
  /** Where the gateway calls the tool. */
  endpoint: string;
  description?: string;
  /** `http` (default), `sse` or `stdio`; the server answers 400 to anything else. */
  transport?: string;
  /** JSON Schema of the tool's arguments. */
  schema?: Record<string, unknown>;
  /** How the gateway authenticates to the tool. Stored by the server, never returned. */
  auth_config?: Record<string, unknown>;
  /** Default `["tool"]`. */
  capabilities?: string[];
  /** Default `true`. `false` registers the tool switched off. */
  enabled?: boolean;
}

// ── Providers & modalities ────────────────────────────────────────────────────

/** A model provider as the control plane reports it. */
export interface Provider {
  id?: string;
  name: string;
  kind: string;
  endpoint?: string;
  models: string[];
  /** Stored config (secrets are masked). `config.modalities` holds the overrides. */
  config?: Record<string, unknown>;
  is_default: boolean;
  created_at?: string;
  /**
   * Effective modalities (`["text", "image", ...]`): the union over the
   * provider's models of what the driver, the model catalog and
   * `config.modalities` allow. The server computes it on list, get, create and
   * update responses; an older server may omit it on create/update, in which
   * case call `getProvider` for the effective list.
   */
  modalities?: string[];
}

export interface CreateProviderRequest {
  name: string;
  kind: string;
  endpoint?: string;
  models?: string[];
  /** Convenience for `config.api_key`. */
  api_key?: string;
  config?: Record<string, unknown>;
  /** Builder (`modalities()`) or the wire-format `config.modalities` object. Validated before sending. */
  modalities?: ModalitiesBuilder | ModalitiesConfig;
  is_default?: boolean;
}

export interface UpdateProviderRequest {
  kind?: string;
  endpoint?: string;
  models?: string[];
  api_key?: string;
  /** Merged into the stored config. */
  config?: Record<string, unknown>;
  /** Merged per modality; `null` deletes a key or a whole modality. */
  modalities?: ModalitiesBuilder | ModalitiesUpdate;
  /**
   * The server overwrites `is_default` on every update. When omitted, the
   * current value is read first and sent back unchanged (one extra GET).
   */
  is_default?: boolean;
}

export interface UpdateProviderResponse {
  provider: Provider;
  /** Ready agents burnt because their model was removed from the provider. */
  agents_burnt: number;
}

/** An agent's A2A card. `modalities` is what its provider offers. */
export interface AgentCard {
  name: string;
  description?: string;
  url: string;
  version?: string;
  capabilities: Record<string, boolean | undefined>;
  skills?: { id: string; name: string; description?: string }[];
  defaultInputModes?: string[];
  defaultOutputModes?: string[];
  modalities?: string[];
}

/** A short-lived token a browser can use to open the realtime socket. */
export interface RealtimeToken {
  token: string;
  expires_in: number;
}

// ── Invoke, agent sessions ───────────────────────────────────────────────────

export interface InvokeOptions {
  message?: string;
  /** Spoken input (needs the `audio` modality). Build with `audioFromPath` / `audioFromBytes` / `audioFromBase64`, or pass raw bytes plus `audioMimeType`. */
  audio?: AudioInput | Uint8Array | ArrayBuffer;
  /** Only for raw-bytes `audio` of a format the SDK cannot recognise. */
  audioMimeType?: string;
  /** Also return the reply as speech. */
  voiceOutput?: boolean;
  /** TTS voice, default `alloy`. Only with `voiceOutput`. */
  voice?: string;
  /** Images, PDFs or video sent with the message (the server answers 422 when the model cannot take them). */
  attachments?: ContentPart[];
  variables?: Record<string, string>;
  sessionId?: string;
  thinkingEnabled?: boolean;
}

export interface InvokeResult {
  agent: string;
  response: string;
  traceId: string;
  sessionId: string;
  turns: number;
  usage: Record<string, unknown>;
  latencyMs: number;
  /** What the server heard, when `audio` was sent. */
  transcript?: string;
  /** The spoken reply, decoded, when `voiceOutput` was set. */
  audio?: AudioClip;
  /** The full response body. */
  raw: Record<string, unknown>;
}

export interface SendSessionMessageOptions {
  content?: string;
  /** Images, PDFs, audio, video: `Attachment.fromPath` / `fromBytes` / `fromUrl`. */
  attachments?: ContentPart[];
  promptVars?: Record<string, string>;
  metadata?: Record<string, unknown>;
}

/** The reply to a session message. */
export interface SessionMessageResponse {
  session_id: string;
  turn_number: number;
  content: string;
  finish_reason?: string;
  usage?: Record<string, unknown>;
  latency_ms?: number;
  status?: string;
  [key: string]: unknown;
}

// ── API Error ─────────────────────────────────────────────────────────────────

export interface APIErrorResponse {
  error: string;
  message?: string;
  code?: string;
}


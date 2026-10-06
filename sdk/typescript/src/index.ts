/**
 * AgentOven TypeScript SDK — enterprise agent orchestration.
 *
 * The tastiest way to manage your AI agents. 🏺
 *
 * @example
 * ```ts
 * import { AgentOvenClient, createAgent, ProClient } from '@agentoven/sdk';
 *
 * // Core OSS operations (native napi-rs)
 * const client = new AgentOvenClient();
 * const agent = createAgent('research-agent', { framework: 'langchain' });
 * await client.registerAgent(agent);
 * await client.bake('research-agent');
 *
 * // Pro operations (REST)
 * const pro = new ProClient({ url: 'https://agentoven.example.com', apiKey: '...' });
 * await pro.createGuardrail({ kind: 'llamaguard', stage: 'both', name: 'safety', config: { endpoint: '...' } });
 * await pro.createSchedule({ recipe_name: 'daily-report', cron: '0 8 * * MON-FRI' });
 * ```
 */

// Re-export native bindings (napi-rs generated). This package's compiled
// output lives at dist/, two levels below the actual generated binding —
// which is the package root's index.js (+ platform .node binary), not a
// dist/native.js that never gets built. Importing from './native' (what
// this used to say) meant the package's own documented entrypoint
// (`import { AgentOvenClient } from '@agentoven/sdk'`) crashed immediately
// with "Cannot find module '.../dist/native'" for every consumer — the
// pro-client-only exports below still worked, which is why that was easy to
// miss. src/native.d.ts supplies the types for this same root file.
export {
  Agent,
  AgentStatus,
  Ingredient,
  IngredientKind,
  Recipe,
  AgentOvenClient,
  createAgent,
} from '../index.js';

// Realtime voice (Pro)
export {
  connectRealtime,
  realtimeUrl,
  RealtimeSession,
  RealtimeError,
  REALTIME_AUDIO_FORMAT,
  REALTIME_SUBPROTOCOL,
  REALTIME_TOKEN_SUBPROTOCOL_PREFIX,
  REALTIME_CLOSE_POLICY,
} from './realtime.js';
export type { RealtimeEvent, RealtimeEventType, RealtimeOptions, WebSocketLike, WebSocketFactory } from './realtime.js';

// Modalities (provider config helpers), media attachments and cascaded voice
export {
  ALL_MODALITIES,
  ModalitiesBuilder,
  ModalitiesError,
  modalities,
  resolveModalities,
  supportsModality,
  validateModalities,
} from './modalities.js';
export type {
  AnyModality,
  AudioModality,
  AudioOptions,
  ModalitiesConfig,
  ModalitiesUpdate,
  ModalityName,
  RealtimeModality,
  RealtimeOptions as RealtimeModalityOptions,
  ToggleModality,
} from './modalities.js';
export {
  Attachment,
  audioFromBase64,
  audioFromBytes,
  audioFromPath,
  guessMimeType,
  saveAudio,
  sniffMimeType,
} from './media.js';
export type { AttachmentOptions, AudioClip, AudioInput, ContentPart, ContentPartType, MediaRef } from './media.js';

// Pro REST client
export { ProClient, AgentOvenAPIError, AudioModalityError } from './pro-client.js';

// Re-export all types
export type {
  AgentCard,
  AgentOvenClientOptions,
  AssertionResult,
  AuditEvent,
  Branch,
  CreateGuardrailRequest,
  CreateProviderRequest,
  CreateScheduleRequest,
  CreateServiceAccountResponse,
  Deployment,
  Environment,
  EpisodeRecord,
  EpisodeVerdict,
  Guardrail,
  GuardrailException,
  GuardrailKind,
  GuardrailStage,
  InvokeOptions,
  InvokeResult,
  Ingredient as IngredientType,
  Kitchen,
  KitchenMember,
  Promotion,
  Provider,
  RealtimeToken,
  Recipe as RecipeType,
  RegisterAgentOptions,
  SendSessionMessageOptions,
  Schedule,
  Scenario,
  ScenarioResult,
  ScenarioRun,
  ScenarioRunRequest,
  ServerInfo,
  ServiceAccount,
  Session,
  SessionMessageResponse,
  Step,
  TestCase,
  TestResult,
  TestRun,
  TestRunSummary,
  TestSuite,
  TraceabilityMatrix,
  UpdateProviderRequest,
  UpdateProviderResponse,
  User,
  UserRole,
  Workload,
  WorldSchema,
  AgentStatus as AgentStatusType,
  EnvironmentKind,
  IngredientKind as IngredientKindType,
} from './types.js';


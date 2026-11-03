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

// Live-feed notification channels: listen to what a recipe's notify step pushes
export { listenToChannel, channelUrl, NotificationListener, NotificationError, CHANNEL_SUBPROTOCOL, CHANNEL_TICKET_SUBPROTOCOL_PREFIX } from './notifications.js';
export type { ListenOptions, Notification } from './notifications.js';

// Recipes built the way the dashboard builds them: new Flow(...) with the builders in `flow`
export { Flow, FlowError } from './flow.js';
export type { AgentOptions, AskOptions, Node as FlowNode, Problem as FlowProblem, WireRecipe, WireStep, Within } from './flow.js';
export * as flow from './flow.js';

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
export {
  ProClient, AgentOvenAPIError, AudioModalityError, OidcSigningRequired, RateLimitedError, BudgetExceededError, SIGNING_TOKEN_HEADER, SIGNING_GATES,
  KITCHEN_ROLES, PERSON_ROLES, BUDGET_UNITS, BUDGET_PERIODS, LIMIT_SCOPES, CACHE_MODES, budgetRecordId,
  TRIGGER_KINDS, TRIGGER_DELIVERY_STATUSES,
} from './pro-client.js';
export type {
  TraceListFilters, TraceView, TraceTimeline, TimelineSpan, TraceAudit,
  Signing, SigningChallenge, SigningTokenReply, SignatureRecord, ApproverPolicy,
  ApprovalRequestSummary, ApprovalRequestDetail, ApprovalEscalationRule,
  ReplayEdits, ReplayVariant, ReplayToolMode, ReplayCallResult, ReplayRunResult, PreparedLiveReplay, SpanCapture,
  Dataset, DatasetSettings, DatasetCase, DatasetExport, KitchenBinding,
  SecretProvider, Secret, CreateSecretRequest, UpdateSecretRequest, SecretProbe, RotateSecretResult, SecretBinding, SecretMigrationPlan,
  Budget, BudgetScope, BudgetUnit, BudgetPeriod, SetBudgetOptions, BudgetLedgerEntry, BudgetAlert, UsageGroup, UsageRow,
  RateLimit, LimitScope, CacheMode, CachePolicy, CacheState,
  ToolPackSwitch, ToolPackManifest, ToolPackTool, ToolPackInstall, InstallToolPackRequest, OpenApiPreview, ImportOpenApiRequest,
  Trigger, TriggerKind, TriggerDelivery, TriggerDeliveryStatus, TriggerFireResult, CreateTriggerRequest,
} from './pro-client.js';

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
  GuardrailAction,
  GuardrailException,
  GuardrailKind,
  GuardrailKinds,
  GuardrailStage,
  GuardrailTestResult,
  TestGuardrailsOptions,
  InvokeOptions,
  InvokeResult,
  Ingredient as IngredientType,
  Kitchen,
  KitchenMember,
  KitchenRole,
  NotificationChannel,
  Promotion,
  Provider,
  RealtimeToken,
  Recipe as RecipeType,
  RegisterAgentOptions,
  RegisterToolRequest,
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
  StepResult,
  TestCase,
  TestResult,
  TestRun,
  TestRunSummary,
  TestSuite,
  Tool,
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


export { Blobs, blobPart } from './blobs.js';
export { Documents } from './documents.js';
export type { DocumentsOptions, DocumentBlock, ExtractedDocument, ExtractOptions, OcrResult, OcrOptions, OcrConfidence, ExtractedField, FieldsResult, FieldsOptions, FieldStatus } from './documents.js';
export { signWebhook } from './triggers.js';
export type { Blob, BlobPart, BlobsOptions, UploadOptions } from './blobs.js';
export { Tasks, TASK_PAUSE_GATES } from './tasks.js';
export type { Task, TaskStatus, PauseKind, StartedTask, StartTaskOptions, ResumeTaskOptions, BudgetExtension, TaskEvent, WaitOptions, TasksOptions } from './tasks.js';

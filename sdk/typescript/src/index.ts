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

// Re-export native bindings (napi-rs generated)
export {
  Agent,
  AgentStatus,
  Ingredient,
  IngredientKind,
  Recipe,
  AgentOvenClient,
  createAgent,
} from './native';

// Pro REST client
export { ProClient, AgentOvenAPIError } from './pro-client';

// Re-export all types
export type {
  AgentOvenClientOptions,
  AuditEvent,
  Branch,
  CreateGuardrailRequest,
  CreateScheduleRequest,
  CreateServiceAccountResponse,
  Deployment,
  Environment,
  Guardrail,
  GuardrailException,
  GuardrailKind,
  GuardrailStage,
  Ingredient as IngredientType,
  Kitchen,
  KitchenMember,
  Promotion,
  Recipe as RecipeType,
  RegisterAgentOptions,
  Schedule,
  ServerInfo,
  ServiceAccount,
  Session,
  Step,
  TestCase,
  TestRun,
  TestSuite,
  TraceabilityMatrix,
  User,
  UserRole,
  Workload,
  AgentStatus as AgentStatusType,
  EnvironmentKind,
  IngredientKind as IngredientKindType,
} from './types';


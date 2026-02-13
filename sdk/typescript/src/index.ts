/**
 * AgentOven TypeScript SDK — enterprise agent orchestration.
 *
 * The tastiest way to manage your AI agents. 🏺
 *
 * @example
 * ```ts
 * import { AgentOvenClient, createAgent } from '@agentoven/sdk';
 *
 * const client = new AgentOvenClient();
 *
 * const agent = createAgent('research-agent', {
 *   description: 'Researches topics and summarizes findings',
 *   framework: 'langchain',
 * });
 *
 * await client.registerAgent(agent);
 * await client.bake('research-agent');
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

// Re-export types
export type {
  AgentOvenClientOptions,
  RegisterAgentOptions,
} from './types';

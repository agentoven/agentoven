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

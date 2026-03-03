# ADR-0005: Kitchen Metaphor as Domain Language

- **Status:** Accepted
- **Date:** 2026-02-25
- **Author(s):** Siddartha Kopparapu

## Context

Enterprise AI platforms tend to use generic, overloaded terminology (workspace, project, pipeline, deployment) that blends into the noise of existing DevOps and MLOps tools. AgentOven needed a distinctive, cohesive vocabulary that:

1. Makes the product instantly recognizable
2. Maps intuitively to real-world concepts
3. Scales from CLI commands to API endpoints to dashboard UI

## Decision

We adopt the **clay oven / kitchen metaphor** 🏺 as the domain language for all AgentOven surfaces:

| Term | Meaning | Generic Equivalent |
|------|---------|-------------------|
| **Oven** | The AgentOven control plane | Platform / server |
| **Kitchen** | A workspace / project (tenant boundary) | Workspace / namespace |
| **Recipe** | A multi-agent workflow (DAG) | Pipeline / workflow |
| **Ingredient** | A model, tool, prompt, or data source | Resource / dependency |
| **Bake** | Deploy an agent or run a workflow | Deploy / execute |
| **Cool** | Pause a running agent | Pause / suspend |
| **Rewarm** | Resume a paused agent | Resume / restart |
| **Retire** | Permanently decommission an agent | Delete / archive |
| **Re-cook** | Redeploy with updated config | Redeploy / update |
| **Baker** | A user / team building agents | Developer / operator |
| **Menu** | The agent catalog / registry | Registry / catalog |

### Where It Applies

- **CLI:** `agentoven bake my-agent`, `agentoven cool my-agent`
- **API:** `POST /api/v1/agents/{name}/bake`, `POST /api/v1/agents/{name}/cool`
- **HTTP Headers:** `X-Kitchen: default` (tenant selection)
- **Models:** `Kitchen`, `Recipe`, `Ingredient`, `Agent.Status = "baking" | "ready" | "cooled" | "retired"`
- **Documentation:** Consistently used across docs site, README, and guides

### Where It Does NOT Apply

- **Internal package names:** Use standard Go/Rust naming (`internal/router`, not `internal/oven`)
- **Wire protocols:** A2A and MCP use their own standard terminology
- **Error messages to external systems:** Use clear technical language, not metaphors

## Consequences

- **Easier:** Strong brand identity. Users remember "bake an agent" more than "deploy an agent." Documentation reads naturally. Reduces confusion with competing platforms.
- **Harder:** Onboarding for contributors who need to learn the vocabulary. Risk of metaphor fatigue if overextended. Non-English speakers may find it less intuitive.
- **Boundary:** The metaphor should enhance understanding, not obscure it. When in doubt, add the generic equivalent in parentheses.

## Alternatives Considered

1. **No metaphor (generic terms)** — Rejected because it makes AgentOven indistinguishable from every other platform.
2. **Factory metaphor** (assembly line, parts, manufacture) — Considered but felt too industrial and cold.
3. **Garden metaphor** (plant, grow, harvest) — Considered but doesn't map well to the precision of agent orchestration.

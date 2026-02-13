-- AgentOven Control Plane — Initial Schema
-- Creates all core tables for the agent control plane.

-- ── Extensions ──────────────────────────────────────────────

CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- ── Kitchens (Tenants/Workspaces) ───────────────────────────

CREATE TABLE kitchens (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        VARCHAR(255) NOT NULL UNIQUE,
    description TEXT,
    owner       VARCHAR(255) NOT NULL,
    tags        JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_kitchens_owner ON kitchens(owner);

-- ── Agents ──────────────────────────────────────────────────

CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name            VARCHAR(255) NOT NULL,
    description     TEXT,
    framework       VARCHAR(64),
    status          VARCHAR(32) NOT NULL DEFAULT 'draft',
    kitchen         UUID NOT NULL REFERENCES kitchens(id),
    version         VARCHAR(64) NOT NULL DEFAULT '0.1.0',

    -- A2A Configuration
    a2a_endpoint    TEXT,
    skills          JSONB DEFAULT '[]',

    -- Model Configuration
    model_provider  VARCHAR(128),
    model_name      VARCHAR(128),

    -- Ingredients (denormalized for read performance)
    ingredients     JSONB DEFAULT '[]',

    -- Metadata
    tags            JSONB DEFAULT '{}',
    created_by      VARCHAR(255),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(name, kitchen)
);

CREATE INDEX idx_agents_kitchen ON agents(kitchen);
CREATE INDEX idx_agents_status ON agents(status);
CREATE INDEX idx_agents_name ON agents(name);

-- ── Agent Versions ──────────────────────────────────────────

CREATE TABLE agent_versions (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    version     VARCHAR(64) NOT NULL,
    config      JSONB NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by  VARCHAR(255),

    UNIQUE(agent_id, version)
);

-- ── Recipes (Workflow DAGs) ─────────────────────────────────

CREATE TABLE recipes (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        VARCHAR(255) NOT NULL,
    description TEXT,
    kitchen     UUID NOT NULL REFERENCES kitchens(id),
    steps       JSONB NOT NULL DEFAULT '[]',
    version     VARCHAR(64) NOT NULL DEFAULT '0.1.0',
    created_by  VARCHAR(255),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE(name, kitchen)
);

CREATE INDEX idx_recipes_kitchen ON recipes(kitchen);

-- ── Recipe Runs ─────────────────────────────────────────────

CREATE TABLE recipe_runs (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    recipe_id   UUID NOT NULL REFERENCES recipes(id),
    kitchen     UUID NOT NULL REFERENCES kitchens(id),
    status      VARCHAR(32) NOT NULL DEFAULT 'submitted',
    input       JSONB,
    output      JSONB,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    duration_ms BIGINT,
    error       TEXT
);

CREATE INDEX idx_recipe_runs_recipe ON recipe_runs(recipe_id);
CREATE INDEX idx_recipe_runs_status ON recipe_runs(status);

-- ── Model Providers ─────────────────────────────────────────

CREATE TABLE model_providers (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    name        VARCHAR(255) NOT NULL UNIQUE,
    kind        VARCHAR(64) NOT NULL, -- azure-openai, anthropic, ollama, etc.
    endpoint    TEXT,
    models      JSONB DEFAULT '[]',
    config      JSONB DEFAULT '{}',
    is_default  BOOLEAN DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Traces ──────────────────────────────────────────────────

CREATE TABLE traces (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    agent_name  VARCHAR(255) NOT NULL,
    recipe_name VARCHAR(255),
    kitchen     UUID NOT NULL REFERENCES kitchens(id),
    status      VARCHAR(32) NOT NULL,
    duration_ms BIGINT,
    total_tokens BIGINT DEFAULT 0,
    cost_usd    DECIMAL(12,6) DEFAULT 0,
    metadata    JSONB DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_traces_kitchen ON traces(kitchen);
CREATE INDEX idx_traces_agent ON traces(agent_name);
CREATE INDEX idx_traces_created ON traces(created_at DESC);

-- ── API Keys ────────────────────────────────────────────────

CREATE TABLE api_keys (
    id          UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    key_hash    VARCHAR(128) NOT NULL UNIQUE,
    name        VARCHAR(255) NOT NULL,
    kitchen     UUID NOT NULL REFERENCES kitchens(id),
    scopes      JSONB DEFAULT '["read","write"]',
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used   TIMESTAMPTZ
);

CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);

-- ── Seed default kitchen ────────────────────────────────────

INSERT INTO kitchens (name, description, owner) VALUES
    ('default', 'Default Kitchen', 'system')
ON CONFLICT (name) DO NOTHING;

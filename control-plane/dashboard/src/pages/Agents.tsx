import { useState, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Bot, Plus, Flame, Snowflake, Trash2, FlaskConical, AlertTriangle, X as XIcon, Sun, RefreshCw, Code, Copy, Check } from 'lucide-react';
import { agents, providers, type Agent, type Ingredient, type IngredientKind, APIError } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, StatusBadge, EmptyState,
  Spinner, ErrorBanner, Button,
} from '../components/UI';

export function AgentsPage() {
  const { data, loading, error, refetch } = useAPI(agents.list);
  const [showForm, setShowForm] = useState(false);

  return (
    <div>
      <PageHeader
        title="Agents"
        description="Register, bake, and manage your AI agents"
        action={
          <Button onClick={() => setShowForm(!showForm)}>
            <Plus size={16} className="mr-1.5" /> Register Agent
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}

      {showForm && <AgentForm onCreated={() => { setShowForm(false); refetch(); }} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {data.map((agent) => (
            <AgentCard key={agent.id} agent={agent} onAction={refetch} />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<Bot size={48} />}
          title="No agents yet"
          description="Register your first AI agent to get started."
          action={<Button onClick={() => setShowForm(true)}>Register Agent</Button>}
        />
      )}
    </div>
  );
}

function AgentCard({ agent, onAction }: { agent: Agent; onAction: () => void }) {
  const [busy, setBusy] = useState(false);
  const [bakeError, setBakeError] = useState<string[] | null>(null);
  const [showIntegration, setShowIntegration] = useState(false);
  const navigate = useNavigate();

  const doAction = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setBakeError(null);
    try { await fn(); onAction(); } catch (e) {
      if (e instanceof APIError && e.details) {
        setBakeError(e.details);
      }
    }
    setBusy(false);
  };

  return (
    <Card>
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <Bot size={18} className="text-[var(--ao-brand-light)]" />
          <span className="font-medium">{agent.name}</span>
        </div>
        <StatusBadge status={agent.status} />
      </div>

      {agent.description && (
        <p className="text-sm text-[var(--ao-text-muted)] mb-3 line-clamp-2">{agent.description}</p>
      )}

      <div className="text-xs text-[var(--ao-text-muted)] space-y-1 mb-3">
        <div className="flex items-center gap-2 mb-1">
          <span className={`px-1.5 py-0.5 rounded text-xs font-medium ${
            agent.mode === 'managed' ? 'bg-blue-500/20 text-blue-400' : 'bg-purple-500/20 text-purple-400'
          }`}>
            {agent.mode || 'managed'}
          </span>
          {agent.mode === 'managed' && (
            <span className="text-[var(--ao-text-muted)]">max {agent.max_turns || 10} turns</span>
          )}
        </div>
        <p>Framework: {agent.framework || '—'}</p>
        <p>Model: {agent.model_provider ? `${agent.model_provider} / ${agent.model_name}` : '—'}</p>
        <p>Version: <span className="font-mono text-[var(--ao-brand-light)]">{agent.version || '—'}</span></p>
        {agent.skills?.length > 0 && <p>Skills: {agent.skills.join(', ')}</p>}
        {agent.ingredients?.length > 0 && (
          <p>Ingredients: {agent.ingredients.map((i) => `${i.kind}:${i.name}`).join(', ')}</p>
        )}
      </div>

      {/* Burnt status shows error */}
      {agent.status === 'burnt' && agent.tags?.error && (
        <div className="mb-3 p-2 rounded bg-red-500/10 border border-red-500/30">
          <p className="text-xs text-red-400 flex items-center gap-1">
            <AlertTriangle size={12} /> {agent.tags.error}
          </p>
        </div>
      )}

      {/* Bake validation errors */}
      {bakeError && (
        <div className="mb-3 p-2 rounded bg-red-500/10 border border-red-500/30 space-y-1">
          {bakeError.map((err, i) => (
            <p key={i} className="text-xs text-red-400">• {err}</p>
          ))}
        </div>
      )}

      {/* Integration panel */}
      {showIntegration && agent.status === 'ready' && (
        <IntegrationPanel agent={agent} onClose={() => setShowIntegration(false)} />
      )}

      <div className="flex gap-2 flex-wrap">
        {agent.status === 'draft' && (
          <Button size="sm" onClick={() => doAction(() => agents.bake(agent.name))} disabled={busy}>
            <Flame size={14} className="mr-1" /> Bake
          </Button>
        )}
        {agent.status === 'burnt' && (
          <>
            <Button size="sm" onClick={() => doAction(() => agents.bake(agent.name))} disabled={busy}>
              <Flame size={14} className="mr-1" /> Retry Bake
            </Button>
            <Button size="sm" variant="secondary" onClick={() => doAction(() => agents.recook(agent.name))} disabled={busy}>
              <RefreshCw size={14} className="mr-1" /> Re-cook
            </Button>
          </>
        )}
        {agent.status === 'ready' && (
          <>
            <Button size="sm" onClick={() => setShowIntegration(!showIntegration)}>
              <Code size={14} className="mr-1" /> Integrate
            </Button>
            <Button size="sm" variant="secondary" onClick={() => navigate(`/agents/${agent.name}/test`)}>
              <FlaskConical size={14} className="mr-1" /> Test
            </Button>
            <Button size="sm" variant="secondary" onClick={() => doAction(() => agents.recook(agent.name))} disabled={busy}>
              <RefreshCw size={14} className="mr-1" /> Re-cook
            </Button>
            <Button size="sm" variant="secondary" onClick={() => doAction(() => agents.cool(agent.name))} disabled={busy}>
              <Snowflake size={14} className="mr-1" /> Cool
            </Button>
          </>
        )}
        {agent.status === 'cooled' && (
          <>
            <Button size="sm" onClick={() => doAction(() => agents.rewarm(agent.name))} disabled={busy}>
              <Sun size={14} className="mr-1" /> Rewarm
            </Button>
            <Button size="sm" variant="secondary" onClick={() => doAction(() => agents.recook(agent.name))} disabled={busy}>
              <RefreshCw size={14} className="mr-1" /> Re-cook
            </Button>
            <Button size="sm" variant="secondary" onClick={() => navigate(`/agents/${agent.name}/test`)}>
              <FlaskConical size={14} className="mr-1" /> Test
            </Button>
          </>
        )}
        {(agent.status === 'active' || agent.status === 'baked') && (
          <Button size="sm" variant="secondary" onClick={() => doAction(() => agents.cool(agent.name))} disabled={busy}>
            <Snowflake size={14} className="mr-1" /> Cool
          </Button>
        )}
        <Button size="sm" variant="danger" onClick={() => doAction(() => agents.delete(agent.name))} disabled={busy}>
          <Trash2 size={14} />
        </Button>
      </div>
    </Card>
  );
}

// ── Integration panel — shows curl, CLI, and Python commands ──

type IntegrationTab = 'invoke' | 'session' | 'test' | 'card';

function IntegrationPanel({ agent, onClose }: { agent: Agent; onClose: () => void }) {
  const [tab, setTab] = useState<IntegrationTab>('invoke');
  const [copied, setCopied] = useState(false);
  const host = window.location.origin;
  const name = agent.name;

  const copy = (text: string) => {
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const snippets: Record<IntegrationTab, { label: string; description: string; curl: string; cli: string; python: string }> = {
    invoke: {
      label: 'Invoke',
      description: 'Full agentic execution — uses resolved ingredients, prompt templates, tool calling loop.',
      curl: `curl -X POST ${host}/api/v1/agents/${name}/invoke \\
  -H "Content-Type: application/json" \\
  -H "X-Kitchen-Id: default" \\
  -d '{
    "message": "Hello, translate this to French: Good morning!",
    "variables": {}
  }'`,
      cli: `agentoven agent test ${name} --message "Hello, translate this to French: Good morning!"`,
      python: `from agentoven import AgentOvenClient

client = AgentOvenClient("${host}")
result = client.invoke_agent("${name}", "Hello, translate this to French: Good morning!")
print(result["response"])`,
    },
    session: {
      label: 'Session',
      description: 'Multi-turn conversations with memory — create a session, then send messages.',
      curl: `# 1. Create a session
curl -X POST ${host}/api/v1/agents/${name}/sessions \\
  -H "Content-Type: application/json" \\
  -H "X-Kitchen-Id: default" \\
  -d '{"max_turns": 10}'

# 2. Send messages (replace SESSION_ID)
curl -X POST ${host}/api/v1/agents/${name}/sessions/SESSION_ID/messages \\
  -H "Content-Type: application/json" \\
  -H "X-Kitchen-Id: default" \\
  -d '{"content": "Hello! What can you help me with?"}'

# 3. Send follow-up (same session — agent remembers context)
curl -X POST ${host}/api/v1/agents/${name}/sessions/SESSION_ID/messages \\
  -H "Content-Type: application/json" \\
  -H "X-Kitchen-Id: default" \\
  -d '{"content": "Now translate that to Spanish"}'`,
      cli: `# Sessions not yet in CLI — use curl or Python SDK`,
      python: `from agentoven import AgentOvenClient

client = AgentOvenClient("${host}")
session = client.create_session("${name}", max_turns=10)

r1 = client.send_message("${name}", session["id"], "Hello!")
print(r1["content"])

r2 = client.send_message("${name}", session["id"], "Now translate that to Spanish")
print(r2["content"])`,
    },
    test: {
      label: 'Test',
      description: 'One-shot test — no memory, no executor, just routes a message through the model.',
      curl: `curl -X POST ${host}/api/v1/agents/${name}/test \\
  -H "Content-Type: application/json" \\
  -H "X-Kitchen-Id: default" \\
  -d '{"message": "Hello!"}'`,
      cli: `agentoven agent test ${name} --message "Hello!"`,
      python: `from agentoven import AgentOvenClient

client = AgentOvenClient("${host}")
result = client.test_agent("${name}", "Hello!")
print(result["response"])`,
    },
    card: {
      label: 'Agent Card',
      description: 'A2A-compatible agent card — capabilities, skills, supported input/output modes.',
      curl: `curl ${host}/api/v1/agents/${name}/card \\
  -H "X-Kitchen-Id: default"`,
      cli: `# Agent card not yet in CLI — use curl`,
      python: `import requests

card = requests.get("${host}/api/v1/agents/${name}/card",
                     headers={"X-Kitchen-Id": "default"}).json()
print(card)`,
    },
  };

  const active = snippets[tab];

  const tabs: IntegrationTab[] = ['invoke', 'session', 'test', 'card'];

  return (
    <div className="mb-3 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)] overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-3 py-2 bg-[var(--ao-surface)]">
        <div className="flex items-center gap-1">
          {tabs.map((t) => (
            <button
              key={t}
              onClick={() => setTab(t)}
              className={`px-2.5 py-1 rounded text-xs font-medium transition-colors ${
                tab === t
                  ? 'bg-[var(--ao-brand)] text-white'
                  : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:bg-[var(--ao-surface-hover)]'
              }`}
            >
              {snippets[t].label}
            </button>
          ))}
        </div>
        <button onClick={onClose} className="text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">
          <XIcon size={14} />
        </button>
      </div>

      <div className="p-3">
        <p className="text-xs text-[var(--ao-text-muted)] mb-2">{active.description}</p>

        {/* curl */}
        <div className="mb-2">
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] font-semibold text-[var(--ao-text-muted)] uppercase">curl</span>
            <button
              onClick={() => copy(active.curl)}
              className="text-[var(--ao-text-muted)] hover:text-[var(--ao-brand-light)]"
            >
              {copied ? <Check size={12} className="text-emerald-400" /> : <Copy size={12} />}
            </button>
          </div>
          <pre className="text-[11px] font-mono p-2 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] overflow-x-auto whitespace-pre text-[var(--ao-text-muted)]">
            {active.curl}
          </pre>
        </div>

        {/* CLI */}
        <div className="mb-2">
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] font-semibold text-[var(--ao-text-muted)] uppercase">CLI</span>
            <button
              onClick={() => copy(active.cli)}
              className="text-[var(--ao-text-muted)] hover:text-[var(--ao-brand-light)]"
            >
              <Copy size={12} />
            </button>
          </div>
          <pre className="text-[11px] font-mono p-2 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] overflow-x-auto whitespace-pre text-[var(--ao-text-muted)]">
            {active.cli}
          </pre>
        </div>

        {/* Python */}
        <div>
          <div className="flex items-center justify-between mb-1">
            <span className="text-[10px] font-semibold text-[var(--ao-text-muted)] uppercase">Python</span>
            <button
              onClick={() => copy(active.python)}
              className="text-[var(--ao-text-muted)] hover:text-[var(--ao-brand-light)]"
            >
              <Copy size={12} />
            </button>
          </div>
          <pre className="text-[11px] font-mono p-2 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] overflow-x-auto whitespace-pre text-[var(--ao-text-muted)]">
            {active.python}
          </pre>
        </div>
      </div>
    </div>
  );
}

// ── Ingredient row helper ─────────────────────────────────────

interface IngredientRow {
  name: string;
  kind: IngredientKind;
  configJson: string;
  required: boolean;
}

const emptyIngredient = (): IngredientRow => ({
  name: '', kind: 'tool', configJson: '{}', required: false,
});

function AgentForm({ onCreated }: { onCreated: () => void }) {
  // Fetch providers for the dropdown
  const providersFetch = useCallback(() => providers.list(), []);
  const { data: providerList } = useAPI(providersFetch);

  const [form, setForm] = useState({
    name: '', description: '', framework: '',
    model_provider: '', model_name: '',
    system_prompt: '',
    skills: '',
    mode: 'managed' as 'managed' | 'external',
    max_turns: 10,
  });
  const [ingredients, setIngredients] = useState<IngredientRow[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const addIngredient = () => setIngredients([...ingredients, emptyIngredient()]);
  const removeIngredient = (i: number) => setIngredients(ingredients.filter((_, idx) => idx !== i));
  const updateIngredient = (i: number, patch: Partial<IngredientRow>) =>
    setIngredients(ingredients.map((ing, idx) => idx === i ? { ...ing, ...patch } : ing));

  // Models available from the selected provider
  const selectedProvider = providerList?.find((p) => p.name === form.model_provider);
  const availableModels = selectedProvider?.models ?? [];

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name) return;
    setSubmitting(true);
    setErr(null);

    // Build ingredients array
    const builtIngredients: Partial<Ingredient>[] = [];

    // System prompt → prompt ingredient
    if (form.system_prompt.trim()) {
      builtIngredients.push({
        name: 'system-prompt',
        kind: 'prompt',
        config: { text: form.system_prompt.trim() },
        required: true,
      });
    }

    // User-added ingredients
    for (const ing of ingredients) {
      if (!ing.name) continue;
      let config: Record<string, unknown> = {};
      try { config = JSON.parse(ing.configJson); } catch { /* ignore */ }
      builtIngredients.push({
        name: ing.name,
        kind: ing.kind,
        config,
        required: ing.required,
      });
    }

    const skills = form.skills.split(',').map((s) => s.trim()).filter(Boolean);

    try {
      await agents.create({
        name: form.name,
        description: form.description,
        framework: form.framework,
        model_provider: form.model_provider,
        model_name: form.model_name,
        ingredients: builtIngredients as Ingredient[],
        skills,
        mode: form.mode,
        max_turns: form.mode === 'managed' ? form.max_turns : undefined,
      });
      onCreated();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Failed');
    }
    setSubmitting(false);
  };

  const inputCls = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';

  return (
    <Card className="mx-8 mt-4">
      <h3 className="text-sm font-semibold mb-4">Register Agent</h3>
      {err && <ErrorBanner message={err} />}
      <form onSubmit={submit} className="space-y-4">
        {/* Row 1: name, framework */}
        <div className="flex flex-wrap gap-3">
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
            <input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              className={inputCls}
              placeholder="my-agent"
            />
          </div>
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
            <input
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
              className={inputCls}
              placeholder="An agent that..."
            />
          </div>
          <div className="w-40">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Framework</label>
            <select
              value={form.framework}
              onChange={(e) => setForm({ ...form, framework: e.target.value })}
              className={inputCls}
            >
              <option value="">Select...</option>
              <option value="langchain">LangChain</option>
              <option value="crewai">CrewAI</option>
              <option value="autogen">AutoGen</option>
              <option value="openai">OpenAI SDK</option>
              <option value="custom">Custom</option>
            </select>
          </div>
          <div className="w-32">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Mode</label>
            <select
              value={form.mode}
              onChange={(e) => setForm({ ...form, mode: e.target.value as 'managed' | 'external' })}
              className={inputCls}
            >
              <option value="managed">Managed</option>
              <option value="external">External</option>
            </select>
          </div>
          {form.mode === 'managed' && (
            <div className="w-24">
              <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Max Turns</label>
              <input
                type="number"
                min={1}
                max={100}
                value={form.max_turns}
                onChange={(e) => setForm({ ...form, max_turns: parseInt(e.target.value) || 10 })}
                className={inputCls}
              />
            </div>
          )}
        </div>

        {/* Row 2: model provider, model name */}
        <div className="flex flex-wrap gap-3">
          <div className="w-48">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Model Provider</label>
            <select
              value={form.model_provider}
              onChange={(e) => setForm({ ...form, model_provider: e.target.value, model_name: '' })}
              className={inputCls}
            >
              <option value="">Select provider...</option>
              {providerList?.map((p) => (
                <option key={p.name} value={p.name}>{p.name} ({p.kind})</option>
              ))}
            </select>
          </div>
          <div className="w-48">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Model Name</label>
            {availableModels.length > 0 ? (
              <select
                value={form.model_name}
                onChange={(e) => setForm({ ...form, model_name: e.target.value })}
                className={inputCls}
              >
                <option value="">Select model...</option>
                {availableModels.map((m) => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </select>
            ) : (
              <input
                value={form.model_name}
                onChange={(e) => setForm({ ...form, model_name: e.target.value })}
                className={inputCls}
                placeholder="gpt-4o"
              />
            )}
          </div>
          <div className="flex-1 min-w-[150px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Skills (comma-sep)</label>
            <input
              value={form.skills}
              onChange={(e) => setForm({ ...form, skills: e.target.value })}
              className={inputCls}
              placeholder="summarize, translate, code"
            />
          </div>
        </div>

        {/* Row 3: system prompt */}
        <div>
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">System Prompt</label>
          <textarea
            value={form.system_prompt}
            onChange={(e) => setForm({ ...form, system_prompt: e.target.value })}
            rows={3}
            className={`${inputCls} resize-y`}
            placeholder="You are a helpful assistant that..."
          />
        </div>

        {/* Row 4: ingredients builder */}
        <div>
          <div className="flex items-center justify-between mb-2">
            <label className="text-xs text-[var(--ao-text-muted)]">Ingredients</label>
            <button
              type="button"
              onClick={addIngredient}
              className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)] flex items-center gap-1"
            >
              <Plus size={12} /> Add ingredient
            </button>
          </div>
          {ingredients.length === 0 && (
            <p className="text-xs text-[var(--ao-text-muted)] italic">
              No extra ingredients. System prompt is added automatically if filled above.
            </p>
          )}
          <div className="space-y-2">
            {ingredients.map((ing, i) => (
              <div key={i} className="flex flex-wrap gap-2 items-center">
                <select
                  value={ing.kind}
                  onChange={(e) => updateIngredient(i, { kind: e.target.value as IngredientKind })}
                  className="w-24 px-2 py-1.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)] text-xs outline-none"
                >
                  <option value="model">Model</option>
                  <option value="tool">Tool</option>
                  <option value="prompt">Prompt</option>
                  <option value="data">Data</option>
                  <option value="observability">Observability</option>
                </select>
                <input
                  value={ing.name}
                  onChange={(e) => updateIngredient(i, { name: e.target.value })}
                  placeholder="Name"
                  className="w-32 px-2 py-1.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)] text-xs outline-none"
                />
                <input
                  value={ing.configJson}
                  onChange={(e) => updateIngredient(i, { configJson: e.target.value })}
                  placeholder='{"key": "value"}'
                  className="flex-1 min-w-[120px] px-2 py-1.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)] text-xs font-mono outline-none"
                />
                <label className="flex items-center gap-1 text-xs">
                  <input
                    type="checkbox"
                    checked={ing.required}
                    onChange={(e) => updateIngredient(i, { required: e.target.checked })}
                  />
                  Req
                </label>
                <button
                  type="button"
                  onClick={() => removeIngredient(i)}
                  className="text-red-400 hover:text-red-300"
                >
                  <XIcon size={14} />
                </button>
              </div>
            ))}
          </div>
        </div>

        <div className="flex justify-end">
          <Button disabled={submitting || !form.name}>
            {submitting ? 'Creating...' : 'Create'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

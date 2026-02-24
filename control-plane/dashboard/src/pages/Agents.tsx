import { useState, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Bot, Plus, Flame, Snowflake, Trash2, FlaskConical, AlertTriangle, Sun, RefreshCw, Code, Copy, Check, Shield } from 'lucide-react';
import { agents, providers, type Agent, type Ingredient, type IngredientKind, type Guardrail, APIError } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, StatusBadge, EmptyState,
  Spinner, ErrorBanner, Button, Modal,
} from '../components/UI';

// ── Main Page ─────────────────────────────────────────────────

export function AgentsPage() {
  const { data, loading, error, refetch } = useAPI(agents.list);
  const [showCreate, setShowCreate] = useState(false);
  const [integrateAgent, setIntegrateAgent] = useState<Agent | null>(null);
  const [recookAgent, setRecookAgent] = useState<Agent | null>(null);

  return (
    <div>
      <PageHeader
        title="Agents"
        description="Register, bake, and manage your AI agents"
        action={
          <Button onClick={() => setShowCreate(true)}>
            <Plus size={16} className="mr-1.5" /> Register Agent
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {data.map((agent) => (
            <AgentCard
              key={agent.id}
              agent={agent}
              onAction={refetch}
              onIntegrate={() => setIntegrateAgent(agent)}
              onRecook={() => setRecookAgent(agent)}
            />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<Bot size={48} />}
          title="No agents yet"
          description="Register your first AI agent to get started."
          action={<Button onClick={() => setShowCreate(true)}>Register Agent</Button>}
        />
      )}

      {/* ── Create Agent Modal ── */}
      <Modal open={showCreate} onClose={() => setShowCreate(false)} title="Register Agent" width="max-w-3xl">
        <AgentForm onCreated={() => { setShowCreate(false); refetch(); }} />
      </Modal>

      {/* ── Integration Modal ── */}
      <Modal
        open={!!integrateAgent}
        onClose={() => setIntegrateAgent(null)}
        title={integrateAgent ? `Integrate — ${integrateAgent.name}` : 'Integrate'}
        width="max-w-3xl"
      >
        {integrateAgent && <IntegrationPanel agent={integrateAgent} />}
      </Modal>

      {/* ── Re-cook Edit Modal ── */}
      <Modal
        open={!!recookAgent}
        onClose={() => setRecookAgent(null)}
        title={recookAgent ? `Re-cook — ${recookAgent.name}` : 'Re-cook'}
        width="max-w-3xl"
      >
        {recookAgent && <AgentEditForm agent={recookAgent} onDone={() => { setRecookAgent(null); refetch(); }} />}
      </Modal>
    </div>
  );
}

function AgentCard({ agent, onAction, onIntegrate, onRecook }: { agent: Agent; onAction: () => void; onIntegrate: () => void; onRecook: () => void }) {
  const [busy, setBusy] = useState(false);
  const [bakeError, setBakeError] = useState<string[] | null>(null);
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
        {(agent as any).backup_provider && (
          <p>Backup: <span className="text-amber-400">{(agent as any).backup_provider}</span></p>
        )}
        <p>Version: <span className="font-mono text-[var(--ao-brand-light)]">{agent.version || '—'}</span></p>
        {agent.skills?.length > 0 && <p>Skills: {agent.skills.join(', ')}</p>}
        {(agent as any).guardrails?.length > 0 && (
          <p className="flex items-center gap-1"><Shield size={10} className="text-emerald-400" /> {(agent as any).guardrails.length} guardrail{(agent as any).guardrails.length > 1 ? 's' : ''}</p>
        )}
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
            <Button size="sm" variant="secondary" onClick={onRecook} disabled={busy}>
              <RefreshCw size={14} className="mr-1" /> Re-cook
            </Button>
          </>
        )}
        {agent.status === 'ready' && (
          <>
            <Button size="sm" onClick={onIntegrate}>
              <Code size={14} className="mr-1" /> Integrate
            </Button>
            <Button size="sm" variant="secondary" onClick={() => navigate(`/agents/${agent.name}/test`)}>
              <FlaskConical size={14} className="mr-1" /> Test
            </Button>
            <Button size="sm" variant="secondary" onClick={onRecook} disabled={busy}>
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
            <Button size="sm" variant="secondary" onClick={onRecook} disabled={busy}>
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

// ── Integration Modal Content ─────────────────────────────────

type IntegrationTab = 'invoke' | 'session' | 'test' | 'card';

function IntegrationPanel({ agent }: { agent: Agent }) {
  const [tab, setTab] = useState<IntegrationTab>('invoke');
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  const host = window.location.origin;
  const name = agent.name;

  const copy = (text: string, key: string) => {
    navigator.clipboard.writeText(text);
    setCopiedKey(key);
    setTimeout(() => setCopiedKey(null), 2000);
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
    <div>
      {/* Tabs */}
      <div className="flex items-center gap-1 mb-4 p-1 rounded-lg bg-[var(--ao-bg)]">
        {tabs.map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`flex-1 px-3 py-2 rounded-md text-sm font-medium transition-colors ${
              tab === t
                ? 'bg-[var(--ao-brand)] text-white shadow-sm'
                : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:bg-[var(--ao-surface-hover)]'
            }`}
          >
            {snippets[t].label}
          </button>
        ))}
      </div>

      <p className="text-sm text-[var(--ao-text-muted)] mb-4">{active.description}</p>

      {/* Code blocks */}
      {(['curl', 'cli', 'python'] as const).map((lang) => (
        <div key={lang} className="mb-4">
          <div className="flex items-center justify-between mb-1.5">
            <span className="text-xs font-semibold text-[var(--ao-text-muted)] uppercase tracking-wider">{lang}</span>
            <button
              onClick={() => copy(active[lang], `${tab}-${lang}`)}
              className="flex items-center gap-1 text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-brand-light)] transition-colors"
            >
              {copiedKey === `${tab}-${lang}` ? (
                <><Check size={12} className="text-emerald-400" /> Copied</>
              ) : (
                <><Copy size={12} /> Copy</>
              )}
            </button>
          </div>
          <pre className="text-xs font-mono p-3 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] overflow-x-auto whitespace-pre text-[var(--ao-text-muted)] leading-relaxed">
            {active[lang]}
          </pre>
        </div>
      ))}
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

// ── Guardrail row helper ──────────────────────────────────────

interface GuardrailRow {
  kind: string;
  stage: 'input' | 'output' | 'both';
  config: string;
}

const GUARDRAIL_KINDS = [
  { value: 'content_filter', label: 'Content Filter', desc: 'Block harmful / toxic content' },
  { value: 'pii_detection', label: 'PII Detection', desc: 'Block personal information leakage' },
  { value: 'topic_restriction', label: 'Topic Restriction', desc: 'Restrict to allowed topics only' },
  { value: 'max_length', label: 'Max Length', desc: 'Limit input/output token length' },
  { value: 'regex_filter', label: 'Regex Filter', desc: 'Block/allow patterns via regex' },
  { value: 'prompt_injection', label: 'Injection Detection', desc: 'Detect prompt injection attacks' },
  { value: 'custom', label: 'Custom', desc: 'Your own validation logic' },
];

const emptyGuardrail = (): GuardrailRow => ({
  kind: 'content_filter', stage: 'both', config: '{}',
});

// ── Create Agent Form (inside modal) ──────────────────────────

function AgentForm({ onCreated }: { onCreated: () => void }) {
  const providersFetch = useCallback(() => providers.list(), []);
  const { data: providerList } = useAPI(providersFetch);

  const [form, setForm] = useState({
    name: '', description: '', framework: '',
    model_provider: '', model_name: '',
    backup_provider: '', backup_model: '',
    system_prompt: '',
    skills: '',
    mode: 'managed' as 'managed' | 'external',
    max_turns: 10,
  });
  const [ingredients, setIngredients] = useState<IngredientRow[]>([]);
  const [guardrails, setGuardrails] = useState<GuardrailRow[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [tab, setTab] = useState<'basic' | 'guardrails' | 'ingredients'>('basic');

  const addIngredient = () => setIngredients([...ingredients, emptyIngredient()]);
  const removeIngredient = (i: number) => setIngredients(ingredients.filter((_, idx) => idx !== i));
  const updateIngredient = (i: number, patch: Partial<IngredientRow>) =>
    setIngredients(ingredients.map((ing, idx) => idx === i ? { ...ing, ...patch } : ing));

  const addGuardrail = () => setGuardrails([...guardrails, emptyGuardrail()]);
  const removeGuardrail = (i: number) => setGuardrails(guardrails.filter((_, idx) => idx !== i));
  const updateGuardrail = (i: number, patch: Partial<GuardrailRow>) =>
    setGuardrails(guardrails.map((g, idx) => idx === i ? { ...g, ...patch } : g));

  const selectedProvider = providerList?.find((p) => p.name === form.model_provider);
  const availableModels = selectedProvider?.models ?? [];
  const backupProvider = providerList?.find((p) => p.name === form.backup_provider);
  const backupModels = backupProvider?.models ?? [];

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name) return;
    setSubmitting(true);
    setErr(null);

    const builtIngredients: Partial<Ingredient>[] = [];

    if (form.system_prompt.trim()) {
      builtIngredients.push({
        name: 'system-prompt', kind: 'prompt',
        config: { text: form.system_prompt.trim() }, required: true,
      });
    }

    for (const ing of ingredients) {
      if (!ing.name) continue;
      let config: Record<string, unknown> = {};
      try { config = JSON.parse(ing.configJson); } catch { /* ignore */ }
      builtIngredients.push({ name: ing.name, kind: ing.kind, config, required: ing.required });
    }

    const skills = form.skills.split(',').map((s) => s.trim()).filter(Boolean);

    const builtGuardrails = guardrails.map((g) => {
      let config: Record<string, unknown> = {};
      try { config = JSON.parse(g.config); } catch { /* ignore */ }
      return { kind: g.kind, stage: g.stage, config };
    });

    try {
      await agents.create({
        name: form.name,
        description: form.description,
        framework: form.framework,
        model_provider: form.model_provider,
        model_name: form.model_name,
        backup_provider: form.backup_provider || undefined,
        backup_model: form.backup_model || undefined,
        ingredients: builtIngredients as Ingredient[],
        guardrails: builtGuardrails.length > 0 ? builtGuardrails : undefined,
        skills,
        mode: form.mode,
        max_turns: form.mode === 'managed' ? form.max_turns : undefined,
      } as any);
      onCreated();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Failed');
    }
    setSubmitting(false);
  };

  const inputCls = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)] transition-colors';
  const tabCls = (active: boolean) =>
    `px-4 py-2 text-sm font-medium rounded-lg transition-colors ${active ? 'bg-[var(--ao-brand)] text-white' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:bg-[var(--ao-surface-hover)]'}`;

  return (
    <div>
      {err && <ErrorBanner message={err} />}

      {/* Tabs */}
      <div className="flex gap-1 mb-5 p-1 rounded-lg bg-[var(--ao-bg)]">
        <button type="button" className={tabCls(tab === 'basic')} onClick={() => setTab('basic')}>
          Basic
        </button>
        <button type="button" className={tabCls(tab === 'guardrails')} onClick={() => setTab('guardrails')}>
          <Shield size={14} className="inline mr-1" />Guardrails {guardrails.length > 0 && `(${guardrails.length})`}
        </button>
        <button type="button" className={tabCls(tab === 'ingredients')} onClick={() => setTab('ingredients')}>
          Ingredients {ingredients.length > 0 && `(${ingredients.length})`}
        </button>
      </div>

      <form onSubmit={submit} className="space-y-4">

        {/* ── Basic Tab ── */}
        {tab === 'basic' && (
          <>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
                <input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} className={inputCls} placeholder="my-agent" />
              </div>
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
                <input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} className={inputCls} placeholder="An agent that..." />
              </div>
            </div>

            <div className="grid grid-cols-3 gap-3">
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Framework</label>
                <select value={form.framework} onChange={(e) => setForm({ ...form, framework: e.target.value })} className={inputCls}>
                  <option value="">Select...</option>
                  <option value="langchain">LangChain</option>
                  <option value="crewai">CrewAI</option>
                  <option value="autogen">AutoGen</option>
                  <option value="openai">OpenAI SDK</option>
                  <option value="custom">Custom</option>
                </select>
              </div>
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Mode</label>
                <select value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value as 'managed' | 'external' })} className={inputCls}>
                  <option value="managed">Managed</option>
                  <option value="external">External</option>
                </select>
              </div>
              {form.mode === 'managed' && (
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Max Turns</label>
                  <input type="number" min={1} max={100} value={form.max_turns} onChange={(e) => setForm({ ...form, max_turns: parseInt(e.target.value) || 10 })} className={inputCls} />
                </div>
              )}
            </div>

            {/* Primary model */}
            <div className="p-3 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)]">
              <p className="text-xs font-semibold text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">Primary Model</p>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Provider</label>
                  <select value={form.model_provider} onChange={(e) => setForm({ ...form, model_provider: e.target.value, model_name: '' })} className={inputCls}>
                    <option value="">Select provider...</option>
                    {providerList?.map((p) => <option key={p.name} value={p.name}>{p.name} ({p.kind})</option>)}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Model</label>
                  {availableModels.length > 0 ? (
                    <select value={form.model_name} onChange={(e) => setForm({ ...form, model_name: e.target.value })} className={inputCls}>
                      <option value="">Select model...</option>
                      {availableModels.map((m) => <option key={m} value={m}>{m}</option>)}
                    </select>
                  ) : (
                    <input value={form.model_name} onChange={(e) => setForm({ ...form, model_name: e.target.value })} className={inputCls} placeholder="gpt-4o" />
                  )}
                </div>
              </div>
            </div>

            {/* Backup model */}
            <div className="p-3 rounded-lg border border-amber-500/30 bg-amber-500/5">
              <p className="text-xs font-semibold text-amber-400 uppercase tracking-wider mb-1">Backup Provider (Failover)</p>
              <p className="text-xs text-[var(--ao-text-muted)] mb-3">Automatically routes to this provider when the primary fails or is unavailable.</p>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Backup Provider</label>
                  <select value={form.backup_provider} onChange={(e) => setForm({ ...form, backup_provider: e.target.value, backup_model: '' })} className={inputCls}>
                    <option value="">None (no failover)</option>
                    {providerList?.filter((p) => p.name !== form.model_provider).map((p) => (
                      <option key={p.name} value={p.name}>{p.name} ({p.kind})</option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Backup Model</label>
                  {backupModels.length > 0 ? (
                    <select value={form.backup_model} onChange={(e) => setForm({ ...form, backup_model: e.target.value })} className={inputCls}>
                      <option value="">Select model...</option>
                      {backupModels.map((m) => <option key={m} value={m}>{m}</option>)}
                    </select>
                  ) : (
                    <input value={form.backup_model} onChange={(e) => setForm({ ...form, backup_model: e.target.value })} className={inputCls} placeholder="claude-3-5-sonnet-20241022" />
                  )}
                </div>
              </div>
            </div>

            {/* System prompt + skills */}
            <div>
              <label className="block text-xs text-[var(--ao-text-muted)] mb-1">System Prompt</label>
              <textarea value={form.system_prompt} onChange={(e) => setForm({ ...form, system_prompt: e.target.value })} rows={3} className={`${inputCls} resize-y`} placeholder="You are a helpful assistant that..." />
            </div>
            <div>
              <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Skills (comma-separated)</label>
              <input value={form.skills} onChange={(e) => setForm({ ...form, skills: e.target.value })} className={inputCls} placeholder="summarize, translate, code" />
            </div>
          </>
        )}

        {/* ── Guardrails Tab ── */}
        {tab === 'guardrails' && (
          <div>
            <div className="flex items-center justify-between mb-3">
              <div>
                <p className="text-sm font-medium">Safety Guardrails</p>
                <p className="text-xs text-[var(--ao-text-muted)]">Add input/output validators to enforce safety and compliance on agent messages.</p>
              </div>
              <button type="button" onClick={addGuardrail} className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)] flex items-center gap-1">
                <Plus size={12} /> Add guardrail
              </button>
            </div>

            {guardrails.length === 0 && (
              <div className="text-center py-8 text-[var(--ao-text-muted)]">
                <Shield size={32} className="mx-auto mb-2 opacity-30" />
                <p className="text-sm">No guardrails configured.</p>
                <p className="text-xs">Add guardrails to validate inputs and outputs for safety.</p>
              </div>
            )}

            <div className="space-y-3">
              {guardrails.map((g, i) => {
                const kindInfo = GUARDRAIL_KINDS.find((k) => k.value === g.kind);
                return (
                  <div key={i} className="p-3 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)]">
                    <div className="flex items-start gap-3">
                      <div className="flex-1 space-y-2">
                        <div className="grid grid-cols-2 gap-2">
                          <div>
                            <label className="block text-[10px] text-[var(--ao-text-muted)] uppercase mb-1">Type</label>
                            <select value={g.kind} onChange={(e) => updateGuardrail(i, { kind: e.target.value })}
                              className="w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                              {GUARDRAIL_KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
                            </select>
                          </div>
                          <div>
                            <label className="block text-[10px] text-[var(--ao-text-muted)] uppercase mb-1">Stage</label>
                            <select value={g.stage} onChange={(e) => updateGuardrail(i, { stage: e.target.value as GuardrailRow['stage'] })}
                              className="w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                              <option value="input">Input only</option>
                              <option value="output">Output only</option>
                              <option value="both">Input &amp; Output</option>
                            </select>
                          </div>
                        </div>
                        {kindInfo && <p className="text-[10px] text-[var(--ao-text-muted)]">{kindInfo.desc}</p>}
                        <div>
                          <label className="block text-[10px] text-[var(--ao-text-muted)] uppercase mb-1">Config (JSON)</label>
                          <input value={g.config} onChange={(e) => updateGuardrail(i, { config: e.target.value })}
                            className="w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs font-mono outline-none"
                            placeholder={g.kind === 'max_length' ? '{"max_tokens": 4096}' : g.kind === 'regex_filter' ? '{"patterns": ["\\\\bSSN\\\\b"]}' : g.kind === 'topic_restriction' ? '{"allowed": ["technology", "science"]}' : '{}'} />
                        </div>
                      </div>
                      <button type="button" onClick={() => removeGuardrail(i)} className="text-red-400 hover:text-red-300 mt-1">
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* ── Ingredients Tab ── */}
        {tab === 'ingredients' && (
          <div>
            <div className="flex items-center justify-between mb-3">
              <div>
                <p className="text-sm font-medium">Ingredients</p>
                <p className="text-xs text-[var(--ao-text-muted)]">Add tools, data sources, and other ingredients. System prompt is added automatically.</p>
              </div>
              <button type="button" onClick={addIngredient} className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)] flex items-center gap-1">
                <Plus size={12} /> Add ingredient
              </button>
            </div>

            {ingredients.length === 0 && (
              <p className="text-xs text-[var(--ao-text-muted)] italic py-4 text-center">
                No extra ingredients. System prompt is added automatically if filled in the Basic tab.
              </p>
            )}

            <div className="space-y-2">
              {ingredients.map((ing, i) => (
                <div key={i} className="flex flex-wrap gap-2 items-center p-2 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)]">
                  <select value={ing.kind} onChange={(e) => updateIngredient(i, { kind: e.target.value as IngredientKind })}
                    className="w-28 px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                    <option value="model">Model</option>
                    <option value="tool">Tool</option>
                    <option value="prompt">Prompt</option>
                    <option value="data">Data</option>
                    <option value="observability">Observability</option>
                  </select>
                  <input value={ing.name} onChange={(e) => updateIngredient(i, { name: e.target.value })} placeholder="Name"
                    className="w-32 px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none" />
                  <input value={ing.configJson} onChange={(e) => updateIngredient(i, { configJson: e.target.value })} placeholder='{"key": "value"}'
                    className="flex-1 min-w-[120px] px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs font-mono outline-none" />
                  <label className="flex items-center gap-1 text-xs">
                    <input type="checkbox" checked={ing.required} onChange={(e) => updateIngredient(i, { required: e.target.checked })} /> Req
                  </label>
                  <button type="button" onClick={() => removeIngredient(i)} className="text-red-400 hover:text-red-300">
                    <Trash2 size={14} />
                  </button>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Submit */}
        <div className="flex justify-end pt-2 border-t border-[var(--ao-border)]">
          <Button disabled={submitting || !form.name}>
            {submitting ? 'Creating...' : 'Create Agent'}
          </Button>
        </div>
      </form>
    </div>
  );
}

// ── Re-cook Edit Form (pre-filled from existing agent) ────────

function AgentEditForm({ agent, onDone }: { agent: Agent; onDone: () => void }) {
  const providersFetch = useCallback(() => providers.list(), []);
  const { data: providerList } = useAPI(providersFetch);

  // Extract system prompt from existing ingredients
  const existingSystemPrompt = agent.ingredients?.find(
    (i) => i.kind === 'prompt' && (i.name === 'system-prompt' || i.config?.text)
  );
  const systemPromptText = (existingSystemPrompt?.config?.text as string) ?? '';

  // Extract non-prompt ingredients
  const otherIngredients: IngredientRow[] = (agent.ingredients ?? [])
    .filter((i) => i !== existingSystemPrompt)
    .map((i) => ({
      name: i.name,
      kind: i.kind,
      configJson: JSON.stringify(i.config ?? {}),
      required: i.required ?? false,
    }));

  // Extract guardrails
  const existingGuardrails: GuardrailRow[] = (agent.guardrails ?? []).map((g) => ({
    kind: g.kind,
    stage: g.stage,
    config: JSON.stringify(g.config ?? {}),
  }));

  const [form, setForm] = useState({
    description: agent.description || '',
    framework: agent.framework || '',
    model_provider: agent.model_provider || '',
    model_name: agent.model_name || '',
    backup_provider: agent.backup_provider || '',
    backup_model: agent.backup_model || '',
    system_prompt: systemPromptText,
    skills: agent.skills?.join(', ') || '',
    mode: agent.mode || 'managed' as 'managed' | 'external',
    max_turns: agent.max_turns || 10,
  });
  const [ingredients, setIngredients] = useState<IngredientRow[]>(otherIngredients);
  const [guardrails, setGuardrails] = useState<GuardrailRow[]>(existingGuardrails);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [tab, setTab] = useState<'basic' | 'guardrails' | 'ingredients'>('basic');

  const addIngredient = () => setIngredients([...ingredients, emptyIngredient()]);
  const removeIngredient = (i: number) => setIngredients(ingredients.filter((_, idx) => idx !== i));
  const updateIngredient = (i: number, patch: Partial<IngredientRow>) =>
    setIngredients(ingredients.map((ing, idx) => idx === i ? { ...ing, ...patch } : ing));

  const addGuardrail = () => setGuardrails([...guardrails, emptyGuardrail()]);
  const removeGuardrail = (i: number) => setGuardrails(guardrails.filter((_, idx) => idx !== i));
  const updateGuardrail = (i: number, patch: Partial<GuardrailRow>) =>
    setGuardrails(guardrails.map((g, idx) => idx === i ? { ...g, ...patch } : g));

  const selectedProvider = providerList?.find((p) => p.name === form.model_provider);
  const availableModels = selectedProvider?.models ?? [];
  const backupProvider = providerList?.find((p) => p.name === form.backup_provider);
  const backupModels = backupProvider?.models ?? [];

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setErr(null);

    const builtIngredients: Partial<Ingredient>[] = [];

    if (form.system_prompt.trim()) {
      builtIngredients.push({
        name: 'system-prompt', kind: 'prompt',
        config: { text: form.system_prompt.trim() }, required: true,
      });
    }

    for (const ing of ingredients) {
      if (!ing.name) continue;
      let config: Record<string, unknown> = {};
      try { config = JSON.parse(ing.configJson); } catch { /* ignore */ }
      builtIngredients.push({ name: ing.name, kind: ing.kind, config, required: ing.required });
    }

    const skills = form.skills.split(',').map((s) => s.trim()).filter(Boolean);

    const builtGuardrails = guardrails.map((g) => {
      let config: Record<string, unknown> = {};
      try { config = JSON.parse(g.config); } catch { /* ignore */ }
      return { kind: g.kind, stage: g.stage, config };
    });

    try {
      await agents.recook(agent.name, {
        description: form.description,
        framework: form.framework,
        model_provider: form.model_provider,
        model_name: form.model_name,
        backup_provider: form.backup_provider || undefined,
        backup_model: form.backup_model || undefined,
        ingredients: builtIngredients as Ingredient[],
        guardrails: builtGuardrails.length > 0 ? builtGuardrails : undefined,
        skills,
        mode: form.mode,
        max_turns: form.mode === 'managed' ? form.max_turns : undefined,
      } as any);
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Re-cook failed');
    }
    setSubmitting(false);
  };

  const inputCls = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)] transition-colors';
  const tabCls = (active: boolean) =>
    `px-4 py-2 text-sm font-medium rounded-lg transition-colors ${active ? 'bg-[var(--ao-brand)] text-white' : 'text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:bg-[var(--ao-surface-hover)]'}`;

  return (
    <div>
      <div className="mb-4 p-3 rounded-lg bg-blue-500/10 border border-blue-500/20">
        <p className="text-xs text-blue-400">
          <RefreshCw size={12} className="inline mr-1" />
          Edit the agent's configuration below. Re-cooking will re-resolve all ingredients and re-bake with the new settings.
          Version-pinned ingredients (tools, prompts) will be refreshed to their latest versions.
        </p>
      </div>

      {err && <ErrorBanner message={err} />}

      {/* Tabs */}
      <div className="flex gap-1 mb-5 p-1 rounded-lg bg-[var(--ao-bg)]">
        <button type="button" className={tabCls(tab === 'basic')} onClick={() => setTab('basic')}>
          Basic
        </button>
        <button type="button" className={tabCls(tab === 'guardrails')} onClick={() => setTab('guardrails')}>
          <Shield size={14} className="inline mr-1" />Guardrails {guardrails.length > 0 && `(${guardrails.length})`}
        </button>
        <button type="button" className={tabCls(tab === 'ingredients')} onClick={() => setTab('ingredients')}>
          Ingredients {ingredients.length > 0 && `(${ingredients.length})`}
        </button>
      </div>

      <form onSubmit={submit} className="space-y-4">

        {/* ── Basic Tab ── */}
        {tab === 'basic' && (
          <>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name</label>
                <input value={agent.name} disabled className={`${inputCls} opacity-60 cursor-not-allowed`} />
              </div>
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
                <input value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} className={inputCls} placeholder="An agent that..." />
              </div>
            </div>

            <div className="grid grid-cols-3 gap-3">
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Framework</label>
                <select value={form.framework} onChange={(e) => setForm({ ...form, framework: e.target.value })} className={inputCls}>
                  <option value="">Select...</option>
                  <option value="langchain">LangChain</option>
                  <option value="crewai">CrewAI</option>
                  <option value="autogen">AutoGen</option>
                  <option value="openai">OpenAI SDK</option>
                  <option value="custom">Custom</option>
                </select>
              </div>
              <div>
                <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Mode</label>
                <select value={form.mode} onChange={(e) => setForm({ ...form, mode: e.target.value as 'managed' | 'external' })} className={inputCls}>
                  <option value="managed">Managed</option>
                  <option value="external">External</option>
                </select>
              </div>
              {form.mode === 'managed' && (
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Max Turns</label>
                  <input type="number" min={1} max={100} value={form.max_turns} onChange={(e) => setForm({ ...form, max_turns: parseInt(e.target.value) || 10 })} className={inputCls} />
                </div>
              )}
            </div>

            {/* Primary model */}
            <div className="p-3 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)]">
              <p className="text-xs font-semibold text-[var(--ao-text-muted)] uppercase tracking-wider mb-3">Primary Model</p>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Provider</label>
                  <select value={form.model_provider} onChange={(e) => setForm({ ...form, model_provider: e.target.value, model_name: '' })} className={inputCls}>
                    <option value="">Select provider...</option>
                    {providerList?.map((p) => <option key={p.name} value={p.name}>{p.name} ({p.kind})</option>)}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Model</label>
                  {availableModels.length > 0 ? (
                    <select value={form.model_name} onChange={(e) => setForm({ ...form, model_name: e.target.value })} className={inputCls}>
                      <option value="">Select model...</option>
                      {availableModels.map((m) => <option key={m} value={m}>{m}</option>)}
                    </select>
                  ) : (
                    <input value={form.model_name} onChange={(e) => setForm({ ...form, model_name: e.target.value })} className={inputCls} placeholder="gpt-4o" />
                  )}
                </div>
              </div>
            </div>

            {/* Backup model */}
            <div className="p-3 rounded-lg border border-amber-500/30 bg-amber-500/5">
              <p className="text-xs font-semibold text-amber-400 uppercase tracking-wider mb-1">Backup Provider (Failover)</p>
              <p className="text-xs text-[var(--ao-text-muted)] mb-3">Automatically routes to this provider when the primary fails or is unavailable.</p>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Backup Provider</label>
                  <select value={form.backup_provider} onChange={(e) => setForm({ ...form, backup_provider: e.target.value, backup_model: '' })} className={inputCls}>
                    <option value="">None (no failover)</option>
                    {providerList?.filter((p) => p.name !== form.model_provider).map((p) => (
                      <option key={p.name} value={p.name}>{p.name} ({p.kind})</option>
                    ))}
                  </select>
                </div>
                <div>
                  <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Backup Model</label>
                  {backupModels.length > 0 ? (
                    <select value={form.backup_model} onChange={(e) => setForm({ ...form, backup_model: e.target.value })} className={inputCls}>
                      <option value="">Select model...</option>
                      {backupModels.map((m) => <option key={m} value={m}>{m}</option>)}
                    </select>
                  ) : (
                    <input value={form.backup_model} onChange={(e) => setForm({ ...form, backup_model: e.target.value })} className={inputCls} placeholder="claude-3-5-sonnet-20241022" />
                  )}
                </div>
              </div>
            </div>

            {/* System prompt + skills */}
            <div>
              <label className="block text-xs text-[var(--ao-text-muted)] mb-1">System Prompt</label>
              <textarea value={form.system_prompt} onChange={(e) => setForm({ ...form, system_prompt: e.target.value })} rows={3} className={`${inputCls} resize-y`} placeholder="You are a helpful assistant that..." />
            </div>
            <div>
              <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Skills (comma-separated)</label>
              <input value={form.skills} onChange={(e) => setForm({ ...form, skills: e.target.value })} className={inputCls} placeholder="summarize, translate, code" />
            </div>
          </>
        )}

        {/* ── Guardrails Tab ── */}
        {tab === 'guardrails' && (
          <div>
            <div className="flex items-center justify-between mb-3">
              <div>
                <p className="text-sm font-medium">Safety Guardrails</p>
                <p className="text-xs text-[var(--ao-text-muted)]">Add input/output validators to enforce safety and compliance on agent messages.</p>
              </div>
              <button type="button" onClick={addGuardrail} className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)] flex items-center gap-1">
                <Plus size={12} /> Add guardrail
              </button>
            </div>

            {guardrails.length === 0 && (
              <div className="text-center py-8 text-[var(--ao-text-muted)]">
                <Shield size={32} className="mx-auto mb-2 opacity-30" />
                <p className="text-sm">No guardrails configured.</p>
                <p className="text-xs">Add guardrails to validate inputs and outputs for safety.</p>
              </div>
            )}

            <div className="space-y-3">
              {guardrails.map((g, i) => {
                const kindInfo = GUARDRAIL_KINDS.find((k) => k.value === g.kind);
                return (
                  <div key={i} className="p-3 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)]">
                    <div className="flex items-start gap-3">
                      <div className="flex-1 space-y-2">
                        <div className="grid grid-cols-2 gap-2">
                          <div>
                            <label className="block text-[10px] text-[var(--ao-text-muted)] uppercase mb-1">Type</label>
                            <select value={g.kind} onChange={(e) => updateGuardrail(i, { kind: e.target.value })}
                              className="w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                              {GUARDRAIL_KINDS.map((k) => <option key={k.value} value={k.value}>{k.label}</option>)}
                            </select>
                          </div>
                          <div>
                            <label className="block text-[10px] text-[var(--ao-text-muted)] uppercase mb-1">Stage</label>
                            <select value={g.stage} onChange={(e) => updateGuardrail(i, { stage: e.target.value as GuardrailRow['stage'] })}
                              className="w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                              <option value="input">Input only</option>
                              <option value="output">Output only</option>
                              <option value="both">Input &amp; Output</option>
                            </select>
                          </div>
                        </div>
                        {kindInfo && <p className="text-[10px] text-[var(--ao-text-muted)]">{kindInfo.desc}</p>}
                        <div>
                          <label className="block text-[10px] text-[var(--ao-text-muted)] uppercase mb-1">Config (JSON)</label>
                          <input value={g.config} onChange={(e) => updateGuardrail(i, { config: e.target.value })}
                            className="w-full px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs font-mono outline-none"
                            placeholder={g.kind === 'max_length' ? '{"max_tokens": 4096}' : g.kind === 'regex_filter' ? '{"patterns": ["\\\\bSSN\\\\b"]}' : g.kind === 'topic_restriction' ? '{"allowed": ["technology", "science"]}' : '{}'} />
                        </div>
                      </div>
                      <button type="button" onClick={() => removeGuardrail(i)} className="text-red-400 hover:text-red-300 mt-1">
                        <Trash2 size={14} />
                      </button>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* ── Ingredients Tab ── */}
        {tab === 'ingredients' && (
          <div>
            <div className="flex items-center justify-between mb-3">
              <div>
                <p className="text-sm font-medium">Ingredients</p>
                <p className="text-xs text-[var(--ao-text-muted)]">Add tools, data sources, and other ingredients. System prompt is on the Basic tab.</p>
              </div>
              <button type="button" onClick={addIngredient} className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)] flex items-center gap-1">
                <Plus size={12} /> Add ingredient
              </button>
            </div>

            {ingredients.length === 0 && (
              <p className="text-xs text-[var(--ao-text-muted)] italic py-4 text-center">
                No extra ingredients.
              </p>
            )}

            <div className="space-y-2">
              {ingredients.map((ing, i) => (
                <div key={i} className="flex flex-wrap gap-2 items-center p-2 rounded-lg border border-[var(--ao-border)] bg-[var(--ao-bg)]">
                  <select value={ing.kind} onChange={(e) => updateIngredient(i, { kind: e.target.value as IngredientKind })}
                    className="w-28 px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none">
                    <option value="model">Model</option>
                    <option value="tool">Tool</option>
                    <option value="prompt">Prompt</option>
                    <option value="data">Data</option>
                    <option value="observability">Observability</option>
                  </select>
                  <input value={ing.name} onChange={(e) => updateIngredient(i, { name: e.target.value })} placeholder="Name"
                    className="w-32 px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs outline-none" />
                  <input value={ing.configJson} onChange={(e) => updateIngredient(i, { configJson: e.target.value })} placeholder='{"key": "value"}'
                    className="flex-1 min-w-[120px] px-2 py-1.5 rounded bg-[var(--ao-surface)] border border-[var(--ao-border)] text-xs font-mono outline-none" />
                  <label className="flex items-center gap-1 text-xs">
                    <input type="checkbox" checked={ing.required} onChange={(e) => updateIngredient(i, { required: e.target.checked })} /> Req
                  </label>
                  <button type="button" onClick={() => removeIngredient(i)} className="text-red-400 hover:text-red-300">
                    <Trash2 size={14} />
                  </button>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Submit */}
        <div className="flex justify-end gap-3 pt-2 border-t border-[var(--ao-border)]">
          <span className="text-xs text-[var(--ao-text-muted)] self-center">
            Current version: <span className="font-mono text-[var(--ao-brand-light)]">{agent.version || '0.1.0'}</span>
          </span>
          <Button disabled={submitting}>
            {submitting ? 'Re-cooking...' : '🔥 Re-cook Agent'}
          </Button>
        </div>
      </form>
    </div>
  );
}

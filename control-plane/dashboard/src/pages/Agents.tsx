import { useState, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Bot, Plus, Flame, Snowflake, Trash2, FlaskConical, AlertTriangle, X as XIcon, Sun } from 'lucide-react';
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
        <p>Framework: {agent.framework || '—'}</p>
        <p>Model: {agent.model_provider ? `${agent.model_provider} / ${agent.model_name}` : '—'}</p>
        <p>Version: {agent.version}</p>
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

      <div className="flex gap-2 flex-wrap">
        {agent.status === 'draft' && (
          <Button size="sm" onClick={() => doAction(() => agents.bake(agent.name))} disabled={busy}>
            <Flame size={14} className="mr-1" /> Bake
          </Button>
        )}
        {agent.status === 'burnt' && (
          <Button size="sm" onClick={() => doAction(() => agents.bake(agent.name))} disabled={busy}>
            <Flame size={14} className="mr-1" /> Retry Bake
          </Button>
        )}
        {agent.status === 'ready' && (
          <>
            <Button size="sm" onClick={() => navigate(`/agents/${agent.name}/test`)}>
              <FlaskConical size={14} className="mr-1" /> Test
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

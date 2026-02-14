import { useState } from 'react';
import { Bot, Plus, Flame, Snowflake, Trash2 } from 'lucide-react';
import { agents, type Agent } from '../api';
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

  const doAction = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    try { await fn(); onAction(); } catch { /* toast */ }
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

      <div className="text-xs text-[var(--ao-text-muted)] space-y-1 mb-4">
        <p>Framework: {agent.framework || '—'}</p>
        <p>Model: {agent.model_provider ? `${agent.model_provider} / ${agent.model_name}` : '—'}</p>
        <p>Version: {agent.version}</p>
        {agent.skills?.length > 0 && <p>Skills: {agent.skills.join(', ')}</p>}
      </div>

      <div className="flex gap-2">
        {agent.status === 'draft' && (
          <Button size="sm" onClick={() => doAction(() => agents.bake(agent.name))} disabled={busy}>
            <Flame size={14} className="mr-1" /> Bake
          </Button>
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

function AgentForm({ onCreated }: { onCreated: () => void }) {
  const [form, setForm] = useState({ name: '', description: '', framework: '' });
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name) return;
    setSubmitting(true);
    try {
      await agents.create(form);
      onCreated();
    } catch { /* toast */ }
    setSubmitting(false);
  };

  return (
    <Card className="mx-8 mt-4">
      <form onSubmit={submit} className="flex flex-wrap gap-3 items-end">
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
          <input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="my-agent"
          />
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
          <input
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="An agent that..."
          />
        </div>
        <div className="w-40">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Framework</label>
          <select
            value={form.framework}
            onChange={(e) => setForm({ ...form, framework: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none"
          >
            <option value="">Select...</option>
            <option value="langchain">LangChain</option>
            <option value="crewai">CrewAI</option>
            <option value="autogen">AutoGen</option>
            <option value="openai">OpenAI SDK</option>
            <option value="custom">Custom</option>
          </select>
        </div>
        <Button disabled={submitting || !form.name}>
          {submitting ? 'Creating...' : 'Create'}
        </Button>
      </form>
    </Card>
  );
}

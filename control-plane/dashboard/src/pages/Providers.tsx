import { useState } from 'react';
import { Cpu, Plus, Trash2, Star } from 'lucide-react';
import { providers, type ModelProvider } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, EmptyState,
  Spinner, ErrorBanner, Button, StatusBadge,
} from '../components/UI';

export function ProvidersPage() {
  const { data, loading, error, refetch } = useAPI(providers.list);
  const [showForm, setShowForm] = useState(false);

  return (
    <div>
      <PageHeader
        title="Model Providers"
        description="Configure model providers for the Model Router"
        action={
          <Button onClick={() => setShowForm(!showForm)}>
            <Plus size={16} className="mr-1.5" /> Add Provider
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {showForm && <ProviderForm onCreated={() => { setShowForm(false); refetch(); }} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {data.map((p) => (
            <ProviderCard key={p.id} provider={p} onAction={refetch} />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<Cpu size={48} />}
          title="No providers configured"
          description="Add an OpenAI, Azure OpenAI, Anthropic, or Ollama provider."
          action={<Button onClick={() => setShowForm(true)}>Add Provider</Button>}
        />
      )}
    </div>
  );
}

function ProviderCard({ provider, onAction }: { provider: ModelProvider; onAction: () => void }) {
  const [busy, setBusy] = useState(false);

  return (
    <Card>
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <Cpu size={18} className="text-[var(--ao-brand-light)]" />
          <span className="font-medium">{provider.name}</span>
          {provider.is_default && (
            <Star size={14} className="text-amber-400 fill-amber-400" />
          )}
        </div>
        <StatusBadge status={provider.kind} />
      </div>

      {provider.endpoint && (
        <p className="text-xs text-[var(--ao-text-muted)] mb-2 truncate">{provider.endpoint}</p>
      )}

      <div className="flex flex-wrap gap-1 mb-4">
        {provider.models?.map((m) => (
          <span
            key={m}
            className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)] text-[var(--ao-text-muted)]"
          >
            {m}
          </span>
        ))}
      </div>

      <Button
        size="sm"
        variant="danger"
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          try { await providers.delete(provider.name); onAction(); } catch {}
          setBusy(false);
        }}
      >
        <Trash2 size={14} />
      </Button>
    </Card>
  );
}

function ProviderForm({ onCreated }: { onCreated: () => void }) {
  const [form, setForm] = useState({ name: '', kind: 'openai', endpoint: '', models: '' });
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name) return;
    setSubmitting(true);
    try {
      await providers.create({
        name: form.name,
        kind: form.kind,
        endpoint: form.endpoint,
        models: form.models.split(',').map((m) => m.trim()).filter(Boolean),
      });
      onCreated();
    } catch { /* toast */ }
    setSubmitting(false);
  };

  return (
    <Card className="mx-8 mt-4">
      <form onSubmit={submit} className="flex flex-wrap gap-3 items-end">
        <div className="w-40">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
          <input
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="my-openai"
          />
        </div>
        <div className="w-36">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Kind</label>
          <select
            value={form.kind}
            onChange={(e) => setForm({ ...form, kind: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none"
          >
            <option value="openai">OpenAI</option>
            <option value="azure-openai">Azure OpenAI</option>
            <option value="anthropic">Anthropic</option>
            <option value="ollama">Ollama</option>
          </select>
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Endpoint</label>
          <input
            value={form.endpoint}
            onChange={(e) => setForm({ ...form, endpoint: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="https://api.openai.com/v1"
          />
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Models (comma-sep)</label>
          <input
            value={form.models}
            onChange={(e) => setForm({ ...form, models: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="gpt-4o, gpt-4o-mini"
          />
        </div>
        <Button disabled={submitting || !form.name}>
          {submitting ? 'Adding...' : 'Add'}
        </Button>
      </form>
    </Card>
  );
}

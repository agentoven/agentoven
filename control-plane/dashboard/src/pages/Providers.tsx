import { useState } from 'react';
import { Cpu, Plus, Trash2, Star, Pencil, HeartPulse, Eye, EyeOff } from 'lucide-react';
import { providers, type ModelProvider } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, EmptyState,
  Spinner, ErrorBanner, Button, StatusBadge,
} from '../components/UI';

export function ProvidersPage() {
  const { data, loading, error, refetch } = useAPI(providers.list);
  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<ModelProvider | null>(null);
  const [healthStatus, setHealthStatus] = useState<Record<string, boolean> | null>(null);
  const [healthLoading, setHealthLoading] = useState(false);

  const testConnection = async () => {
    setHealthLoading(true);
    try {
      const result = await providers.health();
      setHealthStatus(result);
    } catch { /* ignore */ }
    setHealthLoading(false);
  };

  const openEdit = (p: ModelProvider) => {
    setEditing(p);
    setShowForm(true);
  };

  const closeForm = () => {
    setShowForm(false);
    setEditing(null);
    refetch();
  };

  return (
    <div>
      <PageHeader
        title="Model Providers"
        description="Configure model providers for the Model Router"
        action={
          <div className="flex gap-2">
            <Button variant="secondary" onClick={testConnection} disabled={healthLoading}>
              <HeartPulse size={16} className="mr-1.5" />
              {healthLoading ? 'Testing...' : 'Test All'}
            </Button>
            <Button onClick={() => { setEditing(null); setShowForm(!showForm); }}>
              <Plus size={16} className="mr-1.5" /> Add Provider
            </Button>
          </div>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {showForm && (
        <ProviderForm
          existing={editing}
          onDone={closeForm}
          onCancel={() => { setShowForm(false); setEditing(null); }}
        />
      )}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {data.map((p) => (
            <ProviderCard
              key={p.id}
              provider={p}
              onAction={refetch}
              onEdit={() => openEdit(p)}
              healthy={healthStatus?.[p.name]}
            />
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

function ProviderCard({
  provider, onAction, onEdit, healthy,
}: {
  provider: ModelProvider; onAction: () => void; onEdit: () => void; healthy?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const maskedKey = provider.config?.api_key as string | undefined;

  return (
    <Card>
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <Cpu size={18} className="text-[var(--ao-brand-light)]" />
          <span className="font-medium">{provider.name}</span>
          {provider.is_default && (
            <Star size={14} className="text-amber-400 fill-amber-400" />
          )}
          {healthy !== undefined && (
            <span className={`w-2 h-2 rounded-full ${healthy ? 'bg-emerald-400' : 'bg-red-400'}`} />
          )}
        </div>
        <StatusBadge status={provider.kind} />
      </div>

      {provider.endpoint && (
        <p className="text-xs text-[var(--ao-text-muted)] mb-1 truncate">{provider.endpoint}</p>
      )}
      {maskedKey && (
        <p className="text-xs text-[var(--ao-text-muted)] mb-1">
          API Key: <span className="font-mono">{String(maskedKey)}</span>
        </p>
      )}
      {(provider.config?.cost_per_1k_input != null || provider.config?.cost_per_1k_output != null) && (
        <p className="text-xs text-[var(--ao-text-muted)] mb-1">
          Cost: ${String(provider.config.cost_per_1k_input ?? 0)}/1K in · ${String(provider.config.cost_per_1k_output ?? 0)}/1K out
        </p>
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

      <div className="flex gap-2">
        <Button size="sm" variant="secondary" onClick={onEdit}>
          <Pencil size={14} className="mr-1" /> Edit
        </Button>
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
      </div>
    </Card>
  );
}

interface ProviderFormState {
  name: string;
  kind: string;
  endpoint: string;
  models: string;
  api_key: string;
  is_default: boolean;
  cost_input: string;
  cost_output: string;
}

function ProviderForm({
  existing, onDone, onCancel,
}: {
  existing: ModelProvider | null; onDone: () => void; onCancel: () => void;
}) {
  const isEdit = !!existing;
  const [form, setForm] = useState<ProviderFormState>({
    name: existing?.name ?? '',
    kind: existing?.kind ?? 'openai',
    endpoint: existing?.endpoint ?? '',
    models: existing?.models?.join(', ') ?? '',
    api_key: '', // never pre-fill the real key
    is_default: existing?.is_default ?? false,
    cost_input: existing?.config?.cost_per_1k_input != null ? String(existing.config.cost_per_1k_input) : '',
    cost_output: existing?.config?.cost_per_1k_output != null ? String(existing.config.cost_per_1k_output) : '',
  });
  const [showKey, setShowKey] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name) return;
    setSubmitting(true);
    setErr(null);

    const config: Record<string, unknown> = {};
    if (form.api_key) config.api_key = form.api_key;
    if (form.cost_input) config.cost_per_1k_input = parseFloat(form.cost_input);
    if (form.cost_output) config.cost_per_1k_output = parseFloat(form.cost_output);

    const payload: Partial<ModelProvider> = {
      name: form.name,
      kind: form.kind,
      endpoint: form.endpoint,
      models: form.models.split(',').map((m) => m.trim()).filter(Boolean),
      config,
      is_default: form.is_default,
    };

    try {
      if (isEdit) {
        await providers.update(existing.name, payload);
      } else {
        await providers.create(payload);
      }
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Failed');
    }
    setSubmitting(false);
  };

  const inputCls = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';

  return (
    <Card className="mx-8 mt-4">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-sm font-semibold">{isEdit ? `Edit ${existing.name}` : 'Add Provider'}</h3>
        <button onClick={onCancel} className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">Cancel</button>
      </div>
      {err && <ErrorBanner message={err} />}
      <form onSubmit={submit} className="space-y-4">
        {/* Row 1: name, kind */}
        <div className="flex flex-wrap gap-3">
          <div className="w-40">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
            <input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              disabled={isEdit}
              className={`${inputCls} ${isEdit ? 'opacity-60' : ''}`}
              placeholder="my-openai"
            />
          </div>
          <div className="w-36">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Kind</label>
            <select
              value={form.kind}
              onChange={(e) => setForm({ ...form, kind: e.target.value })}
              className={inputCls}
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
              className={inputCls}
              placeholder="https://api.openai.com/v1"
            />
          </div>
        </div>

        {/* Row 2: models, api key */}
        <div className="flex flex-wrap gap-3">
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Models (comma-sep)</label>
            <input
              value={form.models}
              onChange={(e) => setForm({ ...form, models: e.target.value })}
              className={inputCls}
              placeholder="gpt-4o, gpt-4o-mini"
            />
          </div>
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">
              API Key {isEdit && '(leave blank to keep current)'}
            </label>
            <div className="relative">
              <input
                type={showKey ? 'text' : 'password'}
                value={form.api_key}
                onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                className={`${inputCls} pr-10`}
                placeholder={form.kind === 'ollama' ? '(not required)' : 'sk-...'}
              />
              <button
                type="button"
                onClick={() => setShowKey(!showKey)}
                className="absolute right-3 top-2.5 text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
              >
                {showKey ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
            </div>
          </div>
        </div>

        {/* Row 3: cost, default toggle */}
        <div className="flex flex-wrap gap-3 items-end">
          <div className="w-36">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Cost / 1K input ($)</label>
            <input
              type="number"
              step="0.0001"
              value={form.cost_input}
              onChange={(e) => setForm({ ...form, cost_input: e.target.value })}
              className={inputCls}
              placeholder="0.005"
            />
          </div>
          <div className="w-36">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Cost / 1K output ($)</label>
            <input
              type="number"
              step="0.0001"
              value={form.cost_output}
              onChange={(e) => setForm({ ...form, cost_output: e.target.value })}
              className={inputCls}
              placeholder="0.015"
            />
          </div>
          <label className="flex items-center gap-2 text-sm cursor-pointer py-2">
            <input
              type="checkbox"
              checked={form.is_default}
              onChange={(e) => setForm({ ...form, is_default: e.target.checked })}
              className="rounded border-[var(--ao-border)]"
            />
            <Star size={14} className="text-amber-400" /> Default provider
          </label>
          <div className="ml-auto">
            <Button disabled={submitting || !form.name}>
              {submitting ? 'Saving...' : isEdit ? 'Update' : 'Add'}
            </Button>
          </div>
        </div>
      </form>
    </Card>
  );
}

import { useState } from 'react';
import { Wrench, Plus, Trash2, Check, X } from 'lucide-react';
import { tools, type MCPTool } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, EmptyState,
  Spinner, ErrorBanner, Button,
} from '../components/UI';

export function ToolsPage() {
  const { data, loading, error, refetch } = useAPI(tools.list);
  const [showForm, setShowForm] = useState(false);

  return (
    <div>
      <PageHeader
        title="MCP Tools"
        description="Tools available via the MCP Gateway"
        action={
          <Button onClick={() => setShowForm(!showForm)}>
            <Plus size={16} className="mr-1.5" /> Register Tool
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {showForm && <ToolForm onCreated={() => { setShowForm(false); refetch(); }} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8">
          <Card className="overflow-hidden !p-0">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--ao-border)] text-left text-[var(--ao-text-muted)]">
                  <th className="px-4 py-3 font-medium">Name</th>
                  <th className="px-4 py-3 font-medium">Transport</th>
                  <th className="px-4 py-3 font-medium">Endpoint</th>
                  <th className="px-4 py-3 font-medium">Enabled</th>
                  <th className="px-4 py-3 font-medium w-16"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((tool) => (
                  <ToolRow key={tool.id} tool={tool} onAction={refetch} />
                ))}
              </tbody>
            </table>
          </Card>
        </div>
      ) : (
        <EmptyState
          icon={<Wrench size={48} />}
          title="No MCP tools registered"
          description="Register a tool to make it available via the MCP Gateway."
          action={<Button onClick={() => setShowForm(true)}>Register Tool</Button>}
        />
      )}
    </div>
  );
}

function ToolRow({ tool, onAction }: { tool: MCPTool; onAction: () => void }) {
  const [busy, setBusy] = useState(false);

  return (
    <tr className="border-b border-[var(--ao-border)] last:border-0 hover:bg-[var(--ao-surface-hover)]">
      <td className="px-4 py-3">
        <div>
          <p className="font-medium">{tool.name}</p>
          {tool.description && (
            <p className="text-xs text-[var(--ao-text-muted)] line-clamp-1">{tool.description}</p>
          )}
        </div>
      </td>
      <td className="px-4 py-3">
        <span className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">
          {tool.transport}
        </span>
      </td>
      <td className="px-4 py-3 text-xs text-[var(--ao-text-muted)] truncate max-w-[200px]">
        {tool.endpoint}
      </td>
      <td className="px-4 py-3">
        {tool.enabled ? (
          <Check size={16} className="text-emerald-400" />
        ) : (
          <X size={16} className="text-red-400" />
        )}
      </td>
      <td className="px-4 py-3">
        <button
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try { await tools.delete(tool.name); onAction(); } catch {}
            setBusy(false);
          }}
          className="text-red-400 hover:text-red-300 disabled:opacity-50"
        >
          <Trash2 size={14} />
        </button>
      </td>
    </tr>
  );
}

function ToolForm({ onCreated }: { onCreated: () => void }) {
  const [form, setForm] = useState({
    name: '', description: '', endpoint: '', transport: 'http',
  });
  const [submitting, setSubmitting] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name || !form.endpoint) return;
    setSubmitting(true);
    try {
      await tools.create({ ...form, enabled: true });
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
            placeholder="search-tool"
          />
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Endpoint *</label>
          <input
            value={form.endpoint}
            onChange={(e) => setForm({ ...form, endpoint: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="http://localhost:3001/mcp"
          />
        </div>
        <div className="w-28">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Transport</label>
          <select
            value={form.transport}
            onChange={(e) => setForm({ ...form, transport: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none"
          >
            <option value="http">HTTP</option>
            <option value="sse">SSE</option>
            <option value="stdio">stdio</option>
          </select>
        </div>
        <div className="flex-1 min-w-[200px]">
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
          <input
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            className="w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
            placeholder="A tool for..."
          />
        </div>
        <Button disabled={submitting || !form.name || !form.endpoint}>
          {submitting ? 'Registering...' : 'Register'}
        </Button>
      </form>
    </Card>
  );
}

import { useState } from 'react';
import { Wrench, Plus, Trash2, Check, X, Pencil, ToggleLeft, ToggleRight } from 'lucide-react';
import { tools, type MCPTool } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, EmptyState,
  Spinner, ErrorBanner, Button,
} from '../components/UI';

export function ToolsPage() {
  const { data, loading, error, refetch } = useAPI(tools.list);
  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<MCPTool | null>(null);

  const openEdit = (t: MCPTool) => { setEditing(t); setShowForm(true); };
  const closeForm = () => { setShowForm(false); setEditing(null); refetch(); };

  return (
    <div>
      <PageHeader
        title="MCP Tools"
        description="Tools available via the MCP Gateway"
        action={
          <Button onClick={() => { setEditing(null); setShowForm(!showForm); }}>
            <Plus size={16} className="mr-1.5" /> Register Tool
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {showForm && (
        <ToolForm existing={editing} onDone={closeForm} onCancel={() => { setShowForm(false); setEditing(null); }} />
      )}

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
                  <th className="px-4 py-3 font-medium">Capabilities</th>
                  <th className="px-4 py-3 font-medium">Schema</th>
                  <th className="px-4 py-3 font-medium">Enabled</th>
                  <th className="px-4 py-3 font-medium w-24"></th>
                </tr>
              </thead>
              <tbody>
                {data.map((tool) => (
                  <ToolRow key={tool.id} tool={tool} onAction={refetch} onEdit={() => openEdit(tool)} />
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

function ToolRow({
  tool, onAction, onEdit,
}: {
  tool: MCPTool; onAction: () => void; onEdit: () => void;
}) {
  const [busy, setBusy] = useState(false);

  const toggleEnabled = async () => {
    setBusy(true);
    try {
      await tools.update(tool.name, { enabled: !tool.enabled });
      onAction();
    } catch { /* ignore */ }
    setBusy(false);
  };

  const schemaKeys = Object.keys(tool.schema || {});

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
        <div className="flex flex-wrap gap-1">
          {(tool.capabilities || ['tool']).map((cap) => (
            <span key={cap} className={`text-xs px-1.5 py-0.5 rounded ${
              cap === 'notify' ? 'bg-amber-500/20 text-amber-400' : 'bg-blue-500/20 text-blue-400'
            }`}>
              {cap}
            </span>
          ))}
        </div>
      </td>
      <td className="px-4 py-3 text-xs text-[var(--ao-text-muted)]">
        {schemaKeys.length > 0 ? `${schemaKeys.length} field(s)` : '—'}
      </td>
      <td className="px-4 py-3">
        <button
          disabled={busy}
          onClick={toggleEnabled}
          className="text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] disabled:opacity-50"
          title={tool.enabled ? 'Click to disable' : 'Click to enable'}
        >
          {tool.enabled ? (
            <ToggleRight size={20} className="text-emerald-400" />
          ) : (
            <ToggleLeft size={20} className="text-red-400" />
          )}
        </button>
      </td>
      <td className="px-4 py-3">
        <div className="flex gap-2">
          <button
            onClick={onEdit}
            className="text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
          >
            <Pencil size={14} />
          </button>
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
        </div>
      </td>
    </tr>
  );
}

interface ToolFormState {
  name: string;
  description: string;
  endpoint: string;
  transport: string;
  schema: string;
  auth_type: string;
  auth_token: string;
  enabled: boolean;
  cap_tool: boolean;
  cap_notify: boolean;
}

function ToolForm({
  existing, onDone, onCancel,
}: {
  existing: MCPTool | null; onDone: () => void; onCancel: () => void;
}) {
  const isEdit = !!existing;
  const [form, setForm] = useState<ToolFormState>({
    name: existing?.name ?? '',
    description: existing?.description ?? '',
    endpoint: existing?.endpoint ?? '',
    transport: existing?.transport ?? 'http',
    schema: existing?.schema && Object.keys(existing.schema).length > 0
      ? JSON.stringify(existing.schema, null, 2)
      : '',
    auth_type: (existing?.auth_config?.type as string) ?? 'none',
    auth_token: '', // never pre-fill credentials
    enabled: existing?.enabled ?? true,
    cap_tool: existing?.capabilities ? existing.capabilities.includes('tool') : true,
    cap_notify: existing?.capabilities ? existing.capabilities.includes('notify') : false,
  });
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [schemaErr, setSchemaErr] = useState<string | null>(null);

  const validateSchema = () => {
    if (!form.schema.trim()) { setSchemaErr(null); return true; }
    try {
      JSON.parse(form.schema);
      setSchemaErr(null);
      return true;
    } catch (e) {
      setSchemaErr(e instanceof Error ? e.message : 'Invalid JSON');
      return false;
    }
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name || !form.endpoint) return;
    if (!validateSchema()) return;
    setSubmitting(true);
    setErr(null);

    const auth_config: Record<string, unknown> = {};
    if (form.auth_type !== 'none') {
      auth_config.type = form.auth_type;
      if (form.auth_token) auth_config.token = form.auth_token;
    }

    const capabilities: string[] = [];
    if (form.cap_tool) capabilities.push('tool');
    if (form.cap_notify) capabilities.push('notify');
    if (capabilities.length === 0) capabilities.push('tool');

    const payload: Partial<MCPTool> = {
      name: form.name,
      description: form.description,
      endpoint: form.endpoint,
      transport: form.transport,
      schema: form.schema.trim() ? JSON.parse(form.schema) : {},
      auth_config: Object.keys(auth_config).length > 0 ? auth_config : {},
      enabled: form.enabled,
      capabilities,
    };

    try {
      if (isEdit) {
        await tools.update(existing.name, payload);
      } else {
        await tools.create(payload);
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
        <h3 className="text-sm font-semibold">{isEdit ? `Edit ${existing.name}` : 'Register Tool'}</h3>
        <button onClick={onCancel} className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">Cancel</button>
      </div>
      {err && <ErrorBanner message={err} />}
      <form onSubmit={submit} className="space-y-4">
        {/* Row 1: name, transport, endpoint */}
        <div className="flex flex-wrap gap-3">
          <div className="w-40">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
            <input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              disabled={isEdit}
              className={`${inputCls} ${isEdit ? 'opacity-60' : ''}`}
              placeholder="search-tool"
            />
          </div>
          <div className="w-28">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Transport</label>
            <select
              value={form.transport}
              onChange={(e) => setForm({ ...form, transport: e.target.value })}
              className={inputCls}
            >
              <option value="http">HTTP</option>
              <option value="sse">SSE</option>
            </select>
          </div>
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Endpoint *</label>
            <input
              value={form.endpoint}
              onChange={(e) => setForm({ ...form, endpoint: e.target.value })}
              className={inputCls}
              placeholder="http://localhost:3001/mcp"
            />
          </div>
        </div>

        {/* Row 2: description */}
        <div>
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Description</label>
          <input
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
            className={inputCls}
            placeholder="A tool for searching documents..."
          />
        </div>

        {/* Row 3: schema JSON */}
        <div>
          <div className="flex items-center justify-between mb-1">
            <label className="text-xs text-[var(--ao-text-muted)]">Schema (JSON)</label>
            <button
              type="button"
              onClick={validateSchema}
              className="text-xs text-[var(--ao-brand)] hover:text-[var(--ao-brand-light)]"
            >
              Validate JSON
            </button>
          </div>
          <textarea
            value={form.schema}
            onChange={(e) => { setForm({ ...form, schema: e.target.value }); setSchemaErr(null); }}
            rows={4}
            className={`${inputCls} font-mono text-xs resize-y ${schemaErr ? 'border-red-500' : ''}`}
            placeholder='{"type": "object", "properties": {"query": {"type": "string"}}}'
          />
          {schemaErr && <p className="text-xs text-red-400 mt-1">{schemaErr}</p>}
        </div>

        {/* Row 4: auth config */}
        <div className="flex flex-wrap gap-3">
          <div className="w-36">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Auth Type</label>
            <select
              value={form.auth_type}
              onChange={(e) => setForm({ ...form, auth_type: e.target.value })}
              className={inputCls}
            >
              <option value="none">None</option>
              <option value="bearer">Bearer Token</option>
              <option value="header">Custom Header</option>
              <option value="basic">Basic Auth</option>
            </select>
          </div>
          {form.auth_type !== 'none' && (
            <div className="flex-1 min-w-[200px]">
              <label className="block text-xs text-[var(--ao-text-muted)] mb-1">
                {form.auth_type === 'bearer' ? 'Token' : form.auth_type === 'basic' ? 'user:password' : 'Header value'}
                {isEdit && ' (leave blank to keep current)'}
              </label>
              <input
                type="password"
                value={form.auth_token}
                onChange={(e) => setForm({ ...form, auth_token: e.target.value })}
                className={inputCls}
                placeholder="..."
              />
            </div>
          )}
        </div>

        {/* Row 5: capabilities */}
        <div>
          <label className="block text-xs text-[var(--ao-text-muted)] mb-2">Capabilities</label>
          <div className="flex gap-4">
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
                type="checkbox"
                checked={form.cap_tool}
                onChange={(e) => setForm({ ...form, cap_tool: e.target.checked })}
                className="rounded border-[var(--ao-border)]"
              />
              <span className="text-xs px-1.5 py-0.5 rounded bg-blue-500/20 text-blue-400">tool</span>
              <span className="text-xs text-[var(--ao-text-muted)]">Can be called as MCP tool</span>
            </label>
            <label className="flex items-center gap-2 text-sm cursor-pointer">
              <input
                type="checkbox"
                checked={form.cap_notify}
                onChange={(e) => setForm({ ...form, cap_notify: e.target.checked })}
                className="rounded border-[var(--ao-border)]"
              />
              <span className="text-xs px-1.5 py-0.5 rounded bg-amber-500/20 text-amber-400">notify</span>
              <span className="text-xs text-[var(--ao-text-muted)]">Receives notification events</span>
            </label>
          </div>
        </div>

        {/* Row 6: enabled + submit */}
        <div className="flex items-center justify-between">
          <label className="flex items-center gap-2 text-sm cursor-pointer">
            <input
              type="checkbox"
              checked={form.enabled}
              onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
              className="rounded border-[var(--ao-border)]"
            />
            {form.enabled ? (
              <Check size={14} className="text-emerald-400" />
            ) : (
              <X size={14} className="text-red-400" />
            )}
            Enabled
          </label>
          <Button disabled={submitting || !form.name || !form.endpoint}>
            {submitting ? 'Saving...' : isEdit ? 'Update' : 'Register'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

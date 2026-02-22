import { useState } from 'react';
import { FileText, Plus, Trash2, Pencil, History, Eye } from 'lucide-react';
import { prompts, type Prompt } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader, Card, EmptyState,
  Spinner, ErrorBanner, Button,
} from '../components/UI';

export function PromptsPage() {
  const { data, loading, error, refetch } = useAPI(prompts.list);
  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState<Prompt | null>(null);
  const [viewingVersions, setViewingVersions] = useState<string | null>(null);

  const openEdit = (p: Prompt) => { setEditing(p); setShowForm(true); };
  const closeForm = () => { setShowForm(false); setEditing(null); refetch(); };

  return (
    <div>
      <PageHeader
        title="Prompts"
        description="Versioned prompt templates for your agents"
        action={
          <Button onClick={() => { setEditing(null); setShowForm(!showForm); }}>
            <Plus size={16} className="mr-1.5" /> Create Prompt
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}
      {showForm && (
        <PromptForm existing={editing} onDone={closeForm} onCancel={() => { setShowForm(false); setEditing(null); }} />
      )}

      {viewingVersions && (
        <VersionHistory promptName={viewingVersions} onClose={() => setViewingVersions(null)} />
      )}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {data.map((prompt) => (
            <PromptCard
              key={prompt.id}
              prompt={prompt}
              onAction={refetch}
              onEdit={() => openEdit(prompt)}
              onViewVersions={() => setViewingVersions(prompt.name)}
            />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<FileText size={48} />}
          title="No prompts yet"
          description="Create versioned prompt templates for your agents."
          action={<Button onClick={() => setShowForm(true)}>Create Prompt</Button>}
        />
      )}
    </div>
  );
}

function PromptCard({
  prompt, onAction, onEdit, onViewVersions,
}: {
  prompt: Prompt; onAction: () => void; onEdit: () => void; onViewVersions: () => void;
}) {
  const [busy, setBusy] = useState(false);

  return (
    <Card>
      <div className="flex items-start justify-between mb-3">
        <div className="flex items-center gap-2">
          <FileText size={18} className="text-[var(--ao-brand-light)]" />
          <span className="font-medium">{prompt.name}</span>
        </div>
        <span className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">
          v{prompt.version}
        </span>
      </div>

      {/* Template preview */}
      <div className="mb-3 p-2 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">
        <pre className="text-xs text-[var(--ao-text-muted)] whitespace-pre-wrap line-clamp-4 font-mono">
          {prompt.template}
        </pre>
      </div>

      {/* Variables */}
      {prompt.variables && prompt.variables.length > 0 && (
        <div className="mb-3">
          <p className="text-xs text-[var(--ao-text-muted)] mb-1">Variables:</p>
          <div className="flex flex-wrap gap-1">
            {prompt.variables.map((v) => (
              <span key={v} className="text-xs px-1.5 py-0.5 rounded bg-[var(--ao-brand)]/20 text-[var(--ao-brand-light)]">
                {`{{${v}}}`}
              </span>
            ))}
          </div>
        </div>
      )}

      {/* Tags */}
      {prompt.tags && prompt.tags.length > 0 && (
        <div className="mb-3">
          <div className="flex flex-wrap gap-1">
            {prompt.tags.map((t) => (
              <span key={t} className="text-xs px-1.5 py-0.5 rounded bg-[var(--ao-surface-hover)] text-[var(--ao-text-muted)]">
                {t}
              </span>
            ))}
          </div>
        </div>
      )}

      <div className="flex gap-2">
        <button
          onClick={onEdit}
          className="flex items-center gap-1 text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
        >
          <Pencil size={12} /> Edit
        </button>
        <button
          onClick={onViewVersions}
          className="flex items-center gap-1 text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"
        >
          <History size={12} /> Versions
        </button>
        <button
          disabled={busy}
          onClick={async () => {
            setBusy(true);
            try { await prompts.delete(prompt.name); onAction(); } catch {}
            setBusy(false);
          }}
          className="flex items-center gap-1 text-xs text-red-400 hover:text-red-300 ml-auto disabled:opacity-50"
        >
          <Trash2 size={12} /> Delete
        </button>
      </div>
    </Card>
  );
}

function PromptForm({
  existing, onDone, onCancel,
}: {
  existing: Prompt | null; onDone: () => void; onCancel: () => void;
}) {
  const isEdit = !!existing;
  const [form, setForm] = useState({
    name: existing?.name ?? '',
    template: existing?.template ?? '',
    tags: existing?.tags?.join(', ') ?? '',
  });
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Auto-extract variables from template
  const extractedVars = (form.template.match(/\{\{(\w+)\}\}/g) || [])
    .map((m) => m.replace(/\{\{|\}\}/g, ''))
    .filter((v, i, arr) => arr.indexOf(v) === i);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!form.name || !form.template) return;
    setSubmitting(true);
    setErr(null);

    const tags = form.tags.split(',').map((t) => t.trim()).filter(Boolean);

    try {
      if (isEdit) {
        await prompts.update(existing.name, { template: form.template, tags });
      } else {
        await prompts.create({ name: form.name, template: form.template, tags });
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
        <h3 className="text-sm font-semibold">{isEdit ? `Edit ${existing.name}` : 'Create Prompt'}</h3>
        <button onClick={onCancel} className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">Cancel</button>
      </div>
      {err && <ErrorBanner message={err} />}
      <form onSubmit={submit} className="space-y-4">
        <div className="flex flex-wrap gap-3">
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Name *</label>
            <input
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              disabled={isEdit}
              className={`${inputCls} ${isEdit ? 'opacity-60' : ''}`}
              placeholder="system-prompt"
            />
          </div>
          <div className="flex-1 min-w-[200px]">
            <label className="block text-xs text-[var(--ao-text-muted)] mb-1">Tags (comma-sep)</label>
            <input
              value={form.tags}
              onChange={(e) => setForm({ ...form, tags: e.target.value })}
              className={inputCls}
              placeholder="agent, summarizer"
            />
          </div>
        </div>

        <div>
          <label className="block text-xs text-[var(--ao-text-muted)] mb-1">
            Template * <span className="text-[var(--ao-text-muted)]">— use {'{{variable}}'} for placeholders</span>
          </label>
          <textarea
            value={form.template}
            onChange={(e) => setForm({ ...form, template: e.target.value })}
            rows={8}
            className={`${inputCls} font-mono text-xs resize-y`}
            placeholder={`You are {{role}}, a helpful assistant.\n\nYour task: {{task}}\n\nRespond in {{language}}.`}
          />
        </div>

        {extractedVars.length > 0 && (
          <div>
            <p className="text-xs text-[var(--ao-text-muted)] mb-1">Detected variables:</p>
            <div className="flex flex-wrap gap-1">
              {extractedVars.map((v) => (
                <span key={v} className="text-xs px-1.5 py-0.5 rounded bg-[var(--ao-brand)]/20 text-[var(--ao-brand-light)]">
                  {`{{${v}}}`}
                </span>
              ))}
            </div>
          </div>
        )}

        <div className="flex justify-end">
          <Button disabled={submitting || !form.name || !form.template}>
            {submitting ? 'Saving...' : isEdit ? 'Update (new version)' : 'Create'}
          </Button>
        </div>
      </form>
    </Card>
  );
}

function VersionHistory({ promptName, onClose }: { promptName: string; onClose: () => void }) {
  const { data, loading, error } = useAPI(() => prompts.listVersions(promptName));
  const [viewing, setViewing] = useState<Prompt | null>(null);

  return (
    <Card className="mx-8 mt-4">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-sm font-semibold">Version History — {promptName}</h3>
        <button onClick={onClose} className="text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">Close</button>
      </div>

      {error && <ErrorBanner message={error} />}
      {loading && <Spinner />}

      {viewing && (
        <div className="mb-4 p-3 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium">v{viewing.version}</span>
            <button onClick={() => setViewing(null)} className="text-xs text-[var(--ao-text-muted)]">Close preview</button>
          </div>
          <pre className="text-xs text-[var(--ao-text-muted)] whitespace-pre-wrap font-mono">{viewing.template}</pre>
          {viewing.variables?.length > 0 && (
            <div className="mt-2 flex flex-wrap gap-1">
              {viewing.variables.map((v) => (
                <span key={v} className="text-xs px-1.5 py-0.5 rounded bg-[var(--ao-brand)]/20 text-[var(--ao-brand-light)]">
                  {`{{${v}}}`}
                </span>
              ))}
            </div>
          )}
        </div>
      )}

      {data && data.length > 0 && (
        <div className="space-y-2">
          {[...data].reverse().map((v) => (
            <div
              key={v.version}
              className="flex items-center justify-between p-2 rounded hover:bg-[var(--ao-surface-hover)] cursor-pointer"
              onClick={() => setViewing(v)}
            >
              <div className="flex items-center gap-2">
                <span className="text-xs font-medium px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">
                  v{v.version}
                </span>
                <span className="text-xs text-[var(--ao-text-muted)] truncate max-w-[300px]">
                  {v.template.substring(0, 80)}...
                </span>
              </div>
              <div className="flex items-center gap-2">
                <span className="text-xs text-[var(--ao-text-muted)]">
                  {new Date(v.updated_at).toLocaleDateString()}
                </span>
                <Eye size={12} className="text-[var(--ao-text-muted)]" />
              </div>
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

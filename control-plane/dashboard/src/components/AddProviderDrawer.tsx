import { useEffect, useState } from 'react';
import { X } from 'lucide-react';
import { providers, type ModelProvider, type ProviderTemplate } from '../api';
import { Button } from './UI';

const field = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';

/**
 * Adds a provider without leaving the page it was opened from, so a half-filled agent form stays as it
 * was. The Providers page has the full form (costs, capabilities, certificates).
 */
export function AddProviderDrawer({ open, onClose, onCreated }: {
  open: boolean;
  onClose: () => void;
  onCreated: (p: ModelProvider) => void;
}) {
  const [templates, setTemplates] = useState<ProviderTemplate[]>([]);
  const [kind, setKind] = useState('');
  const [name, setName] = useState('');
  const [endpoint, setEndpoint] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [models, setModels] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  useEffect(() => {
    if (!open) return;
    providers.templates().then(setTemplates).catch(() => setTemplates([]));
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onClose]);

  if (!open) return null;

  const pick = (t: ProviderTemplate) => {
    setKind(t.kind);
    setEndpoint(t.default_endpoint);
    setModels(t.default_models.join(', '));
    setName((n) => n || t.kind);
  };

  const save = async () => {
    if (!kind || !name.trim()) { setErr('Choose a type and give the provider a name.'); return; }
    setBusy(true); setErr('');
    try {
      const created = await providers.create({
        name: name.trim(), kind, endpoint,
        models: models.split(',').map((m) => m.trim()).filter(Boolean),
        config: apiKey ? { api_key: apiKey } : {},
        is_default: false,
      });
      setApiKey('');
      onCreated(created);
      onClose();
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'The provider could not be added.');
    }
    setBusy(false);
  };

  const needsKey = kind !== '' && kind !== 'ollama';

  return (
    <div className="fixed inset-0 z-[70]" role="presentation">
      <div className="absolute inset-0 bg-[rgba(15,23,42,0.6)]" onClick={onClose} aria-hidden="true" />
      <aside role="dialog" aria-label="Add a provider" className="absolute top-0 right-0 bottom-0 w-[460px] max-w-full flex flex-col bg-[var(--ao-surface)] border-l border-[var(--ao-border)] shadow-2xl">
        <div className="flex items-start justify-between px-6 py-4 border-b border-[var(--ao-border)]">
          <div>
            <h2 className="text-lg font-semibold">Add a provider</h2>
            <p className="text-sm text-[var(--ao-text-muted)] mt-1">What you were doing stays as you left it.</p>
          </div>
          <button type="button" onClick={onClose} aria-label="Close" className="w-9 h-9 grid place-items-center rounded-lg border border-[var(--ao-border)] text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">
            <X size={16} />
          </button>
        </div>
        <div className="flex-1 min-h-0 overflow-y-auto px-6 py-5 space-y-4">
          <div>
            <div className="text-sm font-medium mb-2">Type</div>
            <div role="listbox" aria-label="Provider types" className="flex flex-wrap gap-2">
              {templates.map((t) => (
                <button key={t.kind} type="button" role="option" aria-selected={kind === t.kind} onClick={() => pick(t)} title={t.description}
                  className={`h-9 px-3.5 rounded-full text-sm font-medium border ${kind === t.kind ? 'border-[var(--ao-brand)] bg-[var(--ao-brand)]/10 text-[var(--ao-text)]' : 'border-[var(--ao-border)] bg-[var(--ao-bg)] text-slate-300 hover:text-[var(--ao-text)]'}`}>
                  {t.display_name}
                </button>
              ))}
              {templates.length === 0 && <p className="text-sm text-[var(--ao-text-muted)]">Loading provider types…</p>}
            </div>
          </div>
          <label className="block text-sm font-medium">Name
            <input value={name} onChange={(e) => setName(e.target.value)} className={`${field} mt-1.5`} placeholder="mistral" />
          </label>
          <label className="block text-sm font-medium">Endpoint
            <input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} className={`${field} mt-1.5`} placeholder="https://api.example.com/v1" />
          </label>
          {needsKey && (
            <label className="block text-sm font-medium">API key
              <input type="password" value={apiKey} onChange={(e) => setApiKey(e.target.value)} className={`${field} mt-1.5`} autoComplete="off" />
              <span className="block text-xs font-normal text-[var(--ao-text-muted)] mt-1">Stored as a secret. It is not shown again.</span>
            </label>
          )}
          <label className="block text-sm font-medium">Models
            <input value={models} onChange={(e) => setModels(e.target.value)} className={`${field} mt-1.5`} placeholder="model-a, model-b" />
            <span className="block text-xs font-normal text-[var(--ao-text-muted)] mt-1">Comma separated. What the provider can handle is worked out from its type.</span>
          </label>
          {err && <p className="text-sm text-red-400">{err}</p>}
        </div>
        <div className="flex items-center justify-end gap-2 px-6 py-4 border-t border-[var(--ao-border)]">
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button onClick={save} disabled={busy}>{busy ? 'Adding…' : 'Add and use'}</Button>
        </div>
      </aside>
    </div>
  );
}

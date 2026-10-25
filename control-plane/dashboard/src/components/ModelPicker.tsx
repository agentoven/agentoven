import { useEffect, useMemo, useRef, useState } from 'react';
import { Check, ChevronDown, Plus, Search } from 'lucide-react';
import type { ModelProvider } from '../api';

const MODALITY_LABEL: Record<string, string> = {
  text: 'Text', image: 'Image', pdf: 'PDF', video: 'Video', audio: 'Speech', realtime: 'Live voice', web: 'Web search',
};
const can = (p: ModelProvider) => (p.modalities?.length ? p.modalities : ['text']).map((m) => MODALITY_LABEL[m] ?? m).join(', ');

/**
 * One searchable list of every model, grouped by provider. Picking a model picks its provider, and the
 * capabilities shown beside each belong to that provider. It scales to as many providers as you have.
 */
export function ModelPicker({ providers, provider, model, onChange, onAddProvider, label = 'Model' }: {
  providers: ModelProvider[];
  provider: string;
  model: string;
  onChange: (provider: string, model: string) => void;
  onAddProvider?: () => void;
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('mousedown', onDown); document.removeEventListener('keydown', onKey); };
  }, [open]);

  const q = query.trim().toLowerCase();
  const groups = useMemo(() => providers
    .map((p) => ({
      p,
      models: p.models?.length ? p.models : [],
    }))
    .map((g) => ({ ...g, shown: g.models.filter((m) => !q || m.toLowerCase().includes(q) || g.p.name.toLowerCase().includes(q) || g.p.kind.toLowerCase().includes(q)) }))
    .filter((g) => g.shown.length > 0 || (g.models.length === 0 && (!q || g.p.name.toLowerCase().includes(q)))), [providers, q]);

  const current = providers.find((p) => p.name === provider);
  const total = providers.reduce((n, p) => n + (p.models?.length ?? 0), 0);
  const choose = (p: string, m: string) => { onChange(p, m); setOpen(false); setQuery(''); };

  return (
    <div ref={ref} className="relative">
      <label className="block text-xs text-[var(--ao-text-muted)] mb-1">{label}</label>
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="w-full flex items-center gap-2 px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm text-left outline-none focus:border-[var(--ao-brand)]"
      >
        <span className="flex-1 min-w-0 truncate font-mono text-[13px]">
          {provider ? `${provider} / ${model || 'choose a model'}` : <span className="text-[var(--ao-text-muted)] font-sans">Choose a model</span>}
        </span>
        {current && <span className="text-xs text-[var(--ao-text-muted)] hidden sm:inline truncate max-w-[40%]">{can(current)}</span>}
        <ChevronDown size={16} className="text-[var(--ao-text-muted)] shrink-0" />
      </button>
      {open && (
        <div className="absolute left-0 right-0 z-30 mt-1 rounded-xl border border-[var(--ao-brand-light)] bg-[var(--ao-bg)] shadow-xl overflow-hidden">
          <div className="flex items-center gap-2 px-3 h-11 border-b border-[var(--ao-border)]">
            <Search size={15} className="text-[var(--ao-text-muted)]" />
            <input
              autoFocus
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              aria-label="Search models and providers"
              placeholder="Search models or providers"
              className="flex-1 min-w-0 bg-transparent outline-none text-sm"
            />
            <span className="text-xs text-[var(--ao-text-muted)]">{total} models</span>
          </div>
          <div role="listbox" aria-label="Models" className="max-h-52 overflow-y-auto p-1.5">
            {groups.length === 0 && <p className="px-3 py-3 text-sm text-[var(--ao-text-muted)]">Nothing matches &ldquo;{query}&rdquo;.</p>}
            {groups.map(({ p, models, shown }) => (
              <div key={p.name}>
                <div className="flex items-center gap-2 px-2.5 pt-2.5 pb-1 text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]">
                  {p.name}
                  <span className="font-normal normal-case tracking-normal">{p.kind}{p.last_test_healthy === false ? ' · last test failed' : ''}</span>
                </div>
                {models.length === 0 ? (
                  <button type="button" role="option" aria-selected={provider === p.name} onClick={() => choose(p.name, '')}
                    className="w-full text-left px-2.5 py-2 rounded-lg text-sm hover:bg-[var(--ao-surface-hover)]">
                    Use {p.name} and type the model name
                  </button>
                ) : shown.map((m) => {
                  const on = provider === p.name && model === m;
                  return (
                    <button key={m} type="button" role="option" aria-selected={on} onClick={() => choose(p.name, m)}
                      className={`w-full flex items-center gap-3 px-2.5 py-2 rounded-lg text-left hover:bg-[var(--ao-surface-hover)] ${on ? 'bg-[var(--ao-surface-hover)]' : ''}`}>
                      <span className="flex-1 min-w-0 truncate font-mono text-[13px]">{m}</span>
                      <span className="text-xs text-[var(--ao-text-muted)] hidden sm:inline">{can(p)}</span>
                      <Check size={15} className="text-[var(--ao-brand-light)]" style={{ visibility: on ? 'visible' : 'hidden' }} />
                    </button>
                  );
                })}
              </div>
            ))}
          </div>
          {onAddProvider && (
            <button type="button" onClick={() => { setOpen(false); onAddProvider(); }}
              className="w-full flex items-center gap-2 px-3.5 py-3 border-t border-[var(--ao-border)] text-sm font-semibold text-[var(--ao-brand-light)] hover:bg-[var(--ao-surface-hover)]">
              <Plus size={16} /> Add a provider
            </button>
          )}
        </div>
      )}
    </div>
  );
}

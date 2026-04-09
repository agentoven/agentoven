import { useState, useEffect, useMemo } from 'react';
import { Search, Check, Library, Wrench, Eye, Zap, BrainCircuit, Braces } from 'lucide-react';
import { catalog, type ModelCapability } from '../api';
import { Modal, Spinner, Button } from './UI';

// ── Cost formatter ───────────────────────────────────────────

function fmtCost(c?: number): string {
  if (c == null || c === 0) return '—';
  if (c < 0.001) return `$${c.toFixed(6)}`;
  return `$${c.toFixed(4)}`;
}

function fmtTokens(n?: number): string {
  if (!n) return '—';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`;
  return String(n);
}

// ── Capability Dot ───────────────────────────────────────────

function CapDot({ active, icon: Icon, label }: { active?: boolean; icon: React.ElementType; label: string }) {
  if (!active) return null;
  return (
    <span title={label} className="inline-flex items-center justify-center w-5 h-5 rounded-full bg-[var(--ao-brand)]/15 text-[var(--ao-brand-light)]">
      <Icon size={10} />
    </span>
  );
}

// ── Model Row ────────────────────────────────────────────────

function ModelRow({
  model,
  selected,
  onToggle,
}: {
  model: ModelCapability;
  selected: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      className={`w-full text-left flex items-center gap-3 px-3 py-2.5 rounded-lg border transition-all ${
        selected
          ? 'border-[var(--ao-brand)] bg-[var(--ao-brand)]/10'
          : 'border-transparent hover:bg-[var(--ao-surface-hover)]'
      }`}
    >
      {/* Checkbox */}
      <div className={`flex-shrink-0 w-4.5 h-4.5 rounded border flex items-center justify-center ${
        selected
          ? 'bg-[var(--ao-brand)] border-[var(--ao-brand)]'
          : 'border-[var(--ao-border)] bg-[var(--ao-bg)]'
      }`}>
        {selected && <Check size={12} className="text-white" />}
      </div>

      {/* Model info */}
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className="font-medium text-sm truncate">
            {model.display_name || model.model_name}
          </span>
          <div className="flex items-center gap-0.5">
            <CapDot active={model.supports_tools} icon={Wrench} label="Tools" />
            <CapDot active={model.supports_vision} icon={Eye} label="Vision" />
            <CapDot active={model.supports_streaming} icon={Zap} label="Streaming" />
            <CapDot active={model.supports_thinking} icon={BrainCircuit} label="Thinking" />
            <CapDot active={model.supports_json} icon={Braces} label="JSON" />
          </div>
        </div>
        <p className="text-xs text-[var(--ao-text-muted)] font-mono truncate">{model.model_id}</p>
      </div>

      {/* Context window */}
      <div className="flex-shrink-0 text-right w-16">
        <p className="text-[10px] text-[var(--ao-text-muted)]">Context</p>
        <p className="text-xs font-medium">{fmtTokens(model.context_window)}</p>
      </div>

      {/* Cost */}
      <div className="flex-shrink-0 text-right w-28">
        <p className="text-[10px] text-[var(--ao-text-muted)]">Cost / 1K (in · out)</p>
        <p className="text-xs font-medium">
          {fmtCost(model.input_cost_per_1k)} · {fmtCost(model.output_cost_per_1k)}
        </p>
      </div>
    </button>
  );
}

// ── Main Picker Modal ────────────────────────────────────────

export interface ModelCatalogPickerProps {
  /** Currently selected model names */
  selectedModels: string[];
  /** Provider kind to pre-filter (e.g. "openai", "anthropic") */
  providerKind?: string;
  /** Called with updated model list when user confirms */
  onConfirm: (models: string[], catalogModels: ModelCapability[]) => void;
  /** Called when modal is closed without confirming */
  onClose: () => void;
  /** Whether the modal is open */
  open: boolean;
}

export function ModelCatalogPicker({
  selectedModels,
  providerKind,
  onConfirm,
  onClose,
  open,
}: ModelCatalogPickerProps) {
  const [models, setModels] = useState<ModelCapability[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set(selectedModels));

  // Fetch catalog when modal opens
  useEffect(() => {
    if (!open) return;
    setLoading(true);
    setError(null);
    setSelected(new Set(selectedModels));
    setSearch('');
    catalog
      .list(providerKind)
      .then((res) => setModels(res.models ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load catalog'))
      .finally(() => setLoading(false));
  }, [open, providerKind]);

  // Filter + sort models
  const filtered = useMemo(() => {
    let list = models;

    // Text search across model_id, model_name, display_name
    if (search) {
      const q = search.toLowerCase();
      list = list.filter(
        (m) =>
          m.model_id.toLowerCase().includes(q) ||
          m.model_name.toLowerCase().includes(q) ||
          (m.display_name?.toLowerCase().includes(q) ?? false),
      );
    }

    // Sort: selected first, then alphabetically
    return list.sort((a, b) => {
      const aSelected = selected.has(a.model_name) ? 0 : 1;
      const bSelected = selected.has(b.model_name) ? 0 : 1;
      if (aSelected !== bSelected) return aSelected - bSelected;
      return (a.display_name || a.model_name).localeCompare(b.display_name || b.model_name);
    });
  }, [models, search, selected]);

  const toggle = (modelName: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(modelName)) {
        next.delete(modelName);
      } else {
        next.add(modelName);
      }
      return next;
    });
  };

  const handleConfirm = () => {
    const selectedArray = Array.from(selected);
    const selectedCatalogModels = models.filter((m) => selected.has(m.model_name));
    onConfirm(selectedArray, selectedCatalogModels);
  };

  return (
    <Modal open={open} onClose={onClose} title="Select Models from Catalog" width="max-w-4xl">
      {/* Search bar */}
      <div className="relative mb-4">
        <Search size={16} className="absolute left-3 top-2.5 text-[var(--ao-text-muted)]" />
        <input
          type="text"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Search models..."
          className="w-full pl-9 pr-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]"
          autoFocus
        />
      </div>

      {/* Info bar */}
      <div className="flex items-center justify-between mb-3 text-xs text-[var(--ao-text-muted)]">
        <span>
          {filtered.length} model{filtered.length !== 1 ? 's' : ''}
          {providerKind && <> for <span className="font-medium text-[var(--ao-text)]">{providerKind}</span></>}
        </span>
        <span>
          {selected.size} selected
          {selected.size > 0 && (
            <button
              type="button"
              onClick={() => setSelected(new Set())}
              className="ml-2 text-[var(--ao-brand-light)] hover:underline"
            >
              Clear all
            </button>
          )}
        </span>
      </div>

      {/* Model list */}
      {loading ? (
        <div className="flex items-center justify-center py-12">
          <Spinner />
        </div>
      ) : error ? (
        <div className="text-center py-12 text-sm text-red-400">{error}</div>
      ) : filtered.length === 0 ? (
        <div className="text-center py-12">
          <Library size={32} className="mx-auto mb-2 text-[var(--ao-text-muted)]" />
          <p className="text-sm text-[var(--ao-text-muted)]">
            {search
              ? 'No models match your search'
              : providerKind
                ? `No catalog models found for "${providerKind}". You can still type model names manually.`
                : 'No models in catalog. Try refreshing the catalog first.'}
          </p>
        </div>
      ) : (
        <div className="space-y-0.5 max-h-[50vh] overflow-y-auto pr-1 -mr-1">
          {filtered.map((m) => (
            <ModelRow
              key={m.model_id}
              model={m}
              selected={selected.has(m.model_name)}
              onToggle={() => toggle(m.model_name)}
            />
          ))}
        </div>
      )}

      {/* Footer */}
      <div className="flex items-center justify-between pt-4 mt-4 border-t border-[var(--ao-border)]">
        <p className="text-xs text-[var(--ao-text-muted)]">
          Costs auto-populated from catalog when available
        </p>
        <div className="flex items-center gap-2">
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          <Button onClick={handleConfirm}>
            Confirm {selected.size > 0 && `(${selected.size})`}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

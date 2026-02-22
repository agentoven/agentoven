import { useState } from 'react';
import {
  Library,
  RefreshCw,
  Radar,
  Wrench,
  Eye,
  Zap,
  BrainCircuit,
  Braces,
  AlertTriangle,
} from 'lucide-react';
import { catalog, providers as providersAPI, type ModelCapability, type ModelProvider } from '../api';
import { useAPI } from '../hooks';
import {
  PageHeader,
  Card,
  EmptyState,
  Spinner,
  ErrorBanner,
  Button,
} from '../components/UI';

// ── Helper: format token counts ──────────────────────────────

function fmtTokens(n?: number): string {
  if (!n) return '—';
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`;
  return String(n);
}

function fmtCost(c?: number): string {
  if (c == null || c === 0) return '—';
  if (c < 0.001) return `$${c.toFixed(6)}`;
  return `$${c.toFixed(4)}`;
}

// ── Capability Badge ─────────────────────────────────────────

function CapBadge({
  active,
  icon: Icon,
  label,
}: {
  active?: boolean;
  icon: React.ElementType;
  label: string;
}) {
  if (!active) return null;
  return (
    <span
      className="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-xs font-medium
                 bg-[var(--ao-brand)]/15 text-[var(--ao-brand-light)]"
      title={label}
    >
      <Icon size={12} />
      {label}
    </span>
  );
}

// ── Source Badge ──────────────────────────────────────────────

function SourceBadge({ source }: { source?: string }) {
  const colors: Record<string, string> = {
    catalog: 'bg-blue-500/20 text-blue-400',
    discovery: 'bg-emerald-500/20 text-emerald-400',
    manual: 'bg-amber-500/20 text-amber-400',
    builtin: 'bg-slate-500/20 text-slate-400',
  };
  const color = colors[source ?? ''] ?? 'bg-slate-500/20 text-slate-400';
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium ${color}`}>
      {source || 'unknown'}
    </span>
  );
}

// ── Model Card ───────────────────────────────────────────────

function ModelCard({ model }: { model: ModelCapability }) {
  return (
    <Card>
      <div className="flex items-start justify-between mb-2">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2 mb-1">
            <span className="font-semibold text-sm truncate">{model.display_name || model.model_name}</span>
            {model.deprecated_at && (
              <span className="flex-shrink-0" title={`Deprecated: ${model.deprecated_at}`}>
                <AlertTriangle size={14} className="text-amber-400" />
              </span>
            )}
          </div>
          <p className="text-xs text-[var(--ao-text-muted)] font-mono truncate" title={model.model_id}>
            {model.model_id}
          </p>
        </div>
        <div className="flex items-center gap-2 flex-shrink-0 ml-2">
          <SourceBadge source={model.source} />
          <span className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)] text-[var(--ao-text-muted)]">
            {model.provider_kind}
          </span>
        </div>
      </div>

      {/* Stats row */}
      <div className="grid grid-cols-3 gap-3 mb-3 text-xs">
        <div>
          <p className="text-[var(--ao-text-muted)]">Context</p>
          <p className="font-medium">{fmtTokens(model.context_window)}</p>
        </div>
        <div>
          <p className="text-[var(--ao-text-muted)]">Max Output</p>
          <p className="font-medium">{fmtTokens(model.max_output_tokens)}</p>
        </div>
        <div>
          <p className="text-[var(--ao-text-muted)]">Cost (in/out)</p>
          <p className="font-medium">
            {fmtCost(model.input_cost_per_1k)} / {fmtCost(model.output_cost_per_1k)}
          </p>
        </div>
      </div>

      {/* Capability badges */}
      <div className="flex flex-wrap gap-1.5">
        <CapBadge active={model.supports_tools} icon={Wrench} label="Tools" />
        <CapBadge active={model.supports_vision} icon={Eye} label="Vision" />
        <CapBadge active={model.supports_streaming} icon={Zap} label="Streaming" />
        <CapBadge active={model.supports_thinking} icon={BrainCircuit} label="Thinking" />
        <CapBadge active={model.supports_json} icon={Braces} label="JSON" />
      </div>

      {/* Modalities */}
      {model.modalities && model.modalities.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1">
          {model.modalities.map((m) => (
            <span
              key={m}
              className="text-[10px] px-1.5 py-0.5 rounded bg-[var(--ao-surface-hover)] text-[var(--ao-text-muted)]"
            >
              {m}
            </span>
          ))}
        </div>
      )}
    </Card>
  );
}

// ── Main Page ────────────────────────────────────────────────

export function CatalogPage() {
  const { data, loading, error, refetch } = useAPI(() => catalog.list());
  const { data: providersList } = useAPI(providersAPI.list);
  const [filter, setFilter] = useState('');
  const [refreshing, setRefreshing] = useState(false);
  const [discovering, setDiscovering] = useState<string | null>(null);
  const [discoverResult, setDiscoverResult] = useState<string | null>(null);

  // Get unique provider kinds from the catalog data
  const providerKinds = data?.models
    ? [...new Set(data.models.map((m) => m.provider_kind))].sort()
    : [];

  // Client-side filtering
  const filtered = data?.models?.filter(
    (m) => !filter || m.provider_kind === filter,
  ) ?? [];

  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      await catalog.refresh();
      // Wait a bit then refetch so the background refresh has time
      setTimeout(() => {
        refetch();
        setRefreshing(false);
      }, 2000);
    } catch {
      setRefreshing(false);
    }
  };

  const handleDiscover = async (providerName: string) => {
    setDiscovering(providerName);
    setDiscoverResult(null);
    try {
      const result = await catalog.discover(providerName);
      setDiscoverResult(`Discovered ${result.count} models from ${providerName}`);
      refetch();
    } catch (e) {
      setDiscoverResult(`Discovery failed: ${e instanceof Error ? e.message : 'unknown error'}`);
    }
    setDiscovering(null);
  };

  return (
    <div>
      <PageHeader
        title="Model Catalog"
        description="Browse model capabilities, context windows, costs, and feature support"
        action={
          <div className="flex items-center gap-2">
            <Button variant="secondary" onClick={handleRefresh} disabled={refreshing}>
              <RefreshCw size={16} className={`mr-1.5 ${refreshing ? 'animate-spin' : ''}`} />
              {refreshing ? 'Refreshing…' : 'Refresh'}
            </Button>
          </div>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}

      {discoverResult && (
        <div className="mx-8 mt-4 p-3 rounded-lg bg-blue-500/10 border border-blue-500/30 text-sm text-blue-400 flex items-center justify-between">
          <span>{discoverResult}</span>
          <button onClick={() => setDiscoverResult(null)} className="text-xs underline ml-4">
            Dismiss
          </button>
        </div>
      )}

      {/* Filter bar + discover actions */}
      <div className="px-8 py-4 flex items-center justify-between gap-4 border-b border-[var(--ao-border)]">
        <div className="flex items-center gap-3">
          <label className="text-sm text-[var(--ao-text-muted)]" htmlFor="provider-filter">
            Provider:
          </label>
          <select
            id="provider-filter"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            className="bg-[var(--ao-bg)] border border-[var(--ao-border)] rounded-lg px-3 py-1.5 text-sm"
            aria-label="Filter by provider kind"
          >
            <option value="">All ({data?.count ?? 0})</option>
            {providerKinds.map((pk) => (
              <option key={pk} value={pk}>
                {pk} ({data?.models?.filter((m) => m.provider_kind === pk).length ?? 0})
              </option>
            ))}
          </select>
          <span className="text-xs text-[var(--ao-text-muted)]">
            Showing {filtered.length} model{filtered.length !== 1 ? 's' : ''}
          </span>
        </div>

        {/* Discover from configured providers */}
        {providersList && providersList.length > 0 && (
          <div className="flex items-center gap-2">
            <span className="text-xs text-[var(--ao-text-muted)]">Discover:</span>
            {providersList.map((p: ModelProvider) => (
              <Button
                key={p.name}
                size="sm"
                variant="secondary"
                onClick={() => handleDiscover(p.name)}
                disabled={discovering === p.name}
              >
                <Radar size={14} className={`mr-1 ${discovering === p.name ? 'animate-pulse' : ''}`} />
                {p.name}
              </Button>
            ))}
          </div>
        )}
      </div>

      {loading ? (
        <Spinner />
      ) : filtered.length > 0 ? (
        <div className="p-8 grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
          {filtered.map((m) => (
            <ModelCard key={m.model_id} model={m} />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<Library size={48} />}
          title="No models in catalog"
          description="The catalog populates automatically from LiteLLM model data. Click Refresh or use Discover to query your providers."
          action={
            <Button onClick={handleRefresh} disabled={refreshing}>
              <RefreshCw size={16} className="mr-1.5" /> Refresh Catalog
            </Button>
          }
        />
      )}
    </div>
  );
}

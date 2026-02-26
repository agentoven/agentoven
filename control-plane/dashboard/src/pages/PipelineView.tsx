import { useState, useEffect, useCallback, useMemo } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import {
  GitBranch, Play, Clock, CheckCircle, XCircle, Pause,
  ArrowRight, ChevronDown, ChevronRight, RefreshCw, AlertTriangle,
  Bot, Search, Shield, Zap, Users, Layers,
} from 'lucide-react';
import {
  recipes, recipeRuns,
  type Recipe, type RecipeRun, type StepResult,
} from '../api';
import {
  PageHeader, Card, Spinner, ErrorBanner, Button, StatusBadge, Modal,
} from '../components/UI';

// ── Step kind → icon + colour mapping ─────────────────────────

const stepMeta: Record<string, { icon: typeof Bot; color: string; bg: string }> = {
  agent:      { icon: Bot,          color: 'text-blue-400',    bg: 'bg-blue-500/20 border-blue-500/40' },
  rag:        { icon: Search,       color: 'text-purple-400',  bg: 'bg-purple-500/20 border-purple-500/40' },
  human_gate: { icon: Users,        color: 'text-amber-400',   bg: 'bg-amber-500/20 border-amber-500/40' },
  evaluator:  { icon: Shield,       color: 'text-cyan-400',    bg: 'bg-cyan-500/20 border-cyan-500/40' },
  condition:  { icon: GitBranch,    color: 'text-teal-400',    bg: 'bg-teal-500/20 border-teal-500/40' },
  fan_out:    { icon: Zap,          color: 'text-orange-400',  bg: 'bg-orange-500/20 border-orange-500/40' },
  fan_in:     { icon: Layers,       color: 'text-orange-400',  bg: 'bg-orange-500/20 border-orange-500/40' },
};

const statusIcon: Record<string, { Icon: typeof CheckCircle; cls: string }> = {
  completed: { Icon: CheckCircle,  cls: 'text-emerald-400' },
  success:   { Icon: CheckCircle,  cls: 'text-emerald-400' },
  running:   { Icon: RefreshCw,    cls: 'text-blue-400 animate-spin' },
  failed:    { Icon: XCircle,      cls: 'text-red-400' },
  error:     { Icon: XCircle,      cls: 'text-red-400' },
  waiting:   { Icon: Clock,        cls: 'text-slate-400' },
  paused:    { Icon: Pause,        cls: 'text-amber-400' },
  pending:   { Icon: Clock,        cls: 'text-slate-400' },
  skipped:   { Icon: ArrowRight,   cls: 'text-slate-500' },
};

// ── Helpers ──────────────────────────────────────────────────

function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function fmtTime(iso?: string): string {
  if (!iso) return '–';
  return new Date(iso).toLocaleTimeString();
}

// ── Layout: compute x/y positions for DAG nodes ─────────────

interface LayoutNode {
  name: string;
  kind: string;
  col: number;
  row: number;
  deps: string[];
  agentRef?: string;
}

function layoutDAG(steps: Recipe['steps']): LayoutNode[] {
  if (!steps || steps.length === 0) return [];

  // Build dependency map
  const depMap = new Map<string, string[]>();
  steps.forEach((s) => depMap.set(s.name, s.depends_on || []));

  // Topological sort with column assignment (longest path = column)
  const cols = new Map<string, number>();

  function getCol(name: string): number {
    if (cols.has(name)) return cols.get(name)!;
    const deps = depMap.get(name) || [];
    if (deps.length === 0) {
      cols.set(name, 0);
      return 0;
    }
    const maxDep = Math.max(...deps.map(getCol));
    const col = maxDep + 1;
    cols.set(name, col);
    return col;
  }

  steps.forEach((s) => getCol(s.name));

  // Group by column, assign rows
  const byCol = new Map<number, string[]>();
  steps.forEach((s) => {
    const c = cols.get(s.name)!;
    if (!byCol.has(c)) byCol.set(c, []);
    byCol.get(c)!.push(s.name);
  });

  const rows = new Map<string, number>();
  byCol.forEach((names) => {
    names.forEach((n, i) => rows.set(n, i));
  });

  return steps.map((s) => ({
    name: s.name,
    kind: s.kind,
    col: cols.get(s.name) ?? 0,
    row: rows.get(s.name) ?? 0,
    deps: s.depends_on || [],
    agentRef: s.agent,
  }));
}

// ── Main Page ────────────────────────────────────────────────

export function PipelineViewPage() {
  const { recipeName, runId } = useParams<{ recipeName?: string; runId?: string }>();
  const navigate = useNavigate();

  const [recipeList, setRecipeList] = useState<Recipe[]>([]);
  const [selectedRecipe, setSelectedRecipe] = useState<Recipe | null>(null);
  const [runList, setRunList] = useState<RecipeRun[]>([]);
  const [selectedRun, setSelectedRun] = useState<RecipeRun | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [polling, setPolling] = useState(false);
  const [detailStep, setDetailStep] = useState<StepResult | null>(null);

  // Load recipe list
  useEffect(() => {
    recipes.list()
      .then(setRecipeList)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  // When recipe selected (or URL param), load it + runs
  const loadRecipe = useCallback(async (name: string) => {
    try {
      const [r, runs] = await Promise.all([
        recipes.get(name),
        recipeRuns.list(name),
      ]);
      setSelectedRecipe(r);
      setRunList(runs || []);
      setError(null);
      // Auto-select latest run or URL-specified run
      if (runId) {
        const found = (runs || []).find((run) => run.id === runId);
        if (found) setSelectedRun(found);
      } else if (runs && runs.length > 0) {
        setSelectedRun(runs[0]);
      }
    } catch (e: unknown) {
      setError((e as Error).message);
    }
  }, [runId]);

  useEffect(() => {
    if (recipeName) loadRecipe(recipeName);
  }, [recipeName, loadRecipe]);

  // Poll running executions
  useEffect(() => {
    if (!selectedRun || !selectedRecipe) return;
    if (selectedRun.status !== 'running') return;

    setPolling(true);
    const timer = setInterval(async () => {
      try {
        const data = await recipeRuns.get(selectedRecipe.name, selectedRun.id);
        const run = (data as { run?: RecipeRun }).run ?? data as RecipeRun;
        setSelectedRun(run);
        if (run.status !== 'running') {
          setPolling(false);
          clearInterval(timer);
        }
      } catch { /* ignore polling errors */ }
    }, 2000);

    return () => { clearInterval(timer); setPolling(false); };
  }, [selectedRun?.id, selectedRun?.status, selectedRecipe?.name]);

  // Layout nodes from recipe steps
  const nodes = useMemo(
    () => (selectedRecipe ? layoutDAG(selectedRecipe.steps) : []),
    [selectedRecipe],
  );

  // Map step results by name for quick lookup
  const resultMap = useMemo(() => {
    const m = new Map<string, StepResult>();
    selectedRun?.step_results?.forEach((sr) => m.set(sr.step_name, sr));
    return m;
  }, [selectedRun]);

  if (loading) return <Spinner />;

  // ── No recipe selected — show picker ──────────────────────

  if (!selectedRecipe) {
    return (
      <div>
        <PageHeader
          title="DishShelf"
          description="Visualize your recipe runs as a pipeline DAG"
        />
        {error && <ErrorBanner message={error} />}

        {recipeList.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-16 text-center">
            <GitBranch size={48} className="text-[var(--ao-text-muted)] mb-4" />
            <h3 className="text-lg font-medium mb-1">No recipes</h3>
            <p className="text-sm text-[var(--ao-text-muted)]">Create a recipe first to visualize its pipeline.</p>
          </div>
        ) : (
          <div className="p-8 grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
            {recipeList.map((r) => (
              <Card
                key={r.id}
                onClick={() => navigate(`/dishshelf/${r.name}`)}
                className="cursor-pointer"
              >
                <div className="flex items-center gap-2 mb-2">
                  <GitBranch size={18} className="text-[var(--ao-brand-light)]" />
                  <span className="font-medium">{r.name}</span>
                </div>
                <p className="text-xs text-[var(--ao-text-muted)] mb-3 line-clamp-2">
                  {r.description || 'No description'}
                </p>
                <div className="flex items-center gap-2 text-xs text-[var(--ao-text-muted)]">
                  <span>{r.steps?.length || 0} steps</span>
                  <span>·</span>
                  <span>v{r.version}</span>
                </div>
              </Card>
            ))}
          </div>
        )}
      </div>
    );
  }

  // ── Recipe selected — show DAG + run ──────────────────────

  const maxCol = Math.max(0, ...nodes.map((n) => n.col));
  const maxRow = Math.max(0, ...nodes.map((n) => n.row));

  const NODE_W = 200;
  const NODE_H = 80;
  const GAP_X  = 100;
  const GAP_Y  = 40;
  const PAD    = 40;

  const svgW = (maxCol + 1) * (NODE_W + GAP_X) + PAD * 2;
  const svgH = (maxRow + 1) * (NODE_H + GAP_Y) + PAD * 2;

  function nodeX(col: number) { return PAD + col * (NODE_W + GAP_X); }
  function nodeY(row: number) { return PAD + row * (NODE_H + GAP_Y); }

  return (
    <div>
      <PageHeader
        title={`Pipeline: ${selectedRecipe.name}`}
        description={selectedRecipe.description || `${nodes.length} steps`}
        action={
          <div className="flex items-center gap-3">
            {polling && (
              <span className="flex items-center gap-1.5 text-xs text-blue-400">
                <RefreshCw size={12} className="animate-spin" /> Live
              </span>
            )}
            <Button
              size="sm"
              variant="secondary"
              onClick={() => { setSelectedRecipe(null); setSelectedRun(null); navigate('/dishshelf'); }}
            >
              ← DishShelf
            </Button>
          </div>
        }
      />

      {error && <ErrorBanner message={error} />}

      <div className="flex h-[calc(100vh-80px)]">
        {/* ── Left: Run list ──────────────────────────────── */}
        <aside className="w-72 border-r border-[var(--ao-border)] overflow-y-auto shrink-0">
          <div className="px-4 py-3 border-b border-[var(--ao-border)] flex items-center justify-between">
            <span className="text-sm font-medium">Runs</span>
            <span className="text-xs text-[var(--ao-text-muted)]">{runList.length}</span>
          </div>
          {runList.length === 0 ? (
            <p className="p-4 text-xs text-[var(--ao-text-muted)]">No runs yet. Execute the recipe to see results.</p>
          ) : (
            <ul>
              {runList.map((run) => (
                <li
                  key={run.id}
                  onClick={() => setSelectedRun(run)}
                  className={`px-4 py-3 cursor-pointer border-b border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] transition-colors ${
                    selectedRun?.id === run.id ? 'bg-[var(--ao-surface-hover)] border-l-2 border-l-[var(--ao-brand)]' : ''
                  }`}
                >
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-xs font-mono text-[var(--ao-text-muted)]">
                      {run.id.slice(0, 8)}…
                    </span>
                    <StatusBadge status={run.status} />
                  </div>
                  <div className="flex items-center gap-2 text-xs text-[var(--ao-text-muted)]">
                    <Clock size={10} />
                    <span>{fmtTime(run.started_at)}</span>
                    {run.duration_ms > 0 && <span>· {fmtMs(run.duration_ms)}</span>}
                  </div>
                  <div className="text-xs text-[var(--ao-text-muted)] mt-1">
                    {run.step_results?.length || 0} step result(s)
                  </div>
                </li>
              ))}
            </ul>
          )}
        </aside>

        {/* ── Right: DAG Canvas ───────────────────────────── */}
        <main className="flex-1 overflow-auto bg-[var(--ao-bg)] p-4">
          {!selectedRun ? (
            <div className="flex flex-col items-center justify-center h-full text-[var(--ao-text-muted)]">
              <Play size={48} className="mb-3" />
              <p className="text-sm">Select a run from the left panel</p>
            </div>
          ) : (
            <>
              {/* Run summary bar */}
              <div className="flex items-center gap-4 mb-6 px-2">
                <StatusBadge status={selectedRun.status} />
                <span className="text-xs text-[var(--ao-text-muted)]">
                  Started {fmtTime(selectedRun.started_at)}
                </span>
                {selectedRun.duration_ms > 0 && (
                  <span className="text-xs text-[var(--ao-text-muted)]">
                    Duration: {fmtMs(selectedRun.duration_ms)}
                  </span>
                )}
                {selectedRun.error && (
                  <span className="text-xs text-red-400 flex items-center gap-1">
                    <AlertTriangle size={12} /> {selectedRun.error.slice(0, 80)}
                  </span>
                )}
              </div>

              {/* DAG SVG */}
              <div
                className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)]"
                style={{ minWidth: svgW, minHeight: svgH, overflow: 'auto' }}
              >
                <svg width={svgW} height={svgH}>
                  {/* ── Edges (dependency arrows) ─────────── */}
                  <defs>
                    <marker
                      id="arrowhead"
                      viewBox="0 0 10 7"
                      refX="10"
                      refY="3.5"
                      markerWidth="8"
                      markerHeight="6"
                      orient="auto"
                    >
                      <polygon points="0 0, 10 3.5, 0 7" fill="var(--ao-text-muted, #64748b)" />
                    </marker>
                  </defs>

                  {nodes.flatMap((node) =>
                    node.deps.map((dep) => {
                      const src = nodes.find((n) => n.name === dep);
                      if (!src) return null;
                      const x1 = nodeX(src.col) + NODE_W;
                      const y1 = nodeY(src.row) + NODE_H / 2;
                      const x2 = nodeX(node.col);
                      const y2 = nodeY(node.row) + NODE_H / 2;
                      const midX = (x1 + x2) / 2;

                      // Determine edge colour from result status
                      const srcResult = resultMap.get(dep);
                      let strokeColor = 'var(--ao-border, #334155)';
                      if (srcResult?.status === 'completed' || srcResult?.status === 'success') strokeColor = '#34d399';
                      else if (srcResult?.status === 'failed' || srcResult?.status === 'error') strokeColor = '#f87171';
                      else if (srcResult?.status === 'running') strokeColor = '#60a5fa';

                      return (
                        <path
                          key={`${dep}->${node.name}`}
                          d={`M${x1},${y1} C${midX},${y1} ${midX},${y2} ${x2},${y2}`}
                          stroke={strokeColor}
                          strokeWidth={2}
                          fill="none"
                          markerEnd="url(#arrowhead)"
                          opacity={0.7}
                        />
                      );
                    }),
                  )}

                  {/* ── Nodes ─────────────────────────────── */}
                  {nodes.map((node) => {
                    const x = nodeX(node.col);
                    const y = nodeY(node.row);
                    const sr = resultMap.get(node.name);
                    const meta = stepMeta[node.kind] || stepMeta.agent;
                    const Icon = meta.icon;
                    const si = statusIcon[sr?.status || 'pending'] || statusIcon.pending;

                    // Node border colour by status
                    let borderColor = 'var(--ao-border, #334155)';
                    if (sr?.status === 'completed' || sr?.status === 'success') borderColor = '#34d399';
                    else if (sr?.status === 'failed' || sr?.status === 'error') borderColor = '#f87171';
                    else if (sr?.status === 'running') borderColor = '#60a5fa';
                    else if (sr?.status === 'paused' || sr?.status === 'waiting') borderColor = '#fbbf24';

                    return (
                      <g
                        key={node.name}
                        onClick={() => sr && setDetailStep(sr)}
                        className="cursor-pointer"
                      >
                        {/* Background */}
                        <rect
                          x={x}
                          y={y}
                          width={NODE_W}
                          height={NODE_H}
                          rx={12}
                          ry={12}
                          fill="var(--ao-surface, #1e293b)"
                          stroke={borderColor}
                          strokeWidth={2}
                        />

                        {/* Kind icon */}
                        <foreignObject x={x + 10} y={y + 12} width={24} height={24}>
                          <Icon size={18} className={meta.color} />
                        </foreignObject>

                        {/* Step name */}
                        <text
                          x={x + 40}
                          y={y + 27}
                          fill="var(--ao-text, #e2e8f0)"
                          fontSize={12}
                          fontWeight={600}
                          fontFamily="inherit"
                        >
                          {node.name.length > 18 ? node.name.slice(0, 17) + '…' : node.name}
                        </text>

                        {/* Kind badge */}
                        <text
                          x={x + 40}
                          y={y + 46}
                          fill="var(--ao-text-muted, #94a3b8)"
                          fontSize={10}
                        >
                          {node.kind}{node.agentRef ? ` → ${node.agentRef}` : ''}
                        </text>

                        {/* Duration */}
                        {sr?.duration_ms != null && sr.duration_ms > 0 && (
                          <text
                            x={x + 40}
                            y={y + 62}
                            fill="var(--ao-text-muted, #94a3b8)"
                            fontSize={10}
                          >
                            ⏱ {fmtMs(sr.duration_ms)}
                          </text>
                        )}

                        {/* Status icon */}
                        <foreignObject x={x + NODE_W - 30} y={y + 10} width={20} height={20}>
                          <si.Icon size={16} className={si.cls} />
                        </foreignObject>
                      </g>
                    );
                  })}
                </svg>
              </div>

              {/* Step results table */}
              <div className="mt-6">
                <h3 className="text-sm font-medium mb-3 px-2">Step Results</h3>
                <div className="rounded-xl border border-[var(--ao-border)] overflow-hidden">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="bg-[var(--ao-surface)] text-[var(--ao-text-muted)] text-xs">
                        <th className="text-left px-4 py-2">Step</th>
                        <th className="text-left px-4 py-2">Kind</th>
                        <th className="text-left px-4 py-2">Status</th>
                        <th className="text-left px-4 py-2">Agent</th>
                        <th className="text-right px-4 py-2">Duration</th>
                        <th className="text-right px-4 py-2">Tokens</th>
                        <th className="text-left px-4 py-2">Error</th>
                      </tr>
                    </thead>
                    <tbody>
                      {(selectedRun.step_results || []).map((sr, i) => {
                        const si = statusIcon[sr.status] || statusIcon.pending;
                        return (
                          <tr
                            key={i}
                            onClick={() => setDetailStep(sr)}
                            className="border-t border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] cursor-pointer transition-colors"
                          >
                            <td className="px-4 py-2 font-mono text-xs">{sr.step_name}</td>
                            <td className="px-4 py-2">
                              <span className="text-xs px-2 py-0.5 rounded-full bg-[var(--ao-bg)] border border-[var(--ao-border)]">
                                {sr.step_kind}
                              </span>
                            </td>
                            <td className="px-4 py-2">
                              <span className={`flex items-center gap-1 ${si.cls}`}>
                                <si.Icon size={12} /> {sr.status}
                              </span>
                            </td>
                            <td className="px-4 py-2 text-xs text-[var(--ao-text-muted)]">
                              {sr.agent_ref || '–'}
                            </td>
                            <td className="px-4 py-2 text-right text-xs">
                              {sr.duration_ms > 0 ? fmtMs(sr.duration_ms) : '–'}
                            </td>
                            <td className="px-4 py-2 text-right text-xs">
                              {sr.tokens > 0 ? sr.tokens.toLocaleString() : '–'}
                            </td>
                            <td className="px-4 py-2 text-xs text-red-400 max-w-[200px] truncate">
                              {sr.error || '–'}
                            </td>
                          </tr>
                        );
                      })}
                      {(!selectedRun.step_results || selectedRun.step_results.length === 0) && (
                        <tr>
                          <td colSpan={7} className="px-4 py-8 text-center text-xs text-[var(--ao-text-muted)]">
                            No step results yet
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
              </div>
            </>
          )}
        </main>
      </div>

      {/* ── Step Detail Modal ─────────────────────────────── */}
      <Modal
        open={!!detailStep}
        onClose={() => setDetailStep(null)}
        title={detailStep ? `Step: ${detailStep.step_name}` : ''}
        width="max-w-3xl"
      >
        {detailStep && <StepDetailPanel step={detailStep} />}
      </Modal>
    </div>
  );
}

// ── Step Detail Panel ────────────────────────────────────────

function StepDetailPanel({ step }: { step: StepResult }) {
  const [expanded, setExpanded] = useState(true);

  return (
    <div className="space-y-4">
      {/* Meta */}
      <div className="grid grid-cols-2 gap-4">
        <InfoItem label="Kind" value={step.step_kind} />
        <InfoItem label="Status" value={step.status} />
        <InfoItem label="Agent" value={step.agent_ref || '–'} />
        <InfoItem label="Duration" value={step.duration_ms > 0 ? fmtMs(step.duration_ms) : '–'} />
        <InfoItem label="Tokens" value={step.tokens > 0 ? step.tokens.toLocaleString() : '–'} />
        <InfoItem label="Cost" value={step.cost_usd > 0 ? `$${step.cost_usd.toFixed(4)}` : '–'} />
        {step.gate_status && <InfoItem label="Gate" value={step.gate_status} />}
        {step.branch_taken && <InfoItem label="Branch" value={step.branch_taken} />}
      </div>

      {/* Error */}
      {step.error && (
        <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30">
          <p className="text-xs font-medium text-red-400 mb-1">Error</p>
          <pre className="text-xs text-red-300 whitespace-pre-wrap font-mono">{step.error}</pre>
        </div>
      )}

      {/* Output */}
      {step.output && Object.keys(step.output).length > 0 && (
        <div>
          <button
            onClick={() => setExpanded(!expanded)}
            className="flex items-center gap-1 text-sm font-medium text-[var(--ao-text)] mb-2"
          >
            {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            Output
          </button>
          {expanded && (
            <pre className="text-xs bg-[var(--ao-bg)] rounded-lg p-4 overflow-auto max-h-96 border border-[var(--ao-border)] font-mono">
              {JSON.stringify(step.output, null, 2)}
            </pre>
          )}
        </div>
      )}
    </div>
  );
}

function InfoItem({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <span className="text-xs text-[var(--ao-text-muted)] block">{label}</span>
      <span className="text-sm font-medium">{value}</span>
    </div>
  );
}

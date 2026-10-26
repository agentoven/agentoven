import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Check, Play } from 'lucide-react';
import { agents, recipes, tools, type Recipe } from '../../api';
import { useAPI } from '../../hooks';
import { Button, ErrorBanner, Spinner } from '../../components/UI';
import FlowCanvas from '../../components/recipes/FlowCanvas';
import StepInspector from '../../components/recipes/StepInspector';
import RunRecipeModal from '../../components/recipes/RunRecipeModal';
import { PALETTE } from '../../components/recipes/stepMeta';
import {
  compile, decompile, findNode, insertNode, newStep, removeNode, resizeLanes, sameWire, setApprovalPaths, updateStep, validate, whereAfter,
  type FlowNode, type StepType, type WireStep, type Where,
} from '../../lib/recipeFlow';

/** Builds a recipe by adding steps one after another, with Yes and No paths where it needs to choose. */
export default function RecipeBuilder() {
  const { name = '' } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const canEdit = true;
  const recipe = useAPI(() => recipes.get(name));
  const agentList = useAPI(() => agents.list());
  const toolList = useAPI(() => tools.list());
  const recipeList = useAPI(() => recipes.list());

  // What was saved, read back into a flow. Edits live apart from it until they are saved.
  const decoded = useMemo(() => (recipe.data ? decompile((recipe.data.steps ?? []) as WireStep[]) : null), [recipe.data]);
  const [edited, setEdited] = useState<FlowNode[] | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [showRun, setShowRun] = useState(false);

  if (recipe.loading && !recipe.data) return <Spinner />;
  if (recipe.error && !recipe.data) return <ErrorBanner message={recipe.error} onRetry={recipe.refetch} />;
  if (!recipe.data || !decoded) return <ErrorBanner message="Recipe not found" />;
  const data: Recipe = recipe.data;

  const agentNames = new Set((agentList.data ?? []).map((a) => a.name));
  const flow: FlowNode[] = edited ?? (decoded.ok ? decoded.flow : []);
  const recipeNames = new Set((recipeList.data ?? []).map((r) => r.name));
  const problems = validate(flow, agentNames, recipeList.data ? { names: recipeNames, self: data.name } : undefined);
  const problemMap = new Map(problems.filter((p) => p.id).map((p) => [p.id, p.message]));
  const dirty = edited !== null && !sameWire(compile(edited), (data.steps ?? []) as WireStep[]);
  const notifyTools = (toolList.data ?? []).filter((t) => t.capabilities?.includes('notify')).map((t) => t.name);

  const change = (next: FlowNode[]) => { setEdited(next); setError(''); };
  const add = (where: Where, type: StepType) => {
    const node = newStep(type, flow);
    change(insertNode(flow, where, node));
    setSelected(node.id);
  };
  const addFromPalette = (type: StepType) => {
    const where = (selected && whereAfter(flow, selected)) || { parent: null, path: null, index: flow.length };
    add(where, type);
  };
  const remove = (id: string) => { change(removeNode(flow, id)); setSelected(null); };
  const update = (id: string, patch: Partial<WireStep>, config?: Record<string, unknown>) => change(updateStep(flow, id, patch, config));

  const save = async () => {
    setSaving(true); setError('');
    try {
      await recipes.update(data.name, { description: data.description, steps: compile(flow) as unknown as Recipe['steps'] });
      setEdited(null);
      recipe.refetch();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'The recipe could not be saved.');
    }
    setSaving(false);
  };

  const sel = selected ? findNode(flow, selected) : undefined;
  const ready = problems.length === 0;

  return (
    <div>
      <div className="px-4 sm:px-8 pt-5 border-b border-[var(--ao-border)]">
        <nav aria-label="Breadcrumb" className="flex items-center gap-2 text-sm text-[var(--ao-text-muted)] mb-3">
          <span>Build</span><span aria-hidden="true">/</span>
          <Link to="/recipes" className="hover:text-[var(--ao-text)]">Recipes</Link><span aria-hidden="true">/</span>
          <span className="text-[var(--ao-text)] font-medium">{data.name}</span>
        </nav>
        <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
          <div className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 min-w-0">
            <h1 className="text-2xl font-bold tracking-tight [overflow-wrap:anywhere]">{data.name}</h1>
            {dirty && <span className="text-xs font-semibold px-2.5 py-0.5 rounded-full bg-amber-400/15 text-amber-200">Unsaved changes</span>}
            {decoded.ok && flow.length > 0 && (ready
              ? <span className="inline-flex items-center gap-1.5 text-[13px] text-emerald-300"><Check size={15} strokeWidth={2.5} /> Every step is ready to run</span>
              : <span className="text-[13px] text-amber-300">{problems.length === 1 ? '1 step needs attention' : `${problems.length} steps need attention`}</span>)}
          </div>
          <div className="flex flex-wrap items-center gap-2.5">
            <button
              type="button"
              onClick={() => setShowRun(true)}
              disabled={dirty || !decoded.ok || !ready}
              title={dirty ? 'Save your changes first' : !ready ? 'Fix the steps marked in the flow first' : undefined}
              className="inline-flex items-center gap-2 h-10 px-4 rounded-[10px] border border-[var(--ao-brand)]/55 text-[var(--ao-brand-light)] text-sm font-semibold hover:bg-[var(--ao-brand)]/10 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              <Play size={14} fill="currentColor" /> Test run
            </button>
            {canEdit && decoded.ok && <Button onClick={save} disabled={!dirty || !ready || saving}>{saving ? 'Saving…' : 'Save'}</Button>}
          </div>
        </div>
        <nav aria-label="Recipe sections" className="flex flex-wrap gap-x-1 mt-4">
          <span className="h-11 px-3.5 inline-flex items-center text-sm font-medium border-b-2 border-[var(--ao-brand)] -mb-px">Design</span>
          <Link to={`/dishshelf/${encodeURIComponent(data.name)}`} className="h-11 px-3.5 inline-flex items-center text-sm font-medium border-b-2 border-transparent -mb-px text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">Runs</Link>
        </nav>
      </div>
      {error && <div className="px-4 sm:px-8 pt-4"><ErrorBanner message={error} /></div>}

      {!decoded.ok ? (
        <div className="px-4 sm:px-8 py-8 max-w-3xl space-y-4">
          <div className="p-4 rounded-xl border border-amber-400/40 bg-amber-400/10 text-sm">
            <div className="font-semibold text-amber-200">The builder cannot show this recipe yet</div>
            <p className="mt-1 text-amber-100/90">{decoded.reason} It is left exactly as it is. Change it with the CLI or the API, or build a new recipe here.</p>
          </div>
          <ol className="rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] divide-y divide-[var(--ao-border)]">
            {(data.steps ?? []).map((s) => (
              <li key={s.name} className="px-4 py-3 text-sm flex flex-wrap gap-x-4 gap-y-1">
                <span className="font-semibold">{s.name}</span>
                <span className="text-[var(--ao-text-muted)]">{s.kind}</span>
                <span className="text-xs text-[var(--ao-text-muted)]">{(s.depends_on ?? []).length ? `after ${(s.depends_on ?? []).join(', ')}` : 'first'}</span>
              </li>
            ))}
          </ol>
        </div>
      ) : (
        <div className="flex flex-wrap min-h-[640px]">
          <aside aria-label="Add a step" className="flex-[0_0_248px] max-w-full box-border p-4 border-r border-[var(--ao-border)] flex flex-col gap-4">
            <div>
              <div className="text-[15px] font-semibold">Add a step</div>
              <div className="text-[12.5px] text-[var(--ao-text-muted)] mt-1 leading-snug">Click one to add it after the selected step. Or use the + between steps.</div>
            </div>
            {PALETTE.map((g) => (
              <div key={g.title} className="flex flex-col gap-1">
                <div className="text-[11.5px] font-semibold tracking-wider uppercase text-[var(--ao-text-muted)] px-1.5 pb-1">{g.title}</div>
                {g.items.map((m) => {
                  const Icon = m.icon;
                  return (
                    <button key={m.type} type="button" disabled={!canEdit} onClick={() => m.type !== 'carry' && m.type !== 'other' && addFromPalette(m.type)}
                      className="flex items-center gap-3 w-full p-2 rounded-[10px] text-left border border-transparent hover:bg-[var(--ao-surface-hover)] disabled:opacity-50">
                      <span className="w-8 h-8 rounded-full grid place-items-center shrink-0" style={{ background: m.bg, color: m.color }}><Icon size={16} /></span>
                      <span className="min-w-0"><span className="block text-[13.5px] font-semibold">{m.label}</span><span className="block text-xs text-[var(--ao-text-muted)]">{m.hint}</span></span>
                    </button>
                  );
                })}
              </div>
            ))}
          </aside>
          <section aria-label="Flow" className="flex-[999_1_460px] min-w-0 px-6 py-7 flex flex-col items-center"
            style={{ backgroundImage: 'radial-gradient(#26324a 1px, transparent 1px)', backgroundSize: '22px 22px' }}>
            <FlowCanvas flow={flow} selectedId={selected} problems={problemMap} onSelect={setSelected} onAdd={canEdit ? add : () => undefined} />
          </section>
          <aside aria-label="Step settings" className="flex-[0_0_340px] max-w-full box-border p-5 border-l border-[var(--ao-border)] bg-[var(--ao-surface)]">
            {sel ? (
              <StepInspector key={sel.id} node={sel} agents={(agentList.data ?? []).map((a) => ({ name: a.name, status: a.status }))} notifyTools={notifyTools} recipes={(recipeList.data ?? []).map((r) => ({ name: r.name, description: r.description, steps: (r.steps ?? []).length }))} self={data.name} onChange={update} onDelete={remove} onLanes={(id, n) => change(resizeLanes(flow, id, n))} onApprovalPaths={(id, on) => change(setApprovalPaths(flow, id, on))} />
            ) : (
              <div className="text-sm text-[var(--ao-text-muted)]">
                <div className="text-base font-semibold text-[var(--ao-text)] mb-1">{flow.length === 0 ? 'Start here' : 'Select a step'}</div>
                {flow.length === 0 ? 'Add the first step from the left, or start from a template.' : 'Click a step in the flow to change it, or add one from the left.'}
                {flow.length === 0 && <Link to="/recipes/new" className="block mt-3 font-semibold text-[var(--ao-brand-light)] hover:underline">See templates</Link>}
                {problems.length > 0 && flow.length > 0 && (
                  <ul className="mt-4 space-y-2 list-none p-0">
                    {problems.map((p) => (<li key={p.id + p.message}><button type="button" onClick={() => p.id && setSelected(p.id)} className="text-left text-[13px] text-amber-300 hover:underline">{p.message}</button></li>))}
                  </ul>
                )}
              </div>
            )}
          </aside>
        </div>
      )}

      <RunRecipeModal recipe={showRun ? data.name : null} stepCount={flow.length} onClose={() => setShowRun(false)} onStarted={(r) => navigate(`/dishshelf/${encodeURIComponent(r)}`)} />
    </div>
  );
}

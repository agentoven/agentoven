import { useEffect, useRef, useState } from 'react';
import { Flag, Play, Plus } from 'lucide-react';
import { hasPaths, isApproval, isParallel, realNodes, titleOf, waitWords, wordsFromExpression, type FlowNode, type StepType, type WireStep, type Where } from '../../lib/recipeFlow';
import { PALETTE, metaOfStep } from './stepMeta';

const LINE = 'bg-slate-600';

function Connector({ h = 'h-3.5' }: { h?: string }) {
  return <div aria-hidden="true" className={`w-0.5 ${h} ${LINE}`} />;
}

/** The small "+" between steps. It opens a short list of what can be added there. */
function AddButton({ where, onAdd, label = 'Add a step here' }: { where: Where; onAdd: (w: Where, t: StepType) => void; label?: string }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false); };
    const key = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', down);
    document.addEventListener('keydown', key);
    return () => { document.removeEventListener('mousedown', down); document.removeEventListener('keydown', key); };
  }, [open]);
  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        aria-label={label}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        className="w-6 h-6 rounded-full grid place-items-center bg-[var(--ao-bg)] border border-slate-600 text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:border-[var(--ao-brand-light)]"
      >
        <Plus size={12} strokeWidth={3} />
      </button>
      {open && (
        <div role="menu" className="absolute z-30 left-1/2 -translate-x-1/2 top-8 w-60 p-1.5 rounded-xl border border-[var(--ao-border)] bg-[var(--ao-surface)] shadow-2xl">
          {PALETTE.flatMap((g) => g.items).map((m) => {
            const Icon = m.icon;
            return (
              <button
                key={m.type}
                type="button"
                role="menuitem"
                onClick={() => { setOpen(false); if (m.type !== 'carry' && m.type !== 'other') onAdd(where, m.type); }}
                className="w-full flex items-center gap-3 px-2 py-2 rounded-lg text-left hover:bg-[var(--ao-surface-hover)]"
              >
                <span className="w-8 h-8 rounded-full grid place-items-center shrink-0" style={{ background: m.bg, color: m.color }}><Icon size={16} /></span>
                <span className="min-w-0"><span className="block text-sm font-semibold">{m.label}</span><span className="block text-xs text-[var(--ao-text-muted)]">{m.hint}</span></span>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function subtitle(step: WireStep): string {
  switch (step.kind) {
    case 'agent': case 'evaluator': return step.agent_ref ? step.agent_ref : 'Choose an agent';
    case 'rag': return 'Searches your knowledge';
    case 'sub_recipe': return step.recipe_ref ? `Runs ${step.recipe_ref}` : 'Choose a recipe';
    case 'notify': {
      const to = [...(((step.config?.channels as string[] | undefined) ?? [])), ...((step.notify_tools as string[] | undefined) ?? [])];
      return to.length ? `Tells ${to.join(', ')}` : 'Choose where to send';
    }
    case 'wait': {
      if (step.config?.duration) return `Waits ${waitWords(step.config.duration)}`;
      if (step.config?.until) { const at = new Date(String(step.config.until)); return Number.isNaN(at.getTime()) ? 'Choose a time' : `Waits until ${at.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' })}`; }
      return 'Choose how long';
    }
    case 'human_gate': {
      const mins = Number(step.config?.max_wait_minutes ?? 0);
      const wait = `${mins > 0 ? `within ${mins >= 1440 && mins % 1440 === 0 ? `${mins / 1440} day${mins === 1440 ? '' : 's'}` : mins >= 60 && mins % 60 === 0 ? `${mins / 60} hours` : `${mins} minutes`}` : 'no time limit'}`;
      return step.config?.on_reject === 'branch' ? `Approved or rejected, ${wait}` : wait;
    }
    case 'condition': {
      const expr = String(step.config?.expression ?? '');
      if (!expr.trim()) return 'Choose what to test';
      const words = wordsFromExpression(expr);
      return words ? `The answer mentions ${words.map((w) => `"${w}"`).join(' or ')}` : expr;
    }
    default: return '';
  }
}

interface Props {
  flow: FlowNode[];
  selectedId: string | null;
  problems: Map<string, string>;
  onSelect: (id: string) => void;
  onAdd: (where: Where, type: StepType) => void;
}

/** The recipe as a top-to-bottom flow. A decision shows its Yes and No paths side by side, an approval its Approved and Rejected paths. */
export default function FlowCanvas({ flow, selectedId, problems, onSelect, onAdd }: Props) {
  const numbers = new Map(realNodes(flow).map((n, i) => [n.id, i + 1]));

  const card = (node: FlowNode) => {
    const meta = metaOfStep(node.step);
    const Icon = meta.icon;
    const selected = node.id === selectedId;
    const problem = problems.get(node.id);
    return (
      <button
        type="button"
        onClick={() => onSelect(node.id)}
        aria-pressed={selected}
        className={`w-full text-left p-3.5 rounded-2xl bg-[var(--ao-surface)] flex items-center gap-3.5 transition-shadow ${
          selected ? 'border-2 border-[var(--ao-brand)] shadow-[0_0_0_4px_rgba(230,81,0,0.14)]' : 'border border-[var(--ao-border)] hover:border-slate-500'
        }`}
      >
        <span className="w-9 h-9 rounded-full grid place-items-center shrink-0" style={{ background: meta.bg, color: meta.color }}><Icon size={18} /></span>
        <span className="min-w-0 flex-1">
          <span className="block text-xs text-[var(--ao-text-muted)]">Step {numbers.get(node.id)}, {meta.label.toLowerCase()}</span>
          <span className="block text-[15px] font-semibold mt-0.5 truncate">{titleOf(node.step)}</span>
          <span className={`block text-[12.5px] mt-0.5 truncate ${problem ? 'text-amber-300' : 'text-[var(--ao-text-muted)]'}`}>{problem ?? subtitle(node.step)}</span>
        </span>
      </button>
    );
  };

  const path = (nodes: FlowNode[], parent: string | null, p: 'yes' | 'no' | number | null): React.ReactNode => (
    <>
      {nodes.map((node, i) => (
        <div key={node.id} className="w-full flex flex-col items-center">
          {i > 0 && (<><Connector /><AddButton where={{ parent, path: p, index: i }} onAdd={onAdd} /><Connector /></>)}
          {renderNode(node)}
        </div>
      ))}
      {nodes.length > 0 && (<><Connector /><AddButton where={{ parent, path: p, index: nodes.length }} onAdd={onAdd} label="Add a step at the end of this path" /></>)}
    </>
  );

  const renderParallel = (node: FlowNode): React.ReactNode => {
    const lanes = node.lanes ?? [];
    const meta = metaOfStep(node.step);
    const Icon = meta.icon;
    const selected = node.id === selectedId;
    const problem = problems.get(node.id);
    const wide = lanes.length > 2;
    return (
      <div className={`flex flex-col items-center ${wide ? 'w-[calc(100%+160px)] -mx-20 max-w-[860px]' : 'w-full'}`}>
        <button type="button" onClick={() => onSelect(node.id)} aria-pressed={selected}
          className={`inline-flex items-center gap-2.5 px-4 py-2 rounded-full bg-[var(--ao-surface)] text-[13.5px] ${selected ? 'border-2 border-[var(--ao-brand)] shadow-[0_0_0_4px_rgba(230,81,0,0.14)]' : 'border border-[var(--ao-border)] hover:border-slate-500'}`}>
          <span className="w-6 h-6 rounded-full grid place-items-center" style={{ background: meta.bg, color: meta.color }}><Icon size={13} /></span>
          <span className="font-semibold">At the same time</span>
          <span className={problem ? 'text-amber-300' : 'text-[var(--ao-text-muted)]'}>{problem ?? `${lanes.length} paths`}</span>
        </button>
        <Connector h="h-4" />
        <div className="w-full grid gap-4 relative" style={{ gridTemplateColumns: `repeat(${lanes.length}, minmax(0, 1fr))` }}>
          <div aria-hidden="true" className={`absolute top-0 h-0.5 ${LINE}`} style={{ left: `${50 / lanes.length}%`, right: `${50 / lanes.length}%` }} />
          {lanes.map((list, li) => (
            <div key={li} className="flex flex-col items-center pt-0.5 min-w-0">
              <Connector />
              <span className="text-xs font-semibold px-3 py-0.5 rounded-full mb-2.5 bg-orange-400/15 text-orange-300">Path {li + 1}</span>
              {list.length === 0 ? (
                <div className="w-full h-[84px] grid place-items-center rounded-2xl border-[1.5px] border-dashed border-slate-600 text-[13px] text-[var(--ao-text-muted)]">
                  <div className="flex flex-col items-center gap-1.5">
                    <AddButton where={{ parent: node.id, path: li, index: 0 }} onAdd={onAdd} label={`Add a step to path ${li + 1}`} />
                    <span>Add a step</span>
                  </div>
                </div>
              ) : path(list, node.id, li)}
            </div>
          ))}
        </div>
        <div className="w-full grid gap-4 relative mt-4" style={{ gridTemplateColumns: `repeat(${lanes.length}, minmax(0, 1fr))` }}>
          <div aria-hidden="true" className={`absolute bottom-0 h-0.5 ${LINE}`} style={{ left: `${50 / lanes.length}%`, right: `${50 / lanes.length}%` }} />
          {lanes.map((_, li) => <div key={li} className="mx-auto"><Connector /></div>)}
        </div>
      </div>
    );
  };

  const renderNode = (node: FlowNode): React.ReactNode => {
    if (isParallel(node)) return renderParallel(node);
    if (!hasPaths(node)) return card(node);
    const approval = isApproval(node);
    return (
      <div className="w-full flex flex-col items-center">
        {card(node)}
        <Connector h="h-4" />
        <div className="w-full grid grid-cols-2 gap-4 relative">
          <div aria-hidden="true" className={`absolute top-0 left-1/4 right-1/4 h-0.5 ${LINE}`} />
          {(['yes', 'no'] as const).map((side) => {
            const list = (side === 'yes' ? node.yes : node.no) ?? [];
            return (
              <div key={side} className="flex flex-col items-center pt-0.5 min-w-0">
                <Connector />
                <span className={`text-xs font-semibold px-3 py-0.5 rounded-full mb-2.5 ${side === 'yes' ? 'bg-teal-400/15 text-teal-300' : approval ? 'bg-rose-400/15 text-rose-300' : 'bg-slate-400/15 text-slate-300'}`}>{side === 'yes' ? (approval ? 'Approved' : 'Yes') : approval ? 'Rejected' : 'No'}</span>
                {list.length === 0 ? (
                  <div className="w-full flex flex-col items-center">
                    <div className="w-full h-[84px] grid place-items-center rounded-2xl border-[1.5px] border-dashed border-slate-600 text-[13px] text-[var(--ao-text-muted)]">
                      <div className="flex flex-col items-center gap-1.5">
                        <AddButton where={{ parent: node.id, path: side, index: 0 }} onAdd={onAdd} label={`Add a step to the ${approval ? (side === 'yes' ? 'approved' : 'rejected') : side} path`} />
                        <span>{side === 'yes' ? 'Add a step' : approval ? 'Add a step, or end here' : 'Add a step, or carry on'}</span>
                      </div>
                    </div>
                  </div>
                ) : path(list, node.id, side)}
              </div>
            );
          })}
        </div>
        <div className="w-full grid grid-cols-2 gap-4 relative mt-4">
          <div aria-hidden="true" className={`absolute bottom-0 left-1/4 right-1/4 h-0.5 ${LINE}`} />
          <div className="mx-auto"><Connector /></div>
          <div className="mx-auto"><Connector /></div>
        </div>
      </div>
    );
  };

  return (
    <div className="w-full max-w-[520px] flex flex-col items-center">
      <div className="flex items-center gap-2.5 px-4 py-2.5 rounded-full bg-[var(--ao-surface)] border border-[var(--ao-border)] text-[13.5px]">
        <Play size={14} className="text-emerald-400" fill="currentColor" />
        <span><span className="font-semibold">Starts with</span> <span className="text-[var(--ao-text-muted)]">whatever you give it when it runs</span></span>
      </div>
      {flow.length === 0 ? (
        <>
          <Connector h="h-4" />
          <div className="w-full p-8 rounded-2xl border-[1.5px] border-dashed border-slate-600 flex flex-col items-center gap-3 text-center">
            <AddButton where={{ parent: null, path: null, index: 0 }} onAdd={onAdd} label="Add the first step" />
            <div className="text-sm font-semibold">Add the first step</div>
            <div className="text-[13px] text-[var(--ao-text-muted)]">Pick what happens first. You can add more after it.</div>
          </div>
        </>
      ) : (
        <>
          <Connector /><AddButton where={{ parent: null, path: null, index: 0 }} onAdd={onAdd} /><Connector />
          {path(flow, null, null)}
          <Connector />
          <div className="flex items-center gap-2.5 px-4 py-2.5 rounded-full bg-[var(--ao-surface)] border border-[var(--ao-border)] text-[13.5px]">
            <Flag size={14} className="text-[var(--ao-brand-light)]" />
            <span><span className="font-semibold">Finishes with</span> <span className="text-[var(--ao-text-muted)]">the output of the last step</span></span>
          </div>
        </>
      )}
    </div>
  );
}


import { useState } from 'react';
import { ShieldCheck, Trash2 } from 'lucide-react';
import { MAX_LANES, isParallel, mentionsExpression, titleOf, wordsFromExpression, type FlowNode, type WireStep } from '../../lib/recipeFlow';
import { metaOfStep } from './stepMeta';

const field = 'w-full h-10 px-3 rounded-[10px] bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';
const label = 'flex flex-col gap-1.5 text-[13px] font-medium text-slate-300';

const WAITS: { minutes: number; text: string }[] = [
  { minutes: 60, text: '1 hour' }, { minutes: 240, text: '4 hours' }, { minutes: 1440, text: '24 hours' }, { minutes: 4320, text: '3 days' }, { minutes: 0, text: 'No limit' },
];

export interface AgentOption { name: string; status: string }

interface Props {
  node: FlowNode;
  agents: AgentOption[];
  /** Names of tools that can send a message, for "Tell them through". */
  notifyTools: string[];
  onChange: (id: string, patch: Partial<WireStep>, config?: Record<string, unknown>) => void;
  onDelete: (id: string) => void;
  /** Sets how many paths a "do at the same time" group has. */
  onLanes?: (id: string, count: number) => void;
}

function Select({ value, onChange, children, label: aria }: { value: string; onChange: (v: string) => void; children: React.ReactNode; label: string }) {
  return (
    <select aria-label={aria} value={value} onChange={(e) => onChange(e.target.value)} className={field}>{children}</select>
  );
}

/** Settings for the selected step, in plain words. */
export default function StepInspector({ node, agents, notifyTools, onChange, onDelete, onLanes }: Props) {
  const s = node.step;
  const meta = metaOfStep(s);
  const Icon = meta.icon;
  const cfg = s.config ?? {};
  const set = (patch: Partial<WireStep>, config?: Record<string, unknown>) => onChange(node.id, patch, config);

  const expr = String(cfg.expression ?? '');
  const words = wordsFromExpression(expr);
  const [custom, setCustom] = useState(expr.trim() !== '' && words === null);
  const [wordsText, setWordsText] = useState((words ?? []).join(', '));

  const minutes = Number(cfg.max_wait_minutes ?? 0);

  return (
    <aside aria-label="Step settings" className="flex flex-col gap-4">
      <div className="flex items-center gap-3">
        <span className="w-9 h-9 rounded-full grid place-items-center" style={{ background: meta.bg, color: meta.color }}><Icon size={18} /></span>
        <div><div className="text-xs text-[var(--ao-text-muted)]">Selected step</div><div className="text-base font-semibold">{meta.label}</div></div>
      </div>

      {isParallel(node) ? (
        <div className="flex flex-col gap-3">
          <p className="m-0 text-[13px] text-slate-300 leading-relaxed">Every path starts as soon as the step before it is done. The next step waits until all of them have finished.</p>
          <div className="flex items-center gap-3">
            <button type="button" aria-label="Fewer paths" disabled={(node.lanes?.length ?? 2) <= 2 || !onLanes}
              onClick={() => {
                const last = node.lanes?.[node.lanes.length - 1] ?? [];
                if (last.length > 0 && !window.confirm('The last path has steps in it. Remove it and its steps?')) return;
                onLanes?.(node.id, (node.lanes?.length ?? 2) - 1);
              }}
              className="w-9 h-9 rounded-[10px] border border-[var(--ao-border)] text-lg disabled:opacity-40">−</button>
            <span className="text-sm font-semibold min-w-[72px] text-center">{node.lanes?.length ?? 2} paths</span>
            <button type="button" aria-label="More paths" disabled={(node.lanes?.length ?? 2) >= MAX_LANES || !onLanes}
              onClick={() => onLanes?.(node.id, (node.lanes?.length ?? 2) + 1)}
              className="w-9 h-9 rounded-[10px] border border-[var(--ao-border)] text-lg disabled:opacity-40">+</button>
          </div>
        </div>
      ) : (
      <label className={label}>Name
        <input className={field} value={titleOf(s)} onChange={(e) => set({}, { title: e.target.value })} />
      </label>
      )}

      {(s.kind === 'agent' || s.kind === 'evaluator') && (
        <>
          <label className={label}>{s.kind === 'evaluator' ? 'Agent that checks' : 'Agent'}
            <Select label="Agent" value={s.agent_ref ?? ''} onChange={(v) => set({ agent_ref: v, agent: v })}>
              <option value="">Choose an agent…</option>
              {agents.map((a) => <option key={a.name} value={a.name}>{a.name}{a.status !== 'ready' ? ` (${a.status})` : ''}</option>)}
            </Select>
            <span className="text-xs font-normal text-[var(--ao-text-muted)]">It receives what the recipe started with and what earlier steps produced.</span>
          </label>
          <div className="grid grid-cols-2 gap-3">
            <label className={label}>Tries
              <Select label="Tries" value={String(s.max_retries ?? 0)} onChange={(v) => set({ max_retries: Number(v) || undefined })}>
                {[0, 1, 2, 3].map((n) => <option key={n} value={n}>{n === 0 ? 'Once' : `${n + 1} times`}</option>)}
              </Select>
            </label>
            <label className={label}>Time limit
              <Select label="Time limit" value={String(s.timeout_secs ?? 0)} onChange={(v) => set({ timeout_secs: Number(v) || undefined })}>
                <option value="0">None</option><option value="30">30 seconds</option><option value="120">2 minutes</option><option value="600">10 minutes</option>
              </Select>
            </label>
          </div>
        </>
      )}

      {s.kind === 'rag' && (
        <label className={label}>How many results
          <input className={field} type="number" min={1} max={20} value={Number(cfg.top_k ?? 3)} onChange={(e) => set({}, { top_k: Math.max(1, Math.min(20, Number(e.target.value) || 3)) })} />
          <span className="text-xs font-normal text-[var(--ao-text-muted)]">Searches for what the recipe started with, or the step before it.</span>
        </label>
      )}

      {s.kind === 'human_gate' && (
        <>
          <label className={label}>Only people from this email domain
            <input className={field} placeholder="company.com" value={String(s.approver_domain ?? '')} onChange={(e) => set({ approver_domain: e.target.value || undefined })} />
          </label>
          <label className={label}>Wait up to
            <Select label="Wait up to" value={String(minutes)} onChange={(v) => set({}, { max_wait_minutes: Number(v) })}>
              {WAITS.map((w) => <option key={w.minutes} value={w.minutes}>{w.text}</option>)}
              {!WAITS.some((w) => w.minutes === minutes) && <option value={minutes}>{minutes} minutes</option>}
            </Select>
            <span className="text-xs font-normal text-[var(--ao-text-muted)]">If nobody answers in time, the run stops here.</span>
          </label>
          <label className={label}>Tell them through
            <Select label="Tell them through" value={(s.notify_tools as string[] | undefined)?.[0] ?? ''} onChange={(v) => set({ notify_tools: v ? [v] : undefined })}>
              <option value="">No message</option>
              {notifyTools.map((t) => <option key={t} value={t}>{t}</option>)}
            </Select>
          </label>
          <div className="flex gap-2.5 p-3 rounded-xl bg-emerald-400/10 border border-emerald-400/30 text-[12.5px] leading-relaxed text-emerald-200">
            <ShieldCheck size={16} className="text-emerald-400 mt-0.5 shrink-0" />
            <span>Who approved and when is written to the audit trail.</span>
          </div>
        </>
      )}

      {s.kind === 'condition' && (
        <>
          <label className={label}>What to test
            <Select label="What to test" value={custom ? 'custom' : 'mentions'} onChange={(v) => {
              setCustom(v === 'custom');
              if (v === 'mentions') { set({}, { expression: mentionsExpression(wordsText.split(',')) }); }
            }}>
              <option value="mentions">The answer mentions certain words</option>
              <option value="custom">My own condition</option>
            </Select>
          </label>
          {custom ? (
            <label className={label}>Condition
              <textarea className={`${field} h-24 py-2 font-mono text-[13px]`} value={expr} placeholder='amount > 200'
                onChange={(e) => set({}, { expression: e.target.value })} />
              <span className="text-xs font-normal text-[var(--ao-text-muted)]">Written like <code>category == "billing"</code>. The words of the step before are in <code>text</code>.</span>
            </label>
          ) : (
            <label className={label}>Words
              <input className={field} placeholder="refund, chargeback" value={wordsText}
                onChange={(e) => { setWordsText(e.target.value); set({}, { expression: mentionsExpression(e.target.value.split(',')) }); }} />
              <span className="text-xs font-normal text-[var(--ao-text-muted)]">Separate words with commas. Capital letters do not matter.</span>
            </label>
          )}
          <p className="text-xs text-[var(--ao-text-muted)] m-0">When it holds, the Yes path runs. Otherwise the No path runs.</p>
        </>
      )}

      <div className="flex-1" />
      <button type="button" onClick={() => onDelete(node.id)} className="self-start inline-flex items-center gap-2 py-1.5 text-[13.5px] font-semibold text-red-400 hover:text-red-300">
        <Trash2 size={14} /> Delete this step
      </button>
    </aside>
  );
}

import { useState } from 'react';
import { ThumbsUp, ThumbsDown, Trash2, X, Plus } from 'lucide-react';
import { traces, type Trace, type TraceScore } from '../../api';
import { Card } from '../UI';

/** Tags on a trace: added and removed in place. */
export function TagEditor({ trace, onChange }: { trace: Trace; onChange: (t: Trace) => void }) {
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const tags = trace.tags ?? [];
  const save = async (next: string[]) => {
    setBusy(true);
    try { onChange({ ...trace, ...(await traces.setTags(trace.id, next)), spans: trace.spans, scores: trace.scores }); } finally { setBusy(false); }
  };
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {tags.map((t) => (
        <span key={t} className="inline-flex items-center gap-1 text-xs px-2 py-0.5 rounded-full bg-[var(--ao-surface-hover)]">
          {t}
          <button disabled={busy} onClick={() => save(tags.filter((x) => x !== t))} title={`Remove ${t}`} className="text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]"><X size={10} /></button>
        </span>
      ))}
      <form onSubmit={(e) => { e.preventDefault(); const t = draft.trim(); if (t && !tags.includes(t)) save([...tags, t]); setDraft(''); }} className="inline-flex items-center">
        <input value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="add tag" maxLength={64}
          className="w-24 text-xs px-2 py-0.5 rounded-full border border-dashed border-[var(--ao-border)] bg-transparent focus:outline-none focus:border-[var(--ao-brand)]" />
        {draft && <button type="submit" title="Add tag" className="ml-1 text-[var(--ao-brand)]"><Plus size={12} /></button>}
      </form>
    </div>
  );
}

/** Feedback on a trace: a thumbs up or down with an optional comment, and what has already been said. */
export function FeedbackPanel({ trace, onChange }: { trace: Trace; onChange: (t: Trace) => void }) {
  const [comment, setComment] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const scores = trace.scores ?? [];

  const give = async (value: number, label: string) => {
    setBusy(true); setErr(null);
    try {
      const sc = await traces.addScore(trace.id, { name: 'quality', value, label, comment: comment.trim() || undefined });
      onChange({ ...trace, scores: [...scores, sc] });
      setComment('');
    } catch (e) { setErr((e as Error).message); } finally { setBusy(false); }
  };
  const remove = async (sc: TraceScore) => {
    await traces.deleteScore(trace.id, sc.id);
    onChange({ ...trace, scores: scores.filter((s) => s.id !== sc.id) });
  };

  return (
    <Card>
      <div className="text-[0.7rem] text-[var(--ao-text-muted)] uppercase tracking-wider mb-2">Feedback</div>
      <div className="flex items-center gap-2 mb-2">
        <button disabled={busy} onClick={() => give(1, 'good')} className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-emerald-500/40 text-emerald-400 hover:bg-emerald-500/10 disabled:opacity-50">
          <ThumbsUp size={14} /> Good
        </button>
        <button disabled={busy} onClick={() => give(0, 'bad')} className="inline-flex items-center gap-1.5 px-3 py-1.5 text-sm rounded-md border border-red-500/40 text-red-400 hover:bg-red-500/10 disabled:opacity-50">
          <ThumbsDown size={14} /> Bad
        </button>
        <input value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Add a comment (optional)"
          className="flex-1 text-sm px-2.5 py-1.5 rounded-md border border-[var(--ao-border)] bg-[var(--ao-bg)]" />
      </div>
      {err && <div className="text-xs text-red-400 mb-2">{err}</div>}
      {scores.length === 0 ? (
        <div className="text-xs text-[var(--ao-text-muted)]">No feedback yet.</div>
      ) : (
        <ul className="flex flex-col gap-1.5">
          {scores.map((s) => (
            <li key={s.id} className="flex items-start gap-2 text-sm border-t border-[var(--ao-border)] pt-1.5">
              <span className={`mt-0.5 ${s.value >= 0.5 ? 'text-emerald-400' : 'text-red-400'}`}>{s.value >= 0.5 ? <ThumbsUp size={13} /> : <ThumbsDown size={13} />}</span>
              <div className="flex-1 min-w-0">
                <div className="text-xs text-[var(--ao-text-muted)]">
                  {s.name} · {s.label || s.value} · {s.source}{s.created_by ? ` · ${s.created_by}` : ''} · {new Date(s.created_at).toLocaleString()}
                </div>
                {s.comment && <div className="whitespace-pre-wrap break-words">{s.comment}</div>}
              </div>
              <button onClick={() => remove(s)} title="Remove" className="text-[var(--ao-text-muted)] hover:text-red-400"><Trash2 size={13} /></button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

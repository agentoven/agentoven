import { useState } from 'react';
import { recipes } from '../../api';
import { Button, Modal } from '../UI';

/** Starts a run. The input is plain words, or JSON when it parses as an object. */
export default function RunRecipeModal({ recipe, onClose, onStarted, stepCount }: { recipe: string | null; onClose: () => void; onStarted: (recipe: string) => void; stepCount?: number }) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const run = async () => {
    if (!recipe) return;
    setBusy(true); setErr('');
    let input: Record<string, unknown> = { text };
    try {
      const parsed = JSON.parse(text);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) input = parsed as Record<string, unknown>;
    } catch { /* plain words */ }
    try {
      await recipes.bake(recipe, input);
      setText('');
      onClose();
      onStarted(recipe);
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'The run could not start.');
    }
    setBusy(false);
  };

  return (
    <Modal title={`Run ${recipe ?? ''}`} open={!!recipe} onClose={onClose}>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1.5 text-[13px] font-medium text-slate-300">What should it start with?
          <textarea rows={4} value={text} onChange={(e) => setText(e.target.value)} placeholder="Type the request in plain words, or paste JSON."
            className="w-full px-3 py-2 rounded-[10px] bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)] resize-y" />
        </label>
        <p className="text-xs text-[var(--ao-text-muted)] m-0">{stepCount ? `${stepCount} step${stepCount === 1 ? '' : 's'} will run for real, using your agents. ` : 'The steps will run for real, using your agents. '}You can follow the run in DishShelf.</p>
        {err && <p className="text-sm text-red-400 m-0">{err}</p>}
        <div className="flex justify-end gap-2"><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={run} disabled={busy}>{busy ? 'Starting…' : 'Start the run'}</Button></div>
      </div>
    </Modal>
  );
}

import { useState } from 'react';
import { approvals } from '../../api';
import { Button } from '../UI';

/**
 * Approve or reject a step that is waiting for a person. A rejection can carry a reason, which is recorded
 * with who decided and is passed on to whatever step follows the Rejected path.
 */
export default function GateDecision({ runId, stepName, onDecided, onError }: {
  runId: string;
  stepName: string;
  onDecided: () => void;
  onError?: (message: string) => void;
}) {
  const [asking, setAsking] = useState(false);
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);

  const decide = async (approved: boolean) => {
    setBusy(true);
    try {
      await approvals.decide(runId, stepName, approved, approved ? undefined : reason.trim());
      setAsking(false);
      setReason('');
      onDecided();
    } catch (e) {
      onError?.(e instanceof Error ? e.message : 'The decision was not recorded.');
    }
    setBusy(false);
  };

  if (asking) {
    return (
      <div className="flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor={`reason-${runId}-${stepName}`}>Why is it rejected</label>
        <input id={`reason-${runId}-${stepName}`} autoFocus value={reason} onChange={(e) => setReason(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') void decide(false); if (e.key === 'Escape') setAsking(false); }}
          placeholder="Why? (optional, passed on to the next step)"
          className="h-9 w-72 max-w-full px-3 rounded-[10px] bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]" />
        <Button size="sm" variant="secondary" onClick={() => setAsking(false)} disabled={busy}>Cancel</Button>
        <Button size="sm" onClick={() => void decide(false)} disabled={busy}>{busy ? 'Sending…' : 'Reject'}</Button>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-2">
      <Button size="sm" variant="secondary" onClick={() => setAsking(true)} disabled={busy}>Reject</Button>
      <Button size="sm" onClick={() => void decide(true)} disabled={busy}>Approve</Button>
    </div>
  );
}

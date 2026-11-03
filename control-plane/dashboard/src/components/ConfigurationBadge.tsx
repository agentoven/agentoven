import { useState } from 'react';
import { Link } from 'react-router-dom';
import { CheckCircle2, ChevronDown, TriangleAlert } from 'lucide-react';
import type { ValidationReport } from '../api';
import { useAPI } from '../hooks';
import { ProblemList } from './ProblemList';

/**
 * "Configuration: valid" or "Configuration: N problems", from the server's read-only check of what an agent
 * or a recipe names (`/validate`). Open it for the list. It changes nothing and never blocks anything: an
 * agent saved before the checks existed keeps running, and this is where it is shown what to fix.
 *
 * Renders nothing while the check is loading and when it cannot be had (a server without the route, a role
 * that may not read it): a badge that guessed would be worse than none.
 */
export function ConfigurationBadge({ load, fixTo, fixLabel = 'Fix in Configuration' }: {
  load: () => Promise<ValidationReport>;
  fixTo?: string;
  fixLabel?: string;
}) {
  const { data } = useAPI(load);
  const [open, setOpen] = useState(false);
  if (!data) return null;
  const errors = data.problems?.length ?? 0;
  const warns = data.warnings?.length ?? 0;
  const bad = errors > 0;
  const tone = bad ? 'border-red-500/40 bg-red-500/10 text-red-300' : warns > 0 ? 'border-amber-500/30 bg-amber-500/10 text-amber-200' : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300';
  const text = bad
    ? `Configuration: ${errors} problem${errors === 1 ? '' : 's'}`
    : warns > 0 ? `Configuration: valid, ${warns} warning${warns === 1 ? '' : 's'}` : 'Configuration: valid';
  const expandable = errors + warns > 0;
  const Icon = bad || warns > 0 ? TriangleAlert : CheckCircle2;

  return (
    <>
      <button type="button" data-configuration={bad ? 'invalid' : 'valid'} aria-expanded={expandable ? open : undefined} disabled={!expandable}
        onClick={() => setOpen((o) => !o)}
        className={`inline-flex items-center gap-1.5 h-7 px-2.5 rounded-full border text-xs font-medium ${tone} ${expandable ? 'cursor-pointer hover:opacity-90' : 'cursor-default'}`}>
        <Icon size={13} /> {text} {expandable && <ChevronDown size={12} className={open ? 'rotate-180' : ''} />}
      </button>
      {open && expandable && (
        <div className="basis-full w-full max-w-4xl flex flex-col gap-3 py-2">
          <ProblemList problems={data.problems} warnings={data.warnings} />
          {fixTo && <Link to={fixTo} className="self-start text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">{fixLabel}</Link>}
        </div>
      )}
    </>
  );
}

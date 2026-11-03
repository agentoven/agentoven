import type { Problem } from '../api';

/**
 * What the server found wrong with a configuration, field by field: the path in the JSON the API takes, the
 * sentence, and the nearest values that would have been right. Errors refuse a save; warnings are advice and
 * are set apart in a calmer colour.
 */
export function ProblemList({ problems = [], warnings = [] }: { problems?: Problem[]; warnings?: Problem[] }) {
  const rows = [...problems.map((p) => ({ p, warn: false })), ...warnings.map((p) => ({ p, warn: true }))];
  if (rows.length === 0) return null;
  return (
    <ul aria-label="Configuration problems" className="m-0 p-0 list-none flex flex-col gap-2">
      {rows.map(({ p, warn }, i) => (
        <li key={`${p.path}-${i}`} data-severity={warn ? 'warning' : 'error'} className={`text-sm rounded-lg border px-3 py-2 ${warn ? 'border-amber-500/25 bg-amber-500/5 text-amber-200' : 'border-red-500/30 bg-red-500/10 text-red-300'}`}>
          <div className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
            <code className="font-mono text-xs px-1.5 py-0.5 rounded bg-black/20">{p.path}</code>
            <span className="[overflow-wrap:anywhere]">{p.message.startsWith(`${p.path}:`) ? p.message.slice(p.path.length + 1).trim() : p.message}</span>
          </div>
          {p.suggestions && p.suggestions.length > 0 && !/did you mean/.test(p.message) && (
            <div className="mt-1 text-xs">Did you mean {p.suggestions.map((s, j) => <span key={s}>{j > 0 && ', '}<code className="font-mono">{s}</code></span>)}?</div>
          )}
        </li>
      ))}
    </ul>
  );
}

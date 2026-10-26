import { splitWait, type WaitUnit, type WireStep } from '../../lib/recipeFlow';

const field = 'w-full h-10 px-3 rounded-[10px] bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';
const label = 'flex flex-col gap-1.5 text-[13px] font-medium text-slate-300';

const UNITS: { unit: WaitUnit; text: string }[] = [
  { unit: 's', text: 'seconds' }, { unit: 'm', text: 'minutes' }, { unit: 'h', text: 'hours' }, { unit: 'd', text: 'days' },
];

/** An ISO time as the "2026-11-02T09:30" a datetime-local box wants, in the browser's time zone. */
function toLocalInput(iso: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}`;
}

/** Settings of a step that waits: for a length of time, or until a time. */
export default function WaitStepSettings({ step, onConfig }: { step: WireStep; onConfig: (config: Record<string, unknown>) => void }) {
  const cfg = step.config ?? {};
  const until = String(cfg.until ?? '');
  const mode = until && !cfg.duration ? 'until' : 'for';
  const parts = splitWait(cfg.duration);
  const raw = String(cfg.duration ?? '');
  return (
    <>
      <label className={label}>Wait
        <select aria-label="Wait" className={field} value={mode}
          onChange={(e) => onConfig(e.target.value === 'until'
            ? { duration: undefined, until: until || new Date(Date.now() + 3600_000).toISOString().replace(/\.\d{3}Z$/, 'Z') }
            : { until: undefined, duration: raw || '5m' })}>
          <option value="for">For a length of time</option>
          <option value="until">Until a date and time</option>
        </select>
      </label>

      {mode === 'for' ? (
        <div className="grid grid-cols-[1fr_1fr] gap-3">
          <label className={label}>How long
            <input aria-label="How long" className={field} type="number" min={0} step="any" value={parts ? parts.value : ''} placeholder={parts ? undefined : raw}
              onChange={(e) => onConfig({ duration: `${e.target.value || 0}${parts?.unit ?? 'm'}`, until: undefined })} />
          </label>
          <label className={label}>Unit
            <select aria-label="Unit" className={field} value={parts?.unit ?? 'm'}
              onChange={(e) => onConfig({ duration: `${parts?.value ?? 5}${e.target.value}`, until: undefined })}>
              {UNITS.map((u) => <option key={u.unit} value={u.unit}>{u.text}</option>)}
            </select>
          </label>
        </div>
      ) : (
        <label className={label}>Until
          <input aria-label="Until" className={field} type="datetime-local" value={toLocalInput(until)}
            onChange={(e) => {
              const at = new Date(e.target.value);
              onConfig({ until: Number.isNaN(at.getTime()) ? '' : at.toISOString().replace(/\.\d{3}Z$/, 'Z'), duration: undefined });
            }} />
          <span className="text-xs font-normal text-[var(--ao-text-muted)]">In your time zone. If that time has already passed when the step starts, the recipe carries on at once.</span>
        </label>
      )}

      <p className="m-0 text-xs text-[var(--ao-text-muted)] leading-relaxed">
        The most a wait can last is 30 days. While it waits, the run page shows when it ends and has a button to end it early. A wait is held in the control plane&apos;s memory, so it does not survive a restart.
      </p>
    </>
  );
}

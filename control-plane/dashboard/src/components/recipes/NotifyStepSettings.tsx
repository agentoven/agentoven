import { useState } from 'react';
import { Radio } from 'lucide-react';
import { notificationChannels, type NotificationChannel } from '../../api';
import { NOTIFY_PLACEHOLDERS, type WireStep } from '../../lib/recipeFlow';

const field = 'w-full h-10 px-3 rounded-[10px] bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';
const label = 'flex flex-col gap-1.5 text-[13px] font-medium text-slate-300';
const NAME_RE = /^[a-z0-9][a-z0-9_-]{1,62}$/;

/** The address a listener connects to for a live-feed channel, from where the dashboard is served. */
const listenUrl = (name: string) => `${window.location.origin.replace(/^http/, 'ws')}/api/v1/channels/${encodeURIComponent(name)}/ws`;

/** Settings of a step that pushes a message out: what to say, and which channels and tools to send it to. */
export default function NotifyStepSettings({ step, channels, tools, onConfig, onPatch, onChannelsChanged }: {
  step: WireStep;
  channels: NotificationChannel[];
  /** Tools that can send a message. */
  tools: string[];
  onConfig: (config: Record<string, unknown>) => void;
  onPatch: (patch: Partial<WireStep>) => void;
  /** Called after a channel was made here, so the list can be read again. */
  onChannelsChanged?: () => void;
}) {
  const cfg = step.config ?? {};
  const chosen = (cfg.channels as string[] | undefined) ?? [];
  const chosenTools = (step.notify_tools as string[] | undefined) ?? [];
  const message = String(cfg.message ?? '');
  const [newName, setNewName] = useState('');
  const [problem, setProblem] = useState('');
  const [busy, setBusy] = useState(false);

  const toggle = (list: string[], name: string) => (list.includes(name) ? list.filter((x) => x !== name) : [...list, name]);
  const known = new Set(channels.map((c) => c.name));
  const missing = chosen.filter((c) => !known.has(c));

  const makeFeed = async () => {
    const name = newName.trim();
    if (!NAME_RE.test(name)) { setProblem("Use 2 to 63 characters: lowercase letters, numbers, '-' or '_'."); return; }
    setBusy(true); setProblem('');
    try {
      await notificationChannels.create({ name, kind: 'websocket' });
      onConfig({ channels: [...chosen, name] });
      setNewName('');
      onChannelsChanged?.();
    } catch (e) {
      setProblem(e instanceof Error ? e.message : 'The channel could not be created.');
    }
    setBusy(false);
  };

  return (
    <>
      <label className={label}>Message
        <textarea aria-label="Message" rows={3} className={`${field} !h-auto py-2 leading-snug resize-y`} value={message}
          onChange={(e) => onConfig({ message: e.target.value })} />
        <span className="text-xs font-normal text-[var(--ao-text-muted)] leading-relaxed">
          Words in double braces are filled in when the step runs:{' '}
          {NOTIFY_PLACEHOLDERS.map((p, i) => (
            <span key={p.name}>{i > 0 && ', '}<code title={p.means}>{`{{${p.name}${p.name.endsWith('.') ? '…' : ''}}}`}</code></span>
          ))}.
        </span>
      </label>

      <label className={label}>Subject (optional)
        <input aria-label="Subject" className={field} value={String(cfg.subject ?? '')} placeholder="Approval needed"
          onChange={(e) => onConfig({ subject: e.target.value || undefined })} />
      </label>

      <fieldset className="border-0 m-0 p-0 min-w-0 flex flex-col gap-2">
        <legend className="p-0 mb-1 text-[13px] font-medium text-slate-300">Send to channels</legend>
        {channels.length === 0 && <p className="m-0 text-xs text-[var(--ao-text-muted)]">No channels yet. Make a live feed below, or add one with the channels API.</p>}
        {channels.map((c) => {
          const on = chosen.includes(c.name);
          return (
            <label key={c.name} className={`flex items-start gap-2.5 p-2.5 rounded-[10px] border cursor-pointer ${on ? 'border-[var(--ao-brand)] bg-[var(--ao-brand)]/10' : 'border-[var(--ao-border)] hover:border-slate-500'} ${c.active ? '' : 'opacity-50'}`}>
              <input type="checkbox" className="mt-0.5 accent-[var(--ao-brand)]" checked={on} disabled={!c.active && !on} onChange={() => onConfig({ channels: toggle(chosen, c.name) })} />
              <span className="min-w-0">
                <span className="block text-[13.5px] font-semibold">{c.name} <span className="ml-1 text-[11px] font-medium text-[var(--ao-text-muted)]">{c.kind === 'websocket' ? 'live feed' : c.kind}{c.active ? '' : ' · inactive'}</span></span>
                {c.kind === 'websocket' && on && <span className="block mt-1 text-[11.5px] font-mono text-[var(--ao-text-muted)] [overflow-wrap:anywhere]">{listenUrl(c.name)}</span>}
              </span>
            </label>
          );
        })}
        {missing.map((m) => <p key={m} className="m-0 text-xs text-amber-300">The channel &ldquo;{m}&rdquo; no longer exists.</p>)}
        <div className="flex gap-2 items-start">
          <input aria-label="Name for a new live feed" className={`${field} !h-9 flex-1`} placeholder="new-live-feed" value={newName} onChange={(e) => setNewName(e.target.value)} />
          <button type="button" onClick={makeFeed} disabled={busy || !newName.trim()}
            className="inline-flex items-center gap-1.5 h-9 px-3 rounded-lg text-[13px] font-medium border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)] disabled:opacity-50 whitespace-nowrap">
            <Radio size={13} /> Make a live feed
          </button>
        </div>
        {problem && <p className="m-0 text-xs text-amber-300">{problem}</p>}
        <p className="m-0 text-xs text-[var(--ao-text-muted)] leading-relaxed">
          A live feed pushes each message to every WebSocket connected to its address. Nothing is kept: someone who connects later does not see earlier messages. A browser asks your backend for a short-lived ticket instead of sending a key.
        </p>
      </fieldset>

      {tools.length > 0 && (
        <fieldset className="border-0 m-0 p-0 min-w-0 flex flex-col gap-1.5">
          <legend className="p-0 mb-1 text-[13px] font-medium text-slate-300">Send through tools</legend>
          {tools.map((t) => (
            <label key={t} className="flex items-center gap-2.5 text-[13.5px]">
              <input type="checkbox" className="accent-[var(--ao-brand)]" checked={chosenTools.includes(t)}
                onChange={() => { const next = toggle(chosenTools, t); onPatch({ notify_tools: next.length ? next : undefined }); }} />
              {t}
            </label>
          ))}
        </fieldset>
      )}

      <label className={label}>If a message cannot be delivered
        <select aria-label="If a message cannot be delivered" className={field} value={String(cfg.on_failure ?? 'stop')}
          onChange={(e) => onConfig({ on_failure: e.target.value === 'carry_on' ? 'carry_on' : undefined })}>
          <option value="stop">Stop the recipe</option>
          <option value="carry_on">Carry on anyway</option>
        </select>
        <span className="text-xs font-normal text-[var(--ao-text-muted)]">A live feed with nobody connected is not a failure.</span>
      </label>
    </>
  );
}

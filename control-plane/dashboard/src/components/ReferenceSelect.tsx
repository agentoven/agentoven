import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { Link } from 'react-router-dom';
import { Check, ChevronDown, Plus, Search, X } from 'lucide-react';
import type { Problem } from '../api';
import {
  STATE_TEXT, dedupe, describeProblem, filterOptions, groupOptions, isOpenProvider, modelOptions, providerOptions,
  stepActive, unresolved, unresolvedModel, type OptionState, type Options, type ProviderLike, type Unresolved,
} from '../lib/references';

// A select that offers only what the kitchen has. The value of a field that names something is chosen here,
// never typed. A value that names nothing (the thing was deleted, or never existed) is not hidden or kept
// quietly: it shows as a red chip, with what it was probably meant to be, until the person replaces or removes it.

const TRIGGER = 'w-full min-h-[38px] flex items-center gap-2 px-3 py-2 rounded-lg bg-[var(--ao-bg)] border text-sm text-left outline-none focus:border-[var(--ao-brand)] transition-colors';

const TONE: Record<OptionState, string> = {
  disabled: 'bg-slate-500/20 text-slate-300',
  inactive: 'bg-slate-500/20 text-slate-300',
  pending: 'bg-blue-500/20 text-blue-300',
  unapproved: 'bg-amber-500/20 text-amber-300',
  unhealthy: 'bg-amber-500/20 text-amber-300',
  cooled: 'bg-amber-500/20 text-amber-300',
  retired: 'bg-red-500/20 text-red-300',
  burnt: 'bg-red-500/20 text-red-300',
};

/** The word beside an option that is not in its normal state. */
export function StateChip({ state }: { state: OptionState }) {
  return <span data-state={state} className={`shrink-0 text-[11px] font-medium px-2 py-0.5 rounded-full ${TONE[state]}`}>{STATE_TEXT[state]}</span>;
}

/** What is wrong with a value that names nothing, in a chip the form cannot ignore. */
export function ErrorChip({ u, onUse, onRemove }: { u: Unresolved; onUse?: (value: string) => void; onRemove?: () => void }) {
  return (
    <span role="alert" data-unresolved={u.value} className="inline-flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
      <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full bg-red-500/15 border border-red-500/40 text-red-300 font-medium [overflow-wrap:anywhere]">
        {u.why}: <span className="font-mono">{u.value}</span>
      </span>
      {u.suggestions.length > 0 && <span className="text-[var(--ao-text-muted)]">Did you mean</span>}
      {u.suggestions.map((s) => (
        <button key={s} type="button" onClick={() => onUse?.(s)} className="px-2 py-0.5 rounded-full border border-[var(--ao-border)] font-mono text-[var(--ao-brand-light)] hover:bg-[var(--ao-surface-hover)]">{s}</button>
      ))}
      {onRemove && <button type="button" onClick={onRemove} className="px-2 py-0.5 rounded-full border border-[var(--ao-border)] text-[var(--ao-text-muted)] hover:bg-[var(--ao-surface-hover)]">Remove</button>}
    </span>
  );
}

/** What the server said about this field when it refused a save. */
export function FieldProblems({ problems }: { problems?: Problem[] }) {
  if (!problems?.length) return null;
  return (
    <ul className="m-0 mt-1.5 p-0 list-none space-y-0.5">
      {problems.map((p, i) => (
        <li key={`${p.path}-${i}`} className={`text-xs [overflow-wrap:anywhere] ${p.severity === 'warning' ? 'text-amber-300' : 'text-red-300'}`}>{describeProblem(p)}</li>
      ))}
    </ul>
  );
}

export interface EmptyHint {
  /** What there is none of: "tools", "skills". */
  what: string;
  /** The page that registers one, and the words of the link. */
  to?: string;
  link?: string;
}

interface PickerProps {
  options: Options;
  loading?: boolean;
  empty?: EmptyHint;
  /** Something that may be done from the list itself, such as adding a provider. */
  action?: { label: string; onClick: () => void };
  value?: string;
  /** Options already chosen (a list field): they are not offered again. */
  taken?: string[];
  searchLabel: string;
  onPick: (value: string) => void;
  onClose: () => void;
}

/** The open list: a search box and the options, by keyboard or mouse. */
function Picker({ options, loading, empty, action, value, taken = [], searchLabel, onPick, onClose }: PickerProps) {
  const [query, setQuery] = useState('');
  const listId = useId();
  const offered = useMemo(() => filterOptions((options ?? []).filter((o) => !taken.includes(o.value)), query), [options, taken, query]);
  const flat = useMemo(() => groupOptions(offered).flatMap((g) => g.options), [offered]);
  const [active, setActive] = useState(() => Math.max(0, flat.findIndex((o) => o.value === value && !o.disabled)));
  const cur = active >= 0 && active < flat.length ? active : stepActive(flat, -1, 'first');
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => { inputRef.current?.focus(); }, []);

  const key = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive(stepActive(flat, cur, 1)); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(stepActive(flat, cur, -1)); }
    else if (e.key === 'Home') { e.preventDefault(); setActive(stepActive(flat, cur, 'first')); }
    else if (e.key === 'End') { e.preventDefault(); setActive(stepActive(flat, cur, 'last')); }
    else if (e.key === 'Enter') { e.preventDefault(); if (cur >= 0 && flat[cur]) onPick(flat[cur].value); }
    else if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); onClose(); }
    else if (e.key === 'Tab') onClose();
  };

  const optId = (i: number) => `${listId}-o${i}`;
  return (
    <div className="absolute left-0 right-0 z-30 mt-1 rounded-xl border border-[var(--ao-brand-light)] bg-[var(--ao-bg)] shadow-xl overflow-hidden">
      <div className="flex items-center gap-2 px-3 h-10 border-b border-[var(--ao-border)]">
        <Search size={14} className="text-[var(--ao-text-muted)]" />
        <input
          ref={inputRef}
          role="combobox"
          aria-expanded="true"
          aria-controls={listId}
          aria-activedescendant={cur >= 0 ? optId(cur) : undefined}
          aria-label={searchLabel}
          value={query}
          onChange={(e) => { setQuery(e.target.value); setActive(0); }}
          onKeyDown={key}
          placeholder="Search"
          className="flex-1 min-w-0 bg-transparent outline-none text-sm"
        />
      </div>
      <div id={listId} role="listbox" aria-label={searchLabel} className="max-h-56 overflow-y-auto p-1.5">
        {offered.length === 0 && (
          <p className="px-3 py-3 text-sm text-[var(--ao-text-muted)] m-0" data-empty={options && options.length === 0 ? empty?.what ?? '' : undefined}>
            {options === undefined
              ? (loading ? 'Loading\u2026' : 'These cannot be listed here, so nothing can be chosen.')
              : options.length === 0
                ? <>There are no {empty?.what ?? 'options'} yet.{' '}{empty?.to && <Link to={empty.to} className="font-semibold text-[var(--ao-brand-light)] hover:underline">{empty.link ?? 'Add one'}</Link>}</>
                : options.every((o) => taken.includes(o.value))
                  ? 'Everything is already chosen.'
                  : `Nothing matches \u201c${query}\u201d.`}
          </p>
        )}
        {groupOptions(offered).map((g) => (
          <div key={g.group} role="group" aria-label={g.group || undefined}>
            {g.group && <div className="px-2.5 pt-2 pb-1 text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]">{g.group}</div>}
            {g.options.map((o) => {
              const i = flat.indexOf(o);
              const on = o.value === value;
              return (
                <div
                  key={o.value}
                  id={optId(i)}
                  role="option"
                  aria-selected={on}
                  aria-disabled={o.disabled || undefined}
                  data-active={i === cur || undefined}
                  onMouseEnter={() => !o.disabled && setActive(i)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => !o.disabled && onPick(o.value)}
                  className={`flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-sm ${o.disabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer'} ${i === cur ? 'bg-[var(--ao-surface-hover)]' : ''}`}
                >
                  <span className="min-w-0 flex-1">
                    <span className="block truncate font-mono text-[13px]">{o.label}</span>
                    {o.detail && <span className="block truncate text-xs text-[var(--ao-text-muted)]">{o.detail}</span>}
                  </span>
                  {o.state && <StateChip state={o.state} />}
                  <Check size={14} className="shrink-0 text-[var(--ao-brand-light)]" style={{ visibility: on ? 'visible' : 'hidden' }} />
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {action && (
        <button type="button" onClick={() => { onClose(); action.onClick(); }}
          className="w-full flex items-center gap-2 px-3.5 py-2.5 border-t border-[var(--ao-border)] text-sm font-semibold text-[var(--ao-brand-light)] hover:bg-[var(--ao-surface-hover)]">
          <Plus size={15} /> {action.label}
        </button>
      )}
    </div>
  );
}

/** Closes when a click lands outside, and returns the box to attach. */
function useOutsideClose(open: boolean, close: () => void) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const down = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) close(); };
    document.addEventListener('mousedown', down);
    return () => document.removeEventListener('mousedown', down);
  }, [open, close]);
  return ref;
}

interface CommonProps {
  /** What the field is called, for assistive technology and the search box. */
  label: string;
  options: Options;
  loading?: boolean;
  empty?: EmptyHint;
  action?: { label: string; onClick: () => void };
  placeholder?: string;
  disabled?: boolean;
  /** What the server said about this field when it last refused a save. */
  problems?: Problem[];
  /** Open on first render: for a test that looks at the open list. */
  defaultOpen?: boolean;
  id?: string;
}

/** Chooses one of the kitchen's things. Empty means "nothing chosen"; `clearable` lets the person go back to it. */
export function ReferenceSelect({ label, options, loading, empty, action, placeholder, disabled, problems, defaultOpen = false, id, value, onChange, clearable = false }: CommonProps & {
  value: string;
  onChange: (value: string) => void;
  clearable?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const close = () => setOpen(false);
  const ref = useOutsideClose(open, () => setOpen(false));
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listId = useId();
  const picked = options?.find((o) => o.value === value);
  const bad = unresolved(value, options);
  const choose = (v: string) => { onChange(v); setOpen(false); triggerRef.current?.focus(); };

  return (
    <div ref={ref} className="relative min-w-0">
      <div className="flex items-center gap-1.5">
        <button
          ref={triggerRef}
          id={id}
          type="button"
          role="combobox"
          aria-label={label}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-controls={open ? listId : undefined}
          aria-invalid={bad ? true : undefined}
          disabled={disabled}
          onClick={() => setOpen((o) => !o)}
          onKeyDown={(e) => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); setOpen(true); } }}
          className={`${TRIGGER} ${bad ? 'border-red-500/60' : 'border-[var(--ao-border)]'} ${disabled ? 'opacity-60 cursor-not-allowed' : ''}`}
        >
          <span className="flex-1 min-w-0 truncate font-mono text-[13px]">
            {value
              ? <span className={bad ? 'text-red-300' : ''}>{picked?.label ?? value}</span>
              : <span className="text-[var(--ao-text-muted)] font-sans">{loading ? 'Loading…' : placeholder ?? 'Choose…'}</span>}
          </span>
          {picked?.state && !bad && <StateChip state={picked.state} />}
          <ChevronDown size={15} className="shrink-0 text-[var(--ao-text-muted)]" />
        </button>
        {clearable && value && !disabled && (
          <button type="button" aria-label={`Clear ${label}`} onClick={() => onChange('')} className="shrink-0 w-8 h-8 grid place-items-center rounded-md text-[var(--ao-text-muted)] hover:text-red-300 hover:bg-[var(--ao-surface-hover)]"><X size={14} /></button>
        )}
      </div>
      {bad && <div className="mt-1.5"><ErrorChip u={bad} onUse={choose} onRemove={() => onChange('')} /></div>}
      <FieldProblems problems={bad ? undefined : problems} />
      {open && <Picker options={options} loading={loading} empty={empty} action={action} value={value} searchLabel={label} onPick={choose} onClose={() => { close(); triggerRef.current?.focus(); }} />}
    </div>
  );
}

/** Chooses several of the kitchen's things. Each is kept once; one that names nothing shows as a red chip until it is replaced or removed. */
export function ReferenceMultiSelect({ label, options, loading, empty, action, placeholder, disabled, problems, defaultOpen = false, id, value, onChange }: CommonProps & {
  value: string[];
  onChange: (value: string[]) => void;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const ref = useOutsideClose(open, () => setOpen(false));
  const triggerRef = useRef<HTMLButtonElement>(null);
  const listId = useId();
  const items = dedupe(value);
  const add = (v: string) => { onChange(dedupe([...items, v])); setOpen(false); triggerRef.current?.focus(); };
  const replace = (from: string, to: string) => onChange(dedupe(items.map((x) => (x === from ? to : x))));
  const remove = (v: string) => onChange(items.filter((x) => x !== v));

  return (
    <div ref={ref} className="relative min-w-0">
      {items.length > 0 && (
        <ul aria-label={`Chosen: ${label}`} className="m-0 mb-2 p-0 list-none flex flex-col gap-1.5">
          {items.map((v) => {
            const bad = unresolved(v, options);
            const o = options?.find((x) => x.value === v);
            return (
              <li key={v} className={`flex flex-wrap items-center gap-2 px-2.5 py-1.5 rounded-lg border text-sm ${bad ? 'border-red-500/50 bg-red-500/5' : 'border-[var(--ao-border)] bg-[var(--ao-bg)]'}`}>
                {bad
                  ? <ErrorChip u={bad} onUse={(s) => replace(v, s)} />
                  : <><span className="font-mono text-[13px] truncate min-w-0">{o?.label ?? v}</span>{o?.state && <StateChip state={o.state} />}</>}
                <span className="flex-1" />
                {!disabled && <button type="button" aria-label={`Remove ${v}`} onClick={() => remove(v)} className="w-6 h-6 grid place-items-center rounded-md text-[var(--ao-text-muted)] hover:text-red-300 hover:bg-[var(--ao-surface-hover)]"><X size={13} /></button>}
              </li>
            );
          })}
        </ul>
      )}
      <button
        ref={triggerRef}
        id={id}
        type="button"
        role="combobox"
        aria-label={`Add to ${label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => { if (e.key === 'ArrowDown' || e.key === 'ArrowUp') { e.preventDefault(); setOpen(true); } }}
        className={`${TRIGGER} border-dashed border-[var(--ao-border)] text-[var(--ao-text-muted)] ${disabled ? 'opacity-60 cursor-not-allowed' : ''}`}
      >
        <Plus size={14} className="shrink-0" />
        <span className="flex-1 min-w-0 truncate">{loading ? 'Loading…' : placeholder ?? 'Add…'}</span>
        <ChevronDown size={15} className="shrink-0" />
      </button>
      <FieldProblems problems={problems} />
      {open && <Picker options={options} loading={loading} empty={empty} action={action} taken={items} searchLabel={label} onPick={add} onClose={() => { setOpen(false); triggerRef.current?.focus(); }} />}
    </div>
  );
}

/** A custom id typed on purpose, for a provider that serves models its list does not name. */
function CustomModelId({ value, onUse, onBack }: { value: string; onUse: (id: string) => void; onBack?: () => void }) {
  const [text, setText] = useState(value);
  const use = () => { if (text.trim()) onUse(text.trim()); };
  return (
    <div className="mt-2 flex flex-col gap-1.5">
      <div className="flex items-center gap-1.5">
        <input aria-label="Custom model id" value={text} onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); use(); } }}
          placeholder="the model id as the provider names it"
          className="flex-1 min-w-0 px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm font-mono outline-none focus:border-[var(--ao-brand)]" />
        <button type="button" onClick={use} disabled={!text.trim() || text.trim() === value}
          className="h-9 px-3 rounded-lg border border-[var(--ao-border)] text-sm font-medium hover:bg-[var(--ao-surface-hover)] disabled:opacity-50">Use this id</button>
      </div>
      <p className="m-0 text-xs text-[var(--ao-text-muted)]">This provider serves models its list does not name, so the id is not checked until the agent runs.</p>
      {onBack && <button type="button" onClick={onBack} className="self-start text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">Pick from the list instead</button>}
    </div>
  );
}

/**
 * A provider, then a model of that provider. The model list appears only once a provider is chosen and holds
 * only what that provider lists. The "enter a custom model id" way out appears only for a provider that serves
 * models its list does not name (openrouter, litellm, ollama, or a provider registered with no list).
 *
 * Why two dropdowns and not one grouped list of every model: a list cannot say "provider first", cannot leave the
 * provider out (a summarizer's provider is optional), has no custom id, and has no error for a model that is gone.
 */
export function ModelReferenceSelect({ label, providers, loading, provider, model, onChange, providerOptional = false, providerPlaceholder, onAddProvider, problems, defaultOpen = false, disabled }: {
  label: string;
  /** The kitchen's providers with their models; undefined while they cannot be read. */
  providers: ProviderLike[] | undefined;
  loading?: boolean;
  provider: string;
  model: string;
  onChange: (provider: string, model: string) => void;
  /** The provider may be left empty (the summarizer): the model is then looked up among every provider's. */
  providerOptional?: boolean;
  providerPlaceholder?: string;
  onAddProvider?: () => void;
  /** Server problems at this pair's paths. */
  problems?: Problem[];
  defaultOpen?: boolean;
  disabled?: boolean;
}) {
  const chosen = providers?.find((p) => p.name === provider);
  const open = chosen ? isOpenProvider(chosen) : false;
  const listed = modelOptions(chosen);
  const isCustom = !!chosen && open && !!model && !listed.some((o) => o.value === model);
  // A custom id the person chose on purpose is a valid value for an open provider, so it is offered as one.
  const modelChoices = isCustom ? [{ value: model, label: model, detail: 'custom id' }, ...listed] : listed;
  const [typing, setTyping] = useState(isCustom || (!!chosen && open && listed.length === 0));
  const providerOpts = providers ? providerOptions(providers) : undefined;
  const noProvider = !provider && model ? unresolvedModel({ provider: '', model }, providers, true) : null;
  const providerProblems = problems?.filter((p) => /(_provider|\.provider)$/.test(p.path));
  const modelProblems = problems?.filter((p) => /(_model|_name|\.model)$/.test(p.path));

  return (
    <div className="flex flex-col gap-2 min-w-0">
      <ReferenceSelect
        label={`${label}: provider`}
        options={providerOpts}
        loading={loading}
        empty={{ what: 'providers', to: '/providers', link: 'Add a provider' }}
        action={onAddProvider ? { label: 'Add a provider', onClick: onAddProvider } : undefined}
        placeholder={providerPlaceholder ?? 'Choose a provider'}
        value={provider}
        clearable={providerOptional}
        problems={providerProblems}
        defaultOpen={defaultOpen}
        disabled={disabled}
        onChange={(p) => { if (p !== provider) { onChange(p, ''); setTyping(false); } }}
      />
      {provider && chosen && (
        <>
          {typing && open ? (
            <CustomModelId value={model} onUse={(id) => onChange(provider, id)} onBack={listed.length > 0 ? () => setTyping(false) : undefined} />
          ) : (
            <ReferenceSelect
              label={`${label}: model`}
              options={modelChoices}
              loading={loading}
              empty={{ what: `models listed by ${provider}` }}
              placeholder="Choose a model"
              value={model}
              problems={modelProblems}
              disabled={disabled}
              onChange={(m) => onChange(provider, m)}
            />
          )}
          {open && !typing && listed.length > 0 && (
            <button type="button" onClick={() => setTyping(true)} className="self-start text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">Enter a custom model id</button>
          )}
          {isCustom && !typing && <p className="m-0 text-xs text-amber-300">{model} is not in {provider}&apos;s list. It is accepted because this provider serves models its list does not name.</p>}
        </>
      )}
      {!provider && model && (providerOptional ? (
        <div className="flex flex-col gap-1.5">
          <p className="m-0 text-xs text-[var(--ao-text-muted)]">
            Using <span className="font-mono">{model}</span>, found among every provider&apos;s models. Choose a provider above to pin it to one.
          </p>
          {noProvider && <ErrorChip u={{ value: model, why: 'not found', suggestions: noProvider.suggestions }} onUse={(m) => onChange('', m)} onRemove={() => onChange('', '')} />}
          {!noProvider && <button type="button" onClick={() => onChange('', '')} className="self-start text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">Clear the model</button>}
        </div>
      ) : (
        <p role="alert" className="m-0 text-xs text-amber-300">
          The model <span className="font-mono">{model}</span> has no provider. Choose the provider that serves it.
        </p>
      ))}
    </div>
  );
}

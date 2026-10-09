import { useEffect, useState } from 'react';
import { ArrowLeft } from 'lucide-react';
import { pluginImport, type ImportRequest, type ImportResponse, type PluginLocation } from '../api';
import { Button, Modal, Spinner, StatusBadge } from './UI';

// Import a Claude Code or Codex plugin from a git location: review what it would register (a dry
// run, nothing is sent to a model), choose the skills, then import. Used by the Skills page.

const inputCls = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';

/** One import verifies each skill with a model, so the server caps how many it takes at once. */
const MAX_IMPORT_SKILLS = 40;

const errText = (e: unknown) => (e instanceof Error ? e.message : 'Request failed');

function Err({ message }: { message: string }) {
  return (
    <div role="alert" className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-400">
      {message}
    </div>
  );
}

export function PluginImportDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  // The body remounts on each open so its state resets.
  return (
    <Modal open={open} onClose={onClose} title="Import a plugin" width="max-w-3xl">
      <PluginImportBody onClose={onClose} />
    </Modal>
  );
}

function PluginImportBody({ onClose }: { onClose: () => void }) {
  const [gitUrl, setGitUrl] = useState('');
  const [gitRef, setGitRef] = useState('');
  const [path, setPath] = useState('');
  const [location, setLocation] = useState<PluginLocation | null>(null);

  if (location) return <ReviewStep location={location} onBack={() => setLocation(null)} onClose={onClose} />;

  const valid = /^https:\/\/\S+$/.test(gitUrl.trim());
  return (
    <div className="space-y-4">
      <p className="text-sm text-[var(--ao-text-muted)]">
        Takes the skills and the MCP servers reachable over HTTP from a Claude Code or Codex plugin in a git repository.
        Commands, hooks and local servers are left out. Each skill is reviewed by a model provider before it can be used,
        so its text is sent to that provider. Browsing catalogs of plugins is available in AgentOven Pro.
      </p>
      <div>
        <label htmlFor="plugin-url" className="block text-xs text-[var(--ao-text-muted)] mb-1">Git URL (https) *</label>
        <input id="plugin-url" value={gitUrl} onChange={(e) => setGitUrl(e.target.value)} className={inputCls} placeholder="https://github.com/owner/plugin-repo" />
      </div>
      <div className="flex flex-wrap gap-3">
        <div className="w-40">
          <label htmlFor="plugin-ref" className="block text-xs text-[var(--ao-text-muted)] mb-1">Branch or tag</label>
          <input id="plugin-ref" value={gitRef} onChange={(e) => setGitRef(e.target.value)} className={inputCls} placeholder="main" />
        </div>
        <div className="flex-1 min-w-[200px]">
          <label htmlFor="plugin-path" className="block text-xs text-[var(--ao-text-muted)] mb-1">Plugin directory in the repository</label>
          <input id="plugin-path" value={path} onChange={(e) => setPath(e.target.value)} className={inputCls} placeholder="plugins/my-plugin (blank: the root)" />
        </div>
      </div>
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button
          disabled={!valid}
          onClick={() => setLocation({
            git_url: gitUrl.trim(),
            git_ref: gitRef.trim() || undefined,
            path: path.trim() || undefined,
            name: gitUrl.trim().replace(/\/+$/, '').split('/').pop()?.replace(/\.git$/, '') || undefined,
          })}
        >
          Review
        </Button>
      </div>
    </div>
  );
}

// ── Review and import ────────────────────────────────────────

function ReviewStep({ location, onBack, onClose }: { location: PluginLocation; onBack: () => void; onClose: () => void }) {
  const [plan, setPlan] = useState<ImportResponse | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [importing, setImporting] = useState(false);
  const [result, setResult] = useState<ImportResponse | null>(null);

  // A dry run: the server fetches the plugin and says what it would register, without calling a model.
  useEffect(() => {
    let stale = false;
    pluginImport.run({ ...location, dry_run: true })
      .then((p) => {
        if (stale) return;
        setPlan(p);
        setChosen(new Set([...(p.skills ?? []).map((s) => s.name), ...(p.server_skill ? [p.server_skill] : [])]));
      })
      .catch((e) => { if (!stale) setErr(errText(e)); });
    return () => { stale = true; };
  }, [location]);

  const skills = plan?.skills ?? [];
  const servers = plan?.servers ?? [];
  const everything = plan ? chosen.size === skills.length + (plan.server_skill ? 1 : 0) : false;
  const tooMany = plan !== null && everything && skills.length > MAX_IMPORT_SKILLS;

  const toggle = (name: string) =>
    setChosen((prev) => { const n = new Set(prev); if (n.has(name)) n.delete(name); else n.add(name); return n; });

  const doImport = async () => {
    if (!plan) return;
    setImporting(true);
    setErr(null);
    const named = Object.fromEntries(Object.entries(creds).filter(([, v]) => v.trim()).map(([k, v]) => [k, v.trim()]));
    const req: ImportRequest = {
      ...location,
      only: everything ? undefined : [...chosen],
      credentials: Object.keys(named).length ? named : undefined,
    };
    try {
      setResult(await pluginImport.run(req));
    } catch (e) {
      setErr(errText(e));
    }
    setImporting(false);
  };

  if (result) {
    const results = result.results ?? [];
    return (
      <div className="space-y-4">
        <p className="text-sm font-medium">Imported {plan?.plugin.name ?? 'plugin'}</p>
        <ul className="divide-y divide-[var(--ao-border)] border border-[var(--ao-border)] rounded-lg">
          {results.length === 0 && <li className="p-3 text-sm text-[var(--ao-text-muted)]">Nothing was registered.</li>}
          {results.map((r) => (
            <li key={r.name} className="flex items-start gap-3 p-3">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium">{r.name}</p>
                {(r.error || r.reasoning) && <p className="text-xs text-[var(--ao-text-muted)] line-clamp-3">{r.error || r.reasoning}</p>}
              </div>
              <StatusBadge status={r.status} />
            </li>
          ))}
        </ul>
        {results.some((r) => r.status === 'needs_review') && (
          <p className="text-xs text-[var(--ao-text-muted)]">Skills at needs_review stay unusable until someone approves them on the Skills page.</p>
        )}
        <Skipped items={result.skipped ?? []} />
        <div className="flex justify-end"><Button onClick={onClose}>Done</Button></div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <button type="button" onClick={onBack} className="flex items-center gap-1 text-xs text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]">
        <ArrowLeft size={14} /> Back
      </button>

      <div>
        <p className="text-base font-medium">
          {plan?.plugin.name ?? location.name}
          {plan?.plugin.version && <span className="ml-2 text-xs font-normal text-[var(--ao-text-muted)]">{plan.plugin.version}</span>}
        </p>
        {plan?.plugin.description && <p className="text-sm text-[var(--ao-text-muted)] line-clamp-3">{plan.plugin.description}</p>}
        <p className="mt-1 text-xs text-[var(--ao-text-muted)] break-all">
          {location.git_url}{location.path ? ` · ${location.path}` : ''}
          {plan && ` · ${plan.plugin.license ? `licence ${plan.plugin.license}` : 'no licence declared'}`}
        </p>
      </div>

      {err && <Err message={err} />}
      {!plan && !err && <Spinner />}

      {plan && (
        <>
          {skills.length === 0 && servers.length === 0 && (
            <p className="text-sm text-[var(--ao-text-muted)]">This plugin has no skills or remote MCP servers AgentOven can use.</p>
          )}

          {(skills.length > 0 || plan.server_skill) && (
            <ul className="divide-y divide-[var(--ao-border)] border border-[var(--ao-border)] rounded-lg max-h-[34vh] overflow-y-auto">
              {skills.map((s) => (
                <li key={s.name}>
                  <label className="flex items-start gap-3 p-3 cursor-pointer">
                    <input type="checkbox" className="mt-1" checked={chosen.has(s.name)} onChange={() => toggle(s.name)} />
                    <span className="min-w-0">
                      <span className="block text-sm font-medium">{s.name}</span>
                      <span className="block text-xs text-[var(--ao-text-muted)] line-clamp-2">{s.description}</span>
                    </span>
                  </label>
                </li>
              ))}
              {plan.server_skill && (
                <li>
                  <label className="flex items-start gap-3 p-3 cursor-pointer">
                    <input type="checkbox" className="mt-1" checked={chosen.has(plan.server_skill)} onChange={() => toggle(plan.server_skill!)} />
                    <span className="min-w-0">
                      <span className="block text-sm font-medium">{plan.server_skill} <span className="text-xs font-normal text-[var(--ao-text-muted)]">MCP servers</span></span>
                      <span className="block text-xs text-[var(--ao-text-muted)]">Registers every tool these servers offer: {servers.map((s) => s.name).join(', ')}.</span>
                    </span>
                  </label>
                </li>
              )}
            </ul>
          )}

          {servers.some((s) => s.needs_credential) && (
            <div className="space-y-2">
              <p className="text-xs text-[var(--ao-text-muted)]">
                These servers need a key. Name the kitchen credential that holds it; a server left blank is skipped.
              </p>
              {servers.filter((s) => s.needs_credential).map((s) => (
                <div key={s.name} className="flex items-center gap-3">
                  <label htmlFor={`cred-${s.name}`} className="w-40 shrink-0 text-xs truncate" title={s.endpoint}>{s.name}</label>
                  <input
                    id={`cred-${s.name}`} value={creds[s.name] ?? ''} className={inputCls} placeholder="credential name"
                    onChange={(e) => setCreds((c) => ({ ...c, [s.name]: e.target.value }))}
                  />
                </div>
              ))}
            </div>
          )}

          <Skipped items={plan.skipped ?? []} />

          {tooMany && (
            <p className="text-xs text-amber-400">
              This plugin has {skills.length} skills and one import takes at most {MAX_IMPORT_SKILLS}. Untick some to import it in parts.
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="secondary" disabled={importing} onClick={onBack}>Cancel</Button>
            <Button disabled={importing || chosen.size === 0 || tooMany} onClick={doImport}>
              {importing ? 'Reviewing and importing…' : `Import ${chosen.size}`}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

/** What the plugin has that AgentOven left out, with the reason for each. */
function Skipped({ items }: { items: { component: string; name?: string; reason: string }[] }) {
  if (items.length === 0) return null;
  return (
    <details className="text-xs">
      <summary className="cursor-pointer text-[var(--ao-text-muted)]">Left out ({items.length})</summary>
      <ul className="mt-2 space-y-1">
        {items.map((s, i) => (
          <li key={i} className="text-[var(--ao-text-muted)]">
            <span className="text-[var(--ao-text)]">{[s.component, s.name].filter(Boolean).join(' ')}</span> — {s.reason}
          </li>
        ))}
      </ul>
    </details>
  );
}

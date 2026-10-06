import { useState } from 'react';
import { Sparkles, Plus, Trash2, ShieldAlert, ShieldCheck, ShieldX, X as XIcon } from 'lucide-react';
import {
  skills, skillsPro, providers, APIError,
  type Skill, type SkillOutcome, type SkillSourceRequest,
} from '../api';
import { useAPI, useSkillsPro } from '../hooks';
import {
  PageHeader, Card, EmptyState, StatusBadge,
  Spinner, ErrorBanner, Button, Modal,
} from '../components/UI';

const inputCls = 'w-full px-3 py-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] text-sm outline-none focus:border-[var(--ao-brand)]';

function errMessage(e: unknown): string {
  return e instanceof Error ? e.message : 'Request failed';
}

function InlineError({ message }: { message: string }) {
  return (
    <div role="alert" className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-400">
      {message}
    </div>
  );
}

function VerdictBadge({ verdict }: { verdict?: string }) {
  if (!verdict) return <span className="text-xs text-[var(--ao-text-muted)]">unverified</span>;
  return <StatusBadge status={verdict} />;
}

/** Splits stored reasoning into the automated verdict text and any appended "[human review by …]" notes. */
function splitReasoning(reasoning: string): { automated: string; reviews: string[] } {
  const [automated, ...reviews] = reasoning.split(/\n\n(?=\[human review by )/);
  return { automated: automated.trim(), reviews: reviews.map((r) => r.trim()) };
}

// ── Page ──────────────────────────────────────────────────────

export function SkillsPage() {
  const { data, loading, error, refetch } = useAPI(skills.list);
  const proAvailable = useSkillsPro();
  const [showRegister, setShowRegister] = useState(false);
  const [selectedName, setSelectedName] = useState<string | null>(null);
  const [analyzing, setAnalyzing] = useState<string | null>(null);

  const selected = data?.find((s) => s.name === selectedName) ?? null;

  return (
    <div>
      <PageHeader
        title="Skills"
        description="Agent Skills (SKILL.md bundles) registered in this kitchen, with their verification results"
        action={
          <Button onClick={() => setShowRegister(true)}>
            <Plus size={16} className="mr-1.5" /> Register Skill
          </Button>
        }
      />

      {error && <ErrorBanner message={error} onRetry={refetch} />}

      {loading ? (
        <Spinner />
      ) : data && data.length > 0 ? (
        <div className="p-8 flex flex-col xl:flex-row gap-6 items-start">
          <Card className="overflow-x-auto !p-0 w-full xl:flex-1 min-w-0">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-[var(--ao-border)] text-left text-[var(--ao-text-muted)]">
                  <th className="px-4 py-3 font-medium">Name</th>
                  <th className="px-4 py-3 font-medium">Source</th>
                  <th className="px-4 py-3 font-medium">Status</th>
                  <th className="px-4 py-3 font-medium">Verdict</th>
                </tr>
              </thead>
              <tbody>
                {data.map((skill) => (
                  <tr
                    key={skill.id}
                    onClick={() => setSelectedName(skill.name)}
                    className={`border-b border-[var(--ao-border)] last:border-0 cursor-pointer hover:bg-[var(--ao-surface-hover)] ${
                      skill.name === selectedName ? 'bg-[var(--ao-surface-hover)]' : ''
                    }`}
                  >
                    <td className="px-4 py-3">
                      <p className="font-medium">{skill.name}</p>
                      {skill.manifest?.description && (
                        <p className="text-xs text-[var(--ao-text-muted)] line-clamp-1">{skill.manifest.description}</p>
                      )}
                    </td>
                    <td className="px-4 py-3">
                      <span className="text-xs px-2 py-0.5 rounded bg-[var(--ao-bg)] border border-[var(--ao-border)]">
                        {skill.source}
                      </span>
                      {skill.source_ref && (
                        <p className="text-xs text-[var(--ao-text-muted)] truncate max-w-[220px] mt-1" title={skill.source_ref}>
                          {skill.source_ref}
                        </p>
                      )}
                    </td>
                    <td className="px-4 py-3"><StatusBadge status={skill.status} /></td>
                    <td className="px-4 py-3"><VerdictBadge verdict={skill.verification_verdict} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Card>

          {selected && (
            <SkillDetail
              key={selected.id}
              skill={selected}
              proAvailable={proAvailable === true}
              onClose={() => setSelectedName(null)}
              onChanged={refetch}
              onDeleted={() => { setSelectedName(null); refetch(); }}
              onAnalyze={() => setAnalyzing(selected.name)}
            />
          )}
        </div>
      ) : (
        !error && (
          <EmptyState
            icon={<Sparkles size={48} />}
            title="No skills registered"
            description="Register a skill from a git repository, a local path, or pasted SKILL.md content. It is reviewed by a model provider before agents can use it."
            action={<Button onClick={() => setShowRegister(true)}>Register Skill</Button>}
          />
        )
      )}

      <RegisterSkillDialog
        open={showRegister}
        proAvailable={proAvailable === true}
        onClose={() => { setShowRegister(false); refetch(); }}
      />

      <Modal
        open={analyzing !== null}
        onClose={() => { setAnalyzing(null); refetch(); }}
        title={`Analyze ${analyzing ?? ''}`}
      >
        {analyzing && <AnalyzeStep name={analyzing} />}
      </Modal>
    </div>
  );
}

// ── Detail panel ──────────────────────────────────────────────

function SkillDetail({
  skill, proAvailable, onClose, onChanged, onDeleted, onAnalyze,
}: {
  skill: Skill;
  proAvailable: boolean;
  onClose: () => void;
  onChanged: () => void;
  onDeleted: () => void;
  onAnalyze: () => void;
}) {
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const { automated, reviews } = splitReasoning(skill.verification_reasoning ?? '');
  const servers = skill.manifest?.mcp_tools ?? [];
  const allowed = skill.manifest?.allowed_tools ?? [];

  const remove = async () => {
    setBusy(true);
    setErr(null);
    try {
      await skills.delete(skill.name);
      onDeleted();
    } catch (e) {
      setErr(errMessage(e));
      setBusy(false);
    }
  };

  return (
    <Card className="w-full xl:w-[28rem] shrink-0 space-y-4">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="text-base font-semibold truncate">{skill.name}</h2>
          {skill.manifest?.description && (
            <p className="text-sm text-[var(--ao-text-muted)] mt-1">{skill.manifest.description}</p>
          )}
        </div>
        <button
          onClick={onClose}
          aria-label="Close details"
          className="p-1 rounded-lg text-[var(--ao-text-muted)] hover:text-[var(--ao-text)] hover:bg-[var(--ao-surface-hover)]"
        >
          <XIcon size={16} />
        </button>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <StatusBadge status={skill.status} />
        <VerdictBadge verdict={skill.verification_verdict} />
        {skill.verified_by_provider && (
          <span className="text-xs text-[var(--ao-text-muted)]">
            verified by {skill.verified_by_provider}
            {skill.verified_at && ` · ${new Date(skill.verified_at).toLocaleString()}`}
          </span>
        )}
      </div>

      {err && <InlineError message={err} />}

      {skill.status === 'pending' && (
        <div className="p-3 rounded-lg bg-blue-500/10 border border-blue-500/30 text-sm text-blue-300">
          Staged, not yet analyzed. This skill is inert and unusable by agents until it has been verified.
          {proAvailable ? (
            <div className="mt-2"><Button size="sm" onClick={onAnalyze}>Analyze…</Button></div>
          ) : (
            <p className="mt-1 text-xs text-[var(--ao-text-muted)]">Analyzing staged skills requires AgentOven Pro.</p>
          )}
        </div>
      )}

      {skill.status === 'needs_review' && (
        <div className="space-y-2">
          <div className="p-3 rounded-lg bg-amber-500/10 border border-amber-500/30 text-sm text-amber-300">
            The automated review was inconclusive. This skill stays inert until an admin approves or rejects it.
          </div>
          <ReviewActions name={skill.name} onDone={onChanged} />
        </div>
      )}

      {skill.status === 'rejected' && (
        <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-300">
          Blocked: no tools are registered for this skill and agents cannot use it. Delete it and register a
          corrected version to try again.
        </div>
      )}

      <section>
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--ao-text-muted)] mb-1">
          Verification reasoning
        </h3>
        {automated ? (
          <p className="text-sm whitespace-pre-wrap max-h-64 overflow-y-auto">{automated}</p>
        ) : (
          <p className="text-sm text-[var(--ao-text-muted)]">No verification has run for this skill yet.</p>
        )}
        {reviews.map((r, i) => (
          <p key={i} className="text-sm whitespace-pre-wrap mt-2 pl-3 border-l-2 border-[var(--ao-brand)]">{r}</p>
        ))}
      </section>

      <section className="text-sm space-y-2">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--ao-text-muted)]">Bundle</h3>
        <p className="text-xs text-[var(--ao-text-muted)]">
          Source: {skill.source}{skill.source_ref ? ` · ${skill.source_ref}` : ''}
          {skill.manifest?.license && ` · License: ${skill.manifest.license}`}
        </p>
        {allowed.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {allowed.map((t) => (
              <span key={t} className="text-xs px-1.5 py-0.5 rounded bg-blue-500/20 text-blue-400">{t}</span>
            ))}
          </div>
        )}
        {servers.length > 0 && (
          <ul className="space-y-1">
            {servers.map((s) => (
              <li key={s.name} className="text-xs p-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)]">
                <span className="font-medium">{s.name}</span>
                <span className="text-[var(--ao-text-muted)]"> · {s.transport} · {s.endpoint}</span>
                {s.auth_type && <span className="text-[var(--ao-text-muted)]"> · auth: {s.auth_type}</span>}
              </li>
            ))}
          </ul>
        )}
        {(skill.registered_tools?.length ?? 0) > 0 && (
          <p className="text-xs text-[var(--ao-text-muted)]">
            Registered tools: {skill.registered_tools!.join(', ')}
          </p>
        )}
        {skill.manifest?.instructions && (
          <details>
            <summary className="text-xs cursor-pointer text-[var(--ao-brand-light)]">Instructions</summary>
            <pre className="text-xs whitespace-pre-wrap mt-1 p-2 rounded-lg bg-[var(--ao-bg)] border border-[var(--ao-border)] max-h-64 overflow-y-auto">
              {skill.manifest.instructions}
            </pre>
          </details>
        )}
      </section>

      <div className="pt-2 border-t border-[var(--ao-border)] flex items-center justify-end gap-2">
        {confirmDelete ? (
          <>
            <span className="text-xs text-[var(--ao-text-muted)] mr-auto">Delete this skill and its registered tools?</span>
            <Button size="sm" variant="secondary" disabled={busy} onClick={() => setConfirmDelete(false)}>Cancel</Button>
            <Button size="sm" variant="danger" disabled={busy} onClick={remove}>
              {busy ? 'Deleting…' : 'Delete'}
            </Button>
          </>
        ) : (
          <Button size="sm" variant="secondary" onClick={() => setConfirmDelete(true)}>
            <Trash2 size={12} className="mr-1" /> Delete
          </Button>
        )}
      </div>
    </Card>
  );
}

// ── Approve / reject (needs_review only) ──────────────────────

function ReviewActions({ name, onDone }: { name: string; onDone: () => void }) {
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState<'approve' | 'reject' | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [done, setDone] = useState<'approved' | 'rejected' | null>(null);

  const act = async (kind: 'approve' | 'reject') => {
    setBusy(kind);
    setErr(null);
    try {
      await (kind === 'approve' ? skills.approve(name, note.trim()) : skills.reject(name, note.trim()));
      setDone(kind === 'approve' ? 'approved' : 'rejected');
      onDone();
    } catch (e) {
      setErr(errMessage(e));
    }
    setBusy(null);
  };

  if (done) {
    return <p className="text-sm text-[var(--ao-text-muted)]">Skill {done}.</p>;
  }

  return (
    <div className="space-y-2">
      {err && <InlineError message={err} />}
      <div>
        <label htmlFor={`review-note-${name}`} className="block text-xs text-[var(--ao-text-muted)] mb-1">
          Note / reason (optional, recorded with the review)
        </label>
        <input
          id={`review-note-${name}`}
          value={note}
          onChange={(e) => setNote(e.target.value)}
          className={inputCls}
          placeholder="Why you are approving or rejecting"
        />
      </div>
      <div className="flex gap-2">
        <Button size="sm" disabled={busy !== null} onClick={() => act('approve')}>
          {busy === 'approve' ? 'Approving…' : 'Approve'}
        </Button>
        <Button size="sm" variant="danger" disabled={busy !== null} onClick={() => act('reject')}>
          {busy === 'reject' ? 'Rejecting…' : 'Reject'}
        </Button>
      </div>
    </div>
  );
}

// ── Verdict result (shared by quick install and analyze) ──────

function VerdictResult({ result, skillName }: { result: SkillOutcome; skillName?: string }) {
  const name = skillName ?? result.skill?.name;
  const { automated } = splitReasoning(result.reasoning);
  const view = {
    accepted: {
      icon: <ShieldCheck size={18} />, title: 'Accepted',
      body: 'Verification passed. The skill is active and its bundled MCP tools are registered.',
      cls: 'bg-emerald-500/10 border-emerald-500/30 text-emerald-300',
    },
    needs_review: {
      icon: <ShieldAlert size={18} />, title: 'Needs review',
      body: result.note ?? 'The automated review was inconclusive. The skill is inert until an admin approves or rejects it.',
      cls: 'bg-amber-500/10 border-amber-500/30 text-amber-300',
    },
    rejected: {
      icon: <ShieldX size={18} />, title: 'Blocked',
      body: 'Verification rejected this skill. It was recorded as rejected, no tools were registered, and agents cannot use it.',
      cls: 'bg-red-500/10 border-red-500/30 text-red-300',
    },
  }[result.outcome];

  return (
    <div className="space-y-3">
      <div className={`p-3 rounded-lg border text-sm ${view.cls}`}>
        <p className="flex items-center gap-2 font-medium">{view.icon} {view.title}{name ? `: ${name}` : ''}</p>
        <p className="mt-1">{view.body}</p>
      </div>
      <div>
        <h3 className="text-xs font-semibold uppercase tracking-wide text-[var(--ao-text-muted)] mb-1">
          Verification reasoning
        </h3>
        <p className="text-sm whitespace-pre-wrap max-h-56 overflow-y-auto">
          {automated || 'The verifier gave no reasoning.'}
        </p>
      </div>
      {result.outcome === 'needs_review' && name && <ReviewActions name={name} onDone={() => {}} />}
      {result.outcome === 'needs_review' && !name && (
        <p className="text-xs text-[var(--ao-text-muted)]">
          Close this dialog and open the skill from the list to approve or reject it.
        </p>
      )}
    </div>
  );
}

// ── Analyze step (Pro): optional provider choice, with disclosure ───

function AnalyzeStep({ name, onResult }: { name: string; onResult?: (r: SkillOutcome) => void }) {
  const { data: providerList, loading, error, refetch } = useAPI(providers.list);
  // '' means "let the server use the kitchen's default provider"; naming one pins it.
  const [chosen, setChosen] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<SkillOutcome | null>(null);

  const list = providerList ?? [];
  const defaultProvider = list.find((p) => p.is_default)?.name;

  const analyze = async () => {
    setSubmitting(true);
    setErr(null);
    try {
      const r = await skillsPro.analyze(name, chosen ? { provider: chosen } : {});
      setResult(r);
      onResult?.(r);
    } catch (e) {
      setErr(e instanceof APIError && e.status === 409
        ? `${e.message}. This skill has already been analyzed.`
        : errMessage(e));
    }
    setSubmitting(false);
  };

  if (result) return <VerdictResult result={result} skillName={name} />;
  if (loading) return <Spinner />;
  if (error) return <InlineError message={`Could not load providers: ${error}`} />;
  if (list.length === 0) {
    return (
      <div className="space-y-2">
        <p className="text-sm">
          No model providers are registered, and one is needed to analyze a skill. Register a provider first.
        </p>
        <Button size="sm" variant="secondary" onClick={refetch}>Re-check</Button>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-[var(--ao-text-muted)]">
        <span className="text-[var(--ao-text)] font-medium">{name}</span> is staged and has not been sent to any model.
        Analysis reviews the skill&apos;s declared intent against its instructions and code.
      </p>
      {err && <InlineError message={err} />}
      <div>
        <label htmlFor="skill-analyze-provider" className="block text-xs text-[var(--ao-text-muted)] mb-1">
          Provider to analyze with (optional)
        </label>
        <select
          id="skill-analyze-provider"
          value={chosen}
          onChange={(e) => setChosen(e.target.value)}
          className={inputCls}
        >
          <option value="">
            Kitchen default{defaultProvider ? ` (${defaultProvider})` : ''}
          </option>
          {list.map((p) => (
            <option key={p.id} value={p.name}>
              {p.name} ({p.kind}){p.is_default ? ' · default' : ''}
            </option>
          ))}
        </select>
      </div>
      <p className="text-sm p-3 rounded-lg border border-amber-500/30 bg-amber-500/10">
        Analyzing sends this skill&apos;s bundle (SKILL.md, its instructions and bundled files) to{' '}
        <strong>{chosen || defaultProvider || 'the kitchen default provider'}</strong>&apos;s model. The content leaves
        this control plane and is processed by that provider. Pick a provider above if that matters.
      </p>
      <div className="flex justify-end">
        <Button disabled={submitting} onClick={analyze}>
          {submitting ? 'Analyzing…' : 'Analyze'}
        </Button>
      </div>
    </div>
  );
}

// ── Register dialog (OSS register, plus Pro governed flow) ────

type Mode = 'install' | 'governed';
type SourceKind = SkillSourceRequest['source'];

function RegisterSkillDialog({
  open, proAvailable, onClose,
}: {
  open: boolean; proAvailable: boolean; onClose: () => void;
}) {
  // Remount the body on each open so state always resets.
  return (
    <Modal open={open} onClose={onClose} title="Register Skill">
      <RegisterSkillBody proAvailable={proAvailable} onClose={onClose} />
    </Modal>
  );
}

function RegisterSkillBody({ proAvailable, onClose }: { proAvailable: boolean; onClose: () => void }) {
  const [mode, setMode] = useState<Mode>('install');
  const [source, setSource] = useState<SourceKind>('git');
  const [gitUrl, setGitUrl] = useState('');
  const [gitRef, setGitRef] = useState('');
  const [path, setPath] = useState('');
  const [skillMd, setSkillMd] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<SkillOutcome | null>(null);
  const [staged, setStaged] = useState<string | null>(null);

  const effectiveMode: Mode = proAvailable ? mode : 'install';

  const payload = (): SkillSourceRequest | null => {
    if (source === 'git') return gitUrl.trim() ? { source, git_url: gitUrl.trim(), git_ref: gitRef.trim() || undefined } : null;
    if (source === 'path') return path.trim() ? { source, path: path.trim() } : null;
    return skillMd.trim() ? { source, skill_md: skillMd } : null;
  };
  const body = payload();

  const submit = async () => {
    if (!body) return;
    setSubmitting(true);
    setErr(null);
    try {
      if (effectiveMode === 'governed') {
        const r = await skillsPro.stage(body);
        setStaged(r.skill.name);
      } else {
        setResult(await skills.register(body));
      }
    } catch (e) {
      setErr(errMessage(e));
    }
    setSubmitting(false);
  };

  // Step 2 of the governed flow, or the final verdict of the quick install.
  if (staged) {
    return (
      <div className="space-y-4">
        <p className="text-xs uppercase tracking-wide text-[var(--ao-text-muted)]">Governed registration · step 2 of 2</p>
        <AnalyzeStep name={staged} />
        <div className="flex justify-end"><Button variant="secondary" onClick={onClose}>Close</Button></div>
      </div>
    );
  }
  if (result) {
    return (
      <div className="space-y-4">
        <VerdictResult result={result} />
        <div className="flex justify-end"><Button variant="secondary" onClick={onClose}>Close</Button></div>
      </div>
    );
  }

  const tab = (m: Mode, label: string) => (
    <button
      key={m}
      type="button"
      onClick={() => setMode(m)}
      aria-pressed={effectiveMode === m}
      className={`px-3 py-1.5 rounded-lg text-xs font-medium ${
        effectiveMode === m
          ? 'bg-[var(--ao-brand)] text-white'
          : 'bg-[var(--ao-surface-hover)] text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'
      }`}
    >
      {label}
    </button>
  );

  return (
    <div className="space-y-4">
      {proAvailable ? (
        <div className="flex gap-2">
          {tab('install', 'Quick install')}
          {tab('governed', 'Governed (stage, analyze, approve)')}
        </div>
      ) : (
        <p className="text-xs text-[var(--ao-text-muted)]">
          Governed registration (stage, optionally choose a provider, analyze) requires AgentOven Pro.
        </p>
      )}

      {effectiveMode === 'governed' ? (
        <p className="text-sm text-[var(--ao-text-muted)]">
          Step 1 of 2: stage the bundle. Nothing is sent to a model yet; you choose the provider and confirm in the next step.
        </p>
      ) : (
        <p className="text-sm text-[var(--ao-text-muted)]">
          The skill is fetched and immediately reviewed by a model provider chosen automatically, so its
          bundle is sent to that provider. It is activated only if the review accepts it.
        </p>
      )}

      {err && <InlineError message={err} />}

      <div>
        <label htmlFor="skill-source" className="block text-xs text-[var(--ao-text-muted)] mb-1">Source</label>
        <select id="skill-source" value={source} onChange={(e) => setSource(e.target.value as SourceKind)} className={inputCls}>
          <option value="git">Git repository</option>
          <option value="path">Local path (on the control plane host)</option>
          <option value="inline">SKILL.md content</option>
        </select>
      </div>

      {source === 'git' && (
        <div className="flex flex-wrap gap-3">
          <div className="flex-1 min-w-[220px]">
            <label htmlFor="skill-git-url" className="block text-xs text-[var(--ao-text-muted)] mb-1">Git URL *</label>
            <input id="skill-git-url" value={gitUrl} onChange={(e) => setGitUrl(e.target.value)} className={inputCls} placeholder="https://github.com/owner/skill-repo" />
          </div>
          <div className="w-36">
            <label htmlFor="skill-git-ref" className="block text-xs text-[var(--ao-text-muted)] mb-1">Ref</label>
            <input id="skill-git-ref" value={gitRef} onChange={(e) => setGitRef(e.target.value)} className={inputCls} placeholder="main" />
          </div>
        </div>
      )}
      {source === 'path' && (
        <div>
          <label htmlFor="skill-path" className="block text-xs text-[var(--ao-text-muted)] mb-1">Directory path *</label>
          <input id="skill-path" value={path} onChange={(e) => setPath(e.target.value)} className={inputCls} placeholder="/opt/skills/my-skill" />
        </div>
      )}
      {source === 'inline' && (
        <div>
          <label htmlFor="skill-md" className="block text-xs text-[var(--ao-text-muted)] mb-1">SKILL.md *</label>
          <textarea
            id="skill-md"
            value={skillMd}
            onChange={(e) => setSkillMd(e.target.value)}
            rows={10}
            className={`${inputCls} font-mono text-xs resize-y`}
            placeholder={'---\nname: my-skill\ndescription: What this skill does and when to use it\n---\n\nInstructions…'}
          />
        </div>
      )}

      <div className="flex justify-end gap-2">
        <Button variant="secondary" disabled={submitting} onClick={onClose}>Cancel</Button>
        <Button disabled={!body || submitting} onClick={submit}>
          {submitting
            ? (effectiveMode === 'governed' ? 'Staging…' : 'Verifying…')
            : (effectiveMode === 'governed' ? 'Stage' : 'Register')}
        </Button>
      </div>
    </div>
  );
}

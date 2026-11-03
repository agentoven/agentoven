import { Link, useOutletContext } from 'react-router-dom';
import { AlertTriangle } from 'lucide-react';
import { traces, type Trace } from '../../api';
import { useAPI } from '../../hooks';
import { Card, StatusBadge } from '../../components/UI';
import type { WorkspaceContext } from './AgentWorkspace';

const label = 'text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]';

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 py-2 border-b border-[var(--ao-border)] last:border-b-0 text-sm">
      <dt className="text-[var(--ao-text-muted)] shrink-0">{k}</dt>
      <dd className="text-right min-w-0 [overflow-wrap:anywhere]">{children}</dd>
    </div>
  );
}

export default function Overview() {
  const { agent } = useOutletContext<WorkspaceContext>();
  const runs = useAPI(() => traces.list({ agent: agent.name }));
  const recent = (runs.data ?? []).slice(0, 5);
  const enabledRules = (agent.guardrails ?? []).filter((g) => g.enabled).length;
  const base = `/agents/${encodeURIComponent(agent.name)}`;
  const created = agent.created_at && !agent.created_at.startsWith('0001') ? new Date(agent.created_at).toLocaleDateString() : '';

  return (
    <div className="flex flex-wrap gap-5 items-start px-4 sm:px-8 py-6">
      <div className="flex-[999_1_460px] min-w-0 space-y-5">
        {agent.status === 'burnt' && agent.tags?.error && (
          <div className="p-3 rounded-xl bg-red-500/10 border border-red-500/30 text-sm text-red-300 flex gap-2">
            <AlertTriangle size={16} className="mt-0.5 shrink-0" />
            <div>
              <div className="font-semibold">The last bake failed</div>
              <div className="mt-0.5 [overflow-wrap:anywhere]">{agent.tags.error}</div>
            </div>
          </div>
        )}
        <Card>
          <h2 className={label}>About</h2>
          <p className={`mt-2 text-sm ${agent.description ? '' : 'italic text-[var(--ao-text-muted)]'}`}>{agent.description || 'No description yet. Add one in Configuration.'}</p>
          <dl className="mt-3">
            <Row k="Status"><StatusBadge status={agent.status} /></Row>
            <Row k="Version"><span className="font-mono text-[var(--ao-brand-light)]">{agent.version ? `v${agent.version.replace(/^v/, '')}` : 'unversioned'}</span></Row>
            <Row k="Kind">{agent.mode || 'managed'}{agent.runtime && agent.runtime !== 'agentoven' ? `, ${agent.runtime}` : ''}{agent.framework && agent.framework !== 'custom' ? `, ${agent.framework}` : ''}</Row>
            {agent.mode === 'managed' && <Row k="Behavior">{agent.behavior === 'agentic' ? `Agentic, up to ${agent.max_turns || 10} turns` : 'Answers in one turn'}</Row>}
            {(created || agent.created_by) && <Row k="Created">{created}{created && agent.created_by ? ' by ' : ''}{agent.created_by}</Row>}
            {agent.a2a_endpoint && <Row k="Address"><span className="font-mono text-xs">{agent.a2a_endpoint}</span></Row>}
          </dl>
        </Card>
        <Card>
          <h2 className={label}>Model</h2>
          <dl className="mt-3">
            <Row k="Primary"><span className="font-mono text-[13px]">{agent.model_provider ? `${agent.model_provider} / ${agent.model_name}` : 'none chosen'}</span></Row>
            {agent.backup_provider && <Row k="Backup if it fails"><span className="font-mono text-[13px]">{agent.backup_provider}{agent.backup_model ? ` / ${agent.backup_model}` : ''}</span></Row>}
            {agent.summary_model && <Row k="Summarizes long chats with"><span className="font-mono text-[13px]">{agent.summary_provider ? `${agent.summary_provider} / ` : ''}{agent.summary_model}</span></Row>}
            {(agent.subagents?.length ?? 0) > 0 && <Row k="May delegate to"><span className="font-mono text-[13px]">{agent.subagents?.join(', ')}</span></Row>}
            {(agent.approval_tools?.length ?? 0) > 0 && <Row k="Needs approval for"><span className="font-mono text-[13px]">{agent.approval_tools?.join(', ')}</span></Row>}
          </dl>
        </Card>
        <Card>
          <h2 className={label}>Wiring</h2>
          <ul className="mt-3 grid grid-cols-1 sm:grid-cols-3 gap-3 text-sm">
            <li><Link to={`${base}/ingredients`} className="block p-3 rounded-lg border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]"><div className="text-lg font-semibold">{agent.ingredients?.length ?? 0}</div><div className="text-xs text-[var(--ao-text-muted)]">Ingredients</div></Link></li>
            <li><Link to={`${base}/ingredients`} className="block p-3 rounded-lg border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]"><div className="text-lg font-semibold">{agent.skills?.length ?? 0}</div><div className="text-xs text-[var(--ao-text-muted)]">Skills</div></Link></li>
            <li><Link to={`${base}/guardrails`} className="block p-3 rounded-lg border border-[var(--ao-border)] hover:bg-[var(--ao-surface-hover)]"><div className="text-lg font-semibold">{enabledRules}</div><div className="text-xs text-[var(--ao-text-muted)]">Guardrails on</div></Link></li>
          </ul>
        </Card>
      </div>
      <div className="flex-1 basis-72 max-w-md min-w-0 space-y-5">
        <Card>
          <div className="flex items-center justify-between"><h2 className={label}>Recent runs</h2><Link to={`${base}/traces`} className="text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">All runs</Link></div>
          {recent.length === 0 ? (
            <p className="mt-3 text-sm text-[var(--ao-text-muted)]">No runs yet. <Link to={`${base}/test`} className="text-[var(--ao-brand-light)] hover:underline">Try it in Test</Link>.</p>
          ) : (
            <ul className="mt-3 space-y-1">
              {recent.map((t: Trace) => (
                <li key={t.id}>
                  <Link to={`/traces/${t.id}`} className="flex items-center gap-3 py-2 text-sm hover:text-[var(--ao-brand-light)]">
                    <span className={`w-2 h-2 rounded-full shrink-0 ${t.status === 'completed' || t.status === 'ok' ? 'bg-emerald-400' : t.status === 'error' || t.status === 'failed' ? 'bg-red-400' : 'bg-slate-400'}`} />
                    <span className="flex-1 min-w-0 truncate">{t.created_at ? new Date(t.created_at).toLocaleString() : t.id}</span>
                    <span className="font-mono text-xs text-[var(--ao-text-muted)]">{(t.duration_ms / 1000).toFixed(1)} s</span>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </div>
  );
}

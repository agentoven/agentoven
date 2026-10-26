import { useEffect, useState } from 'react';
import { Link, NavLink, Outlet, useNavigate, useParams } from 'react-router-dom';
import { Code, Flame, RefreshCw, Snowflake, Sun, Trash2 } from 'lucide-react';
import { agents, type Agent } from '../../api';
import { useAPI } from '../../hooks';
import { Button, ErrorBanner, Modal, Spinner, StatusBadge } from '../../components/UI';
import { ActionDropdown, type MenuItem } from '../../components/ActionDropdown';
import { IntegrationPanel } from '../Agents';

/** What every tab of an agent's workspace gets from the layout. */
export interface WorkspaceContext {
  agent: Agent;
  refetch: () => void;
}

const TABS = [
  { to: '', label: 'Overview' },
  { to: 'test', label: 'Test' },
  { to: 'configuration', label: 'Configuration' },
  { to: 'ingredients', label: 'Ingredients' },
  { to: 'guardrails', label: 'Guardrails' },
  { to: 'traces', label: 'Traces' },
];

/** One page per agent: who it is, with a tab for each thing you do to it. */
export default function AgentWorkspace() {
  const { name } = useParams<{ name: string }>();
  const navigate = useNavigate();
  const { data: agent, loading, error, refetch } = useAPI(() => agents.get(name!));
  const [showIntegrate, setShowIntegrate] = useState(false);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState('');

  useEffect(() => { refetch(); }, [name, refetch]);

  if (loading && !agent) return <Spinner />;
  if (error && !agent) return <ErrorBanner message={error} onRetry={refetch} />;
  if (!agent) return <ErrorBanner message="Agent not found" />;

  const base = `/agents/${encodeURIComponent(agent.name)}`;
  const act = async (fn: () => Promise<unknown>) => {
    setBusy(true); setProblem('');
    try { await fn(); refetch(); } catch (e) { setProblem(e instanceof Error ? e.message : 'That did not work'); }
    setBusy(false);
  };

  const ready = agent.status === 'ready';
  const menu: MenuItem[] = [];
  if (ready || agent.status === 'burnt' || agent.status === 'cooled') menu.push({ label: 'Re-cook', icon: <RefreshCw size={14} />, onClick: () => navigate(`${base}/configuration`) });
  if (ready || agent.status === 'active' || agent.status === 'baked') menu.push({ label: 'Cool', icon: <Snowflake size={14} />, onClick: () => act(() => agents.cool(agent.name)) });
  menu.push({
    label: 'Delete', icon: <Trash2 size={14} />, danger: true,
    onClick: () => {
      if (window.confirm(`Delete agent '${agent.name}'? It is retired and can no longer be invoked.`)) {
        act(async () => { await agents.delete(agent.name); navigate('/agents'); });
      }
    },
  });

  return (
    <div>
      <div className="px-4 sm:px-8 pt-5 border-b border-[var(--ao-border)]">
        <nav aria-label="Breadcrumb" className="flex items-center gap-2 text-sm text-[var(--ao-text-muted)] mb-3">
          <span>Build</span><span aria-hidden="true">/</span>
          <Link to="/agents" className="hover:text-[var(--ao-text)]">Agents</Link><span aria-hidden="true">/</span>
          <span className="text-[var(--ao-text)] font-medium">{agent.name}</span>
        </nav>
        <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 min-w-0">
            <h1 className="text-2xl font-bold tracking-tight [overflow-wrap:anywhere]">{agent.name}</h1>
            <StatusBadge status={agent.status} />
            {agent.model_provider && (
              <span className="font-mono text-xs px-2.5 py-1 rounded-lg bg-[var(--ao-surface)] border border-[var(--ao-border)] text-slate-300">
                {agent.model_provider} / {agent.model_name}
              </span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {(agent.status === 'draft' || agent.status === 'burnt') && (
              <Button onClick={() => act(() => agents.bake(agent.name))} disabled={busy}><Flame size={15} className="mr-1.5" /> {agent.status === 'burnt' ? 'Retry bake' : 'Bake'}</Button>
            )}
            {agent.status === 'cooled' && (
              <Button onClick={() => act(() => agents.rewarm(agent.name))} disabled={busy}><Sun size={15} className="mr-1.5" /> Rewarm</Button>
            )}
            {ready && (
              <button type="button" onClick={() => setShowIntegrate(true)} className="inline-flex items-center gap-2 h-10 px-4 rounded-[10px] border border-[var(--ao-border)] text-sm font-medium hover:bg-[var(--ao-surface-hover)]">
                <Code size={16} /> Integrate
              </button>
            )}
            <ActionDropdown items={menu} disabled={busy} label={agent.name} />
          </div>
        </div>
        {problem && <div className="mt-3"><ErrorBanner message={problem} /></div>}
        <nav aria-label="Agent sections" className="flex gap-1 mt-5 overflow-x-auto">
          {TABS.map((t) => (
            <NavLink
              key={t.label}
              to={t.to ? `${base}/${t.to}` : base}
              end
              className={({ isActive }) =>
                `h-11 px-3.5 inline-flex items-center whitespace-nowrap text-sm font-medium -mb-px border-b-2 transition-colors ${
                  isActive ? 'border-[var(--ao-brand)] text-[var(--ao-text)]' : 'border-transparent text-[var(--ao-text-muted)] hover:text-[var(--ao-text)]'
                }`
              }
            >
              {t.label}
            </NavLink>
          ))}
        </nav>
      </div>

      <Outlet context={{ agent, refetch } satisfies WorkspaceContext} />

      <Modal open={showIntegrate} onClose={() => setShowIntegrate(false)} title={`Integrate: ${agent.name}`} width="max-w-3xl">
        <IntegrationPanel agent={agent} />
      </Modal>
    </div>
  );
}

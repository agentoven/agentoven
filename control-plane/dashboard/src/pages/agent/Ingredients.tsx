import { Link, useOutletContext } from 'react-router-dom';
import { Puzzle } from 'lucide-react';
import { Card, EmptyState } from '../../components/UI';
import type { WorkspaceContext } from './AgentWorkspace';

const KIND: Record<string, string> = {
  tool: 'Tool', prompt: 'Prompt', model: 'Model', embedding: 'Embedding model', vectorstore: 'Vector store',
  retriever: 'Retriever', scenario: 'Scenario', skill: 'Skill',
};

/** Only the names of the settings, never their values, which can hold keys and tokens. */
const brief = (config: Record<string, unknown> | undefined) => {
  const keys = Object.keys(config ?? {});
  return keys.length ? `Settings: ${keys.join(', ')}` : '';
};

/** What the agent is made from, besides its model. Change any of it in Configuration. */
export default function Ingredients() {
  const { agent } = useOutletContext<WorkspaceContext>();
  const list = agent.ingredients ?? [];
  const skills = agent.skills ?? [];
  const config = `/agents/${encodeURIComponent(agent.name)}/configuration`;
  return (
    <div className="px-4 sm:px-8 py-6 space-y-5 max-w-5xl">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-[var(--ao-text-muted)]">Tools, prompts, knowledge and skills this agent uses.</p>
        <Link to={config} className="h-9 px-4 inline-flex items-center rounded-[10px] border border-[var(--ao-border)] text-sm font-medium hover:bg-[var(--ao-surface-hover)]">Change in Configuration</Link>
      </div>
      {list.length === 0 && skills.length === 0 ? (
        <EmptyState icon={<Puzzle size={44} />} title="Nothing attached" description="Ingredients give an agent tools, a prompt, or something to look things up in." />
      ) : (
        <>
          {list.length > 0 && (
            <Card className="!p-0 overflow-hidden">
              {list.map((i) => (
                <div key={i.id || `${i.kind}-${i.name}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3.5 border-b border-[var(--ao-border)] last:border-b-0">
                  <div className="min-w-0 flex-1 basis-56">
                    <div className="font-semibold truncate">{i.name}</div>
                    {brief(i.config) && <div className="text-xs text-[var(--ao-text-muted)] truncate mt-0.5" title={brief(i.config)}>{brief(i.config)}</div>}
                  </div>
                  <span className="text-xs px-2.5 py-0.5 rounded-full bg-[var(--ao-surface-hover)] text-slate-300">{KIND[i.kind] ?? i.kind}</span>
                  <span className={`text-xs ${i.required ? 'text-amber-300' : 'text-[var(--ao-text-muted)]'}`}>{i.required ? 'Required' : 'Optional'}</span>
                </div>
              ))}
            </Card>
          )}
          {skills.length > 0 && (
            <Card>
              <h2 className="text-xs font-semibold uppercase tracking-wider text-[var(--ao-text-muted)]">Skills</h2>
              <div className="mt-3 flex flex-wrap gap-2">
                {skills.map((s) => <span key={s} className="text-sm px-3 py-1 rounded-full border border-[var(--ao-border)]">{s}</span>)}
              </div>
              <Link to="/skills" className="inline-block mt-3 text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">Open Skills</Link>
            </Card>
          )}
        </>
      )}
    </div>
  );
}

import { Link, useOutletContext } from 'react-router-dom';
import { Card } from '../../components/UI';
import type { WorkspaceContext } from './AgentWorkspace';

const KIND: Record<string, string> = {
  content_filter: 'Harmful content', pii_detection: 'Personal data', topic_restriction: 'Topic', max_length: 'Length',
  regex_filter: 'Pattern', prompt_injection: 'Prompt injection', custom: 'Custom',
};
const STAGE: Record<string, string> = { input: 'Input', output: 'Output', both: 'Input and output' };

/** The checks this agent runs on what comes in and what goes out. Only a rule that is on is enforced. */
export default function Guardrails() {
  const { agent } = useOutletContext<WorkspaceContext>();
  const own = agent.guardrails ?? [];
  return (
    <div className="px-4 sm:px-8 py-6 space-y-5 max-w-5xl">
      <p className="text-sm text-[var(--ao-text-muted)] max-w-2xl">A rule only checks anything while it is switched on. Attaching a rule switches it on; turning it off is a choice you make here or in Configuration.</p>
      <Card className="!p-0 overflow-hidden">
        {own.length === 0 ? (
          <p className="px-5 py-4 text-sm text-[var(--ao-text-muted)]">No guardrails on this agent. Add some in Configuration.</p>
        ) : own.map((g, i) => (
          <div key={g.id ?? i} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-5 py-3 border-b border-[var(--ao-border)] last:border-b-0 text-sm">
            <span className="font-medium flex-1 basis-48">{KIND[g.kind] ?? g.kind}</span>
            <span className="text-xs text-[var(--ao-text-muted)]">{STAGE[g.stage] ?? g.stage}</span>
            <span className={`text-xs ${g.enabled ? 'text-emerald-300' : 'text-[var(--ao-text-muted)]'}`}>{g.enabled ? 'On' : 'Off'}</span>
          </div>
        ))}
        <div className="px-5 py-3 border-t border-[var(--ao-border)]">
          <Link to={`/agents/${encodeURIComponent(agent.name)}/configuration`} className="text-xs font-semibold text-[var(--ao-brand-light)] hover:underline">Edit guardrails in Configuration</Link>
        </div>
      </Card>
    </div>
  );
}

import { useOutletContext } from 'react-router-dom';
import { Card } from '../../components/UI';
import { AgentEditForm } from '../Agents';
import type { WorkspaceContext } from './AgentWorkspace';

/** The same form the Re-cook dialog used, as a page. Saving re-cooks the agent. */
export default function Configuration() {
  const { agent, refetch } = useOutletContext<WorkspaceContext>();
  return (
    <div className="px-4 sm:px-8 py-6 max-w-4xl">
      <p className="text-sm text-[var(--ao-text-muted)] mb-4">Changing the configuration re-cooks <strong className="text-[var(--ao-text)]">{agent.name}</strong>. Running requests finish on the old version.</p>
      <Card>
        <AgentEditForm key={agent.updated_at} agent={agent} onDone={refetch} />
      </Card>
    </div>
  );
}

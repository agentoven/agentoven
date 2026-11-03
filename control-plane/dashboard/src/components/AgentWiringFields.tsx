import type { Problem } from '../api';
import { describeUnresolved, type FormUnresolved } from '../lib/agentRefs';
import { AgentsField, SkillsField, ToolsField } from './ReferenceFields';

const label = 'block text-xs text-[var(--ao-text-muted)] mb-1';

/**
 * What an agent is wired to besides its model: skills, the tools whose calls wait for a person, and the agents
 * it may hand work to. Each is chosen from what the kitchen has. Delegation and approval apply to a managed
 * agent, so an external one is shown only the skills.
 */
export function AgentWiringFields({ self, managed, skills, approvalTools, subagents, onChange, problems }: {
  /** The agent being edited, which it may not delegate to. Empty while it is being created. */
  self: string;
  managed: boolean;
  skills: string[];
  approvalTools: string[];
  subagents: string[];
  onChange: (patch: { skills?: string[]; approval_tools?: string[]; subagents?: string[] }) => void;
  /** What the server last said was wrong (errors and warnings). */
  problems?: Problem[];
}) {
  const at = (prefix: string) => problems?.filter((p) => p.path.startsWith(prefix));
  return (
    <div className="space-y-4">
      <div>
        <span className={label}>Skills</span>
        <SkillsField label="Skills" value={skills} onChange={(v) => onChange({ skills: v })} problems={at('skills[')} />
      </div>
      {managed && (
        <>
          <div>
            <span className={label}>Approval tools (a person approves each call)</span>
            <ToolsField label="Approval tools" value={approvalTools} onChange={(v) => onChange({ approval_tools: v })} problems={at('approval_tools[')} placeholder="Add a tool that needs approval" />
          </div>
          <div>
            <span className={label}>Subagents (agents this one may delegate to)</span>
            <AgentsField label="Subagents" self={self} value={subagents} onChange={(v) => onChange({ subagents: v })} problems={at('subagents[')} />
          </div>
        </>
      )}
    </div>
  );
}

/** Beside a disabled Save: what has to be replaced or removed first. */
export function UnresolvedNotice({ items }: { items: FormUnresolved[] }) {
  if (items.length === 0) return null;
  return (
    <div role="alert" data-blocking={items.length} className="p-3 rounded-lg border border-red-500/40 bg-red-500/10 text-xs text-red-300">
      <p className="m-0 font-semibold">Replace or remove these before saving. The kitchen does not have them.</p>
      <ul className="m-0 mt-1.5 p-0 list-none space-y-0.5">
        {items.map((u, i) => (
          <li key={`${u.where}-${u.value}-${i}`} className="[overflow-wrap:anywhere]">{describeUnresolved(u)}{u.suggestions.length > 0 && <> (did you mean {u.suggestions.map((s) => `"${s}"`).join(', ')}?)</>}</li>
        ))}
      </ul>
    </div>
  );
}

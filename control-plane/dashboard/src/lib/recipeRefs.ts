// What a recipe's steps name that the flow's own checks (agents, recipes) do not cover: the channels a notify step
// sends to and the tools it sends through. Same shape as recipeFlow's problems, so the builder lists them
// together and keeps Save off until each is replaced or removed.
import { allNodes, titleOf, type FlowNode, type Problem } from './recipeFlow.ts';
import { unresolved, type Options } from './references.ts';

export function stepReferenceProblems(flow: FlowNode[], lists: { channels?: Options; notifyTools?: Options }): Problem[] {
  const out: Problem[] = [];
  for (const n of allNodes(flow)) {
    const s = n.step;
    const t = titleOf(s);
    if (s.kind === 'notify') {
      for (const c of ((s.config?.channels as unknown[] | undefined) ?? []).filter((x): x is string => typeof x === 'string')) {
        const u = unresolved(c, lists.channels);
        if (u) out.push({ id: n.id, message: `"${t}" sends to the channel ${c} (${u.why}).` });
      }
    }
    if (s.kind === 'notify' || s.kind === 'human_gate') {
      for (const tool of ((s.notify_tools as unknown[] | undefined) ?? []).filter((x): x is string => typeof x === 'string')) {
        const u = unresolved(tool, lists.notifyTools);
        if (u) out.push({ id: n.id, message: `"${t}" sends through the tool ${tool} (${u.why}).` });
      }
    }
  }
  return out;
}

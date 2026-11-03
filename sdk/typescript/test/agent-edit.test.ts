import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { applyEdit, type AgentConfig, type AgentEdit } from '../src/refcheck.js';

/** The cases every implementation of an edit runs (sdk/fixtures/agent-edit-cases.json): the control plane, the Python SDK, this one and the Rust client. */
const fixture = JSON.parse(readFileSync(new URL('../../fixtures/agent-edit-cases.json', import.meta.url), 'utf8')) as {
  cases: { name: string; agent: AgentConfig; edit: AgentEdit; result: AgentConfig }[];
};

describe('applyEdit follows the shared cases', () => {
  it('has the cases the other implementations run', () => {
    expect(fixture.cases.length).toBeGreaterThanOrEqual(9);
  });
  for (const c of fixture.cases) {
    it(c.name, () => {
      const agent = structuredClone(c.agent);
      const edit = structuredClone(c.edit);
      expect(applyEdit(agent, edit)).toEqual(c.result);
      expect(agent).toEqual(c.agent);
      expect(edit).toEqual(c.edit);
    });
  }
});

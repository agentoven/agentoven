import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { ProClient } from '../src/pro-client.js';
import { FakeControlPlane } from './fake-server.js';

const api = new FakeControlPlane();
let pro: ProClient;
const RID = '0f1e2d3c-aaaa-bbbb-cccc-0123456789ab';

beforeAll(async () => {
  await api.start();
});
afterAll(async () => {
  await api.stop();
});
beforeEach(() => {
  api.requests = [];
  pro = new ProClient({ url: api.url, apiKey: 'ao_key', kitchen: 'acme' });
});

describe('approvals where people work', () => {
  it('lists requests by status, gate and record', async () => {
    api.reply('GET', '/api/v1/approval-requests?status=open', { requests: [{ id: RID, gate: 'tool_approval', delivered: 3 }] });
    expect((await pro.listApprovalRequests())[0].delivered).toBe(3);
    api.reply('GET', '/api/v1/approval-requests?status=all&gate=recipe_gate&record_id=run+1%3Astep', { requests: [] });
    expect(await pro.listApprovalRequests({ status: 'all', gate: 'recipe_gate', recordId: 'run 1:step' })).toEqual([]);
  });

  it('notify status is the latest request with its timeline, or null', async () => {
    api.reply('GET', '/api/v1/approval-requests?status=all&gate=budget_extension&record_id=t1', { requests: [{ id: RID }] });
    api.reply('GET', `/api/v1/approval-requests/${RID}`, { request: { id: RID }, events: [{ kind: 'notified', channel: 'ops' }] });
    expect((await pro.approvalNotifyStatus('budget_extension', 't1'))?.events[0].channel).toBe('ops');
    api.reply('GET', '/api/v1/approval-requests?status=all&gate=budget_extension&record_id=never', { requests: [] });
    expect(await pro.approvalNotifyStatus('budget_extension', 'never')).toBeNull();
  });

  it('claims, lets go, hands on and snoozes: posts to the request that decide nothing', async () => {
    for (const action of ['claim', 'unclaim', 'decline'] as const) {
      api.reply('POST', `/api/v1/approval-requests/${RID}/${action}`, { request: { id: RID } });
      const call = { claim: pro.claimApprovalRequest, unclaim: pro.unclaimApprovalRequest, decline: pro.declineApprovalRequest }[action];
      expect((await call.call(pro, RID)).id).toBe(RID);
      expect(api.last.method).toBe('POST');
      expect(api.last.path).toBe(`/api/v1/approval-requests/${RID}/${action}`);
    }
    api.reply('POST', `/api/v1/approval-requests/${RID}/snooze`, { request: { id: RID } });
    await pro.snoozeApprovalRequest(RID, 30);
    expect(api.last.body).toEqual({ minutes: 30 });
  });

  it('refuses an unbounded snooze before sending it, and escapes the id', async () => {
    api.requests = [];
    for (const bad of [0, -1, 1441, 1.5]) await expect(pro.snoozeApprovalRequest(RID, bad)).rejects.toThrow(RangeError);
    expect(api.requests).toEqual([]);
    api.reply('POST', '/api/v1/approval-requests/..%2Fadmin/claim', { request: {} });
    await pro.claimApprovalRequest('../admin');
    expect(api.last.path).toBe('/api/v1/approval-requests/..%2Fadmin/claim');
  });

  it('reads and replaces the escalation rules', async () => {
    api.reply('GET', '/api/v1/approval-escalation', { policy: {}, default: { max_reminders: 3 }, gates: ['tool_approval'], min_interval_seconds: 60 });
    expect((await pro.getApprovalEscalation()).min_interval_seconds).toBe(60);
    api.reply('PUT', '/api/v1/approval-escalation', { policy: { '*': {} } });
    const rule = { remind_after_seconds: 3600, remind_every_seconds: 3600, max_reminders: 2, escalate_after_seconds: 0, expire_after_seconds: 0 };
    await pro.setApprovalEscalation({ '*': rule });
    expect(api.last.body).toEqual({ policy: { '*': rule } });
  });
});

# @agentoven/sdk

TypeScript SDK for [AgentOven](https://agentoven.dev) — the enterprise agent control plane.

```ts
import { ProClient } from '@agentoven/sdk';

const pro = new ProClient({ url: 'http://localhost:8080', apiKey: process.env.AGENTOVEN_API_KEY, kitchen: 'default' });
```

## Recipes

Build a recipe the way the dashboard's recipe builder does, check it, create it, run it and answer its approvals.
The steps written are the same as the dashboard writes, so the recipe opens there too. Plain TypeScript: no native module is needed to build one.

```ts
import { Flow, flow, ProClient } from '@agentoven/sdk';
const { agent, ask, check, decide, mentions, together } = flow;

const refunds = new Flow('refund-review', 'Triage a ticket, ask a person about refunds', [
  agent('Triage', 'triage-agent'),
  decide('Needs a person?', mentions('refund', 'chargeback'), {
    yes: [ask('Approve the refund', {
      within: '1 day',
      approved: [agent('Issue the refund', 'refunder')],
      rejected: [agent('Explain the decision', 'writer')],
    })],
    no: [together([agent('Reply', 'writer')], [check('Tone check', 'reviewer')])],
  }),
  agent('Log the outcome', 'logger'),
]);

const pro = new ProClient({ url: 'http://localhost:8080', apiKey: '...' });
await pro.createRecipe(refunds, { checkAgents: true });      // a FlowError lists every problem first
const run = await pro.runRecipe('refund-review', { text: 'I want a refund' });
const state = await pro.waitForRecipeRun('refund-review', run.run_id as string); // returns at a gate, or when finished
if (state.status === 'paused') await pro.rejectGate(run.run_id as string, state.pending_gates![0], 'The tone is too casual');
```

Builders: `agent`, `check`, `lookup`, `decide`, `ask`, `together`, `wait`, `notify`, `subRecipe`, `each`, and `mentions` for a condition.
A `notify` step pushes to a live-feed channel that `listenToChannel({ url, channel, apiKey })` reads.
Also `Flow.add`, `Flow.validate`, `Flow.toWire`, `updateRecipe`, `deleteRecipe`, `listRecipeRuns` and `approveGate`.

## Modalities

A *modality* is something an agent can take in or give out beyond text: `image`,
`pdf`, `video`, `audio` (cascaded voice: speech-to-text in, text-to-speech out)
and `realtime` (live speech-to-speech). Modalities belong to the **provider**: an
agent has exactly the modalities of its own provider. Text is always on and has
no entry. You do not have to set anything: defaults come from the provider
driver and the model catalog.

### Enable or disable modalities on a provider

```ts
import { ProClient, modalities } from '@agentoven/sdk';

await pro.createProvider({
  name: 'openai',
  kind: 'openai',
  api_key: process.env.OPENAI_API_KEY,
  models: ['gpt-4o'],
  modalities: modalities()
    .pdf(false) // switch PDF input off
    .video(true) // widen for a gateway the catalog doesn't know
    .audio({ sttModel: 'whisper-1', ttsModel: 'tts-1' })
    .realtime({ enabled: true, model: 'gpt-realtime' }),
});

// Updates are merged per modality. null / clear() deletes a key (or a whole
// modality) and puts it back on its default.
await pro.updateProvider('openai', { modalities: modalities().clear('pdf').audio({ ttsModel: 'tts-1-hd' }) });

const provider = await pro.getProvider('openai');
console.log(provider.modalities); // effective: ['text', 'image', 'audio', ...]
```

The rules the server enforces are checked before the request is sent (unknown
modality, an entry for `text`, unknown settings, wrong types) and throw
`ModalitiesError`. `audio` and `realtime` cannot be enabled beyond what the
provider kind supports; the server answers 400 (`AgentOvenAPIError`).

### Check what an agent can do

```ts
import { supportsModality } from '@agentoven/sdk';

const card = await pro.agentCard('support-agent');
console.log(card.modalities); // e.g. ['text', 'image', 'audio', 'realtime']
if (supportsModality(card, 'audio')) {
  /* ... */
}
await pro.supports('support-agent', 'pdf'); // shorthand
```

### Send an image or PDF

```ts
import { Attachment } from '@agentoven/sdk';

const session = await pro.createAgentSession('vision-agent');
const reply = await pro.sendSessionMessage('vision-agent', session.id, {
  content: 'Summarise these.',
  attachments: [
    await Attachment.fromPath('chart.png'), // Node; MIME type inferred
    await Attachment.fromPath('report.pdf'),
    Attachment.fromUrl('https://example.com/photo.jpg'),
    Attachment.fromBytes(new Uint8Array(await file.arrayBuffer()), { mimeType: 'image/webp' }), // browser
  ],
});
console.log(reply.content);
```

Audio and video attachments work the same way. Audio is transcribed by the
server first, so any agent whose provider offers `audio` can take it.

### Cascaded voice

```ts
import { audioFromPath, saveAudio, AudioModalityError } from '@agentoven/sdk';

try {
  const r = await pro.invoke('support-agent', {
    audio: await audioFromPath('question.wav'), // or audioFromBytes(...), audioFromBase64(...)
    voiceOutput: true,
    voice: 'alloy',
  });
  console.log(r.transcript); // what the agent heard
  console.log(r.response); // its text reply
  await saveAudio(r.audio!, 'answer'); // decoded bytes in r.audio.data -> answer.mp3
} catch (err) {
  if (err instanceof AudioModalityError) console.log("this agent's provider does not offer the audio modality");
  else throw err;
}
```

### Realtime (live speech-to-speech)

The agent always runs on its own provider, which must offer the `realtime`
modality. Server-side (Node), with your API key:

```ts
const call = await pro.realtime('support-agent', { voice: 'marin' });
call.sendAudio(pcmChunk); // PCM16, 24 kHz, mono
for await (const ev of call) {
  if (ev.type === 'audio') speaker.write(ev.audio!);
  if (ev.type === 'transcript' && ev.final) console.log(ev.role, ev.text);
}
```

If the agent's input guardrails block what the caller said you get an `error`
event, then the loop rejects with a `RealtimeError` (close code 1008) carrying
the reason; `call.closeCode` / `call.closeReason` are set once the call ends.

### Realtime in a browser

Browsers cannot set headers on a WebSocket. Your backend, which holds the API
key, mints a short-lived token (bound to the agent and kitchen, checked only when
the socket opens):

```ts
// backend
const { token, expires_in } = await pro.realtimeToken('support-agent', 60); // 1-300 seconds
res.json({ token });
```

The page connects with no auth headers; the SDK offers the WebSocket
subprotocols `['agentoven.v1', 'agentoven-token.<token>']`:

```ts
// browser
import { connectRealtime } from '@agentoven/sdk';

const { token } = await (await fetch('/my-backend/realtime-token')).json();
const call = await connectRealtime({ url: 'https://agentoven.example.com', agent: 'support-agent', token });
```

## Roles

`restaurateur` (platform-wide, a role of a person in the user directory) and, inside a kitchen, `head_chef`, `chef`,
`baker`, `taster`, `auditor`, `legal`, `finance` and `viewer` (`KITCHEN_ROLES`, `PERSON_ROLES`, the types `KitchenRole`
and `UserRole`). The retired `admin` is refused wherever a role is given (`createUser`, `createServiceAccount`,
`createKitchenBinding`, `setApproverPolicy`): it was a Head Chef inside a kitchen and a Restaurateur across the
installation.

## Agent versions

An agent's version moves only on a bake whose configuration differs from the last bake's. The first bake keeps the
registered version (`0.1.0`), a bake that changes nothing keeps its version, a changed one takes the next minor
version and keeps the old one, and editing a registered agent does not bump it.

```ts
await pro.listAgentVersions('support-agent');          // [{ version: '0.1.0', ... }, { version: '0.2.0', ... }]
await pro.getAgentVersion('support-agent', '0.1.0');   // the agent as it was when that version was baked
```

## Signed decisions (Pro)

A decision at a gate is signed: you prove who you are again and get a single-use token bound to exactly that
decision, sent in the `X-Signing-Token` header (`SIGNING_TOKEN_HEADER`). `sign` does both steps with the password of
your local account; an account that signs in through an identity provider rejects with `OidcSigningRequired` (sign in
the dashboard instead). Every method that takes a decision accepts `{ password }` (it signs for you) or
`{ signingToken }`.

```ts
const token = await pro.sign('legal_hold_release', 'dataset:ds-1', 'approved', { reason: 'case closed' }, password);
await pro.liftDatasetLegalHold('ds-1', 'case closed', { signingToken: token });   // or: { password }

await pro.runRecipe('refunds', { order: 7 }, { environment: 'prod', password });  // gate approval_skip
await pro.signedRequest('POST', '/api/v1/promotions', body, token);               // any other gate
```

Two gates came with the roles: `legal_hold_release` (a dataset's or the audit trail's legal hold, record id
`dataset:<id>` or `audit:<kitchen>`, payload `{ reason }`) and `approval_skip` (running a recipe on an environment that
requires approval, record id `<recipe>@<environment>`, payload `{ input }`). Also: `listSignatures`, `getSignature`,
`verifySignatures`, `getApproverPolicy`, `setApproverPolicy`.

## Replay (Pro)

```ts
const out = await pro.replayCall(traceId, spanId, { variants: [{ label: 'cooler', edits: { temperature: 0.1 } }], reason: 'why' });
const run = await pro.replayRun(traceId, fromSpan, { edits: { model: 'claude-haiku' } });                        // tools frozen
const live = await pro.replayRun(traceId, fromSpan, { tools: 'live', liveTools: ['lookup'], password });         // signed
const capture = await pro.getSpanCapture(traceId, spanId);
```

A replay is an investigation, never evidence: models are not deterministic, and no simulated tool result is ever made
up. `prepareLiveReplay` returns what a live replay signs, computed by the server.

## Governed datasets (Pro)

`listDatasets`, `createDataset`, `getDataset`, `updateDataset`, `addDatasetCase`, `addDatasetCaseFromTrace`,
`listDatasetCases`, `retireDatasetCase`, `eraseDatasetCase`, `publishDataset`, `listDatasetVersions`,
`getDatasetVersion`, `verifyDatasetVersion`, `exportDataset`, `lowerDatasetMinimum`, `placeDatasetLegalHold`,
`liftDatasetLegalHold`, `purgeDataset`, `listExpiringDatasets`, `listDatasetReviews`, `listDatasetTombstones`, and
`placeAuditLegalHold` / `liftAuditLegalHold`. On a regulated dataset publishing, purging and lowering the minimum are
signed (`{ password }` or `{ signingToken }`).

## Identity-provider bindings and credential owners (Pro)

`listKitchenBindings`, `createKitchenBinding(kitchenId, claimValue, role)`, `deleteKitchenBinding`,
`setServiceAccountOwner`, `setScopedKeyOwner`, `myKitchens`. A service account or scoped key belongs to a person
(`ownerEmail` / `owner_email` when it is created); when the owner leaves the kitchen it stops until a Head Chef gives it
to someone.

## Budgets, rate limits and the response cache (Pro)

```ts
await pro.setBudget(500, { period: 'monthly' });                                      // the kitchen, dollars
await pro.setBudget(2_000_000, { unit: 'tokens', agent: 'triage', period: 'daily' });  // one agent (or provider, apiKey)
await pro.setBudget(800, { period: 'monthly', password });                            // a raise is signed (gate budget_raise)
await pro.listBudgets(); await pro.listBudgetLedger(); await pro.listBudgetAlerts(); await pro.budgetUsage('agent', { days: 7 });
await pro.removeBudget({ agent: 'triage', unit: 'tokens', password });

await pro.setLimit('agent', 'triage', { requestsPerMinute: 60, tokensPerMinute: 200_000 });   // '*' for each on its own
await pro.listLimits(); await pro.removeLimit('agent', 'triage');

await pro.setCachePolicy('always', { agent: 'faq-bot', ttlSeconds: 900 });
await pro.getCache(); await pro.purgeCache('faq-bot'); await pro.removeCachePolicy('faq-bot');
```

`RateLimitedError` (HTTP 429: `retryAfter` from `Retry-After`, `scope`, `limit`) and `BudgetExceededError` (HTTP 402:
`scope`, `unit`, `period`, `resetsAt`) extend `AgentOvenAPIError`; the SDK does not retry for you. Lowering a budget or
creating one needs no signature; raising or removing one needs `password` or `signingToken` (record id from
`budgetRecordId`). See [docs/budgets-limits-cache.md](../../docs/budgets-limits-cache.md).


## Check a configuration against the kitchen

An agent or a recipe names things: a provider and a model, a compaction model (`summary_provider` + `summary_model`),
tools, skills, prompts, subagents, steps, channels, credentials. The server refuses a write or a bake that names
something the kitchen does not have, with HTTP 422, and the SDK raises it as a `ConfigurationError` (an
`AgentOvenAPIError`) whose `problems` give the `path` of each field, the bad `value` and the nearest valid
`suggestions`; `warnings` is advice that did not refuse it.

```ts
import { ConfigurationError, ProClient } from '@agentoven/sdk';

try {
  await pro.createAgent({ name: 'support', model_provider: 'anthropic', model_name: 'claude-haiku-4-5',
                          summary_provider: 'anthropic', summary_model: 'claude-hiku-4-5' });
} catch (e) {
  if (e instanceof ConfigurationError) for (const p of e.problems) console.log(p.path, p.value, p.suggestions);
  // summary_model claude-hiku-4-5 [ 'claude-haiku-4-5' ]
}

// Ask first, with what the kitchen has now (nothing is saved):
const { valid, problems, warnings } = await pro.validateAgent(agent);        // also validateRecipe(recipe | flow)
await pro.createAgent(agent, { validate: true });   // also updateAgent, bakeAgent, recookAgent, createRecipe, updateRecipe, runRecipe, bakeRecipe
await pro.agentValidate('support');                  // the server's verdict on a stored agent (GET /agents/{name}/validate); recipeValidate too
```

`validate: true` reads the kitchen's lists (`loadCatalog()`), runs the same checks as the server (`checkAgent`,
`checkRecipe`, `nearest`; pinned to the server's by `sdk/fixtures/refcheck-cases.json`) and throws the same
`ConfigurationError` before anything is sent. It is off by default and the server remains the authority: a check here
only saves the round trip. A list you may not read (403) or that your edition does not have (404, for example scenarios
outside Pro) is left unchecked, never treated as empty; roles have no list, so `approver_roles` is left to the server.
The native `registerAgent` and `bake` are not pre-checked: use `createAgent`/`bakeAgent` here for that.

## Timeouts, error bodies and the route contract

A request gives up after 120 seconds (an invocation waits for a model): `new ProClient({ timeoutMs: 30_000 })`. A
failure whose body is not JSON (a proxy's HTML 502 page) is an `AgentOvenAPIError` whose `detail` is the text. Ids
and names are escaped as one path segment, so a name such as `../../x` cannot choose another route.

`registerAgent` brings a retired agent (one that was deleted) back with the same name and id: it creates when the
agent is absent or retired and updates when it is live. There is no `deleteWorldSchema`: the server has no such
route.

`test/route-contract.test.ts` reads every call the client makes and checks its verb and path against
`sdk/fixtures/server-routes.json`, the server's route list generated from its router (Pro repository:
`UPDATE_SDK_ROUTES=1 go test ./internal/routeguard -run SDKRoute`). A method for a route the server does not have
fails the build.

# Claim triage: a Salesforce Case becomes a ServiceNow incident

A banking or insurance claims team keeps cases in Salesforce and works IT and operations incidents in ServiceNow. This example reads a new claim case, proposes an incident, and **creates it only after a person has approved**. It uses the two curated packs ([ServiceNow](../../docs/tool-pack-servicenow.md), [Salesforce](../../docs/tool-pack-salesforce.md)); both are Pro.

```
read_case  (claims-reader: sf_soql_query)           reads the case, no approval needed
   |
assess     (claims-triager: no tools)               proposes severity and the incident text
   |
approve_incident  (human_gate: head_chef, chef)     a person decides, signed
   |
file_incident (claims-filer: sn_incident_create,    each write is held for approval
               sf_add_case_comment)                  and audited
```

Files: `agents.json` (the three agents), `recipe.json` (the recipe).

## Set up

```bash
# credentials, pinned to the vendors' hosts
agentoven secret create sf-token --allow-host acme.my.salesforce.com
agentoven secret create sn-user   --allow-host acme.service-now.com
agentoven secret create sn-pass   --allow-host acme.service-now.com
agentoven packs install salesforce --name sf --set my_domain=acme --set auth_type=bearer --secret token=vault://sf-token
agentoven packs install servicenow --name sn --set instance=acme --set auth_type=basic \
    --secret username=vault://sn-user --secret password=vault://sn-pass
agentoven packs check sf && agentoven packs check sn
```

Tool names are `<install>_<tool>`, so the agents in `agents.json` use `sf_soql_query`, `sn_incident_create` and `sf_add_case_comment`. Change `model_provider` / `model_name` to a provider of yours, create the agents, bake them, then create the recipe from `recipe.json`.

## What approves what

- **Each write is a signed approval.** `sn_incident_create` and `sf_add_case_comment` are write tools: when an agent calls one, its run pauses (`202`, in the approvals queue) until someone with approval rights signs. The comment on the case is internal (`IsPublished: false`); a customer-visible comment needs the install's `allow_public_comments` to be `yes` and still waits for approval.
- **The recipe's `human_gate`** is the decision to file at all, with the triage text in front of the approver.
- Nothing is written to either system without both. The audit trail has `toolpack.write_call` (with the risk class, never the arguments) for each write.

## Running it as a recipe

A recipe's agent steps run in the agent's own process, and that process asks the control plane to run each tool call (it never calls a tool itself), so pack tools work in a recipe step and the same rules apply as when you invoke the agent: the two writes of `claims-filer` each pause until a person approves them (a signed approval, in the approvals queue), and the step carries on when they do. While it waits, the `file_incident` step stays running: it is bounded by the step's `timeout_secs`, `AGENTOVEN_TOOL_APPROVAL_WAIT` (default 4 minutes) and `AGENTOVEN_BACKEND_CALL_TIMEOUT` (default 5 minutes), so give the step a longer `timeout_secs` and raise the other two if your approvers are slower than that. A call nobody decides in time is refused and the tool is not run. `scripts/e2e/e2e_curated_packs.py` runs this recipe to completion against a running server (Pro ADR-0062, ADR-0044 here).

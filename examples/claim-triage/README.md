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

## Today's limit: recipe agent steps cannot call pack tools

A recipe runs each agent step inside the agent's own process, and that process calls a tool's endpoint itself. A pack tool has no endpoint of that kind (its transport is `native`: the control plane makes the call so that approval, pinning and egress rules apply), so inside a recipe step the call fails with a tool error and **nothing is written**: it fails closed. `scripts/e2e/e2e_curated_packs.py` shows both halves against a running server: the same three agents driven through the control plane (read, assess, then two signed approvals, incident created, internal comment written) and the recipe run (the gate releases, no write happens).

Until recipe steps can reach native tools, drive the triage through the agents: invoke `claims-reader`, pass its answer to `claims-triager`, then to `claims-filer`, and approve the two pauses in the approvals queue. That is the flow the end-to-end script runs. The fix belongs to the recipe runtime, not to the packs; it is listed in Pro ADR-0061 "Not done".

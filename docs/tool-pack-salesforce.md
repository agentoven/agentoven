# Salesforce tool pack (Pro)

The `salesforce` pack gives agents governed access to a Salesforce org: SOQL queries, records, Cases, Accounts, Contacts, Opportunities, case comments, Chatter posts and object descriptions. It is a **curated pack** written against Salesforce's documented REST API (see [curated-tool-pack-format.md](curated-tool-pack-format.md)); read [tool-packs.md](tool-packs.md) first for approval, pinned credentials and the network allowlist.

> Status: tested against in-process fakes that mimic the documented API shapes (`internal/toolpacks/vendorfake`). **No Salesforce org has been used yet.** What a real org must confirm is at the end of this page and in Pro ADR-0061.

## The address is fixed

You give your **My Domain** name, and the pack connects to `https://<my_domain>.my.salesforce.com` and nowhere else. `environment` selects `sandbox` (`<my_domain>.sandbox.my.salesforce.com`, where a sandbox's My Domain looks like `acme--uat`), `developer` (`.develop.my.salesforce.com`) or `scratch` (`.scratch.my.salesforce.com`). The My Domain is only the part before the domain (letters, digits, hyphens), so it cannot point elsewhere. The OAuth token response's `instance_url` is **ignored**: every call goes to the host you pinned.

## Tools

| Tool | Risk | Does |
|---|---|---|
| `soql_query` | read | One validated SOQL `SELECT`; pages with `cursor` |
| `get_record` | read | One record by object and id, optionally limited to `fields` |
| `describe_object` | read | Fields (name, label, type, required, picklist values), references and what the object allows |
| `create_case`, `update_case` | write | Case (Subject, Description, Status, Priority, Origin, Type, Reason, AccountId, ContactId) |
| `create_account`, `update_account` | write | Account (Name, Type, Industry, Phone, Website, billing address, ...) |
| `create_contact`, `update_contact` | write | Contact (FirstName, LastName, Email, Phone, Title, AccountId, mailing address, ...) |
| `create_opportunity`, `update_opportunity` | write | Opportunity (Name, StageName, CloseDate, Amount, AccountId, ...) |
| `add_case_comment` | write | A comment on a case: internal unless `public` is true, and `public` needs the install's `allow_public_comments` to be `yes` |
| `post_chatter` | write | A plain-text Chatter message on a record's feed (one text segment: no mentions, links or markup) |

Every **write waits for a person's signed approval**. Standard fields are named in each tool's schema; your own `__c` fields go in `custom_fields`, and only `__c` names are accepted there.

## What a SOQL query may be

`soql_query` accepts one statement beginning `SELECT`: no semicolon, no comment, no character outside SOQL's alphabet, balanced parentheses and strings that end. The words `INSERT UPDATE DELETE UPSERT MERGE UNDELETE` are refused (which also refuses `FOR UPDATE`), as are `FOR VIEW`, `FOR REFERENCE` (they write the recently-viewed list) and `ALL ROWS` (it reads the recycle bin). The object after the statement's own `FROM` must be in the install's `allowed_objects` (default Account, Contact, Case, Opportunity, CaseComment; `*` for any). A missing `LIMIT` is added and a larger one than `max_rows` (default 1000, at most 2000) is refused. Results come in pages of `page_size` rows (200 to 2000); the answer carries `next_cursor` and the next call passes it as `cursor`. A cursor is checked against the shape of Salesforce's query locator and becomes one path segment under the pinned host; the `nextRecordsUrl` Salesforce sends is never followed.

Be clear about what this does not do: relationship paths and sub-selects (`Account.Owner.Email`, `(SELECT ... FROM Contacts)`) can read fields of other objects than the one queried. `allowed_objects` governs the root object of a query and the object of every record tool; what the integration user can see is limited by its profile and permission sets, which is where field-level security belongs.

## Authentication

| `auth_type` | Secrets |
|---|---|
| `bearer` (default) | `token`: an access token you obtained yourself; it expires with the session, so rebind when it does (a 401 is reported as `auth_failed` with that hint) |
| `oauth2_client_credentials` | `client_id`, `client_secret`: the consumer key and secret of a connected app (or external client app) with the client credentials flow enabled and a "run as" user; the pack exchanges them at `/services/oauth2/token` on your pinned My Domain (form fields), caches the token in memory and fetches a new one once on a 401 |

Create the credentials with `allowed_hosts` set to your My Domain host. Run the app as an **integration user whose profile and permission sets give only what the tools need**.

## Install

```bash
agentoven secret create sf-cid --allow-host acme.my.salesforce.com
agentoven secret create sf-csec --allow-host acme.my.salesforce.com
agentoven packs install salesforce --name sf --set my_domain=acme \
    --set auth_type=oauth2_client_credentials \
    --secret client_id=vault://sf-cid --secret client_secret=vault://sf-csec
agentoven packs check sf                                # GET /services/data/v62.0/limits
```

Or use the **Tool packs** page. The API version is the setting `api_version` (default `v62.0`).

## Limits and errors

- 120 reads and 20 writes a minute per install by default, over budget answers `rate_limited` and never reaches Salesforce (in-memory, per server process).
- Failures come back as `{"error": {"kind", "vendor_code", "message", "retryable", "hint"}}`. Salesforce's own message is kept (`Required fields are missing: [LastName]`) with the kind: `REQUEST_LIMIT_EXCEEDED` is `quota_exceeded` (not retryable now), `INVALID_SESSION_ID` is `auth_failed`, `UNABLE_TO_LOCK_ROW` is `conflict` (retryable), `MALFORMED_QUERY` / `INVALID_FIELD` / `REQUIRED_FIELD_MISSING` are `invalid_request` with a hint to use `describe_object`.

## What a real org has to confirm

1. The client credentials flow: token endpoint on the My Domain, the response shape (no `expires_in`; the pack caches for 4.5 minutes and refreshes on a 401), form-field client authentication.
2. `Sforce-Query-Options: batchSize=<n>` (valid range 200 to 2000) and the `nextRecordsUrl` locator shape (`01g...-2000`) your org returns.
3. `POST /sobjects/CaseComment` with `IsPublished` and the Chatter `POST /chatter/feed-elements` body, with the integration user's permissions.
4. API version: `v62.0` is the default; set the one your org's integration is certified on.
5. Sandbox and scratch My Domain host forms for your org (`acme--uat.sandbox.my.salesforce.com`).
6. Standard-field names and picklist values of Case, Account, Contact and Opportunity in your org (they are customisable), via `describe_object`.

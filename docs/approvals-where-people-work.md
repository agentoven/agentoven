# Approvals where people work (Pro)

When a person is asked to decide something, AgentOven Pro tells the people who can decide it, through the kitchen's notification channels, keeps asking, and asks someone more senior if nobody answers. It never lets anyone decide from chat: **a decision is a signature, made in the dashboard**, after logging in and confirming it is you.

What is announced: a recipe's human gate, an agent's tool call that needs approval, a run stopped at its budget, a run that kept repeating itself, an environment promotion that needs approval.

## What a message says

What is being decided, who asked for it, why it waits on a person, the amount if there is one (a run's spend against its cap), when it will be refused if nobody decides, who was asked, and a link to the signing sheet. A tool call's arguments and a recipe step's inputs are **not** in the message (they may be personal data): the message names the tool or step, and the arguments are shown in the signing sheet.

The link opens the Approvals page on that request after the person has logged in. It is one-time for each person, bound to the request and the kitchen, and grants nothing: the Approve and Reject buttons still ask you to confirm it is you. A person who could not sign the request (the wrong role, not named by the approver policy, or in a GxP kitchen the person who started it) is turned away without using the link up.

## Set it up

1. **Channels.** Create notification channels as usual (`agentoven` dashboard, Channels; or `POST /api/v1/channels`). Slack, Teams, Discord, e-mail, webhook and Zapier get approval messages. A channel's URL (and an e-mail channel's SMTP host) follows the same rule as a tool endpoint: loopback, private and cloud-metadata addresses are refused when the channel is saved and again when a message is sent, unless `AGENTOVEN_TOOL_EGRESS_ALLOW` or the kitchen's egress allowlist names the host (a Slack, Teams or webhook receiver inside your network needs that). A channel can opt out with `"config": {"approvals": false}`. For e-mail set `"to_approvers": true` to send to the people asked (by their address in the user directory) as well as to `to_addresses`.
2. **The address of the dashboard.** Start the server with `AGENTOVEN_DASHBOARD_URL=https://agentoven.example.com`. Without it messages carry no link.
3. **Who is asked.** The kitchen's approver policy (Kitchen settings, Who may approve) says who may decide each gate. People who may decide are asked **most junior role first**; the next stage is the next more senior role that may approve, and so on. A person the policy names one by one is asked in the first stage.
4. **How it is chased** (Kitchen settings, Chasing what waits for a person; `PUT /api/v1/approval-escalation`; `agentoven approvals policy --set rules.json`). A rule can be for everything (`*`), a gate (`tool_approval`) or one thing a gate is about (`recipe_gate:approve-refund`, `budget_extension:my-agent`):

   | field | meaning |
   |---|---|
   | `channels` | where the first message goes (default: every active channel) |
   | `remind_after_seconds`, `remind_every_seconds`, `max_reminders` | reminders to the people asked at the current stage |
   | `escalate_after_seconds` | how long a stage waits before the next is asked |
   | `escalation` | optional stages of `roles`, `users`, `channels` replacing the derived ones |
   | `expire_after_seconds` | after this long the request is **refused**; 0 never |
   | `claim_hold_seconds` | how long "I am looking at it" quiets reminders (default 1 h) |

   With no rule: reminders after 4 h, every 4 h, three times; escalation after a day; **never refused**. A decision that waited for ever before still does. The shortest interval is 60 s (`AGENTOVEN_APPROVAL_MIN_INTERVAL_SECONDS`). A request keeps the deadline it was made with when the rule later changes.
5. **Giving up is a refusal.** At the deadline a recipe gate takes its `on_timeout: branch` path or fails the step (the recipe's own `max_wait_minutes` still applies if it is shorter); a paused run is cancelled; a promotion is rejected. Nothing is ever approved by waiting. Nobody can sign it afterwards.

## Slack and Teams buttons

Chat buttons can say "I am looking at it" (reminders go quiet for a while, at most three times), "not mine" (the next stage is asked now) and "snooze" (an hour; the deadline does not move). A button or message that says approve or reject only replies with the signing link and decides nothing.

- **Slack**: create a Slack app with Interactivity on, request URL `https://<server>/hooks/chat/approvals/slack/<kitchen>/<channel name>`, and use the channel's incoming-webhook URL as the channel's `url`. Put the app's signing secret in a vault credential and name it in the channel config: `"config": {"signing_secret_ref": "vault://slack-signing", "slack_team_id": "T0123"}`. Requests are checked with Slack's `v0` signature (constant time, five-minute window) and each is applied once.
- **Teams**: an incoming webhook for the card, and an outgoing webhook to `https://<server>/hooks/chat/approvals/teams/<kitchen>/<channel name>`; the channel config names the outgoing webhook's security token: `"signing_secret_ref": "vault://teams-token"`, optionally `"teams_tenant_id"`. Say `claim <short id>`, `decline <short id>`, `snooze <short id> 60` to the bot (the short id is in the card).
- The chat user is recorded as what they are, a name from the chat platform, not an AgentOven person; their claim is audited as such.

## See what happened

Approvals page: each waiting row says how many messages went out, reminders, escalation and who is looking at it; **Who was asked** shows the escalation chain and the timeline. CLI: `agentoven approvals list`, `agentoven approvals notify-status <gate> <record id>`, `claim`, `snooze`, `decline`, `policy`. SDK: `list_approval_requests`, `approval_notify_status`, `claim_approval_request`, `snooze_approval_request`, `set_approval_escalation` (Python); the same names in camelCase (TypeScript). Every message, reminder, escalation, claim, link opening and refusal is an audit event (`approval.*`).

`AGENTOVEN_APPROVAL_NOTIFY=off` turns the whole thing off.

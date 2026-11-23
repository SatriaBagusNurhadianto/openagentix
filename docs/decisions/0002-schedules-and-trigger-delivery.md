# ADR 0002: Scheduled runs and delivery of results from triggered agents

- **Status:** Accepted (schedules: option A implemented; result delivery: notify tool in progress)
- **Update 2026-11-23:** the owner kept the recommendation (the CronJob approach, column B of the schedules table). Schedules are implemented as a `schedules[]` values block rendered into CronJobs that call the agent over A2A with `X-User-Id: schedule:<name>` (`tools/a2a-call`, README "Schedules"). On the live cluster a manual run, a memory-scoped pair of runs and a real cron firing completed against the memory-enabled `assistant`, which is the case kagent's own scheduler failed. The delivery part (notify tool) is not built yet. The record below is unchanged evidence; where it says nothing is implemented or proposes the `kagent` namespace for the CronJobs, read it as of 2026-11-22: the implementation uses its own namespace `openagentix-schedules` and a pod-scoped policy peer.
- **Original status:** Proposed. Nothing in the record below was implemented when it was written; it is what was found and what we recommend.
- **Date:** 2026-11-22
- **Evidence:** kagent v1.0.1 source (`proto/kagent/api/v1/scheduled_runs.proto`, `go/core/internal/grpcserver/{scheduledrun,grpcweb,interceptors,policy}.go`, `go/core/internal/controller/scheduledrun/`, `go/core/internal/service/{scheduledrun,session}/`) read through raw GitHub files; live calls on the kind cluster `kind-openagentix` (kagent 1.0.1, Agent Kourier 0.6.0); `spikes/kourier/NOTES.md`; README "Channels and triggers".

## Question

Agents today answer when somebody talks to them (Telegram bridge) or when a signed webhook arrives (Agent Kourier). Two things are missing:

1. **Schedules.** "Run this prompt every weekday at 08:00." kagent 1.0.1 has a scheduled-run API but no CRD, so it cannot be declared in the chart the way agents and bindings are.
2. **Delivery.** When a scheduled run or a webhook trigger finishes, the answer has nowhere to go. Kourier's webhook platform has no reply route (`output: none` only).

## What kagent 1.0.1 offers

`ScheduledRunService` (Create, Get, List, Update, Delete, Trigger, GetExecution, ListExecutions). A schedule has an agent reference (`namespace`, `name`), a config (`name`, 5-field cron `schedule`, `timeZone` default UTC, `prompt` up to 32 KiB, `paused`, `executionTimeout` default 15 minutes) and an `etag` for updates. It is stored in kagent's PostgreSQL (`kagent.scheduled_run`, `kagent.scheduled_run_execution`). Each firing creates an execution and a fresh Session, sends the prompt as an A2A message, and records state (`PENDING`, `RUNNING`, `SUCCEEDED`, `FAILED`, `TIMED_OUT`), `sessionId`, `taskId` and `failureReason`. Delete is a soft delete (`deletedAt`): history stays, no new firing.

### How it is reached (observed)

- It is **gRPC only**, on the controller port **8083**, the same port as A2A. The service name is `kagent.api.v1.ScheduledRunService`; the proto has no HTTP annotations. A GET on `/api/scheduledruns` (and similar guesses) returns an empty `200` from a catch-all, not a REST API.
- The controller serves native gRPC (HTTP/2, `application/grpc`) and **gRPC-Web** on the same port: `POST http://kagent-controller.kagent.svc.cluster.local:8083/kagent.api.v1.ScheduledRunService/<Method>` with `Content-Type: application/grpc-web+proto`, `X-Grpc-Web: 1`, and a protobuf body framed as one flag byte (0) plus a 4-byte big-endian length plus the message. A leading `/api` is stripped. The status comes back in the response trailers (`grpc-status: 0`).
- **Identity in insecure mode** (our `server.auth.mode`): the controller forwards two request headers into the call, `Authorization` and `X-User-Id`. The value of `X-User-Id` becomes the principal and therefore the schedule's `creator`. Every method is scoped to the creator: another user id does not see the schedule. A request without the header was answered (`200`) too; which principal it maps to was not checked. Like everything on 8083 this is an unauthenticated claim.
- **Network position.** The NetworkPolicy `openagentix-kagent-controller` admits only the `kagent` namespace, the Telegram bridge pod, `atenet-egress`, Kourier's pod when channels are on, and `networkPolicy.extraA2ACallers`. The calls below were made from a throw-away pod in the `kagent` namespace (an allowed position); the policy was not changed. A reconciler would run there too (or be listed in `extraA2ACallers`, pod-scoped).
- Reflection is a server option (`Reflection` in `grpcserver.Config`); we did not test whether it is on. Clients need the proto (`buf.validate` imports included), which is easy to generate and should be vendored with a pin if we build on it.

Requests, as sent (JSON form of the protobuf; the real body is binary):

```
CreateScheduledRun  X-User-Id: sched-probe
{"agent":{"namespace":"kagent","name":"assistant"},"requestId":"adr0002-probe-1",
 "config":{"name":"adr0002-probe","schedule":"57 16 * * *","timeZone":"UTC",
           "prompt":"Reply with exactly the single word PONG-SCHED and nothing else. Do not use any tool.",
           "executionTimeout":"300s"}}
-> {"scheduledRun":{"id":"f1663663-...","creator":"sched-probe","etag":"6c13108c-...",
    "nextExecutionTime":"2026-11-22T16:57:00Z", ...}}   grpc-status: 0
ListScheduledRuns {"page":{"limit":10}}                    -> {"page":{}}   (empty before, empty after deleting)
ListScheduledRunExecutions {"scheduledRunId":"f1663663-..."}
TriggerScheduledRun {"scheduledRunId":"e743c8ff-...","requestId":"adr0002-trigger-1"}
DeleteScheduledRun  {"scheduledRunId":"..."}               -> deletedAt set
```

## What happened when it ran

Two agents, three kinds of firing. Model calls used for this part: 1 (a direct A2A control call).

1. **Cron firing on `assistant`** (created 16:54:27, due 16:57:00 UTC). The execution row appeared at 16:57:00.7 (the scheduler polls about once a second), a Session was created at once, an A2A task was created about 10 seconds later and finished `FAILED` at 16:57:12: `failureReason: "A2A task TASK_STATE_FAILED"`, and the task's status text is **"agent run failed: preload memory search failed: memory requires caller identity"**. The scheduler dispatches with the control-plane identity, not with the creator's `X-User-Id`; `assistant` has long-term memory on, and kagent's memory preload refuses a call with no caller identity. So **a scheduled run cannot drive a memory-enabled agent in 1.0.1.** The session itself is owned by the creator (`runtime_instance.user_id = sched-probe`).
2. **Firings on a second agent without memory or tools** (a temporary `schedprobe` agent added through a values file, removed afterwards): one manual `Trigger` (16:59:35), one cron firing (17:01:00) and a second `Trigger` (17:02:37) all stayed `RUNNING` with no `taskId`. The controller logged `failed to advance scheduled execution ... input was not accepted; retry after the session becomes available` exactly 10 seconds after each dispatch (`reserveDispatch` waits 10 s for the session's dispatch slot, then returns `ErrSendNotAccepted`). The scheduler had already claimed the dispatch, and its code says a claimed send without a task id "remains uncertain until history appears or the deadline expires" and is never re-sent, so these executions do not recover. At 17:02:16 a direct A2A call to the same agent, from the same network position, answered `PONG-DIRECT`, so the agent was healthy. I did not find the root cause (the dispatch slot was empty in `kagent.session` afterwards; likely a race between session creation and the first send on a cold sandbox, since the cold start takes about 10 seconds, but that is a guess). **I did not observe a successful scheduled execution.**
3. After `DeleteScheduledRun` both schedules show `deletedAt`, `ListScheduledRuns` returns nothing, and `kagent.scheduled_run.next_execution_time` is empty: nothing is left scheduled. Executions already started are not cancelled by deleting their schedule; the three stuck ones were still `RUNNING` after their deadlines (5 minutes) and ended `TIMED_OUT` (3 rows `TIMED_OUT`, 1 `FAILED` in `kagent.scheduled_run_execution` at the end), with the probe agent already removed.

### Where results go

Nowhere but kagent's store. The execution row holds state, `sessionId`, `taskId`, `failureReason`; the answer is in the task history (`kagent.session_task`, protobuf bytes, readable only through A2A `GetTask` or SQL). There is no push, no webhook and no channel for the result. The UI lists schedules and their sessions; that is the only consumer.

### Whose user id and memory scope

The `creator` (the `X-User-Id` at creation) owns the schedule, the executions and the sessions. Memory is keyed by agent and user id, but, see 1, the scheduled send carries no caller identity into the agent, so memory-enabled agents fail and there is currently no way to give a schedule a memory scope.

## Related gap: webhook triggers have no reply route

Kourier's webhook platform only supports `output: none`: it can start a turn, but it cannot post the answer anywhere. The same is true of schedules. Both need the **agent** to deliver its result through a tool.

## Options

### Schedules

| | A. `schedules[]` values reconciled into kagent's API | B. Kubernetes `CronJob` posting A2A | C. `CronJob` posting a signed webhook to Kourier |
|---|---|---|---|
| Declarative in the chart | Values, but needs a Helm hook Job or an install step that diffs schedules by name against the API (list, create, update with etag, delete) | `schedules[]` renders `CronJob` objects: plain Kubernetes, `helm diff` and `kubectl` work | Same as B, plus a Kourier connection and binding per schedule source |
| What runs the timer | kagent's scheduler (leader-elected, in the controller) | the Kubernetes CronJob controller | the Kubernetes CronJob controller |
| Caller identity and memory | **Broken in 1.0.1** for memory agents (no identity) | works: the job sets `X-User-Id: schedule:<name>`; a direct A2A call with an id answered normally | works: Kourier sets `X-User-Id` from the binding's `userId` |
| Dedupe, limits, thread continuity | execution ledger, no limits | none (`concurrencyPolicy: Forbid` helps) | Kourier gives body-hash dedupe, rate limits, `maxRunsPerDay`, one thread per key |
| Reliability seen | dispatch stuck on a cold agent, no retry | A2A call is the same path the bridge uses and worked | the same path as the proven webhook test |
| State outside the chart | rows in kagent PostgreSQL, easy to drift or lose | none | none |
| Needs a signing secret per schedule | no | no | yes (HMAC; one Secret per connection, generated by `install.sh`) |
| Effort | 3 to 5 days: gRPC client (Go or the grpc-web framing in a small Job image), diff and etag logic, pruning, tests; plus waiting on upstream fixes | 1 to 2 days: template, a pinned `curl` image, values, tests, README | 2 to 3 days: B plus signing in the job, a connection and a binding per source, tests |
| Main risk | upstream bugs above; API is gRPC-only and may change | the job image must be allowed on port 8083 (run it in `kagent`, or list it in `extraA2ACallers`, pod-scoped) | Kourier is 0.x with one maintainer |

### Delivery of results

| | 1. A "notify" MCP tool | 2. Poll kagent's store and forward | 3. Slack `output: live` binding |
|---|---|---|---|
| Idea | A small MCP server with tools like `notify_telegram(chat, text)` and `notify_slack(channel, text)`, attached to the vMCP like the knowledge base, allowed per agent in `tools:` | A custom job reads finished executions/tasks and posts them | Use Kourier's Slack platform for scheduled and triggered turns |
| Works for webhook triggers and schedules | yes, the agent decides what to send and where | yes | only for Slack-origin events (a webhook binding cannot use `live`) |
| Uses what exists | Same pattern as `services/kb` (Go, MCP, vMCP entry, NetworkPolicy); Telegram token and Slack token stay in the tool's Secret, never in an agent | No | Yes, but tokens and design untested here |
| Risks | The destination must be constrained (a fixed allowlist of chat ids and channels in config, not model-chosen free text) or a prompt-injected agent can message anyone; the vMCP's permit-all Cedar policy lets any agent that lists the tool call it | Duplicated delivery, parsing protobuf blobs, no trigger context | Not testable until Slack tokens exist; no help for Telegram |
| Effort | 2 to 4 days: service, tests, chart block, allowlist, README | 3 to 5 days and brittle | 0.5 day of values, after Slack is tested |

## Recommendation

1. **Schedules: option B first, with a path to C.** Add a `schedules[]` values block that renders one `CronJob` per entry (agent from `agents[].name`, validated like bindings; `schedule` and `timeZone`; `prompt`; `userId` default `schedule:<name>`; `concurrencyPolicy: Forbid`; short `activeDeadlineSeconds`) in the `kagent` namespace, calling the agent's A2A endpoint with `A2A-Version: 1.0` and an `X-User-Id`. It needs no new service, works with memory-enabled agents today, and is plain Kubernetes. Move to C (job posts a signed webhook to a Kourier connection) only if we want Kourier's limits, dedupe and thread continuity for schedules, since it adds a secret per source. Do not build on kagent's `ScheduledRunService` until upstream passes a caller identity to the agent and a cold dispatch no longer sticks; re-test with `kagent` newer than 1.0.1 and open an upstream issue for both observations. Effort for B: about 1 to 2 days.
2. **Delivery: a small "notify" MCP tool** (option 1), built like the knowledge base service, with destinations restricted by configuration (named destinations such as `ops-telegram`, `eng-slack`, each mapping to one chat id or channel in the service's Secret/config, never free-form ids from the model), logging every send, and reachable only from the vMCP. It serves schedules, webhook triggers and ordinary chats alike, and it keeps the bot tokens out of agents. Consider Kourier Slack `live` output only for Slack-origin events after Slack is tested. Effort: about 3 days.
3. Order: B, then the notify tool, then re-evaluate native scheduled runs.

## What this decision would not change

The Telegram bridge stays our own component (Kourier has no DMs). The default render of the chart stays unchanged when `schedules` is empty.

## Open questions

- Should a schedule's result be allowed to go to a chat at all without a human having asked for it (budget, spam, prompt injection through fetched content)? A per-destination rate limit in the notify tool is probably required.
- Which memory scope should a schedule have: its own (`schedule:<name>`), or the owner's? One scope per schedule is the safe default, consistent with one scope per Kourier Binding.
- What caused the stuck dispatch on a cold agent (`ErrSendNotAccepted` after exactly 10 seconds)? It needs a repeat with debug logging on the controller, ideally on a node with more headroom than this one, and an upstream issue.
- Does kagent plan a CRD for scheduled runs? If so, option A becomes a values-to-CRD render and the best choice.
- Is `X-User-Id` absent mapped to a shared principal? (Answered `200` here; not traced.) If so, all unlabelled callers share one schedule list.
- Alerting: Kourier exposes metrics for turn outcomes; a CronJob exposes failed jobs. Where do failed schedule runs surface to a human, if delivery itself is by the agent?

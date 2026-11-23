# a2a-call

A tiny A2A 1.0 caller. It sends one prompt to an agent, waits for the task to end, prints a one-line JSON summary and
exits `0` only when the task reached `TASK_STATE_COMPLETED`. The OpenAgentix chart runs it as the pod of each
`schedules[]` CronJob. Go, standard library only.

## What it sends

`POST $A2A_URL`, JSON-RPC `SendMessage`, headers `A2A-Version: 1.0`, `Content-Type: application/json` and
`X-User-Id: $A2A_USER_ID`. The message has a fresh random `messageId`, role `ROLE_USER`, one text part and **no
`contextId`**, so every run is a new conversation (a new kagent session). kagent keys sessions and long-term memory by the
user id, so the same id on every run gives a schedule one memory scope; an empty id is refused before anything is sent
(kagent fails memory-enabled agents that have no caller identity).

The task is read from `result.task`: `status.state`, `id`, and the answer from `artifacts[].parts[].text` (checked
against kagent 1.0.1). A response without `result.task` (a bare message, an empty result) is an error.

## Configuration (environment)

| Variable | Meaning |
|---|---|
| `A2A_URL` | agent endpoint, `http` or `https` (required) |
| `A2A_USER_ID` | caller identity (required) |
| `PROMPT_FILE` / `PROMPT` | the prompt; the file wins |
| `A2A_TIMEOUT` | whole-call timeout, Go duration, default `300s` |
| `MAX_RESPONSE_BYTES` | response body limit, default 4 MiB; larger is an error |
| `PRINT_ANSWER` | `true` adds the answer to the summary. Off by default: answers can contain private data and job logs are widely readable |

Redirects are never followed (a 3xx is an HTTP error).

## Output and exit codes

stdout: one line, e.g. `{"state":"TASK_STATE_COMPLETED","taskId":"...","userId":"schedule:daily","durationMs":9120,"answerChars":212}`.
On failure the line also has `error`, and stderr has `a2a-call: FAILED: <reason>` (a failed task's status text is
included, cut to 300 characters).

| Exit | Meaning |
|---|---|
| 0 | task completed |
| 1 | failed task, input/auth required, canceled, rejected or still working; HTTP error; JSON-RPC error; timeout; malformed or oversized response |
| 2 | invalid configuration (nothing was sent) |

## Develop

`make test` (race detector), `make cover`, `make vet`, `make docker`. The tests run against `httptest` servers.

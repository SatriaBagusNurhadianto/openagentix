# a2a-gate

A small authenticating reverse proxy in front of the kagent controller's A2A port (8083). It is off by default
and is switched on with the chart value `governance.a2a.enabled`. Why it exists, and what it does and does not
protect: `docs/decisions/0006-governance.md` (Question 1) and `docs/diary/features/security.md`.

What it does for each request:

1. Reads `Authorization: Bearer <token>`. Two kinds of token are accepted:
   - a projected ServiceAccount token with the audience `openagentix-a2a`, checked with a Kubernetes
     TokenReview (a positive answer is cached for 30 seconds, a refusal for 5);
   - a static key. Its SHA-256 (64 hex characters) is compared with the files in the mounted keys Secret; the
     file name is the caller name. Agent Kourier sends the key as the Binding's `identity.tokenSecretRef`.
2. Finds the caller's rule in the rules file (JSON, rendered by the chart, holds no secret): the agents the
   caller may reach (`namespace/name`) and the user ids it may assert (`exact`, `prefix` or `fixed`).
3. Passes only these routes, for an allowed agent, and answers 403 for everything else (gRPC and gRPC-Web
   included, content type `application/grpc*`):
   - `POST /agents/<ns>/<name>` and `POST .../message:send`, `message:stream` (also under `/v1/`);
   - `GET /agents/<ns>/<name>/.well-known/agent-card.json` (and `agent.json`).
4. Removes `Authorization`, `Cookie`, `X-User-Id`, `X-Agent-Name`, `X-Share-Token`, `Baggage`, the `X-Forwarded-*`
   headers and the whole query string (kagent reads `?user_id=` before `X-User-Id`), then sets `X-User-Id`
   itself from the rule. The controller stays in `insecure` mode and never sees a caller credential.
5. Streams the response with an immediate flush (SSE).
6. Writes one JSON audit line to stdout: `time`, `caller`, `agent`, `user_id`, `context_id`, `trace_id`, `status`,
   `duration_ms`, `method` and, for a refusal, `reason`. `user_id` is a salted hash by default (`h:` and 8 hex
   digits; the salt is random per process); `A2A_GATE_AUDIT_USER_ID=raw` logs the id itself. The gate reads at
   most `A2A_GATE_SNIFF_BYTES` of a request body to find `contextId`, and never logs or keeps the body.

## Settings (environment)

| Variable | Default | Meaning |
|---|---|---|
| `A2A_GATE_UPSTREAM` | required | the controller, `http://kagent-controller.kagent.svc.cluster.local:8083` |
| `A2A_GATE_RULES` | required | path of the rules JSON |
| `A2A_GATE_KEYS_DIR` | `/etc/a2a-gate/keys` | the key hashes (one file per caller) |
| `A2A_GATE_AUDIENCE` | `openagentix-a2a` | audience projected tokens must carry |
| `A2A_GATE_AUDIT_USER_ID` | `hash` | `hash` or `raw` |
| `A2A_GATE_SNIFF_BYTES` | `65536` | body bytes read to find `contextId` (at most 1 MiB) |
| `A2A_GATE_MAX_BODY` | `8388608` | largest request body |
| `A2A_GATE_ADDR` | `:8080` | listen address |

The gate needs the `system:auth-delegator` ClusterRole for TokenReview (the chart binds it). It has no
dependency outside the Go standard library.

## Tests

`make test` runs everything with `-race` and needs nothing but Go.

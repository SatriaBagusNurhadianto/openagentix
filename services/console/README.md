# console

The OpenAgentix web console (ADR 0007, "Console"): a small Go service with a JSON API, and the single-page app (Svelte, in
`console/` at the repository root) embedded in the binary with `go:embed`. One image, no Node at run time. The design
contract is `docs/console/UI-SPEC.md`.

It has no dependency outside the Go standard library and no Kubernetes client library: `internal/kube` sends requests that
this program builds.

## The six rules of the ADR, and where they are kept

1. **No relay.** The routes below are the whole API; each is written out in `internal/server`. The Kubernetes client has no
   code path that copies a header from the browser's request. `TestNothingFromTheBrowserIsForwardedUpstream` asserts on
   what the fake API server received, and `TestOnlyFixedUpstreamPaths` on the paths.
2. **Narrow identity.** The console's ServiceAccount reads (see "Deploying") and writes only `agentdefinitions.openagentix.io`.
3. **Closed network.** The chart's NetworkPolicy admits nobody; the console is reached with `kubectl port-forward`.
4. **Login.** A bearer token from a mounted Secret, compared in constant time, read again when the file changes. Strict CSP.
5. **One operator identity.** Every user acts as `console`.
6. **Only real data.** The API returns what the cluster and the two mounted files hold, nothing else. The demo mode says so.

## Routes

| Route | Does |
|---|---|
| `GET /healthz` | liveness and readiness; no token |
| `GET /api/v1/session` | who am I (`console`), role (`operator` or `viewer`), `canWrite`, `demo`, version, namespace |
| `GET /api/v1/platform` | catalog and policy summary read from the two mounted JSON files; no secrets, no images |
| `GET /api/v1/agents` | built definitions plus chart agents (`source: "chart"`, read-only), `used` = built definitions counted by `maxAgents` |
| `POST /api/v1/agents` | create a definition: body `{name, spec}`; 201, 409 `exists`, 422 `invalid` |
| `POST /api/v1/agents/validate` | dry run: the API server's schema check with `dryRun=All` (nothing is written) and a **predicted** list |
| `GET /api/v1/agents/{name}` | spec, status, conditions, YAML text; for a chart agent what could be read of it, `readOnly: true` |
| `PUT /api/v1/agents/{name}` | update `spec`; body `{resourceVersion, spec}`; 409 `conflict` when the version is stale |
| `DELETE /api/v1/agents/{name}` | delete a definition; 202 (the controller's finalizer removes the rest) |
| `/` | any other path: the web app for `GET` and `HEAD` (files, then `index.html` for single-page routes), a JSON 404 or 405 otherwise |

Everything under `/api/` needs `Authorization: Bearer <token>` (401 otherwise, `WWW-Authenticate: Bearer`). Methods other than
GET, HEAD, POST, PUT and DELETE are 405. Errors are `{"error": {"code", "message"}}` with one plain sentence.

**Chart agents.** kagent `agents.api.kagent.dev` objects that carry no `openagentix.io/agent-definition` label are listed as
`source: chart`. Their template, harness and model config are read (best effort; a refusal becomes a `warnings` entry) to
fill in model, tools, skills and memory. They cannot be written: `PUT` and `DELETE` answer 403 `read_only`.

**What validate returns.** `schema` is the API server's own answer to a dry run (the CRD's limits and name rules). `predicted`
is a list computed by a **small rule set written for the console** (`internal/predict`): unknown model or profile, a tool the
profile does not offer, knowledge, notify or memory the policy forbids, a skill outside the catalog, an egress origin the policy
patterns do not match, a reserved name, the agent count. The response says `"prediction": true` and carries a note. The
controller's `status.violations` stay the authority. The controller's validator is in `services/control-plane/internal/policy`,
a Go internal package another module cannot import, and the console does not copy it. **Follow-up:** move the validator to a public
package so the console, the controller and the chart tests share one implementation, and the prediction becomes the real answer.

## Resource shape

The console reads and writes `AgentDefinition` in the shape of ADR 0012 ("Change now"): `spec.knowledge` and `spec.notify` are
`{enabled, collections|destinations}`, `spec.skills` is `[{name, version?}]`, `spec.suspend` pauses an agent (state `paused`).
The old boolean and string-list forms are refused (400, unknown or mistyped field). From `status` it reads only the public contract:
`conditions`, `observedGeneration`, `endpoint`, `revision`, `effectiveTools`, `effectiveEgress`, `violations`, `capabilities`,
`agentRef`; `status.controller` is passed on as internal and never relied on. Labels `openagentix.io/team` and `openagentix.io/project`
are shown as tags. Chart agents are read from kagent objects and mapped to the same shape.

## Settings (environment)

| Variable | Default | Meaning |
|---|---|---|
| `CONSOLE_ADDR` | `:8080` (`127.0.0.1:8080` in demo mode) | listen address |
| `CONSOLE_TOKEN_FILE` | required | file holding the login token (the mounted Secret); read again when it changes |
| `CONSOLE_CATALOG_FILE` | required | the platform catalog JSON the controller reads |
| `CONSOLE_POLICY_FILE` | required | the platform policy JSON the controller reads |
| `CONSOLE_NAMESPACE` | the catalog's `namespaces.agents` | the only namespace the console reads and writes |
| `CONSOLE_VIEWER` | off | `1` makes every write 403 (one role, read only) |
| `CONSOLE_KUBE_API` | `https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT` | API server address |
| `CONSOLE_KUBE_TOKEN_FILE` | `/var/run/secrets/kubernetes.io/serviceaccount/token` | ServiceAccount token, read again for every request |
| `CONSOLE_KUBE_CA_FILE` | the ServiceAccount `ca.crt` when the address is in-cluster | cluster CA |
| `CONSOLE_KAGENT_API` | `api.kagent.dev/v1alpha3` | group and version of the kagent kinds read for chart agents |
| `CONSOLE_CONTEXT` | empty | text shown under the wordmark (for example `kind-openagentix · kagent`) |
| `CONSOLE_REQUEST_TIMEOUT` | `15s` | per-request deadline, also passed to the Kubernetes calls |
| `CONSOLE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CONSOLE_DEV_FAKE` | off | demo mode, see below |
| `CONSOLE_DEV_FAKE_SETTLE` | `8` | demo mode: seconds before the fake controller gives a new or changed definition a verdict (0: never) |

## Extension points (ADR 0010)

In package `github.com/openagentix/openagentix/services/console/ext` (not `internal`, so a separate build can implement them and
call `server.New`, once that package is made public too; today only `ext` is public):

```go
type Authenticator interface { Authenticate(r *http.Request) (Identity, error) }      // error: 401
type Authorizer    interface { Authorize(ctx, Identity, Action, Resource) Decision }  // not allowed: 403 with Decision.Reason
type AuditSink     interface { Record(ctx, AuditEvent) }                              // one call per write
```

Community implementations (`internal/builtin`): `TokenAuth` (bearer token from a file, constant-time compare), `RoleAuthz` (one
role that may do everything; `CONSOLE_VIEWER` makes writes 403) and `LogAudit` (one JSON log line per write on stdout:
`who`, `action`, `agent`, `outcome`, `status`, `request_id`). `AuditEvent` has no field for a body, an instruction text or a
token, so a sink cannot log them. Denied writes are audited too (`outcome: denied`).

## Protections

- `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'` and
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy: same-origin` on **every** response; `Cache-Control: no-store` on the API.
- A `POST`, `PUT` or `DELETE` with a foreign `Origin`, or `Sec-Fetch-Site` other than `same-origin` or `none`, gets 403 `cross_origin`.
- Bodies must be `application/json` (415), at most 256 KiB (413), without unknown fields or trailing data (400).
- Header block at most 16 KiB (431), read deadline, per-request deadline (504), `{name}` in a path must be a definition name.
- A write is sent to the API server with the console's own ServiceAccount token and `fieldManager=openagentix-console`.

## Demo mode

`CONSOLE_DEV_FAKE=1` serves an in-memory fake with the demo data of the design mockup: chart agents `assistant` and
`profile-demo`; built agents `researcher` (Ready), `cve-triage` (Refused, with the two violation sentences) and `release-notes`
(Pending). No cluster is read or written. The token is `demo-token` (printed in the log) unless `CONSOLE_TOKEN_FILE` is set.
`/api/v1/session` and `/api/v1/platform` report `demo: true`, and only then does the web app show its "Demo data" tag. The
service refuses to start when `CONSOLE_DEV_FAKE` is set together with `CONSOLE_KUBE_API`, `CONSOLE_CATALOG_FILE`,
`CONSOLE_POLICY_FILE`, `CONSOLE_KUBE_TOKEN_FILE`, `CONSOLE_KUBE_CA_FILE` or `KUBERNETES_SERVICE_HOST`.

```sh
./build-web.sh                                 # builds ../../console and copies it to web/dist (needs Node 22 and pnpm)
CONSOLE_DEV_FAKE=1 go run ./cmd/console        # open http://127.0.0.1:8080 and sign in with demo-token
```

## Build and test

```sh
make test          # go test ./... -race
make web           # ./build-web.sh
make build         # CGO_ENABLED=0 go build -o bin/console ./cmd/console (with the web app embedded)
make docker        # docker build; the Dockerfile builds the web app in a Node stage first
```

`web/dist/` holds only `PLACEHOLDER` in Git; `build-web.sh` fills it. Before the first front-end build the binary still compiles, and
the app shell answers 503 with a hint.

## Deploying (what the chart needs)

- **Image** `openagentix/console`, port **8080**, non-root, read-only root filesystem works (it writes nothing).
- **ServiceAccount** `openagentix-console` with a Role in the agents namespace (kagent): `openagentix.io` `agentdefinitions` get, list, watch,
  create, update, delete; and read only (get, list) on `api.kagent.dev` `agents`, `agenttemplates`, `harnesses`, `modelconfigs`.
  Nothing on Secrets, nothing cluster-wide, no `agentdefinitions/status`.
- **Secret** `console-token`, key `token`, generated by `install.sh` when absent and never printed; mounted as a file, path in `CONSOLE_TOKEN_FILE`.
- **Mounts** the two ConfigMaps the controller reads (catalog and policy) as files, paths in `CONSOLE_CATALOG_FILE` and `CONSOLE_POLICY_FILE`.
- **NetworkPolicy** ingress: nobody (use `kubectl port-forward svc/console 8080:8080`); egress: DNS and the Kubernetes API only.
- **Probes** `GET /healthz` (no token) on 8080.
- **Env** `CONSOLE_NAMESPACE=kagent`, optionally `CONSOLE_CONTEXT`, `CONSOLE_VIEWER=1` for a read-only console.
- The catalog may carry `"demo": true` (rendered from the demo values files); then the web app shows its "Demo data" tag.

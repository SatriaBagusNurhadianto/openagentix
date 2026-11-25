# CI

Workflows are in `.github/workflows/`. Every check also runs locally through the root `Makefile` (`make help`).

## What runs where

| Job (`ci.yml`) | Does | Local equivalent | Typical time |
|---|---|---|---|
| `changes` | decides whether the change is docs-only; emits the Go module and Dockerfile matrices | `hack/ci/changes.sh`, `hack/ci/go-modules.sh --json` | under 1 min |
| `go` (one per module) | `gofmt`, `go vet`, `go test ./... -race -count=1`, coverage artifact | `make lint-go test-go` | 1-3 min each |
| `chart` | `helm lint`, `charts/openagentix/tests/run.sh` | `make test-chart` | 2 min |
| `shell` | channels, dashboard and bench tests, shellcheck report (non-blocking) | `make test-shell lint-shell` | 2-3 min |
| `integration` | kb tests against PostgreSQL+pgvector, memory-expiry job (Docker) | `make test-integration` | 3-6 min |
| `secrets` | gitleaks over the pushed commit range, honours `.gitleaksignore` | `make secrets` | under 1 min |
| `images` (one per Dockerfile) | `docker buildx build`, linux/amd64, nothing pushed | `docker build <dir>` | 1-3 min each |
| `ci-ok` | passes when every job above passed or was skipped | | seconds |

Make **`ci-ok`** the only required status check in the branch protection rule. Docs-only changes (`docs/`, `website/`, `*.md`,
`LICENSE`) skip everything except `secrets`; `ci-ok` still passes. Superseded runs of the same ref are cancelled.
`docs.yml` is separate and untouched. `e2e.yml` is a scaffold that fails by design (see `E2E-PLAN.md`).

## Tools and pins

`helm` v3.22.0 (`azure/setup-helm`), `jq` from the runner image (1.7.x), `yq` **kislyuk 3.4.3** (the Python jq wrapper that
the chart tests and `install.sh` use, never mikefarah's; `pip install yq==3.4.3`), gitleaks 8.30.1, shellcheck 0.11.0, kind 0.33.0.
The last four are installed by `hack/ci/install-tools.sh` and verified by SHA-256. Go comes from each module's `go.mod`; Node is 22
(same as `docs.yml`, npm with `website/package-lock.json`). Actions are pinned by commit SHA with the version in a comment;
Dependabot (`.github/dependabot.yml`) proposes updates weekly, grouped.

## Run it locally

```
make test              # go + chart + shell, all offline
make test-integration  # needs a running Docker daemon
make lint              # gofmt, go vet, shellcheck (shellcheck only reports until the tree is clean; SHELLCHECK_STRICT=1 enforces)
make secrets           # needs gitleaks
```

The dashboard test runs its jsdom part only when `website/node_modules` exists (`cd website && npm ci`).

## Add a Go service

Create the module (`go.mod`) and a Dockerfile. Nothing else: `hack/ci/go-modules.sh` finds every `go.mod` (not under `spikes/` or
`node_modules`), and `hack/ci/dockerfiles.sh` finds every Dockerfile with a `FROM golang... AS build` stage. Add the module's
directory to `.github/dependabot.yml` (it cannot discover modules). A module whose `go.mod` has a `replace` to a path outside the
repository is skipped with a message (today `bridges/ax-adapter`, which needs a local AX checkout and a prebuilt binary). To skip
something else, add its directory to `hack/ci/go-modules.skip`.

For a service with PostgreSQL-backed tests, gate them on an environment variable and `t.Skip` when it is unset (as `services/kb`
does), then add a Docker-based target to the `integration` job.

## Not built yet

- **Console (Svelte).** `make test-console` fails with "not built yet". When the console exists, uncomment the job below in `ci.yml`
  (adjust `console/`), add `console` to `ci-ok.needs`, and replace the `test-console` recipe in the Makefile with
  `cd console && pnpm install --frozen-lockfile && pnpm test`.
- **e2e.** See `E2E-PLAN.md`.
- **arm64 images.** Only linux/amd64 is built. The Dockerfiles do not pin a platform, so arm64 on a hosted amd64 runner means QEMU
  emulation; use an arm64 runner (`ubuntu-24.04-arm`) in the `images` matrix when that is needed.

```yaml
  # console:
  #   needs: changes
  #   if: needs.changes.outputs.heavy == 'true'
  #   runs-on: ubuntu-24.04
  #   timeout-minutes: 20
  #   defaults:
  #     run:
  #       working-directory: console
  #   steps:
  #     - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  #       with:
  #         persist-credentials: false
  #     - uses: pnpm/action-setup@<full commit SHA> # <version>; look it up with `gh api repos/pnpm/action-setup/releases/latest`
  #       with:
  #         version: 10                 # or set "packageManager" in package.json and drop this line
  #     - uses: actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0
  #       with:
  #         node-version: 22
  #         cache: pnpm
  #         cache-dependency-path: console/pnpm-lock.yaml
  #     - run: pnpm install --frozen-lockfile
  #     - run: pnpm check             # svelte-check, if present
  #     - run: pnpm test              # vitest
  #     - run: pnpm build
  #
  # console-e2e:                      # Playwright
  #   needs: changes
  #   if: needs.changes.outputs.heavy == 'true'
  #   runs-on: ubuntu-24.04
  #   timeout-minutes: 30
  #   defaults:
  #     run:
  #       working-directory: console
  #   steps:
  #     - (checkout, pnpm/action-setup, setup-node with cache: pnpm, as above)
  #     - run: pnpm install --frozen-lockfile
  #     - run: pnpm exec playwright install --with-deps chromium
  #     - run: pnpm exec playwright test
  #     - if: failure()
  #       uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
  #       with:
  #         name: playwright-report
  #         path: console/playwright-report
```

Note: `website/` uses npm, not pnpm; the pnpm skeleton is for the new console only.

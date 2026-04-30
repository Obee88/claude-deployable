# claude-deployable — system plan

A template repo that gives a Claude agent (running in the Cowork sandbox) the
ability to drive a real project end-to-end: edit code, commit and push to
GitHub, watch CI, react to failures, and observe/restart Docker containers on
a VPS.

## Goals

The sandbox boundary is the core constraint. The agent edits files in the
mounted working tree, runs `git` against it via the `bash` tool, and pushes
to GitHub directly over the sandbox's allowlisted egress. The Cowork sandbox
cannot reach `api.github.com`, so anything that needs the GitHub REST API
(CI runs, logs) is fronted by a small HTTP service on the VPS — which
already needs to exist to expose container observability and a deploy hook.
One service, two responsibilities.

Concretely the agent should be able to:

1. Edit files in the mounted working tree, then commit with `claude-agent`
   authorship and push to origin via `git` over the sandbox's allowlisted
   egress.
2. Pull / branch / reset to keep the working tree in a known-good state
   across sessions.
3. Know whether the most recent GitHub Actions run for a given commit passed
   or failed, and on failure read the logs of the failed steps so it can
   diagnose and retry — via the VPS agent's `/ci/*` endpoints.
4. Read container status and logs from the VPS, and restart unhealthy
   services — via the VPS agent's `/containers/*` endpoints.

## Decisions

| Area | Decision |
|---|---|
| Language (VPS agent + dummy service) | Go |
| Git transport | Sandbox-native: `git` runs in the Cowork sandbox via the `bash` tool against the mounted working tree. No host-side bridge or local server. Push goes to `github.com` over the sandbox's allowlisted egress |
| Sandbox file-delete grant | Forker grants `allow_cowork_file_delete` on the working folder once at install time. Without it, `git checkout`, `git reset --hard`, `git gc` fail because the sandbox can't unlink files it didn't create |
| GitHub API access from sandbox | Not direct — `api.github.com` is blocked by the Cowork egress proxy. CI run/log queries go through the VPS agent at `https://ops.<domain>/ci/*`, which holds an `actions:read` PAT and proxies to GitHub |
| VPS agent location | `https://ops.<domain>` fronted by Caddy, Go service on `127.0.0.1:8080` |
| VPS agent auth | Bearer tokens over HTTPS, separate read vs write tokens. `/ci/*` and `/containers/*` use the read token; `/containers/*/restart` requires the write token |
| Commit author | Dedicated `claude-agent <claude-agent@users.noreply.github.com>` identity, set per-commit via `GIT_AUTHOR_*` / `GIT_COMMITTER_*` env vars on the bash invocation. No bot GitHub account. Commits will not link to a profile in the GitHub UI, but the author-email pattern makes them easy to grep in `git log` |
| Push credential | Fine-grained PAT scoped to the single repo, stored in `<repo>/.env` (gitignored, mode `0600`). Permissions: `contents:write`, `metadata:read`, **`workflows:write`** (the workflows permission is required from M2 onward because the agent ships `.github/workflows/*.yml` updates; GitHub rejects PAT-driven pushes that touch workflow files when this scope is missing). Injected into the remote URL for the single push (`https://oauth2:$PAT@github.com/<owner>/<repo>.git`) and discarded — never persisted to `.git/config` |
| Actions-read credential | Separate fine-grained PAT scoped to `actions:read` on the same repo, stored on the VPS in the VPS agent's env file. Not present in the sandbox |
| Branch policy | Direct commits/pushes to `main` — deliberate solo-project choice; moving to a PR flow is an architectural change, not a silent upgrade |
| Deploy mechanism | `docker compose` with images pulled from GHCR; VPS runs `docker login ghcr.io` once at install time using a `read:packages` PAT. Deploy itself is driven from GitHub Actions over SSH (M2), not from the VPS agent |
| Concurrency | Single-agent assumption: don't run two Cowork sessions against the same working tree simultaneously. Without a per-repo mutex (which we lost when the bridge went away), racing pushes from two sessions can produce non-fast-forward errors that the agent has to recover from manually |
| Secrets | `<repo>/.env` (mode `0600`, gitignored) holds the push PAT; the working tree is mounted into the sandbox so the sandbox reads it directly. `EnvironmentFile=` for the VPS systemd unit holds the actions-read PAT, GHCR pull PAT, and bearer tokens |

## Architecture

```
┌─ Cowork sandbox ──────────────────────┐    ┌─ GitHub ───────────┐
│                                       │    │                    │
│  agent ── bash + git ─────── push ────┼────▶  repo + Actions    │
│        ├ file tools (mount)           │    │                    │
│        └ HTTPS (egress allowlist)     │    └─────────▲──────────┘
│                  │                    │              │
└──────────────────┼────────────────────┘              │
                   │ HTTPS + bearer            actions:read PAT
                   ▼                                   │
┌─ VPS ──────────────────────────────────┐             │
│  Caddy (TLS, Let's Encrypt)            │── api.github.com ──────┘
│     └─▶ vps-agent (Go, 127.0.0.1:8080) │
│            ├─ /containers/*            │
│            ├─ /ci/*  (proxy)           │
│            └─ /healthz                 │
│                                        │
│  docker compose workloads (from GHCR)  │
└────────────────────────────────────────┘
```

## Repo layout

```
claude-deployable/
├── .github/workflows/
│   ├── ci.yml                       # build + test on push
│   └── deploy.yml                   # on main: build image, push to GHCR, SSH to VPS, compose up
├── cmd/
│   └── vps-agent/main.go            # VPS agent entrypoint (M3)
├── internal/
│   ├── auth/bearer.go               # bearer-token middleware
│   ├── httpx/                       # HTTP helpers (JSON errors, structured logging)
│   ├── ciops/                       # GitHub REST API client (runs, logs zip + failed-step filter)
│   ├── dockerops/                   # docker CLI wrappers (list, logs, health, restart)
│   └── deployops/                   # pull + compose up logic (used by deploy.yml's SSH script in M2; embedded in vps-agent if /deploy is ever added)
├── deploy/
│   ├── vps-agent.service            # systemd unit (M3)
│   ├── Caddyfile.example            # TLS termination for ops.<domain> (M3)
│   ├── compose.yml.example          # compose project on the VPS referencing the hello image (M2)
│   └── install-vps.sh               # idempotent installer run over SSH (M3)
├── services/
│   └── hello/                       # minimal Go HTTP service used as the deploy target (M2)
│       ├── main.go
│       └── Dockerfile
├── configs/
│   └── vps-agent.env.example        # VPS_READ_TOKEN, VPS_WRITE_TOKEN, ACTIONS_READ_PAT, GHCR_PAT, compose project dir
├── .claude/
│   └── skills/
│       ├── ship-a-change/           # full edit→commit→push (M1) → react to CI + verify health (M3)
│       │   └── SKILL.md
│       ├── diagnose-ci-failure/     # playbook for red CI runs (M3)
│       │   └── SKILL.md
│       └── investigate-service/     # playbook for unhealthy VPS containers (M3)
│           ├── SKILL.md
│           └── scripts/ops          # thin CLI wrapping the VPS agent API
├── .env.example                     # GH_PAT (push), GITHUB_OWNER, GITHUB_REPO, claude-agent identity
├── AGENTS.md                        # reference: bash recipes, VPS endpoints, service↔container map
├── SETUP.md                         # human-facing: GitHub repo + PAT, Cowork delete grant, VPS install
├── PLAN.md                          # this file
└── README.md                        # elevator pitch + pointers to SETUP.md and AGENTS.md
```

A single Go module at the repo root builds the VPS agent and the dummy
service, with as much code as possible shared via `internal/`. The Go module
arrives in M2; M1 has no Go code at all.

## Sandbox-native git

The agent runs `git` directly via the `bash` tool against the mounted
working tree. There is no bridge process, no MCP tool surface for git, no
custom error envelope — `git` exit codes and stderr are the contract.
AGENTS.md spells out the canonical recipes so the agent doesn't have to
derive them; `ship-a-change` orchestrates them at the workflow level.

Canonical sequence (full version in AGENTS.md):

```sh
# 0. Source .env to get GH_PAT, GITHUB_OWNER, GITHUB_REPO, CLAUDE_AGENT_*
set -a; . "$REPO/.env"; set +a

# 1. Status check — proceed only if porcelain output is empty
git -C "$REPO" status --porcelain

# 2. Sync
git -C "$REPO" pull --ff-only origin main

# 3. (edit via Read/Write/Edit on the mount)

# 4. Commit, attributed to claude-agent
GIT_AUTHOR_NAME="$CLAUDE_AGENT_NAME"   GIT_AUTHOR_EMAIL="$CLAUDE_AGENT_EMAIL" \
GIT_COMMITTER_NAME="$CLAUDE_AGENT_NAME" GIT_COMMITTER_EMAIL="$CLAUDE_AGENT_EMAIL" \
  git -C "$REPO" commit -am "<message>"

# 5. Push — PAT-injected URL, never persisted to .git/config
git -C "$REPO" push \
  "https://oauth2:$GH_PAT@github.com/$GITHUB_OWNER/$GITHUB_REPO.git" main

# 6. Capture the SHA we just pushed (used by /ci/* in M3)
git -C "$REPO" rev-parse HEAD
```

Failure modes the agent is expected to recognize and handle, rather than
loop on:

- *Dirty tree* — `git status --porcelain` non-empty before step 1. Stop,
  surface to the human, do not proceed. The agent must not silently mix
  unfinished work into its commit.
- *Non-fast-forward push* — origin moved between pull and push (concurrent
  pusher). Re-run pull, retry push once; if it fails again, escalate.
- *PAT auth failure* — `fatal: Authentication failed`. Stop and ask the
  human to rotate the PAT.
- *Merge / rebase / cherry-pick in flight* — `.git/MERGE_HEAD`,
  `.git/REBASE_HEAD`, etc. exist. Stop, surface, do not auto-abort. The
  recovery commands (`git ... --abort`, `git reset`) are listed in
  AGENTS.md as recovery-only operations; they are not part of the canonical
  flow.

## VPS agent — API surface

Base URL: `https://ops.<domain>`.

Read requests require `Authorization: Bearer <VPS_READ_TOKEN>`. Write
requests (`POST /containers/*/restart`) require
`Authorization: Bearer <VPS_WRITE_TOKEN>`.

CI proxy (forwards to `api.github.com` using the actions-read PAT held on
the VPS):

- `GET /ci/runs?head_sha={sha}&timeout_s={seconds}` →
  `{run_id, status, conclusion, html_url}` — polls
  `GET /repos/{owner}/{repo}/actions/runs?head_sha={head_sha}` until a run
  matching the pushed SHA is conclusive (or timeout). Filtering by
  `head_sha` instead of branch keeps concurrent pushes from confusing the
  agent.
- `GET /ci/runs/{id}/logs?failed_only=true&tail_lines=500&max_bytes=65536`
  → plain text — downloads the run's logs zip, optionally filters to
  failed steps, tails and truncates with explicit
  `[...truncated N bytes]` so the agent knows when output is partial.

Containers:

- `GET /containers` → `[{name, image, status, uptime_s, health}]`
- `GET /containers/{name}/logs?since=10m&tail=500&max_bytes=65536` → plain
  text, same truncation discipline as CI logs
- `GET /containers/{name}/health` → `{status, last_check, details}` —
  reads `State.Health.Status` from `docker inspect`; services opt in by
  declaring a `HEALTHCHECK` in their Dockerfile
- `POST /containers/{name}/restart` → `{status, restarted_at}` — runs
  `docker compose restart <service>` in the configured project dir; only
  allowlisted service names accepted
- `GET /healthz` → `ok`

The CI proxy is a thin pass-through. The VPS agent forwards GitHub's
response shape with minimal massaging (pagination handling for the runs
list, tail/truncate for log payloads, failed-step filtering). It does not
cache, retain, or schedule. `owner`/`repo` are loaded from config, not
client-supplied — the proxy targets exactly one repo per VPS deployment.

The service list and compose project path are loaded from config. The
agent cannot specify arbitrary compose files or service names — only
allowlisted ones. Deploy (image pull + `docker compose up`) is
intentionally *not* an endpoint — it's driven from GitHub Actions over SSH
(see M2). A future revision may add `POST /deploy/{service}` and
`/deploy/{service}/rollback` if the agent needs to trigger deploys
directly.

## CI feedback strategy

The sandbox cannot reach `api.github.com` — the Cowork egress proxy
returns 403, and that's a deliberate property of the sandbox, not a
configuration we can flip per-fork. The VPS, by contrast, has unrestricted
outbound, and already has to exist for container observability. So the
natural place for GitHub-API mediation is the VPS agent.

Three paths were considered.

**Direct from sandbox.** Rejected: `api.github.com` is blocked, and we
don't want to ask forkers to extend Cowork's egress allowlist (most
can't, and the allowlist is centrally controlled).

**Anthropic MCP connector for GitHub.** Considered, then ruled out: there
is no GitHub connector in Anthropic's MCP registry as of 2026-04-29
(probed by `search_mcp_registry` with multiple keyword sets). If/when one
ships, this section is worth revisiting — it would let the sandbox call CI
tools directly without the VPS being in the loop, and would shrink the
VPS agent's responsibilities back to containers only.

**Chosen path: VPS-mediated.** The VPS agent gains `/ci/*` endpoints that
proxy `api.github.com`. The actions-read PAT lives on the VPS, not in the
sandbox. The sandbox calls these endpoints with the same bearer read
token it uses for `/containers/*`. Implementation is in `internal/ciops`,
sharing the auth/logging/error machinery with the container handlers.

**Identifying the right run.** Branch alone is insufficient — if a human
or another agent pushes concurrently, "latest run on branch X" is
ambiguous. The sandbox passes the `head_sha` it just pushed (captured
from `git rev-parse HEAD` after the push) to `/ci/runs?head_sha=...`. The
proxy filters runs by that SHA so the agent always watches the run
actually triggered by its commit.

**Tradeoff.** CI visibility now depends on the VPS being up. For a
template targeted at solo developers running a single VPS, this is
acceptable: if the VPS is down, the deploy story is broken anyway and the
agent should escalate. The alternative — webhooks pushed to a separate
public endpoint — adds a service category we'd otherwise not need.

## Security model

The agent runs in the Cowork sandbox with file access to the mounted
working tree (including `<repo>/.env`, which holds the push PAT) and
`bash` access scoped to that mount and the network egress allowlist.
There is no host-side process to compromise — compromising the sandbox
is equivalent to handing the attacker the push PAT and `bash` access to
the working tree.

The push PAT is the blast-radius ceiling for a compromised sandbox: a
fine-grained PAT scoped to the single repo, with only the permissions
needed to push (`contents:write`, `metadata:read`, and from M2 onward
`workflows:write`). A leaked token cannot touch other repos and cannot
read Actions logs or change repo settings. The actions-read PAT — which
can read CI logs — lives only on the VPS and is never reachable from
the sandbox.

**Why `workflows:write` is on the push PAT, and what it costs.** GitHub
forbids PAT-driven pushes from creating or modifying files under
`.github/workflows/` unless the PAT carries the workflows scope, even
when `contents:write` is present. M2 requires the agent to ship CI and
deploy workflows, so the scope is necessary for the agent-driven model.
The cost: a leaked push PAT can now also rewrite workflow files —
which can register a malicious workflow that, on its next trigger,
reads any repo secret available to that workflow. Mitigations the
template leans on: (a) the PAT's repo scope is still a single repo, so
the radius is one project, not an org; (b) repo secrets in this
template are limited to the M2 deploy story (VPS host / user / SSH
key / known_hosts / compose dir), and the SSH key is itself scoped to
a sudoless deploy user; (c) the PAT has a 90-day expiration with
rotation pinned in `SETUP.md`. The alternative — split the workflow-
push capability into a second PAT — was considered and rejected as
adding rotation surface without meaningfully shrinking the worst-case
radius (a leaked sandbox holds both `.env` files anyway).

The VPS continues to be the credential perimeter for the `actions:read`
PAT, the GHCR `read:packages` PAT, and the bearer tokens. Caddy
terminates TLS on a Let's Encrypt cert; the Go service listens only on
`127.0.0.1:8080`. The two bearer tokens (read, write) are stored
separately so the write token can be rotated without invalidating reads;
`/ci/*` and `/containers/*` use the read token, `/containers/*/restart`
requires write.

Structured JSON logging — request-id, endpoint, repo or service,
duration, outcome — goes to stdout on the VPS agent and is captured by
journald. The sandbox's bash output is captured by Cowork's session log
on the laptop; structured logging is not enforced for bash calls.

**What we lose by removing the bridge.** The previous architecture had
two structural mitigations the new one doesn't:

1. *Per-repo mutex.* Two Cowork sessions against the same working tree
   can race. Mitigation: AGENTS.md spells out the single-session
   assumption; `ship-a-change`'s pull-before-push step catches most
   divergence and forces an explicit retry.
2. *Curated tool surface.* `bash` gives the agent the full `git` CLI,
   not just six well-shaped tools. Mitigation: skills steer the agent to
   the canonical recipes; AGENTS.md flags destructive commands
   (`reset --hard`, `clean -fd`, `push --force`) as recovery-only and
   to be run only after the agent has surfaced the situation to the
   human.

Both losses are real but, given the solo-developer template scope, judged
acceptable in exchange for the simplicity gain. They are listed in the
risks section and are reasonable upgrade points if a downstream fork
needs more guardrails.

## Agent usage contract — `AGENTS.md`

The agent needs to know the canonical sequence:

1. `git -C $REPO status --porcelain` — if non-empty, stop and surface to
   the human; do not proceed.
2. `git -C $REPO pull --ff-only origin main` to sync with origin.
3. Edit files via the sandbox's file tools.
4. Commit with `claude-agent` authorship via `GIT_AUTHOR_*` /
   `GIT_COMMITTER_*` env vars; recipe in the "Sandbox-native git" section.
5. Push with the PAT-injected URL; capture
   `git -C $REPO rev-parse HEAD` as `head_sha`.
6. (M3+) `GET https://ops.<domain>/ci/runs?head_sha=<head_sha>&timeout_s=300`.
7. (M3+) If `conclusion == "failure"` →
   `GET /ci/runs/{run_id}/logs?failed_only=true&tail_lines=500` →
   diagnose → loop to step 3 (retry limit: 3 consecutive failures, then
   escalate).
8. (M3+) Once CI is green, `deploy.yml` builds the image, pushes to GHCR,
   SSHes in, and runs `docker compose up`. The agent confirms via
   `GET /containers/{name}/health`; on `unhealthy`, hand off to
   `investigate-service`.

`AGENTS.md` also lists the service ↔ container-name mapping, the bash
recipes for the recovery operations (`reset --hard`, `merge --abort`,
etc., flagged as recovery-only), and the exact endpoint contracts so the
agent doesn't have to read this document to find them.

## Skills

We split agent knowledge into two layers. Reference material — endpoint
URLs, auth headers, the bash recipes for git ops, the service↔container
map — lives in `AGENTS.md` as always-on context. Workflows — multi-step
procedures with decision points — live as triggered skills in
`.claude/skills/`, each co-located with the helper scripts it needs.

Three skills ship with the template, building up across milestones.

**`ship-a-change`.** Triggers on phrases like "ship this", "deploy this
fix", or "push to prod". Grows across milestones. In M1 the skill encodes
only the git side: status check (stop on dirty tree or merge state),
pull, edit, commit with `claude-agent` identity, push, capture
`head_sha`. In M2 it gains GHA awareness: after push, the agent expects
`deploy.yml` to run, but does not poll, since the VPS agent doesn't exist
yet — the skill notes that CI feedback is "check the GitHub UI manually
until M3 lands." In M3 the skill adds `/ci/runs` polling keyed on the
captured SHA and post-deploy-health checks against
`/containers/{name}/health`. Retry limits and escalation paths are
defined explicitly so the agent doesn't loop on flaky tests forever.

**`diagnose-ci-failure`.** First ships in M3, alongside the `/ci/*`
endpoints. Triggers on "CI failed", "build is red", or automatically
after `ship-a-change` observes a failed run. Prescribes: fetch
failed-step logs via `GET /ci/runs/{id}/logs?failed_only=true`, classify
the failure (compile error, test failure, flaky infra, missing secret),
decide retry vs fix vs escalate. The classification rubric is the
load-bearing part — it's what keeps the agent from mechanically retrying
flaky tests forever. Deliberately not in M2: there's no API path for the
agent to read CI logs in M2, so a stub skill would be misleading.

**`investigate-service`.** Ships in M3. Triggers on "container is
unhealthy", "service X is down", or "check logs on VPS". Walks through
`GET /containers` → identify the misbehaving one →
`GET /containers/{name}/logs?since=30m` → decide between restart,
redeploy, and escalate. Ships with a `scripts/ops` CLI wrapping the VPS
agent.

Git operations are deliberately *not* behind a skill of their own. They
happen in nearly every session and belong in always-on reference
(`AGENTS.md`), not triggered workflows. `ship-a-change` orchestrates them
at the workflow level when a coherent edit→push intent is in play.

## Build order / milestones

Restructured on 2026-04-29 after dropping the local-bridge architecture
(see ADR-0002). The three milestones still end at demonstrable,
user-visible capabilities, but their internal shape has changed
substantially: M1 has no Go code at all, and CI-feedback responsibility
moves entirely into M3.

A forker who stops after any milestone has a coherent, usable subset of
the template.

0. **Sandbox reachability spike.** *Done — see ADR-0001 for findings,
   ADR-0002 for the architectural conclusion now in force.*

1. **Agent commits and pushes from the sandbox.** No Go module yet.
   Deliverables:
   - `<repo>/.env.example` with `GH_PAT`, `GITHUB_OWNER`, `GITHUB_REPO`,
     `CLAUDE_AGENT_NAME`, `CLAUDE_AGENT_EMAIL`.
   - `.gitignore` covering `.env`.
   - `.claude/skills/ship-a-change/SKILL.md` — M1 slice (git-only, no CI).
   - `AGENTS.md` — M1 slice: bash recipes for status / pull / commit /
     push, recovery-only commands flagged as such, single-session
     assumption, single-repo-per-fork assumption.
   - `SETUP.md` — M1 slice: create the GitHub repo, mint the fine-grained
     push PAT (`contents:write`, `metadata:read`, `workflows:write`
     on the single repo — workflows is required from M2 onward, see
     security section),
     populate `<repo>/.env`, grant `allow_cowork_file_delete` on the
     working folder, smoke-test by running `ship-a-change` end-to-end.
   - `README.md` — elevator pitch + pointers to SETUP.md and AGENTS.md.
   - **Cleanup of the previous architecture:** delete `cmd/bridge`,
     `cmd/vps-agent` (skeleton), `internal/{auth,ciops,deployops,
     dockerops,gitops,httpx,mcpx,repomux}`, `deploy/cowork-plugin/`,
     `configs/bridge.env.example`, `.claude-plugin/`, `go.mod`, `go.sum`,
     `scripts/bootstrap.sh`, `QUICKSTART.md`. The repo at the end of M1
     is small: docs, the one skill, `.env.example`.
   *Closeout:* from a Cowork session, edit a file in the mounted repo,
   trigger `ship-a-change`; verify the commit appears on GitHub authored
   as `claude-agent`.

2. **GHA builds and deploys a dummy container after agent push.**
   Introduce the Go module here. Deliverables:
   - `services/hello/` — minimal Go HTTP server (`GET /` returns a
     version string, `GET /healthz` returns 200) and Dockerfile.
   - `.github/workflows/ci.yml` — build + test on push.
   - `.github/workflows/deploy.yml` — on `main`: build image, push to
     GHCR (sha tag), SSH into the VPS and run `docker login ghcr.io &&
     docker compose pull hello && docker compose up -d hello`. Deploy
     is driven directly from GHA over SSH using a deploy key — the VPS
     agent is *not* introduced in this milestone.
   - `deploy/compose.yml.example` — compose project on the VPS
     referencing the `hello` image.
   - SETUP.md M2 slice: provision the VPS (sudo user, docker +
     docker-compose, GHCR `read:packages` PAT for `docker login`, GHA
     deploy key + SSH known hosts, `compose.yml` placement).
   - AGENTS.md M2 slice: the deploy pipeline, plus a clear note that CI
     feedback is "check the GitHub UI manually until M3."
   - `ship-a-change` skill stays git-only — no CI polling yet, since the
     API path doesn't exist.
   *Closeout:* hand the agent a routine edit; it pushes; GHA runs CI
   green, builds the image, SSHs to the VPS, container is updated.
   Failure recovery still requires human eyes on GHA in M2.

3. **VPS agent ships with both container and CI surfaces; agent reacts
   to failing CI and unhealthy containers.** Build `cmd/vps-agent`:
   HTTP on `127.0.0.1:8080`, bearer-gated. Split into three substeps,
   each ending at a demonstrable artifact (sub-plan adopted
   2026-04-30).

   **M3.A — Skeleton, install path, container surface.** Outcome: a
   real `https://ops.<domain>` endpoint, Caddy-fronted, bearer-gated,
   serving the four `/containers/*` endpoints against the live hello
   container.
   - `cmd/vps-agent/main.go` — `net/http` ServeMux, `log/slog` JSON to
     stdout, graceful shutdown, listens on `127.0.0.1:8080`.
   - `internal/auth/bearer.go` (constant-time compare, separate read
     and write tokens), `internal/httpx/` (JSON error envelope,
     request-id middleware, structured access log),
     `internal/dockerops/` (shells to `docker ps/logs/inspect` for
     reads, `docker compose -f <dir>/compose.yml restart <svc>` for
     restart; service allowlist enforcement; tail/truncate with
     explicit `[...truncated N bytes]` markers).
   - Endpoints: `GET /healthz`, `GET /containers`,
     `GET /containers/{name}/logs`, `GET /containers/{name}/health`,
     `POST /containers/{name}/restart`.
   - Deploy artifacts: `deploy/vps-agent.service` (systemd,
     `User=deploy`, `EnvironmentFile=/home/deploy/etc/vps-agent.env`),
     `deploy/Caddyfile.example` (reverse_proxy `127.0.0.1:8080`),
     `deploy/install-vps.sh` (idempotent: installs Caddy if absent,
     drops the systemd unit + Caddyfile, writes a
     `sudoers.d/vps-agent` entry granting deploy user
     `NOPASSWD: /bin/systemctl restart vps-agent` only, reloads —
     does not touch Docker or recreate the deploy user).
   - `configs/vps-agent.env.example`: `VPS_READ_TOKEN`,
     `VPS_WRITE_TOKEN`, `VPS_COMPOSE_DIR`,
     `VPS_ALLOWED_SERVICES=hello`, `GITHUB_OWNER`, `GITHUB_REPO`,
     `ACTIONS_READ_PAT=` (stub for M3.B).
   - `.github/workflows/deploy-vps-agent.yml` — paths-filter on
     `cmd/vps-agent/**`, `internal/**`, `deploy/vps-agent.service`,
     `deploy/Caddyfile.example`. Builds static `linux/amd64`, scps
     to `/tmp`, ssh-runs `mv /tmp/vps-agent
     /home/deploy/bin/vps-agent && sudo systemctl restart vps-agent`.
     Reuses the M2 SSH secrets.
   - `ci.yml` extension: `go vet ./... && go test ./...` exercises the
     whole module on every push.
   *Closeout:* curl-tests `/healthz`, `/containers`,
   `/containers/hello/logs`, `/containers/hello/health`, and
   `POST /containers/hello/restart` (read token rejected on restart).
   Push a vps-agent code change; observe `deploy-vps-agent.yml`
   restarting the unit cleanly.

   **M3.B — CI proxy.** Outcome: the VPS agent serves CI run status
   and failed-step logs filtered/truncated for agent consumption.
   - `internal/ciops/` — `net/http` GitHub REST client, no third-party
     SDK. Runs polled by `head_sha`; logs zip via `archive/zip`;
     failed-step filter; `tail_lines` + `max_bytes` truncation
     discipline.
   - Endpoints: `GET /ci/runs?head_sha=...&timeout_s=...`,
     `GET /ci/runs/{id}/logs?failed_only=...&tail_lines=...&max_bytes=...`
     (read-token gated).
   - Setup: mint a fine-grained `actions:read` PAT for the single
     repo, populate `ACTIONS_READ_PAT` in
     `/home/deploy/etc/vps-agent.env`, restart the unit.
   *Closeout:* `/ci/runs?head_sha=<recent green>` returns a conclusive
   run JSON; after a deliberate-fail commit,
   `/ci/runs/{id}/logs?failed_only=true&tail_lines=200` returns failed
   step logs only with truncation markers as needed.

   **M3.C — Skills, ship-a-change upgrade, docs, full E2E.** Outcome:
   the agent self-corrects through CI failures and investigates
   unhealthy containers end-to-end.
   - `.claude/skills/investigate-service/SKILL.md` plus `scripts/ops`
     (bash CLI: `ops containers|logs|health|restart`).
   - `.claude/skills/diagnose-ci-failure/SKILL.md` — failure
     classification rubric (compile / test / flaky / missing-secret),
     retry cap of 3, escalate path.
   - `ship-a-change` M3 upgrade: post-push, poll
     `/ci/runs?head_sha=<sha>&timeout_s=600`; on green and after
     `deploy.yml` lands, verify `/containers/hello/health`; on red,
     trigger `diagnose-ci-failure`.
   - AGENTS.md M3 slice: CI tools, container tools, service↔container
     map.
   - SETUP.md M3 slice: subdomain DNS, run `install-vps.sh`, mint
     `actions:read` PAT, generate read+write bearer tokens, populate
     `/home/deploy/etc/vps-agent.env`, append `OPS_BASE_URL`,
     `OPS_READ_TOKEN`, `OPS_WRITE_TOKEN` to `<repo>/.env`, smoke
     tests.
   - README status flip: M3 → Shipped on closeout.
   - `.env.example`: append the three `OPS_*` vars.
   *Closeout:* (a) deliberate-fail edit → push → CI red →
   `diagnose-ci-failure` → fix → push → green → deploy → health green;
   (b) force hello unhealthy (e.g. `docker exec` to break the probe);
   ask "what's up with hello?"; `investigate-service` reports +
   restarts + confirms healthy.

   **Cross-cutting M3 decisions** (locked 2026-04-30):
   - `vps-agent` runs as the existing `deploy` user (already in the
     `docker` group from M2) — not root, not a new user. The only
     added privilege is a `sudoers.d/vps-agent` entry:
     `deploy ALL=(root) NOPASSWD: /bin/systemctl restart vps-agent`.
   - Paths: `/home/deploy/bin/vps-agent` for the binary,
     `/home/deploy/etc/vps-agent.env` (mode 0600) for the env file.
   - No HTTP router library, no docker SDK. `net/http` ServeMux plus
     `os/exec` of the `docker` CLI. Smaller binary, no version
     coupling, no proxy-blocked Go deps.
   - Stdlib-only ambition: `net/http`, `encoding/json`, `archive/zip`
     (M3.B), `log/slog`, `os/exec`, `crypto/subtle`. No new Go deps
     unless something forces our hand.
   - `install-vps.sh` is additive, not destructive: refuses to
     overwrite Caddyfile/systemd unit unless `--force`. Does not
     reinstall Docker or recreate the deploy user.

### Deferred to later / out of scope

- **`POST /deploy/{service}` on the VPS agent.** The M2 SSH-from-GHA path
  deploys fine. Moving the deploy call through the VPS agent (so the
  agent itself can trigger deploys) is a future refactor, worth doing
  only once there's a concrete need (e.g. wanting to deploy outside
  GHA's trigger model).
- **`/deploy/{service}/rollback`.** Depends on the `/deploy` endpoint.
  An SSH-driven rollback script is a fine interim if ever needed.
- **GitHub webhooks for CI.** Polling against `/ci/runs` is sufficient;
  webhooks would only matter if a second consumer of CI events appears.
- **GitHub MCP connector path.** Worth revisiting if Anthropic ships
  one — would let the sandbox skip the VPS for CI queries entirely and
  shrink the VPS agent's responsibilities.

## Risks and open items

**Single-session assumption.** No per-repo mutex now that the bridge is
gone. Two Cowork sessions against the same working tree will race.
AGENTS.md flags this; if it becomes a real failure mode, a future
revision could add a `.claude-deployable.lock` file held during the
commit/push window.

**`.env` lives in the working tree.** The sandbox can read the push PAT
directly. Mitigated by the PAT's scope (single repo, contents+metadata
only); a leaked PAT cannot touch other repos or read CI logs. Forkers
who want host-only secret storage have no clean path here today —
moving the PAT off the working tree means moving git off the sandbox,
which is the architecture we just left.

**Direct-to-main is load-bearing.** No human gate between the agent and
production. Deliberate solo-template choice; treat the VPS agent's
`restart` endpoint and (eventually) deploy rollback as the primary
safety net. Adding branch protection requiring CI green would force a
PR-based flow, which is a real architectural change (new PR-creation
recipes, different skill wiring), not a silent upgrade.

**CI feedback unavailable until M3.** M2 ships without `/ci/*`
endpoints. If the agent pushes a broken change in M2, CI blocks the
deploy but the agent doesn't know until M3 lands. Acceptable because
M2's deliverable is "deploys-on-green works at all," not "agent can
self-correct."

**VPS as single point of failure for CI visibility.** If the VPS is
down, the agent's CI awareness goes blind. For a template targeted at
single-VPS solo deploys, this is the same failure mode as the deploy
itself being unavailable, so it does not add a new category — but it is
worth surfacing in SETUP.md so forkers don't expect CI awareness to
survive a VPS outage.

**Push PAT rotation.** Fine-grained PATs have no automatic rotation.
SETUP.md should include a rotation checklist and recommend a 90-day
expiration so forgetting to rotate surfaces as an auth failure rather
than a silent risk.

**Deploy model is single-tag, GHA-driven.** The compose file on the VPS
references the image by name; GHA pushes a new image to GHCR tagged
with the commit SHA and updates `.env` on the VPS (or uses a floating
`:main` tag) before calling `docker compose up -d`. Blue/green, canary,
and multi-service coordinated deploys are out of scope. Rollback in
M2/M3 is a manual `docker compose up -d` with the previous SHA. If a
service has dependent migrations or schema changes, the agent has no
visibility into those and should not drive them.

**Bash gives the agent the full git CLI.** No curated tool surface to
constrain destructive commands. Mitigated by skill phrasing and
AGENTS.md flagging `reset --hard`, `clean -fd`, `push --force` as
recovery-only with human confirmation, but a determined or confused
agent could still do damage. The trade-off was made consciously when
removing the bridge — see ADR-0002.

**CI proxy targets one repo per VPS.** `owner`/`repo` in `/ci/*` are
loaded from the VPS agent's config, not client-supplied. A forker who
wants one VPS to serve multiple repos would need to extend the proxy
to accept (auth-gated) `owner`/`repo` parameters, or run multiple VPS
agents.

## ADR-0001 — Milestone 0 spike result: local bridge on UDS is not reachable from the Cowork sandbox

*Date:* 2026-04-24. *Status:* **superseded by ADR-0002 (2026-04-29).** The
findings below are still accurate as a record of Cowork sandbox
capabilities and remain useful reference; the proposed Pivot A is no
longer in force.

### What we tested

Ran an empirical probe from inside the Claude Cowork sandbox that would
eventually host the agent, looking for any transport by which it could reach
a process on the developer's laptop (macOS host). Specifically covered the
three fallbacks the original milestone 0 named: host-side UDS visible
through the mount, TCP on a routable host interface, TCP on host loopback
with cert-pinned TLS.

### What we found

The sandbox is more isolated than the original plan assumed.

1. *Host filesystem is not visible.* Only the folders the user explicitly
   connects under Cowork are mounted (here: `claude-deployable`, `outputs`,
   `uploads`, read-only `.claude/skills`, and the memory dir). `~/.claude-deployable`
   on the host does not resolve from the sandbox. A UDS placed there is
   invisible.
2. *UDS in a mounted path does not behave as a socket on the guest kernel.*
   Sockets are kernel objects, not file content; the file-share layer only
   proxies metadata. Even if we placed `bridge.sock` inside the mounted
   working dir from the host, the sandbox sees a file but cannot `connect()`
   to it as a UDS.
3. *No routable network path to the host.* Only `lo` (sandbox-local) is
   visible. All outbound traffic goes through a mandatory proxy at
   `localhost:3128` (HTTPS) / `localhost:1080` (SOCKS) with a host allowlist.
   Probing the host's private address space (`127.0.0.1`, `host.docker.internal`,
   `10.0.2.2`, `172.17.0.1`, `192.168.65.2`) through the proxy returns 403
   from the proxy itself — the proxy refuses to CONNECT there. The
   `CLAUDE_CODE_HOST_SOCKS_PROXY_PORT` environment variable hints at a
   host-side SOCKS endpoint, but empirically it is the same egress proxy,
   not a tunnel to the developer's laptop.
4. *Named pipes / FIFOs have the same filesystem-visibility constraint as UDS;
   no improvement.*
5. *Outbound HTTPS works, but the allowlist is narrow.* `github.com` passes
   (200). `api.github.com`, `ghcr.io`, and `raw.githubusercontent.com` return
   403 from the proxy. `objects.githubusercontent.com`, `registry.npmjs.org`,
   and `pypi.org` pass. Practical consequence: the sandbox can `git clone`
   and `git push` to `github.com` directly, but **`gh` CLI does not work**
   because it depends on `api.github.com`. Direct GitHub REST API calls from
   the sandbox are blocked for the same reason.
6. *Mounted dirs are create/append-only from the sandbox; unlink returns
   EPERM.* Files the sandbox itself creates cannot be deleted by the sandbox
   unless the user explicitly grants delete permission via the Cowork
   `allow_cowork_file_delete` flow (Cowork has a built-in tool for this).
   Relevant because a bridge running inside the sandbox could not safely
   drive local git state — `git checkout`, `git reset --hard`, and `git gc`
   all delete files.

### Conclusion (as of 2026-04-24)

The "local Go bridge listening on `~/.claude-deployable/bridge.sock`"
architecture is fundamentally incompatible with the Cowork sandbox model.
None of the original PLAN.md fallbacks (host UDS / host TCP / cert-pinned
localhost) are reachable. This is not a configuration issue — it is a
deliberate property of the sandbox isolation.

Two architectural pivots were identified that preserved the plan's goals:
**Pivot A** — bridge becomes a Cowork MCP server on the host (chosen at
the time, then itself superseded by ADR-0002); **Pivot B** — drop the
bridge, agent works directly in the sandbox (reconsidered and adopted as
the basis for ADR-0002 once the file-delete grant and a CI-proxy path
were factored in); **Pivot C** — document the template as
non-Cowork-only (rejected as too restrictive).

### Why we revisited (2026-04-29)

Pivot A was implemented in M1 (commit `c96acd4` on `main` and prior).
The bridge worked, but two facts about the design surfaced during
implementation that we hadn't fully internalized:

1. The fork-template friction was higher than expected (build a Go
   binary, install a Cowork plugin, populate a `.env`, debug the MCP
   handshake — before any productive work).
2. The bridge was not actually the credential perimeter we'd argued it
   was, because the working tree's `.env` is mounted into the sandbox
   and the agent can read the PAT directly. The decisions table
   acknowledged this; we underweighted it.

Pivot B (originally rejected for the `api.github.com` reachability
problem) becomes viable once we route CI traffic through the VPS agent
that already exists in M3 for container observability — a small extension
to a service we were going to build anyway. ADR-0002 records that
re-evaluation and supersedes the Pivot A choice made here.

## ADR-0002 — Drop the bridge; sandbox-native git, CI proxied via the VPS agent

*Date:* 2026-04-29. *Status:* **accepted.** *Supersedes:* the Pivot A
recommendation in ADR-0001; the "Local bridge — MCP tool surface" design
in earlier revisions of PLAN.md.

### Context

ADR-0001 selected Pivot A — bridge as a Cowork MCP server on the host —
as the way around sandbox isolation. M1 was implemented under that
pivot: a Go MCP server (`cmd/bridge`) using the official Anthropic Go
MCP SDK, registered with Cowork via a `.claude-plugin/` +
`.mcp.json` manifest, exposing six git tools and the start of the CI
tools. The implementation works, but during use two design properties
that we hadn't fully weighted in M0 became important:

1. *Fork-template friction.* The whole point of this repo is to be
   forked into many future projects. Every forker has to build a Go
   binary, install a Cowork plugin, populate a `.env`, and trust a
   host process — before they can do anything productive. The setup
   surface area is roughly twice that of a "normal" repo, and that
   tax compounds across forks.
2. *The bridge was not really protecting the credential.* The push PAT
   lives in `<repo>/.env`, which is mounted into the sandbox. The
   decisions table already acknowledged this. The bridge's
   "blast-radius ceiling" argument boils down to "the agent can only
   do allowlisted git operations through the bridge" — but the agent
   can also read the PAT and call `git` directly on the mount. The
   bridge constrains tool ergonomics, not credential exposure.

### Decision

Drop the host-side bridge entirely. The agent runs `git` from inside
the Cowork sandbox via the `bash` tool, against the mounted working
tree. CI status and logs (the one capability that genuinely needs
`api.github.com`, which the sandbox cannot reach) are folded into the
VPS agent's `/ci/*` endpoints, which the sandbox already calls for
`/containers/*` in M3.

### Consequences

- M1 collapses from "scaffold a Go MCP server, ship a Cowork plugin
  manifest, write `internal/mcpx`, `internal/repomux`, six git tools"
  to "write skills + AGENTS.md + SETUP.md, ship `.env.example`." No
  Go code in M1 at all.
- M2 is unaffected at the code level (still ships GHA + dummy
  service), but its scope shrinks at the docs/skill level:
  `diagnose-ci-failure` is deferred to M3 because there's no API path
  for log retrieval until then.
- M3 grows: `internal/ciops` and `/ci/*` endpoints are added to the
  VPS agent alongside the container surface.
- The previous M1 deliverables (committed to `main` as of `c96acd4`)
  are removed in M1 of the new plan: `cmd/bridge`, `cmd/vps-agent`
  (skeleton), `internal/{auth,ciops,deployops,dockerops,gitops,httpx,
  mcpx,repomux}`, `deploy/cowork-plugin/`,
  `configs/bridge.env.example`, `.claude-plugin/`, `go.mod`, `go.sum`,
  `QUICKSTART.md`, `scripts/bootstrap.sh`.
- The Anthropic Go MCP SDK dependency drops out, taking the
  `golang.org/x` `replace` block in `go.mod` with it (relevant only
  because Cowork's allowlist blocked the proxy for those vanity
  hosts). When `go.mod` returns in M2 for the hello service, it will
  be much smaller and proxy-blocked deps are unlikely.

### Alternatives considered (and rejected)

**Keep the bridge, lean on it harder.** Tempting because we just built
it. Rejected: the friction tax compounds across forks, and the
bridge's main load-bearing argument (credential isolation) wasn't
real.

**GitHub MCP connector for CI.** Probed `search_mcp_registry` with
multiple keyword sets (`github`, `actions`, `ci`, `workflow`, `pull
request`, `git`, `repository`, `code`) on 2026-04-29 — no GitHub
connector exists in Anthropic's registry. If/when one ships, this ADR
is worth revisiting; it would let the sandbox skip the VPS for CI
queries entirely.

**Webhook-driven CI feedback.** Real-time but adds HMAC signature
validation, a public webhook receiver, and event-ordering concerns.
Polling `/ci/runs` is good enough for the human-scale cadence we're
optimizing for.

**Pivot C revisited (non-Cowork-only template).** Same rejection as in
ADR-0001 — the template's whole point is to live with Cowork-style
sandboxing. Restricting the audience would defeat the purpose.

### Migration

Current `main` (`c96acd4`) ships the M1-as-Pivot-A artifacts. M1 of
the new plan is, in part, a destructive cleanup of those artifacts.
A single squash-merged "M1: drop bridge, sandbox-native git" commit
(or equivalent) is preferable to a long surgical sequence — the
intermediate states aren't useful and the diff is easier to review as
one block.

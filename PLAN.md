# claude-deployable — system plan

A template repo that gives a Claude agent (running in a sandbox) the ability to
drive a real project end-to-end: edit code, commit and push to GitHub, watch CI,
react to failures, and observe/deploy Docker containers on a VPS.

## Goals

The sandbox boundary is the core constraint. The agent cannot run `git` against
the host filesystem, cannot SSH into the VPS directly, and cannot reach the
GitHub API without help. We solve this with two small HTTP services, one on the
developer's laptop and one on the VPS, both fronting a narrow, allowlisted set
of operations.

Concretely the agent should be able to:

1. Create a branch, commit changes, push to origin, and pull from origin to
   sync the local working copy with remote.
2. Know whether the most recent GitHub Actions run for a given branch passed
   or failed, and on failure read the logs of the failed steps so it can
   diagnose and retry.
3. Read container status and logs from the VPS, and trigger a redeploy of a
   specific service once it has pushed a fix.

## Decisions

| Area | Decision |
|---|---|
| Language (both services) | Go |
| MCP SDK (bridge) | Official Anthropic Go MCP SDK (`github.com/modelcontextprotocol/go-sdk`). Rationale: template fork-reliability benefits from matching the spec upstream rather than a hand-rolled parser. Pinned via go.mod so forks don't drift |
| Local bridge transport | MCP server spawned by Cowork as a host-side subprocess over stdio (see ADR-0001). The bridge has full host-filesystem and host-network access; Cowork handles the sandbox↔host boundary. No TCP/UDS listener is exposed to the OS |
| VPS agent location | `https://ops.<domain>` fronted by Caddy, Go service on `127.0.0.1:8080` |
| Auth | Bridge: Cowork launches the binary; OS-user isolation is the boundary, no bearer token on the stdio transport. Repo allowlist stays as the blast-radius ceiling. VPS: bearer tokens over HTTPS, separate read vs write tokens |
| CI detection | Bridge calls `api.github.com` directly from the host, filtering runs by `headSha` (not branch alone); uses a fine-grained PAT shared with the push credential. `gh` CLI path is *not* used — it adds a dependency and the sandbox can't call `api.github.com` anyway, so we go straight to REST |
| Commit author | Dedicated `claude-agent <claude-agent@users.noreply.github.com>` identity, set per-commit via `GIT_AUTHOR_*` / `GIT_COMMITTER_*`. No bot GitHub account. Commits will not link to a profile in the GitHub UI, but the author-email pattern makes them easy to grep in `git log` |
| Push credential | Fine-grained PAT scoped to the repos in the bridge's allowlist, stored in the bridge `.env`; bridge pushes via `https://oauth2:<pat>@github.com/…` URL injected per-push (never persisted to the repo's config) |
| Branch policy | Direct commits/pushes to `main` — deliberate solo-project choice; moving to a PR flow is an architectural change, not a silent upgrade |
| Deploy mechanism | `docker compose` with images pulled from GHCR; VPS runs `docker login ghcr.io` once at install time using a `read:packages` PAT |
| Concurrency | Per-repo mutex in the bridge around any working-state mutation; CI-read tool calls are not gated by the mutex |
| Secrets | `<repo>/.env` (mode `0600`, gitignored) for the bridge, loaded at process start. The path is passed to the bridge via `CLAUDE_DEPLOYABLE_ENV` set in the Cowork plugin's `.mcp.json` `env` block. Tradeoff: the working tree is mounted into the Cowork sandbox, so the sandbox can read the PAT directly; the bridge is no longer the sole credential perimeter. Mitigated by the PAT's narrow scope (allowlisted repo, contents+actions only). `EnvironmentFile=` for the VPS systemd unit |

## Architecture

```
┌─ developer machine ────────────────────────┐        ┌─ GitHub ──────────┐
│                                            │        │                   │
│  Claude sandbox                            │        │   repo + Actions  │
│       │                                    │        │                   │
│       │ MCP tool calls (Cowork-routed)     │        └─────────▲─────────┘
│       ▼                                    │                  │
│  bridge (host process, Go MCP server) ─────┼──▶ git (+ PAT) ──┤ push
│       │                                    │                  │
│       └────────────────────────────────────┼──▶ api.github.com┘ (CI status/logs)
│                                            │
└────────────────────────────────────────────┘
                       │
                       │ HTTPS + bearer
                       ▼
┌─ VPS ──────────────────────────────────────┐
│  Caddy (TLS, Let's Encrypt)                │
│     └─▶ vps-agent (Go, 127.0.0.1:8080)     │
│            ├─ GET  /containers             │
│            ├─ GET  /containers/{name}/logs │
│            ├─ GET  /containers/{name}/health
│            └─ POST /deploy/{service}       │
│                                            │
│  docker compose workloads (pulled from GHCR)│
└────────────────────────────────────────────┘
```

## Repo layout

```
claude-deployable/
├── .github/workflows/
│   ├── ci.yml                       # build + test on push
│   └── deploy.yml                   # on main: build image, push to GHCR, POST /deploy
├── cmd/
│   ├── bridge/main.go               # local bridge entrypoint
│   └── vps-agent/main.go            # VPS agent entrypoint
├── internal/
│   ├── auth/bearer.go               # bearer-token middleware (VPS agent only)
│   ├── httpx/                       # HTTP helpers for the VPS agent (JSON errors, logging)
│   ├── mcpx/                        # MCP tool-registration helpers, JSON-schema for inputs, structured errors
│   ├── gitops/                      # branch/commit/push/pull wrappers around `git`
│   ├── ciops/                       # GitHub REST API client (runs, workflow-logs download + failed-step filter)
│   ├── dockerops/                   # `docker` CLI wrappers (list, logs, health)
│   └── deployops/                   # pull + compose up logic
├── deploy/
│   ├── vps-agent.service            # systemd unit for the VPS agent (M3)
│   ├── Caddyfile.example            # TLS termination for ops.<domain> (M3)
│   ├── cowork-plugin/               # Cowork plugin registering the bridge MCP server (M1)
│   │   ├── .claude-plugin/
│   │   │   └── plugin.json          # {name, version, description}
│   │   └── .mcp.json                # {"bridge": {"command": "bridge", "args": [...], "env": {...}}}
│   ├── compose.yml.example          # docker compose project on the VPS referencing the hello image (M2)
│   └── install-vps.sh               # idempotent installer run over SSH (M3)
├── services/
│   └── hello/                       # minimal Go HTTP service used as the deploy target (M2)
│       ├── main.go
│       └── Dockerfile
├── configs/
│   ├── bridge.env.example           # GH_PAT, repo allowlist, claude-agent identity
│   └── vps-agent.env.example        # VPS_READ_TOKEN, VPS_WRITE_TOKEN, GHCR_PAT, compose project dir
├── .claude/
│   └── skills/
│       ├── ship-a-change/           # full edit→push→CI→deploy→verify loop
│       │   └── SKILL.md
│       ├── diagnose-ci-failure/     # playbook for red CI runs
│       │   └── SKILL.md
│       └── investigate-service/     # playbook for unhealthy VPS containers
│           ├── SKILL.md
│           └── scripts/ops          # thin CLI wrapping the VPS agent API
├── AGENTS.md                        # reference: bridge MCP tools, VPS endpoints, allowlists, service↔container map
├── SETUP.md                         # human-facing: GitHub secrets, PATs, VPS users, Cowork plugin install, first-run checklist
├── PLAN.md                          # this file
└── README.md                        # elevator pitch + pointers to SETUP.md and AGENTS.md
```

A single Go module at the repo root builds both binaries, with as much code as
possible shared via `internal/`.

## Local bridge — MCP tool surface

The bridge is a Go MCP server launched by Cowork as a host-side subprocess
(stdio transport). No TCP or UDS listener is exposed. Cowork's plugin
registration is the only way to reach it; the OS-user boundary is the
security perimeter. The bridge loads its config from
`~/.claude-deployable/.env` at startup.

The bridge maintains an **allowlist of repo paths** loaded from config. Any
tool call whose `repo` argument resolves outside the allowlist is rejected
with a structured error. A **per-repo mutex** serialises any tool that
mutates working state (pull, branch, commit, push, reset); `git_status` and
the CI-read tools run outside the mutex so they don't block while a push is
in flight.

Git tools:

- `git_status({repo})` → `{branch, head_sha, dirty_files[], ahead, behind, state}` — `state` is one of `clean|dirty|merging|rebasing|detached`
- `git_pull({repo, branch?})` → `{updated_to_sha}`
- `git_branch({repo, name, from_ref?})` → `{branch, sha}`
- `git_commit({repo, message, files?})` → `{sha}` — stages `files` or all changes if omitted; uses `claude-agent` identity via `GIT_AUTHOR_*` / `GIT_COMMITTER_*`
- `git_push({repo, branch, set_upstream?})` → `{pushed_sha}` — push uses the PAT injected into the remote URL for this call only; never written to `.git/config`
- `git_reset({repo, mode: "soft"|"mixed"|"hard", ref})` → `{head_sha}`
- `git_abort({repo})` → `{state_before, state_after}` — runs the right `--abort` for the current state (merge/rebase/cherry-pick)

CI tools (GitHub REST API, called from the host where `api.github.com` is reachable):

- `ci_wait_for_run({repo, head_sha, timeout_s})` → `{run_id, status, conclusion, html_url}` — polls `GET /repos/{owner}/{repo}/actions/runs?head_sha={head_sha}` and returns once a run matching the pushed SHA is conclusive, so we can't confuse the agent's run with a concurrent push
- `ci_logs({repo, run_id, failed_only: true, tail_lines: 500, max_bytes: 65536})` → plain text — downloads the run's logs zip, filters to failed steps if requested, tails and truncates; truncation is explicit (`[...truncated N bytes]`) so the agent knows it's partial

The MCP server also exposes a trivial `bridge_ping` tool that returns the
loaded config summary (allowlist entries + claude-agent identity, no
secrets) — used as an `AGENTS.md`-level "is the bridge wired up correctly?"
check instead of a network healthz.

## VPS agent — API surface

Base URL: `https://ops.<domain>`.
Read requests require `Authorization: Bearer <VPS_READ_TOKEN>`.
Write requests (`POST /deploy/*`) require `Authorization: Bearer <VPS_WRITE_TOKEN>`.

- `GET  /containers` → `[{name, image, status, uptime_s, health}]`
- `GET  /containers/{name}/logs?since=10m&tail=500&max_bytes=65536` → plain text, truncated the same way as CI logs
- `GET  /containers/{name}/health` → `{status, last_check, details}` — reads `State.Health.Status` from `docker inspect`; services opt in by declaring a `HEALTHCHECK` in their Dockerfile
- `POST /containers/{name}/restart` → `{status, restarted_at}` — runs `docker compose restart <service>` in the configured project dir; only allowlisted service names accepted
- `GET  /healthz` → `ok`

The service list and compose project path are loaded from config. The agent
cannot specify arbitrary compose files or service names — only allowlisted
ones. `/containers/*/restart` is the only write endpoint and requires the
write token. Deploy (image pull + `docker compose up`) is intentionally
*not* an endpoint in M3 — it's driven from GitHub Actions over SSH (see
M2). A future revision may add `POST /deploy/{service}` and
`/deploy/{service}/rollback` if the agent needs to trigger deploys
directly.

## CI feedback strategy

Three options were considered:

**Polling with `gh` CLI.** Simplest on paper, but the sandbox probe in
ADR-0001 showed `api.github.com` is blocked by the Cowork proxy anyway, and
even running `gh` on the *host* via the bridge just puts a second tool on
top of the REST API we were going to call. Not worth the dependency.

**Direct GitHub REST API.** A fine-grained PAT stored in the bridge's
`.env`, called from the host (where `api.github.com` is reachable). Bridge
invokes `GET /repos/{owner}/{repo}/actions/runs?head_sha={sha}` for run
selection and downloads the run's logs zip on failure. This is the chosen
path.

**GitHub webhooks.** Real-time `workflow_run` events pushed to a public
endpoint on the VPS agent, which caches the latest run per branch. Fastest
reaction, but introduces a cross-service dependency (CI visibility depends
on the VPS being up) and needs HMAC signature validation.

**Chosen path.** Direct GitHub REST API from the bridge. Skip webhooks
unless a second consumer of CI events appears.

**Identifying the right run.** Branch alone is insufficient — if a human or
another agent pushes concurrently, the "latest run on branch X" is
ambiguous. The bridge filters runs by the `head_sha` the agent just pushed
(returned from `git_push`), so we always watch the run actually triggered
by the agent's commit.

## Security model

The bridge is a stdio MCP server spawned by Cowork as the user's own OS
process. There is no network or filesystem listener — the only way to
invoke the bridge is for Cowork to have launched it. OS-user isolation is
the perimeter; any attacker who can spawn processes as the user can
already do everything the bridge does. Secrets live in `<repo>/.env`
with mode `0600` (gitignored), loaded at process start; the path is
provided to the bridge via the `CLAUDE_DEPLOYABLE_ENV` env var set in the
Cowork plugin's `.mcp.json`. The repo allowlist loaded from that env
file is the bridge's internal gate: any tool call with a `repo` argument
outside the allowlist is rejected.

Storing `.env` inside the working tree means the Cowork sandbox can read
the PAT directly (the working tree is mounted into the sandbox); the
bridge is no longer the sole barrier between the agent and the
credential. Practically this matters less than it sounds because the
PAT's scope is fine-grained (allowlisted repos only, `contents:write` +
`metadata:read` + `actions:read`), so the agent gains no capability it
couldn't already obtain by calling `git_push` through the bridge — only
the ability to make API calls outside the bridge's curated tool surface.
Forkers who want host-only secret storage can move the file to
`~/.claude-deployable/.env` (or anywhere else) and update
`CLAUDE_DEPLOYABLE_ENV` in their plugin spec accordingly; the bridge
loader is location-agnostic.

The push PAT is the blast-radius ceiling for a compromised bridge: it is a
fine-grained PAT scoped only to the repos in the allowlist, with only the
permissions needed to push (contents: write, metadata: read) and read
Actions runs + logs (actions: read). A leaked token cannot touch repos
outside the allowlist or do anything on GitHub beyond those scopes.

On the VPS, Caddy terminates TLS on a real cert from Let's Encrypt, and the Go
service listens only on `127.0.0.1:8080`. Two tokens are configured: a read
token with broad access to `/containers/*`, and a write token that is the
only way to reach `/deploy/*`. The write token is stored separately so it can
be rotated without invalidating the read path. The GHCR pull PAT lives only
on the VPS (used once by `docker login ghcr.io` at install time) and is
scoped to `read:packages`.

Structured JSON logging — request-id (or MCP call-id), tool/endpoint, repo
or service, duration, outcome — goes to stderr on the bridge (stdout is
claimed by MCP stdio transport) and to stdout on the VPS agent, captured
by journald on the VPS and by Cowork's plugin log on the laptop.

## Agent usage contract — `AGENTS.md`

The agent needs to know the canonical sequence:

1. `git_status({repo})` — if `state != clean`, stop and surface to the human; do not proceed.
2. `git_pull({repo})` to sync with origin.
3. Edit files via the sandbox's file tools.
4. `git_commit({repo, message})` — authored as `claude-agent`.
5. `git_push({repo, branch})` — capture the returned `pushed_sha`.
6. `ci_wait_for_run({repo, head_sha: pushed_sha, timeout_s})`.
7. If `conclusion == "failure"` → `ci_logs({repo, run_id, failed_only: true, tail_lines: 500})` → diagnose → go to step 3 (retry limit: 3 consecutive failures, then escalate).
8. Once CI is green, `deploy.yml` builds the image, pushes to GHCR, and calls `POST /deploy/{service}` on the VPS. The agent confirms via `GET /containers/{name}/health`; on `unhealthy`, `POST /deploy/{service}/rollback` and escalate.

`AGENTS.md` also lists the service ↔ container-name mapping and the exact
tool/endpoint contracts so the agent doesn't have to read this doc to find
them.

## Skills

We split agent knowledge into two layers. Reference material — endpoint URLs,
auth headers, the repo allowlist, the service↔container map — lives in
`AGENTS.md` as always-on context. Workflows — multi-step procedures with
decision points — live as triggered skills in `.claude/skills/`, each
co-located with the helper scripts it needs.

Three skills ship with the template.

**`ship-a-change`.** Triggers on phrases like "ship this", "deploy this
fix", or "push to prod". Grows across M2 and M3. In M2 the skill encodes:
call `git_status` — if the tree is dirty or in a merge/rebase state, stop
and surface it to the human rather than silently mixing unfinished work
into the agent's commit — `git_pull` → edit → `git_commit` with the
`claude-agent` identity → `git_push` (capture `pushed_sha`) →
`ci_wait_for_run` keyed on that SHA → on failure jump to
`diagnose-ci-failure` → on success, the GHA `deploy.yml` has already
pushed the image and SSH'd the deploy, so the skill stops there. In M3
the skill adds a post-deploy step: once CI is green, poll
`GET /containers/hello/health` via the VPS agent until healthy or
timeout, and on unhealthy hand off to `investigate-service`. Includes
retry limits (stop and ask the human after three consecutive CI failures)
and the exact timeouts to use. No helper CLI is needed on the bridge side
— the agent calls the MCP tools directly. The VPS agent still gets a
`scripts/ops` CLI because it's a plain HTTPS API.

**`diagnose-ci-failure`.** Triggers on "CI failed", "build is red", or
automatically after `ship-a-change` observes a failed run. Prescribes the
diagnosis pattern: fetch failed-step logs via
`ci_logs({..., failed_only: true})`, classify the failure (compile error,
test failure, flaky infra, missing secret), and decide retry vs fix vs
escalate. The classification rubric is the load-bearing part — it's what
keeps the agent from mechanically retrying flaky tests forever.

**`investigate-service`.** Triggers on "container is unhealthy", "service X is
down", or "check logs on VPS". Walks through `GET /containers` → identify the
misbehaving one → `GET /containers/{name}/logs?since=30m` → decide between
restart, redeploy, and escalate. Ships with a `scripts/ops` CLI wrapping the
VPS agent API.

Git operations (branch/commit/push/pull) are deliberately *not* behind a
skill. They happen in nearly every session and belong in always-on reference,
not triggered workflows.

## Build order / milestones

Restructured on 2026-04-24 into three outcome-level slices instead of the
original eight layer-level slices. Each milestone ends at a
demonstrable, user-visible capability; docs (SETUP.md, AGENTS.md) and
skills grow incrementally alongside the code rather than being deferred.

A forker who stops after any milestone has a coherent, usable subset of
the template.

0. **Sandbox reachability spike.** *Done — see ADR-0001.* The Cowork
   sandbox cannot reach any host-side local transport. Pivot A adopted:
   the bridge is a Cowork MCP server spawned on the host.

1. **Agent can commit, push, and pull.** Scaffold the Go module and
   shared internals (`internal/mcpx`, `internal/repomux`, skeletons for
   `internal/auth` and `internal/httpx`). Build `cmd/bridge` with the
   full git tool set: `git_status`, `git_pull`, `git_branch`,
   `git_commit`, `git_push`, `git_reset`, `git_abort`. PAT-injected push
   URL is never persisted to `.git/config`. Ship
   `deploy/cowork-plugin/` (`.claude-plugin/plugin.json` + `.mcp.json`)
   and `configs/bridge.env.example`.
   Write the M1 slice of `SETUP.md` (create GitHub repo, mint the push +
   Actions-read fine-grained PAT, install the bridge plugin in Cowork,
   populate `~/.claude-deployable/.env`) and the M1 slice of `AGENTS.md`
   (git tools, canonical sequence for making a change).
   *Closeout:* from a Cowork session, edit a file in the mounted repo,
   call `git_commit` + `git_push`, verify the commit appears on GitHub
   authored as `claude-agent`.

2. **GHA deploys a dummy container after agent push; agent reacts to
   failing CI.** Add a minimal `services/hello/` Go HTTP server (`GET /`
   returns a version string, `GET /healthz` returns 200) with a
   Dockerfile. Add `.github/workflows/ci.yml` (build + test on push) and
   `.github/workflows/deploy.yml` (on `main`: build image, push to GHCR,
   SSH into the VPS and run `docker login ghcr.io && docker compose pull
   hello && docker compose up -d hello`). Deploy is driven directly from
   GHA over SSH using a deploy key — the VPS agent is *not* introduced
   in this milestone. Extend `cmd/bridge` with `ci_wait_for_run` and
   `ci_logs` against `api.github.com`. Add `.claude/skills/ship-a-change`
   (edit → commit → push → wait for CI → on red, jump to diagnose) and
   `.claude/skills/diagnose-ci-failure` (fetch failed-step logs, classify,
   decide retry vs fix vs escalate). Append the M2 slice of SETUP.md
   (provision the VPS: sudo user, docker + docker-compose, GHCR
   `read:packages` PAT for `docker login`, GHA deploy key + SSH known
   hosts, `compose.yml` on the VPS referencing the `hello` image) and the
   M2 slice of AGENTS.md (CI tools, skill trigger phrasing).
   *Closeout:* hand the agent a dummy edit that deliberately fails CI
   once; it reads the failed logs, fixes the code, pushes again, CI goes
   green, GHCR gets the new image, VPS runs the updated container.

3. **Agent reads container status and restarts services.** Build
   `cmd/vps-agent`: HTTP on `127.0.0.1:8080`, bearer-gated `GET /containers`,
   `GET /containers/{name}/logs`, `GET /containers/{name}/health`,
   `POST /containers/{name}/restart`. Write the systemd unit, Caddy config
   (TLS terminator on `ops.<domain>`), and `install-vps.sh`. Add
   `configs/vps-agent.env.example` and `scripts/ops` CLI for the VPS agent.
   Add `.claude/skills/investigate-service` (list → identify bad one →
   tail logs → restart or escalate). Extend `ship-a-change` to verify
   post-deploy health via the VPS agent and hand off to
   `investigate-service` on unhealthy. Append the M3 slice of SETUP.md
   (install VPS agent via `install-vps.sh`, wire Caddy, issue the read +
   write tokens, add `ops.<domain>` to Cowork's outbound allowlist) and
   the M3 slice of AGENTS.md (container tools, service↔container map).
   *Closeout:* intentionally kill the hello service's health probe, ask
   Cowork "what's up with the hello service", agent runs
   `investigate-service`, reads logs, restarts, confirms healthy.

### Deferred to later / out of scope

- **`/deploy` endpoint on the VPS agent.** The M2 SSH-from-GHA path
  deploys fine. Moving the deploy call through the VPS agent (so the
  agent itself can trigger deploys) is a future refactor, worth doing
  only once there's a concrete need (e.g. wanting to deploy outside GHA's
  trigger model).
- **`/deploy/{service}/rollback`.** Depends on the `/deploy` endpoint. A
  SSH-driven rollback script is a fine interim if ever needed before the
  endpoint exists.
- **GitHub webhooks for CI.** Still only worth adding if a second consumer
  of CI events appears.

## Risks and open items

**Commit attribution.** Decision taken (see decisions table): use
`claude-agent <claude-agent@users.noreply.github.com>` with no bot GitHub
account. Downstream forks can upgrade to a bot account if attribution
badges matter — the only change is the committer email in
`configs/bridge.env.example`.

**Direct-to-main is load-bearing.** There's no human gate between the agent
and production. This was a deliberate choice for solo-project speed, not a
default we'll silently "upgrade" away from — adding branch protection
requiring CI green would force a PR-based flow, which is a real architectural
change (new endpoints for PR creation, different skill wiring). Treat the
rollback endpoint + health check as the primary safety net until then.

**CI polling blocks a goroutine.** Each `ci_wait_for_run` call holds a
goroutine for the full duration of a CI run (1–5 min). Fine for a
single-agent setup, but if we ever run multiple concurrent agents, a
future revision should split into `ci_start_watch` + `ci_get_run({id})`
for stateless polling driven by a background watcher.

**Deploy model is single-tag, driven from GHA.** The compose file on the
VPS references the image by name; GHA pushes a new image to GHCR tagged
with the commit SHA and updates the `.env` on the VPS (or uses a floating
`:main` tag) before calling `docker compose up -d`. Blue/green, canary,
and multi-service coordinated deploys are out of scope. Rollback in M2 is
a manual `docker compose up -d` with the previous SHA; a proper rollback
endpoint is deferred with `POST /deploy` itself. If a service has
dependent migrations or schema changes, the agent has no visibility into
those and should not drive them.

**Push PAT rotation.** The fine-grained PAT sits in the bridge `.env` and
has no automatic rotation. `SETUP.md` should include a rotation checklist,
and we should set expiration to something reasonable (90 days) so forgetting
to rotate surfaces as an auth failure rather than a silent risk.

**Sandbox reachability.** Resolved in ADR-0001. The UDS transport is not
viable in Cowork; Pivot A (bridge as a Cowork MCP server on the host) is
adopted. Downstream risk: the bridge now depends on Cowork's MCP plugin
spec; if Cowork changes that format, the bridge's registration file has
to follow.

**MCP tool ergonomics.** MCP tools return text+JSON content, not HTTP
status codes. The bridge wraps errors in a structured JSON object
(`{error: "...", code: "..."}`) so skills can reason about them the same
way they would parse an HTTP error body. Worth calling out in `AGENTS.md`
so agents don't just `try/except` the whole call.

## ADR-0001 — Milestone 0 spike result: local bridge on UDS is not reachable from the Cowork sandbox

*Date:* 2026-04-24. *Status:* **accepted** — Pivot A adopted on 2026-04-24.

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

### Conclusion

The "local Go bridge listening on `~/.claude-deployable/bridge.sock`"
architecture is fundamentally incompatible with the Cowork sandbox model.
None of the PLAN.md fallbacks (host UDS / host TCP / cert-pinned localhost)
are reachable. This is not a configuration issue — it is a deliberate
property of the sandbox isolation.

Two architectural pivots preserve the plan's goals (agent drives
edit→commit→push→CI→deploy end-to-end). A third path would be to scope the
template to a non-Cowork agent runtime where the original plan still holds.

**Pivot A — Bridge becomes a Cowork MCP server on the host.**
The bridge stays a Go program that runs on the developer's laptop, outside
the sandbox. Instead of HTTP-over-UDS, it registers with Cowork as an MCP
server exposing tools with the same shape as the original endpoints
(`git_status`, `git_pull`, `git_branch`, `git_commit`, `git_push`,
`git_reset`, `git_abort`, `ci_wait_for_run`, `ci_logs`). Cowork routes the
tool calls across the sandbox boundary. All host-side operations (`gh` CLI,
arbitrary file deletion, `git` on the real working copy) work normally
because the bridge is a host process. The decisions table entries for
allowlists, per-repo mutex, PAT scoping, commit identity, and the
`head_sha`-filtered CI run selection are unchanged — only the transport
line and the bearer-token defence-in-depth change. This is the Cowork-native
way to extend agent capabilities and is the recommended direction if the
template is intended for Cowork users.

**Pivot B — Drop the bridge; agent works directly in the sandbox.**
The sandbox edits the mounted working copy directly (already possible via
file tools) and runs `git commit` / `git push` from inside the sandbox over
the `github.com` allowlist. Requires the user to (a) grant
`allow_cowork_file_delete` for the working folder once so `git checkout`,
`git reset`, and `git gc` work, and (b) surface `api.github.com` in the
allowlist if we want CI polling via REST — without it, CI feedback has to
come from a public endpoint (e.g. on the VPS) that the sandbox can reach.
The template's VPS agent already lives on a public domain, so extending it
with a small `/ci/*` path that proxies GitHub REST on the agent's behalf is
feasible. Simpler operationally (one service to run instead of two) but
more coupled: the VPS agent now knows about GitHub, and CI visibility
depends on the VPS being up.

**Pivot C — Document that the template targets a non-Cowork runtime.**
Keep PLAN.md as-is. Say explicitly in the README and SETUP.md that the
agent runtime must be one that allows the sandbox to reach a host-side UDS
(e.g. a Claude Code or Agent SDK configuration with a relaxed network /
filesystem boundary, or a bare-Linux dev machine where the sandbox is just
a process namespace). Mark Cowork as out of scope. The smallest change to
the existing plan; largest restriction on who can use the template.

### Decisions table — changes proposed

If Pivot A is chosen, the following row changes:

| Area | From | To |
|---|---|---|
| Local bridge transport | Unix domain socket at `~/.claude-deployable/bridge.sock` (mode `0600`) | MCP server process on the host (registered with Cowork), Unix-socket stdio transport or TCP-loopback on the host — Cowork handles the sandbox-host boundary |
| Auth | Bearer token on UDS | Auth is handled by Cowork's MCP registration; bridge still enforces the repo allowlist. Bearer token on the bridge becomes optional defence-in-depth |

Other decisions table rows (commit identity, push PAT, branch policy,
deploy mechanism, concurrency model, secrets location) are unchanged by
the pivot.

### Build-order impact

Milestone 1 ("scaffold") changes slightly under each pivot:

- Under **Pivot A**: scaffold the bridge as a Go MCP server (not an HTTP
  server); `internal/httpx` becomes `internal/mcpx`; the `cmd/bridge`
  entry point registers tools instead of routes. The VPS agent is
  unaffected (still an HTTP service behind Caddy).
- Under **Pivot B**: skip `cmd/bridge` entirely; scaffold only
  `cmd/vps-agent` and add an allowlisted `/ci/*` sub-router there.
- Under **Pivot C**: milestone 1 proceeds unchanged; add a "supported
  runtimes" section to SETUP.md listing what's required of the agent
  host.

### Recommendation

Pivot A, if the template is meant for Cowork users (which is the stated
use case — `.claude/skills/` live in the repo and the agent contract in
`AGENTS.md` is written against Cowork-style tool calls). It preserves the
separation of concerns that makes PLAN.md coherent (host does
state-mutating ops, sandbox does reasoning and editing), keeps the repo
allowlist as the blast-radius ceiling, and lets us carry the existing
decisions forward nearly intact. The tradeoff is that the bridge depends
on Cowork's MCP plumbing; if that plumbing changes, the bridge has to
follow. Acceptable given the template's target audience.

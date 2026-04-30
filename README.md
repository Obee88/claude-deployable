# claude-deployable

A template repository for projects that hand a real-world software
loop — edit → commit → push → CI → deploy → observe — to a Claude
agent running in the Cowork sandbox. Fork it, point it at a fresh
GitHub repo (and, in M2/M3, a VPS), and the included docs and skills
give the agent everything it needs to drive the loop end-to-end.

The goal is **fork reliability**: every choice in this repo is made
to survive being forked many times into projects with different
shapes. From-scratch setup docs and stable agent contracts are
load-bearing.

## How it's wired

The agent runs `git` directly inside the Cowork sandbox via the
`bash` tool, against the mounted working tree. There is no host-side
bridge. Push goes to `github.com` over the sandbox's allowlisted
egress; CI status and logs (M3) are proxied through a small Go
service on the VPS, because the sandbox can't reach
`api.github.com`. See `PLAN.md` for the full architecture and
ADR-0002 for why the host bridge was dropped.

## What's in here today

- `PLAN.md` — architecture, decisions table, milestone list, ADRs.
  The source of truth for what this template is and isn't.
- `SETUP.md` — human-facing, milestone-by-milestone installation
  checklist. Start here when forking.
- `AGENTS.md` — always-on agent reference: bash recipes for the
  canonical edit→commit→push sequence, failure modes, recovery
  commands flagged as such.
- `.claude/skills/ship-a-change/` — the M1 skill that orchestrates
  edit→commit→push as one workflow.
- `.env.example` — the variables the agent expects in `.env`
  (gitignored).
- `services/hello/` — minimal Go HTTP server (M2) that the deploy
  pipeline ships to the VPS.
- `.github/workflows/{ci,deploy}.yml` — `go vet` + `go test` on
  every push (CI), and build-image → push-to-GHCR → SSH-and-roll
  on every push to `main` (deploy, gated on the `DEPLOY_ENABLED`
  repo variable until the VPS is up).
- `deploy/compose.yml.example` — compose project the VPS uses to
  run the `hello` container.

M3 adds the VPS agent (`cmd/vps-agent/`, `internal/`), the
`investigate-service` and `diagnose-ci-failure` skills, and the
`/ci/*` and `/containers/*` endpoints. A forker who stops after any
milestone has a coherent, usable subset.

## Read these in order

1. [`PLAN.md`](PLAN.md) — what the template does and why.
2. [`SETUP.md`](SETUP.md) — get a fork running end-to-end on M1.
3. [`AGENTS.md`](AGENTS.md) — the contract the agent operates under.

## Status

Built milestone-by-milestone (see `PLAN.md`):

- **M1 — Agent commits and pushes from the sandbox.** *Shipped.*
  No Go code yet; the milestone is docs, the `ship-a-change` skill,
  and `.env.example`.
- **M2 — GitHub Actions deploys a dummy container after agent
  push.** *Implemented; deploy E2E validating against Hetzner.* The
  Go module, `services/hello`, `ci.yml`, `deploy.yml`, and
  `deploy/compose.yml.example` are in place; CI runs green on
  every push. The deploy job is gated on the `DEPLOY_ENABLED` repo
  variable, so it stays a clean *skip* until a forker walks
  through the M2 slice of `SETUP.md` and sets the secrets.
- **M3 — VPS agent ships; agent reacts to failing CI and unhealthy
  containers.** *Not yet.*

The plan deliberately stops at three demonstrable, user-visible
capabilities; anything beyond that is in `PLAN.md`'s "deferred /
out of scope" section.

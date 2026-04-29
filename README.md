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

M2 introduces a `services/hello/` dummy app, GitHub Actions for CI
and deploy, and a `deploy/compose.yml.example` for the VPS. M3 adds
the VPS agent (`cmd/vps-agent/`, `internal/`), the
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
  push.** *Not yet.*
- **M3 — VPS agent ships; agent reacts to failing CI and unhealthy
  containers.** *Not yet.*

The plan deliberately stops at three demonstrable, user-visible
capabilities; anything beyond that is in `PLAN.md`'s "deferred /
out of scope" section.

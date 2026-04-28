# claude-deployable

A template repository for projects that hand a real-world software
loop — edit → commit → push → CI → deploy → observe — to a Claude
agent running in a sandbox. Fork it, point it at a fresh GitHub repo
and a VPS, and the included plugin gives the agent the host-side
tooling it needs to drive the loop end-to-end.

The goal is **fork reliability**: every choice in this repo is made
to survive being forked many times into projects with different
shapes. From-scratch setup docs and stable tool contracts are
load-bearing.

## What's in here

- `cmd/bridge/` — Go MCP server that runs on the developer's host and
  exposes git + (later) GitHub-CI tools to the Cowork agent. The
  agent never executes `git` itself; the bridge does, with normal OS
  permissions.
- `cmd/vps-agent/` — Go HTTP service for the VPS, behind Caddy + TLS.
  Read-only container introspection plus a narrow restart endpoint.
  Lands in Milestone 3.
- `deploy/cowork-plugin/` — Cowork plugin manifest that registers the
  bridge as an MCP server.
- `services/hello/` — minimal HTTP service used as the deployment
  target in Milestone 2.
- `.claude/skills/` — multi-step playbooks the agent can trigger
  (ship-a-change, diagnose-ci-failure, investigate-service). Added
  alongside the milestones that need them.

## Read these in order

- [`PLAN.md`](PLAN.md) — architecture, the decisions table, the
  milestone list, and ADR-0001 for why the bridge is structured the
  way it is.
- [`SETUP.md`](SETUP.md) — human-facing, milestone-by-milestone
  installation checklist. Start here when forking.
- [`AGENTS.md`](AGENTS.md) — always-on agent reference: tool contracts,
  the canonical edit→commit→push sequence, error-code conventions.

## Status

This repo is built milestone-by-milestone (see `PLAN.md`):

- **M1 — Agent can commit, push, and pull.** *In place.* Bridge,
  plugin, env example, M1 SETUP/AGENTS slices are all here.
- **M2 — GHA deploys a dummy container; agent reacts to failing CI.**
  *Not yet.*
- **M3 — Agent reads container status and restarts services.** *Not
  yet.*

The plan deliberately stops at three demonstrable, user-visible
capabilities; anything beyond that is in `PLAN.md`'s "deferred /
out of scope" section.

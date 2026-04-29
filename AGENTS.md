# AGENTS.md — claude-deployable

Always-on reference for an agent operating in this repo from inside
the Cowork sandbox. Multi-step procedures live in `.claude/skills/`;
this document is the contract for the bash recipes those skills build
on top of, plus the assumptions the agent must respect.

This file covers the **Milestone 1** and **Milestone 2** surface:
sandbox-native `git` against the mounted working tree (M1) plus the
GitHub-Actions-driven deploy pipeline that builds `services/hello`
and ships it to the VPS on every push to `main` (M2). M2 adds no
agent-callable CI surface; until M3's `/ci/*` proxy lands, CI
feedback is "open the GitHub UI and read it yourself" — the
`ship-a-change` skill explicitly tells the agent to do that after
each push.

---

## What the agent has

The Cowork sandbox mounts the working tree (`$REPO`) and gives the
agent two tool families that matter here:

- **File tools** (Read / Write / Edit) — operate on files inside
  `$REPO`. Use these for all edits.
- **`bash`** — runs in the sandbox's Linux environment. The mount
  appears at the path Cowork advertises for "this folder"; resolve it
  once and treat it as `$REPO` for every recipe below. Each `bash`
  call is independent: no cwd, no env carryover. Always pass `-C
  "$REPO"` to git, and source `.env` at the top of any sequence that
  needs `GH_PAT` / `GITHUB_OWNER` / `GITHUB_REPO` /
  `CLAUDE_AGENT_*`.

There is **no curated tool surface** for git — `bash` exposes the full
CLI. That's a deliberate choice (see ADR-0002 in PLAN.md). The
canonical recipes below are the ones skills should use; anything
destructive lives in the recovery section and must not be invoked
without surfacing the situation to the human first.

---

## Single-session, single-repo assumptions

These are not enforced by code. They are load-bearing for everything
below.

- **One Cowork session per working tree at a time.** There is no
  per-repo mutex. Two agents pushing concurrently can produce
  non-fast-forward errors that the agent has no clean way to recover
  from. If the human runs a second session against the same clone,
  expect to surface a divergence and stop.
- **One repo per fork.** `GITHUB_OWNER` / `GITHUB_REPO` in `.env`
  identify the single repo the PAT can push to and (in M3) the single
  repo the VPS agent's `/ci/*` proxy queries. A multi-repo setup needs
  more than one fork.

---

## Canonical sequence: ship a change

Every "edit and push" the agent does should follow this sequence. The
`ship-a-change` skill orchestrates it; the recipes below are what the
skill executes.

### 0. Source `.env` and resolve `$REPO`

```sh
# $REPO is the absolute path to the mounted working tree inside the
# sandbox (the path Cowork advertises for the connected folder).
set -a; . "$REPO/.env"; set +a
```

`set -a` exports every variable defined for the rest of the bash
call. The `.env` is read fresh on each bash invocation because there
is no env carryover between calls.

### 1. Status check

```sh
git -C "$REPO" status --porcelain
```

If the output is **non-empty**, the working tree has uncommitted
changes the agent did not make in this session. **Stop and surface to
the human.** The agent must not silently mix unfinished work into its
commit.

Then check for in-progress operations:

```sh
ls "$REPO/.git" | grep -E '^(MERGE_HEAD|REBASE_HEAD|CHERRY_PICK_HEAD)$' && \
  echo "in-progress operation; stop and ask the human"
```

Anything matched here is a hand-off — the recovery commands
(`--abort`, `reset --hard`) live in the recovery section and are
**not** part of the canonical flow.

### 2. Sync with origin

```sh
git -C "$REPO" pull --ff-only origin main
```

Fast-forward only. If this fails because origin has diverged from the
local branch, **stop and surface** — a non-FF state means a human or
another agent pushed concurrently, and the resolution is not the
agent's call.

### 3. Edit files

Use the file tools (Read / Write / Edit) on paths under `$REPO`. Do
not run editors via `bash`.

### 4. Commit as `claude-agent`

```sh
GIT_AUTHOR_NAME="$CLAUDE_AGENT_NAME" \
GIT_AUTHOR_EMAIL="$CLAUDE_AGENT_EMAIL" \
GIT_COMMITTER_NAME="$CLAUDE_AGENT_NAME" \
GIT_COMMITTER_EMAIL="$CLAUDE_AGENT_EMAIL" \
  git -C "$REPO" commit -am "<message>"
```

The identity is set per-commit via env vars; the user's global git
config is never modified. Commit messages should explain *why*, not
just *what*.

### 5. Push, with the PAT injected for the call only

```sh
git -C "$REPO" push \
  "https://oauth2:$GH_PAT@github.com/$GITHUB_OWNER/$GITHUB_REPO.git" main
```

The PAT-injected URL is used for this single push and is **never**
written to `.git/config`. Do not run `git remote set-url` with the
PAT — that would persist it.

### 6. Capture `head_sha`

```sh
git -C "$REPO" rev-parse HEAD
```

Save the output. M3's `/ci/runs?head_sha=...` keys off it; in M1 it's
the SHA the agent reports back to the human ("pushed `<sha>`").

---

## Failure modes the agent should recognise

Listed in priority order — the first three are common, the last two
are rarer and indicate something the human should look at before the
agent retries.

- **Dirty tree before step 1.** `status --porcelain` non-empty. Stop,
  surface, do not proceed.
- **Non-fast-forward push.** Origin moved between pull (step 2) and
  push (step 5) — concurrent pusher. Re-run pull, retry push **once**.
  If it fails again, escalate.
- **PAT auth failure.** `fatal: Authentication failed for ...`. Stop
  and ask the human to rotate or re-issue the PAT. Do not retry.
- **Merge / rebase / cherry-pick already in flight.**
  `.git/MERGE_HEAD`, `.git/REBASE_HEAD`, `.git/CHERRY_PICK_HEAD`
  exist. Stop, surface, do not auto-abort.
- **Mounted-file delete `EPERM`.** The Cowork sandbox cannot unlink
  files it didn't create unless the user has granted
  `allow_cowork_file_delete` on the working folder. `git checkout`,
  `git reset --hard`, and `git gc` all delete files. If a recipe
  fails with `Operation not permitted`, ask the human to grant the
  delete permission via Cowork before retrying.

---

## Recovery operations — do not run without human confirmation

These commands destroy local state and exist for the cases where the
canonical flow has wedged. The agent should surface the situation to
the human and quote the command it intends to run **before**
executing it.

```sh
# Abort an in-progress merge / rebase / cherry-pick.  Run the one
# matching the .git/<state>_HEAD file you found in step 1.
git -C "$REPO" merge --abort
git -C "$REPO" rebase --abort
git -C "$REPO" cherry-pick --abort

# Throw away local commits and uncommitted changes back to origin.
# Loses work.
git -C "$REPO" reset --hard origin/main

# Wipe untracked files and directories.  Loses work.
git -C "$REPO" clean -fd

# Force-push.  Rewrites history on origin.  Almost never the right
# move on a shared branch.
git -C "$REPO" push --force-with-lease \
  "https://oauth2:$GH_PAT@github.com/$GITHUB_OWNER/$GITHUB_REPO.git" main
```

`--force-with-lease` is preferred over `--force` because it refuses
to overwrite commits the local clone hasn't seen — it catches the
case where another agent or human pushed in the gap.

---

---

## Deploy pipeline (M2)

After every push to `main`, two GitHub-Actions workflows fire:

- **`.github/workflows/ci.yml`** — runs `go vet`, `go test -race`, and
  `go build` against the module. Triggers on every push (any branch)
  and on PRs targeting `main`. Required-check material — if it goes
  red, do not merge; fix it on the branch.
- **`.github/workflows/deploy.yml`** — gated on the repo variable
  `DEPLOY_ENABLED`. When enabled, it builds the `services/hello`
  image, pushes it to GHCR (tagged `:<sha>`, `:<short-sha>`,
  `:main`), then SSHes into the VPS and runs `docker compose pull
  hello && docker compose up -d hello` against the project at
  `$VPS_COMPOSE_DIR`. When `DEPLOY_ENABLED` is unset or anything
  other than the literal string `"true"`, the job reports as
  *skipped* — that's the intended state until the VPS is provisioned
  per `SETUP.md` M2.

### Image coordinates

```
ghcr.io/<github_owner>/<github_repo>/hello:<tag>
```

where `<tag>` is one of:

- `<full-sha>` — immutable, this is what to pin in compose if you
  want a manual rollback.
- `<short-sha>` — same image, easier to read in `docker ps`.
- `main` — floating, always points to the latest deploy from `main`.
  This is what `deploy/compose.yml.example` references.

### What the agent should do after a push

1. Capture `head_sha` (step 7 of `ship-a-change`).
2. Tell the user:
   - the SHA pushed,
   - `https://github.com/<owner>/<repo>/commit/<sha>` to view it,
   - `https://github.com/<owner>/<repo>/actions` to watch CI and
     deploy.
3. Stop. **Do not** poll `api.github.com` from the sandbox — it is
   blocked by the Cowork egress proxy. The `/ci/*` agent-side path
   ships in M3.

### Failure modes the agent should recognise (M2)

The agent does not read GHA logs in M2, but it will sometimes hear
about failures from the user. Useful framing for those conversations:

- **CI red on the branch the agent just pushed.** Ask the user to
  paste the failed step's log. Do not ask the user to "click
  around"; ask for a copy/paste of the relevant lines.
- **Deploy job *skipped* on `main` after merge.** Expected before
  the VPS is provisioned. `SETUP.md` M2 has the flip-on checklist
  (`DEPLOY_ENABLED=true` repo variable, plus the five `VPS_*`
  secrets).
- **Deploy job *failed* on `main`.** Almost always one of: missing
  / stale secret, `known_hosts` doesn't match the VPS's current
  host key (rotated key?), or the VPS's compose dir doesn't exist.
  Do not silently re-run; surface the failure to the user with the
  step name from the GHA UI.

---

## What's not here yet

- **CI feedback recipes.** `/ci/runs` and `/ci/runs/{id}/logs` arrive
  with the VPS agent in M3. Until then: when a push lands, the agent
  reports the SHA and the human checks the GitHub Actions UI.
- **Container recipes.** The VPS agent and its `/containers/*`
  surface arrive in M3. Service ↔ container-name mapping is added to
  this document at that point — for `services/hello`, the container
  is named `hello` (see `deploy/compose.yml.example`).
- **`scripts/ops` CLI.** Ships with the `investigate-service` skill
  in M3.

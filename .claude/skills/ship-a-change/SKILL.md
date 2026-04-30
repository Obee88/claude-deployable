---
name: ship-a-change
description: Edit, commit, and push a change to GitHub from the Cowork sandbox, attributed to claude-agent. Use when the user says "ship this", "ship it", "deploy this fix", "push to prod", "push this change", "land this", or otherwise asks for an edit to make it onto main. M1 slice — git-only; CI polling and post-deploy health checks arrive in M3.
---

# ship-a-change

Orchestrates the canonical edit -> commit -> push sequence from
`AGENTS.md`. This is the skill the agent reaches for whenever the
user wants a change to land on `main`.

This is the **M1 slice**: it stops once the push is reported and the
SHA is captured. CI polling (`/ci/runs?head_sha=...`), failure
classification, and post-deploy health checks are added when M3
lands. Until then, after the push the agent reports the SHA and tells
the user to check the GitHub Actions UI.

## When to trigger

User phrases that should fire this skill:

- "ship this" / "ship it"
- "deploy this fix"
- "push to prod" / "push this change" / "push it"
- "land this on main"
- Any request that ends in "and push" / "and commit"

If the user only asks for an edit ("update the README"), don't fire
this skill — make the edit and stop. Fire only when the intent is to
land the change on the remote.

## Preconditions

- `.env` exists at the repo root with `GH_PAT`, `GITHUB_OWNER`,
  `GITHUB_REPO`, `CLAUDE_AGENT_NAME`, `CLAUDE_AGENT_EMAIL`. If it
  doesn't, surface the error and point the user at `SETUP.md`.
- One Cowork session is active against this clone. There is no
  per-repo mutex; concurrent sessions can race.

The Cowork file-delete grant is **not** a user-arranged
precondition — step 0 of the procedure below requests it itself.
Once granted on a folder, the grant persists for the session, so
re-requesting on an already-granted folder is a cheap no-op.

## Procedure

`$REPO` is the absolute mount path Cowork advertises for the
connected folder. Resolve it once at the start of the run.

### 0. Request the file-delete grant

Call `mcp__cowork__allow_cowork_file_delete` on the working folder
before any `git` invocation. Without it, the sandbox can't unlink
files inside the working tree — and `git commit` fails mid-flow
with `fatal: Unable to create '.git/index.lock': File exists` if
the sandbox can't remove a stale `.git/index.lock`. Same failure
mode applies if a `pull --ff-only` checkout needs to delete a file.

The grant only applies to the connected working folder, persists
for the rest of the Cowork session, and is a no-op when already
granted. So request it unconditionally — don't try to check first.

If the user denies the grant, **stop** and surface that the skill
can't run safely without it.

### 1. Source `.env`

```sh
set -a; . "$REPO/.env"; set +a
```

If this fails, surface "no .env at repo root — see SETUP.md" and
stop.

### 2. Status check

```sh
git -C "$REPO" status --porcelain
```

- **Empty output** -> working tree clean, continue.
- **Non-empty** -> uncommitted changes from a prior session. **Stop.**
  Quote the dirty paths to the user and ask whether to include them
  in this commit, discard them, or hand off.

Then check for in-progress merge / rebase / cherry-pick:

```sh
ls "$REPO/.git" 2>/dev/null | grep -E '^(MERGE_HEAD|REBASE_HEAD|CHERRY_PICK_HEAD)$'
```

- Any match -> **stop.** Surface the in-progress state to the user.
  Recovery (`--abort`, `reset --hard`) is **not** the agent's call;
  list the AGENTS.md recovery commands the user can pick from.

### 3. Sync with origin

```sh
git -C "$REPO" pull --ff-only origin main
```

- Success -> continue.
- `Not possible to fast-forward` -> origin has diverged. **Stop.** A
  human or another agent pushed concurrently; the agent is not
  authorised to merge.
- Any other failure -> surface and stop.

### 4. Make the edit

Use the file tools (Read / Write / Edit) on paths under `$REPO`.
Do not call editors via `bash`.

If the user's request was "ship the edit I already described
above" — the edit is what was discussed earlier in the conversation;
re-state it in one sentence before applying so there's no ambiguity.

### 5. Commit as `claude-agent`

```sh
GIT_AUTHOR_NAME="$CLAUDE_AGENT_NAME" \
GIT_AUTHOR_EMAIL="$CLAUDE_AGENT_EMAIL" \
GIT_COMMITTER_NAME="$CLAUDE_AGENT_NAME" \
GIT_COMMITTER_EMAIL="$CLAUDE_AGENT_EMAIL" \
  git -C "$REPO" commit -am "<message>"
```

Message rules:

- One-line subject, imperative mood, <=72 chars.
- Body (after a blank line) explains *why*, not *what* — the diff
  shows what.
- If the change is small (typo fix, comment edit), subject-only is
  fine.

### 6. Push, with the PAT injected for the call only

```sh
git -C "$REPO" push \
  "https://oauth2:$GH_PAT@github.com/$GITHUB_OWNER/$GITHUB_REPO.git" main
```

- Success -> continue.
- `non-fast-forward` -> origin moved between step 3 and now. Re-run
  step 3 (pull) and retry this push **once**. If it fails again,
  **stop and escalate** — repeated divergence means concurrent
  pushers and the agent should not loop.
- `Authentication failed` -> **stop.** Ask the user to rotate the
  PAT in GitHub and update `.env`. Do not retry.
- Any other failure -> surface and stop.

### 7. Capture and report `head_sha`

```sh
git -C "$REPO" rev-parse HEAD
```

Report the resulting SHA back to the user, plus the GitHub web URL
for the commit (`https://github.com/$GITHUB_OWNER/$GITHUB_REPO/commit/<sha>`).

In M1, also tell the user to check the GitHub Actions UI for the
run triggered by this push — there's no agent-side CI polling
yet. M3 replaces this paragraph with `/ci/runs?head_sha=...`
polling and an automatic green/red report.

## Don'ts

- Don't run `git remote set-url` with the PAT in the URL — that
  persists the secret to `.git/config`. The PAT goes in the push
  argument only.
- Don't run `reset --hard`, `clean -fd`, `merge --abort`, or
  `push --force` as part of the canonical flow. These are recovery
  commands. If one is needed, surface the situation and quote the
  command to the user before running it.
- Don't retry a push more than once after a `non-fast-forward`
  failure. Repeated divergence is a hand-off.
- Don't proceed past step 2 if the tree is dirty or in an
  in-progress operation.
- Don't add `Actions: read` scope to `GH_PAT` in `.env` if the user
  asks — that scope is reserved for M3's separate VPS-held PAT.

## What lands later

- **M2:** the deploy pipeline becomes real (GHA builds and pushes
  an image, SSHes to the VPS, runs `docker compose up`). The skill
  doesn't change in M2 — it still stops after the push and tells the
  user to watch the Actions UI.
- **M3:** after step 7, the skill polls
  `GET https://ops.<domain>/ci/runs?head_sha=<head_sha>&timeout_s=300`,
  reports the conclusion, and on `failure` hands off to the
  `diagnose-ci-failure` skill. On `success`, it polls
  `GET /containers/<name>/health` to confirm the deploy.

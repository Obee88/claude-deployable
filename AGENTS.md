# AGENTS.md — claude-deployable

Always-on reference for an agent operating in this repo through the
claude-deployable bridge. Multi-step procedures live in
`.claude/skills/`; this document is the contract for what tools exist
and how to call them.

This file currently covers the **Milestone 1** surface: git tools and
the canonical edit→commit→push sequence. CI tools and skills land in
M2, container tools in M3.

---

## Boundary

The bridge runs as a Cowork MCP server on the developer's host. Tool
calls cross the sandbox→host boundary via Cowork; the bridge then runs
real `git` commands against the working copy and (later) calls
`api.github.com`.

The bridge maintains a **repo allowlist** loaded from its env file. Any
tool call whose `repo` argument resolves outside the allowlist is
rejected with the structured error `{"error":"…","code":"outside_allowlist"}`.
Symlinks are resolved before the comparison, so a symlinked path is
fine as long as its target is allowlisted.

A **per-repo mutex** serialises any state-mutating tool (pull, branch,
commit, push, reset, abort). Read-only tools (`git_status`,
`bridge_ping`) skip the mutex so they don't block while a push is in
flight.

---

## Tools

Every tool returns a single text content block whose body is JSON. On
success, it is the structured result described below. On failure, it is
`{"error": "<message>", "code": "<code>"}` and the result is flagged
`isError: true`.

Common error codes:

- `outside_allowlist` — the `repo` argument isn't permitted.
- `invalid_args` — required argument missing or malformed.
- `git_failed` — the underlying git command returned non-zero. The
  `error` message contains git's stderr.

### `bridge_ping`

No arguments. Returns `{version, config: {allowlist, identity_name,
identity_email, gh_pat_present}}`. Use this once after install to
confirm the plugin is wired up. The PAT itself is never returned.

### `git_status`

```
{repo: string} →
{branch, head_sha, dirty_files[], ahead, behind, state}
```

`state` is one of `clean | dirty | merging | rebasing | cherry-picking
| detached`. **Anything other than `clean` or `dirty` is a hand-off** —
the agent must surface to the human and stop, not try to fix the state
itself.

### `git_pull`

```
{repo: string, branch?: string} → {updated_to_sha}
```

Fast-forward only. Refuses to merge — non-FF state must be resolved by
a human. If `branch` is omitted, pulls the current branch's upstream.

### `git_branch`

```
{repo: string, name: string, from_ref?: string} → {branch, sha}
```

Creates and checks out `name`, defaulting to branching from `HEAD`.
Fails if the branch already exists.

### `git_commit`

```
{repo: string, message: string, files?: string[]} → {sha}
```

Stages either `files` (if given) or all working-tree changes. Commits
using the bridge's configured `claude-agent` identity via
`GIT_AUTHOR_*` / `GIT_COMMITTER_*` — the user's global git config is
never touched. Empty messages are rejected.

### `git_push`

```
{repo: string, branch: string, set_upstream?: bool} → {pushed_sha}
```

Pushes to `origin`. The bridge injects its `GH_PAT` into the remote URL
**for the call only** (never persisted to `.git/config`), so HTTPS
remotes Just Work. SSH remotes are passed through unchanged and will
fail unless the user's SSH agent has a key for GitHub.

`pushed_sha` is what subsequent CI tools (M2) should key off of.

### `git_reset`

```
{repo: string, mode: "soft"|"mixed"|"hard", ref: string} → {head_sha}
```

Standard `git reset --<mode> <ref>`. There is no protection against a
hard reset wiping uncommitted work — that's by design (sometimes the
agent needs to throw away a botched edit).

### `git_abort`

```
{repo: string} → {state_before, state_after}
```

Runs the right `--abort` (`merge`, `rebase`, or `cherry-pick`) for the
current state. Returns an error if the repo is not in any in-progress
state.

---

## Canonical sequence: making a change

This is the always-on procedure for any non-trivial edit. Skills can
encode richer flows on top of it.

1. **`git_status`.** If `state ∉ {clean, dirty}`, **stop** — surface
   the in-progress state to the human and ask for direction. Do not
   attempt to recover automatically; merging/rebasing/cherry-picking
   are not the agent's job.
2. **`git_pull`.** Sync with origin so the commit lands on top of the
   latest upstream.
3. **Edit files** via the sandbox's normal file tools.
4. **`git_commit`.** Authored as `claude-agent`. Write a real message
   that explains *why*, not just *what*.
5. **`git_push`.** Capture the returned `pushed_sha` — M2's
   `ci_wait_for_run` will key off it.

That's M1. M2 will extend the sequence with `ci_wait_for_run` →
`ci_logs` → on red, classify and retry; M3 will add post-deploy health
verification via the VPS agent.

---

## Notes on the structured error contract

MCP tool replies do not have HTTP status codes. The bridge wraps every
failure in a JSON object with two fields:

```json
{"error": "human-readable message", "code": "stable_machine_code"}
```

Skills should branch on `code`, not `error`. The text is for human
readers and may change without notice.

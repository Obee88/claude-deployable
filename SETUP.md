# claude-deployable — setup

A human-facing checklist for getting the template running on a fresh machine. Follow the milestone slices in order; each one ends at a working subset (per `PLAN.md`).

This document covers the **Milestone 1** slice only: getting the bridge running locally so a Cowork agent can commit, push, and pull against a real GitHub repo. M2 (CI feedback + dummy deploy) and M3 (VPS agent) are added in their own slices later.

---

## Milestone 1 — bridge wired to GitHub

### What you'll have at the end

- A `bridge` binary on `$PATH`.
- A `.env` at the **repo root** (gitignored) populated with your PAT and repo allowlist.
- The `claude-deployable-bridge` plugin installed in Cowork. The bridge auto-discovers `.env` by walking up from cwd to the nearest `.git/` boundary, so `.mcp.json` ships path-free.
- A Cowork session can call `git_status`, `git_pull`, `git_branch`, `git_commit`, `git_push`, `git_reset`, `git_abort` against the allowlisted repo, and the resulting commit shows up on GitHub authored as `claude-agent <claude-agent@users.noreply.github.com>`.

### Prerequisites

- Go 1.25 or later on the host. The `go.mod` declares `go 1.25.0`. (The `replace` directives in `go.mod` only matter when building from inside the Cowork sandbox; a normal host build resolves through `proxy.golang.org` and is unaffected.)
- `git` 2.30+.
- A GitHub account with permission to create or push to the repo you want the agent to drive.

### 1. Create or pick the GitHub repo

The template is meant to be **forked**, so the typical flow is:

1. Fork or copy this repository under your own account/org.
2. Clone it locally to a path you'll add to the bridge allowlist (e.g. `~/coding/my-deployable-thing`).
3. Confirm the clone uses an `https://github.com/<owner>/<repo>.git` origin — the bridge's PAT injection is for HTTPS only. SSH origins are passed through unchanged and the agent's pushes will fail.

### 2. Mint a fine-grained PAT

GitHub → Settings → Developer settings → **Fine-grained personal access tokens** → Generate new token.

- **Repository access:** only the repos you'll list in `CLAUDE_DEPLOYABLE_ALLOWLIST`.
- **Permissions** (Repository):
  - Contents: **Read and write** — for pushes.
  - Metadata: **Read-only** — always required.
  - Actions: **Read-only** — needed in M2 for `ci_wait_for_run` / `ci_logs`. Add it now to avoid a re-issue.
- **Expiration:** 90 days. An un-rotated token then surfaces as an auth failure instead of a silent risk; rotation is on you.

Store the value somewhere temporary — you'll paste it into `./.env` (at the repo root) in step 4.

### 3. Build the bridge and put it on `$PATH`

From this repo:

```sh
go build -o ~/.local/bin/bridge ./cmd/bridge
```

Confirm `which bridge` resolves and that the binary is executable. The Cowork plugin spec calls `bridge` by basename, so anywhere on `$PATH` is fine.

### 4. Populate `./.env` at the repo root

The fast path is the bootstrap script, which copies `configs/bridge.env.example` to `.env`, rewrites the placeholder allowlist path to this clone's absolute path, and sets mode `0600`:

```sh
./scripts/bootstrap.sh
$EDITOR .env   # paste GH_PAT
```

It refuses to overwrite an existing `.env`. Manual equivalent if the script can't run on your platform:

```sh
cp configs/bridge.env.example .env
chmod 600 .env
```

The `.env` is gitignored (see `.gitignore`); the committed reference file is `configs/bridge.env.example`. Living next to the code keeps the PAT one `cd` away from the rest of the project — at the cost that the Cowork sandbox, which mounts the working tree read-only, can read it. Acceptable because the PAT's scope is fine-grained (this repo only, contents+actions); the `~/.claude-deployable/.env` location used in earlier drafts kept the secret host-only at the cost of an extra directory to remember.

Fields to set (the script handles `CLAUDE_DEPLOYABLE_ALLOWLIST` for you):

- `CLAUDE_DEPLOYABLE_ALLOWLIST` — comma-separated absolute paths to the working copies the bridge is allowed to touch. Anything outside this list is rejected with `outside_allowlist`. For a single-repo setup, this is just the absolute path to this repo.
- `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` — leave as `claude-agent` unless you have a reason to change. These land on every commit the bridge makes.
- `GH_PAT` — paste the fine-grained PAT from step 2.

The bridge logs a warning to stderr if the file is not mode `0600`, but it still starts.

### 5. Install the Cowork plugin

The plugin lives under `deploy/cowork-plugin/`. Its layout matches Cowork's empirical convention (`.claude-plugin/plugin.json` for metadata, sibling `.mcp.json` for the MCP server registration).

No edits to `.mcp.json` are required for the common case. The bridge discovers its `.env` at startup by walking up from its current working directory looking for a `.git/` entry; if a `.env` sits next to that `.git/`, it's loaded as the bridge config. The discovery order is:

1. `$CLAUDE_DEPLOYABLE_ENV` if set — explicit override, used as-is.
2. `.env` at the root of the nearest enclosing git repo (the common case).
3. `.env` next to the `bridge` binary (handy if you keep a personal env file alongside the binary in `~/.local/bin/`).
4. `~/.claude-deployable/.env` — legacy host-only fallback.

The `.git/` boundary in step 2 keeps the bridge from accidentally picking up an unrelated `.env` in `$HOME` or `/etc`. If your setup defeats discovery (e.g., Cowork spawns the bridge with cwd outside this repo on your platform), add an explicit override to the plugin's `.mcp.json`:

```json
{
  "bridge": {
    "command": "bridge",
    "args": [],
    "env": {
      "CLAUDE_DEPLOYABLE_ENV": "/Users/you/coding/claude-deployable/.env"
    }
  }
}
```

That `env` block is per-machine, so don't commit it back to `main` — leave the path-free version in the template.

Install via the Cowork UI's plugin install flow, pointing at the `deploy/cowork-plugin/` directory. After install, restart Cowork (or reload the plugin) so it spawns the bridge.

### 6. Verify

In a Cowork session targeting the repo you allowlisted, run the `bridge_ping` tool. The result should look roughly like:

```json
{
  "version": "dev",
  "config": {
    "allowlist": ["/Users/you/coding/my-deployable-thing"],
    "identity_name": "claude-agent",
    "identity_email": "claude-agent@users.noreply.github.com",
    "gh_pat_present": true
  }
}
```

`gh_pat_present: false` means the bridge couldn't find your PAT in the env file. The PAT itself is **never** included in the response.

### 7. Closeout — make a real change

Ask the agent to:

1. Call `git_status` and confirm `state: clean`.
2. Edit a file (anything — a README typo).
3. Call `git_commit` with a message and `git_push` to the configured branch.
4. Open the repo on GitHub and confirm the commit shows up authored as `claude-agent`.

If push fails with `Authentication failed`, the PAT is wrong or scoped to the wrong repo — re-check step 2.

---

## Milestone 2 — coming next

CI feedback (`ci_wait_for_run`, `ci_logs`) plus a `services/hello/` dummy container deployed via GitHub Actions. The PAT issued in step 2 already has the `Actions: read` scope it needs, so no new credentials at the M2 boundary.

## Milestone 3 — coming after that

VPS-side agent for container introspection and restart, behind Caddy + TLS, with separate read/write bearer tokens. Requires its own provisioning checklist (sudo user, Docker, GHCR `read:packages` PAT, deploy keys).

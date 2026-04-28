# claude-deployable — quickstart

The three things every fresh clone needs before the bridge can talk to GitHub. After these, follow `SETUP.md` to build the binary and install the Cowork plugin.

Assumes you've already cloned this repo to your laptop, e.g. `~/coding/claude-deployable`.

## 1. Mint a fine-grained GitHub PAT

GitHub → Settings → Developer settings → **Fine-grained personal access tokens** → Generate new token.

- **Repository access:** only the repo this clone points at (matches the bridge allowlist; the PAT's scope is your blast-radius ceiling).
- **Permissions:**
  - Contents: **Read and write** — needed for `git_push`.
  - Metadata: **Read-only** — always required.
  - Actions: **Read-only** — needed for `ci_wait_for_run` / `ci_logs` in M2. Add it now to avoid re-issuing.
- **Expiration:** 90 days. Forgetting to rotate then surfaces as an auth failure rather than a silent risk.

Copy the token value once — GitHub won't show it again.

## 2. Create `.env` at the repo root and paste the PAT

The `.env` is gitignored (see `.gitignore`); the committed reference file is `configs/bridge.env.example`. Create your local copy from it:

```sh
cd ~/coding/claude-deployable          # or wherever you cloned to
cp configs/bridge.env.example .env
chmod 600 .env
$EDITOR .env
```

Fill in:

- `GH_PAT=` — paste the token from step 1.
- `CLAUDE_DEPLOYABLE_ALLOWLIST=` — the absolute path to this clone (e.g. `/Users/you/coding/claude-deployable`). Comma-separated if you have more than one repo the bridge should drive.
- Leave `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` as `claude-agent` unless you have a reason to change. These land on every commit the bridge makes.

## 3. Point the Cowork plugin at your `.env`

Edit `deploy/cowork-plugin/.mcp.json` and replace the `/REPLACE_ME/...` placeholder with the absolute path to the `.env` you just created:

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

This change is per-machine (your absolute path won't match anyone else's), so don't commit it back to `main`. Either leave the working tree dirty, stash it, or run `git update-index --skip-worktree deploy/cowork-plugin/.mcp.json` to keep git from tracking your local edit. The placeholder on `main` is what new forkers should see when they clone.

If you forget step 3, the bridge fails loudly at startup with a config-load error pointing at `/REPLACE_ME/...` — by design.

## What's next

`SETUP.md` picks up from here: build the bridge binary, install the Cowork plugin, restart Cowork, and run the closeout test (an agent-driven `git_commit` + `git_push` round-trip from a Cowork session).

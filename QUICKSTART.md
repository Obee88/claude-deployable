# claude-deployable — quickstart

The two things every fresh clone needs before the bridge can talk to GitHub. After these, follow `SETUP.md` to build the binary and install the Cowork plugin.

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

That's it for the quickstart. The bridge auto-discovers the `.env` at runtime by walking up from its working directory until it finds a `.git/` entry; the `.env` next to that is taken to be the bridge's config. So as long as Cowork spawns the bridge with cwd anywhere inside this repo, no per-machine path lives in `.mcp.json`. If your setup is unusual and discovery fails, set `CLAUDE_DEPLOYABLE_ENV` to an absolute path in the plugin spec's `env` block as an explicit override.

## What's next

`SETUP.md` picks up from here: build the bridge binary, install the Cowork plugin, restart Cowork, and run the closeout test (an agent-driven `git_commit` + `git_push` round-trip from a Cowork session).

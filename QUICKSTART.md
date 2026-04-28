# claude-deployable — quickstart

The three things every fresh clone needs to get from `git clone` to a working bridge binary. After these, follow `SETUP.md` to install the Cowork plugin and run the closeout round-trip.

Assumes you've already cloned this repo to your laptop, e.g. `~/coding/claude-deployable`, and have Go 1.25+ on `$PATH`.

## 1. Mint a fine-grained GitHub PAT

GitHub → Settings → Developer settings → **Fine-grained personal access tokens** → Generate new token.

- **Repository access:** only the repo this clone points at (matches the bridge allowlist; the PAT's scope is your blast-radius ceiling).
- **Permissions:**
  - Contents: **Read and write** — needed for `git_push`.
  - Metadata: **Read-only** — always required.
  - Actions: **Read-only** — needed for `ci_wait_for_run` / `ci_logs` in M2. Add it now to avoid re-issuing.
- **Expiration:** 90 days. Forgetting to rotate then surfaces as an auth failure rather than a silent risk.

Copy the token value once — GitHub won't show it again.

## 2. Run the bootstrap script and paste the PAT

```sh
cd ~/coding/claude-deployable          # or wherever you cloned to
./scripts/bootstrap.sh
$EDITOR .env                           # paste your GH_PAT
```

`bootstrap.sh` copies `configs/bridge.env.example` to `.env` (which is gitignored), rewrites the placeholder allowlist path to this clone's absolute path, and sets mode `0600`. It refuses to overwrite an existing `.env` so a working setup can't be clobbered. The one field it can't fill in is the PAT — open the file and replace `GH_PAT=ghp_replace_me` with the token from step 1.

Manual fallback if the script can't run on your platform:

```sh
cp configs/bridge.env.example .env
chmod 600 .env
$EDITOR .env
```

Then set `CLAUDE_DEPLOYABLE_ALLOWLIST` to the absolute path of this clone (comma-separated if you want the bridge to drive more than one repo), paste the PAT into `GH_PAT=`, and leave `GIT_AUTHOR_NAME` / `GIT_AUTHOR_EMAIL` as `claude-agent` unless you have a reason to change them — they land on every commit the bridge makes.

## 3. Build the bridge binary

```sh
go build -o ~/.local/bin/bridge ./cmd/bridge
bridge </dev/null; echo "exit=$?"
```

The second line feeds an empty stdin so the MCP server starts, sees EOF, and exits cleanly. A healthy run prints one JSON line with `gh_pat_present: true`, your allowlist, and the `claude-agent` identity, then `exit=0`. If you see `bridge: config error: ...` instead, the `.env` didn't validate — re-check step 2.

`~/.local/bin` needs to be on `$PATH` (it usually is on macOS via `/etc/paths.d/`); the Cowork plugin spec calls `bridge` by basename, so anywhere on `$PATH` works.

The bridge auto-discovers the `.env` at runtime by walking up from its working directory until it finds a `.git/` entry; the `.env` next to that is taken to be the bridge's config. So as long as Cowork spawns the bridge with cwd anywhere inside this repo, no per-machine path lives in `.mcp.json`. If your setup is unusual and discovery fails, set `CLAUDE_DEPLOYABLE_ENV` to an absolute path in the plugin spec's `env` block as an explicit override.

## What's next

`SETUP.md` picks up from here: install the Cowork plugin, restart Cowork, and run the closeout test (an agent-driven `git_commit` + `git_push` round-trip from a Cowork session).

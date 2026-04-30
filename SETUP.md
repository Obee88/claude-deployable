# claude-deployable — setup

A human-facing checklist for getting the template running on a fresh
fork. Follow the milestone slices in order; each one ends at a
working subset (per `PLAN.md`). M1 is small and entirely
configuration — no binaries to build, no host-side process to install.

This document covers the **Milestone 1** slice only: a Cowork agent
in the connected working folder can edit files, commit as
`claude-agent`, and push to GitHub. M2 (GitHub-Actions deploy of a
dummy container) and M3 (VPS agent for CI logs and container
introspection) are added in their own slices later.

---

## Milestone 1 — agent commits and pushes from the sandbox

### What you'll have at the end

- A GitHub repo (typically a fork of this template) with a
  fine-grained PAT scoped to it.
- A `.env` at the repo root (gitignored, mode `0600`) populated with
  the PAT and identity.
- The Cowork session connected to the working folder, with the
  `allow_cowork_file_delete` grant in place.
- A real commit on `main` authored as `claude-agent
  <claude-agent@users.noreply.github.com>`, made by the agent
  end-to-end via the `ship-a-change` skill.

### Prerequisites

- A GitHub account with permission to create or push to the repo the
  agent will drive.
- Cowork (the desktop client). The sandbox has `git` 2.30+ and
  `bash` available; you don't install anything inside it.
- The repo cloned to a path on your machine you can connect as a
  Cowork folder. HTTPS origin is required (`git@github.com:...`
  origins won't work — the PAT-injected push URL is HTTPS-only).

### 1. Create or pick the GitHub repo

The template is meant to be **forked**. Typical flow:

1. Fork or copy this repository under your own account/org.
2. Clone the fork locally to a folder you'll connect to Cowork
   (e.g. `~/coding/my-deployable-thing`).
3. Confirm `git remote -v` shows an `https://github.com/<owner>/<repo>.git`
   origin. If it shows an SSH URL, run
   `git remote set-url origin https://github.com/<owner>/<repo>.git`.

### 2. Mint a fine-grained PAT (push)

GitHub → Settings → Developer settings → **Fine-grained personal
access tokens** → Generate new token.

- **Repository access:** Only select repositories — pick the single
  fork from step 1.
- **Permissions** (Repository):
  - Contents: **Read and write** — for pushes.
  - Metadata: **Read-only** — always required.
  - Workflows: **Read and write** — required as soon as the agent
    edits anything under `.github/workflows/` (which it does in M2
    when shipping `ci.yml` / `deploy.yml`). GitHub rejects pushes
    that touch workflow files from a PAT lacking this scope, even
    if Contents:write is present. PLAN.md's security section
    weighs the trade-off.
  - **Do not** add `Actions: read` to this token. M3's CI proxy uses
    a separate PAT held on the VPS; keeping them split limits the
    blast radius if either leaks.
- **Expiration:** 90 days. An un-rotated token then surfaces as an
  auth failure rather than a silent risk; rotation is on you. Add a
  recurring reminder.

Copy the token to a temporary buffer — you'll paste it into `.env`
in step 4.

### 3. Connect the working folder in Cowork and grant file-delete

In the Cowork desktop client, open the working folder you cloned in
step 1 as a connected folder for the session.

Then **grant the file-delete permission** on that folder. The
sandbox cannot unlink files it didn't create unless this grant is in
place; without it `git checkout`, `git reset --hard`, and `git gc`
all fail with `Operation not permitted`. The agent will request
this grant on first need, but doing it up front avoids the
interrupt.

In Cowork: when the agent calls `allow_cowork_file_delete`, approve
it. Or pre-grant by asking the agent to "enable file deletion on
this folder" before any work starts.

### 4. Populate `.env` at the repo root

```sh
cp .env.example .env
chmod 600 .env
```

Open `.env` and fill in:

- `GH_PAT` — the token from step 2.
- `GITHUB_OWNER` — your GitHub username or org (the part before the
  `/` in the repo URL).
- `GITHUB_REPO` — the repo name (the part after the `/`).
- `CLAUDE_AGENT_NAME` / `CLAUDE_AGENT_EMAIL` — leave the defaults
  (`claude-agent` / `claude-agent@users.noreply.github.com`) unless
  you have a reason to change them. These end up as `Author:` /
  `Committer:` on every commit the agent makes.

The `.env` is gitignored (see `.gitignore`); the committed reference
is `.env.example`.

**Why at the repo root?** The Cowork sandbox mounts the working tree
and the agent runs `git` inside the sandbox. Sourcing one file next
to the code is one less path the agent has to know about. The
trade-off — the sandbox can read the PAT — is the same blast radius
the agent already has via `git push`. PLAN.md's security section
spells this out.

### 5. Verify

In a Cowork session connected to the working folder, ask the agent
something like:

> Run `git -C "$REPO" status --porcelain` and tell me what `$REPO`
> resolves to.

The output should be empty (clean tree) and `$REPO` should be the
mount path Cowork advertises for the connected folder. If the agent
can't find `.env`, double-check it's at the repo root, not in
`configs/` or `~/.claude-deployable/` — bridge-era locations that
no longer apply.

### 6. Closeout — make a real change

Ask the agent to ship a small edit:

> Edit README.md to fix a typo and ship it.

The `ship-a-change` skill should trigger and walk through the
canonical sequence (status → pull → edit → commit → push → report
SHA). On GitHub, the resulting commit should show:

- Author: `claude-agent <claude-agent@users.noreply.github.com>`
- Committer: same.
- The avatar is GitHub's default for noreply commits — there is no
  bot account, by design.

If push fails with `Authentication failed`, the PAT is wrong or
scoped to the wrong repo — re-check step 2. If it fails with
`non-fast-forward`, someone else pushed in the gap; re-run the
sequence (the skill handles one retry).

### Rotating the PAT

When the 90-day expiry approaches (or any time you suspect leakage):

1. Mint a new PAT with the same scopes (step 2).
2. Update `GH_PAT=` in `.env`. No restart of anything is needed —
   the next bash call sources the new value.
3. Revoke the old PAT in GitHub's UI.

---

## Milestone 2 — GHA builds and deploys a dummy container

### What you'll have at the end

- A `services/hello/` Go HTTP server that returns a version banner
  on `/` and `200 ok` on `/healthz`, packaged in a multi-stage
  Docker image with a `HEALTHCHECK`.
- `.github/workflows/ci.yml` running `go vet`, `go test`, `go build`
  on every push.
- `.github/workflows/deploy.yml` building the image, pushing it to
  GHCR, and rolling the VPS container — gated on the `DEPLOY_ENABLED`
  repo variable so it stays a clean *skip* until the VPS is up.
- A VPS running Docker, with the deploy key authorised, the GHCR
  pull credential cached, and `compose.yml` placed at
  `$VPS_COMPOSE_DIR`.
- Agent-driven E2E: agent edits a file, ships it via `ship-a-change`,
  CI runs green, deploy runs green, `curl http://<vps>:8080/healthz`
  returns `ok` and `curl http://<vps>:8080/` returns the new SHA.

### Provisioning the VPS

The VPS is a Linux box you control. The instructions below assume
Ubuntu 22.04 / 24.04 LTS but anything that runs Docker works. Run
each block as root or via `sudo`.

**Before you provision:** add your laptop's SSH public key to your
cloud provider's account (e.g. on Hetzner: project → Security → SSH
Keys → Add SSH Key) **and** select that key on the create-server
form. Otherwise the new VPS boots with only an emailed root
password and you'll need a recovery dance (web console paste, or
"Rebuild" the server with the key selected) before key-based
`ssh root@<vps>` works. The recipes below assume key auth from the
moment the box comes up. If you rebuild after generating a key, you
may also need `ssh-keygen -R <vps_host>` on your laptop to clear a
stale entry from `known_hosts`.

#### a. Create the deploy user

```sh
adduser --disabled-password --gecos "" deploy
```

The user does not need passwordless `sudo`; the deploy script only
runs `docker compose ...`, which works once the user is in the
`docker` group (added in §b, after Docker is installed and the
group exists).

#### b. Install Docker + Compose, add deploy to the docker group

Use Docker's official apt repo so `docker compose` (the CLI plugin)
is available; the older standalone `docker-compose` is not used.
Pin to a specific channel if your fork has compliance reasons.

```sh
curl -fsSL https://get.docker.com | sh
systemctl enable --now docker
usermod -aG docker deploy
```

`usermod` runs *after* the Docker install so the `docker` group
exists when we add `deploy` to it. Verify:

```sh
docker version
docker compose version
id deploy   # groups should include 'docker'
```

`docker compose version` (with a space — the v2 CLI plugin) is the
expected output. The legacy standalone `docker-compose` won't work
with the v2-syntax `compose.yml` we ship in §f.

#### c. Mint the GHCR pull PAT and `docker login`

GitHub → Settings → Developer settings → Personal access tokens →
**Tokens (classic)** → Generate new token (classic).

- **Note:** something descriptive like `claude-deployable-ghcr-pull`.
- **Expiration:** 90 days, with a rotation reminder.
- **Scopes:** tick **`read:packages` only** (under the `write:packages`
  group). Leave everything else unchecked. `read:packages` alone is
  sufficient for `docker pull`.

Why classic and not fine-grained? Fine-grained PATs don't expose a
per-repo Packages permission, and GHCR's `docker login` flow still
authenticates through the classic-PAT endpoint. GitHub's own GHCR
docs endorse this path. If/when fine-grained-PAT support for
GHCR ships, this section is worth revisiting — until then, a
narrowly scoped classic PAT (`read:packages` only, no `repo`, no
`write:packages`) is the smallest blast radius available for a
pull-only deploy box.

On the VPS, as the `deploy` user (don't put the PAT in shell history
— `read -s` keeps it out of `~/.bash_history`):

```sh
su - deploy
read -s -p "Paste GHCR PAT and press Enter: " GHCR_PAT
echo "$GHCR_PAT" | docker login ghcr.io -u <github_username> --password-stdin
unset GHCR_PAT
```

This caches the credential in `/home/deploy/.docker/config.json`
(mode 0600). Subsequent `docker compose pull` calls use it without
further interaction. The "stored unencrypted" warning that prints
is expected on a server context — credential helpers are a
laptop-keychain feature, not a VPS one. Mode-0600 plaintext is the
practical baseline.

#### d. Generate a deploy key for GitHub Actions

On any trusted machine (your laptop is fine):

```sh
ssh-keygen -t ed25519 -N "" -C "claude-deployable-gha" -f /tmp/deploy_key
```

This produces `/tmp/deploy_key` (private) and `/tmp/deploy_key.pub`
(public). Keep both off the VPS for now — they go to specific
places below.

On the **VPS** as the `deploy` user, append the public half to
`authorized_keys`:

```sh
mkdir -p ~/.ssh && chmod 700 ~/.ssh
cat <<'EOF' >> ~/.ssh/authorized_keys
<paste contents of /tmp/deploy_key.pub here>
EOF
chmod 600 ~/.ssh/authorized_keys
```

Optional but recommended: lock the entry down with
`from="<github-actions-ip-range>",no-agent-forwarding,...` —
GitHub publishes its current IP ranges at
`https://api.github.com/meta`. The simpler choice is to leave it
unrestricted and rely on the key + sudoless deploy user as the
boundary.

**Verify the deploy key actually authenticates** before stuffing
the private half into a GHA secret. From your laptop:

```sh
ssh -i /tmp/deploy_key -o IdentitiesOnly=yes deploy@<vps_host> 'whoami; id; docker ps'
```

Should print `deploy`, the deploy user's group memberships
(including `docker`), and an empty `docker ps` table (just the
header). If you instead get `Permission denied (publickey)`, the
public-key append in the previous step didn't land — recheck
`~deploy/.ssh/authorized_keys` on the VPS. Catching this here saves
a confusing `deploy.yml` failure later.

#### e. Capture `known_hosts` for the VPS

From your laptop (a host that already trusts the VPS — verify the
fingerprint out-of-band the first time):

```sh
ssh-keyscan -t ed25519,rsa <vps_host> > /tmp/vps_known_hosts
cat /tmp/vps_known_hosts
wc -l /tmp/vps_known_hosts
```

Expect 2–4 lines: one ed25519 host-key line, one rsa host-key line
(some Ubuntu 24.04 builds drop rsa, in which case you'll see one
host-key line), plus optional `# <host>:22 SSH-2.0-...`
informational comments that OpenSSH ignores. The line count itself
isn't load-bearing — what matters is that at least one host-key
line is present.

Hold on to this file — you'll paste it into the `VPS_KNOWN_HOSTS`
secret in the next step. If you ever rotate the VPS host key, you
must re-run this and update the secret, otherwise deploys hang on
strict host-key checking.

#### f. Place `compose.yml` on the VPS

On the VPS as `deploy`, create the directory:

```sh
mkdir -p /home/deploy/claude-deployable
```

Then transfer `deploy/compose.yml.example` from your local clone
to that directory on the VPS. The cleanest path is `scp` from your
laptop using the deploy key you just generated:

```sh
scp -i /tmp/deploy_key \
  /path/to/your/clone/deploy/compose.yml.example \
  deploy@<vps_host>:/home/deploy/claude-deployable/compose.yml
```

That copies the template straight to its final path, dropping the
`.example` suffix. Then on the VPS as `deploy`, substitute the
placeholders (use `#` as the sed delimiter — the `|` form gets
mangled in some terminal/chat paste paths because the lines being
replaced contain no `#`):

```sh
cd /home/deploy/claude-deployable
sed -i 's#REPLACE_ME_OWNER/REPLACE_ME_REPO#<your-owner>/<your-repo>#' compose.yml
grep '^[[:space:]]*image:' compose.yml
docker compose config | grep image:
```

Both grep outputs should show
`image: ghcr.io/<your-owner>/<your-repo>/hello:main`. Use lowercase
for `<your-owner>` even if your GitHub login has capitals — GHCR
normalizes container references to lowercase. `docker compose
config` parses the full project; if the file has YAML errors it
prints them loudly here rather than at deploy time.

If `sed` fights you for any reason (paste mangling has happened in
the wild), an equivalent fallback is to overwrite the file with a
verbatim heredoc — `cat > compose.yml <<'YAML' ... YAML` — using
the substituted image path inline.

Don't run `docker compose up -d` or `docker compose pull hello`
yet — the `:main` tag doesn't exist in GHCR until `deploy.yml`
runs once. Trying now will fail with `manifest unknown` and clutter
your `docker images` cache with junk.

### Wiring up GitHub Actions

#### g. Set the repo secrets

GitHub repo → Settings → Secrets and variables → Actions → **Secrets** → New repository secret:

| Secret name        | Value                                                    |
|--------------------|----------------------------------------------------------|
| `VPS_HOST`         | hostname or IP of the VPS                                |
| `VPS_USER`         | `deploy` (or whatever name you used in step a)           |
| `VPS_SSH_KEY`      | full contents of `/tmp/deploy_key` (private half)        |
| `VPS_KNOWN_HOSTS`  | full contents of `/tmp/vps_known_hosts` from step e      |
| `VPS_COMPOSE_DIR`  | absolute path on the VPS, e.g. `/home/deploy/claude-deployable` |

Then **delete the local copies**.

On Linux (`shred` is GNU coreutils):

```sh
shred -u /tmp/deploy_key /tmp/deploy_key.pub /tmp/vps_known_hosts
```

On macOS (`shred` doesn't ship), use the BSD equivalent or plain
`rm`:

```sh
rm -P /tmp/deploy_key /tmp/deploy_key.pub /tmp/vps_known_hosts
# or, equivalently for most threat models on APFS:
# rm /tmp/deploy_key /tmp/deploy_key.pub /tmp/vps_known_hosts
```

`rm -P` overwrites with three passes before unlinking. On APFS the
security gain over plain `rm` is mostly theoretical (CoW + SSD FTL
defeat block-level secure-erase guarantees); both defeat casual
filesystem-undelete and Time-Machine-restore, which is the actual
threat model here.

#### h. Set the `DEPLOY_ENABLED` variable

Same UI page → **Variables** tab → New repository variable:

- Name: `DEPLOY_ENABLED`
- Value: `true`

Until this variable is set to the literal string `true`, every push
to `main` will enqueue `deploy.yml` but the job will be skipped.
That's by design — it lets M2 land cleanly before VPS provisioning
without dirtying the Actions UI with red runs.

### Verify

#### i. CI green

Push any branch (e.g. via `ship-a-change` on a small README edit):

```
https://github.com/<owner>/<repo>/actions/workflows/ci.yml
```

`ci` should go green. If `Tidy check` fails, run `go mod tidy`
locally and commit the result.

#### j. Deploy green

After merging the PR (or pushing to main directly per the project's
branch policy), watch:

```
https://github.com/<owner>/<repo>/actions/workflows/deploy.yml
```

The job should run all five steps (checkout → buildx → GHCR login →
build & push → SSH deploy) and finish green. On the VPS:

```sh
docker compose ps hello       # status: running
docker compose logs hello | tail
curl -s http://127.0.0.1:8080/healthz   # ok
curl -s http://127.0.0.1:8080/          # hello from claude-deployable <sha>
```

The version banner should match `git rev-parse HEAD` on `main`.

### Things that are NOT in M2

- **CI awareness in the agent.** `ship-a-change` still stops after
  the push and reports the SHA; the agent does not poll
  `api.github.com`. M3 lands `/ci/*` on the VPS agent.
- **Container introspection from the agent.** Same story —
  `/containers/*` arrives in M3 alongside the
  `investigate-service` skill.
- **Rollback automation.** Manual: edit `compose.yml` on the VPS to
  pin a previous SHA tag and `docker compose up -d hello`.
- **Blue/green or canary.** Out of scope; the deploy is a single
  rolling restart.

## Milestone 3 — coming after that

The VPS agent ships, fronted by Caddy + TLS. SETUP.md M3 adds:
issuing the read + write bearer tokens, minting the **separate**
`actions:read` PAT for the CI proxy, and adding `ops.<your-domain>`
to the Cowork outbound allowlist if your fork uses custom egress
restrictions. The `ship-a-change` skill picks up CI polling and
post-deploy health checks at this point.

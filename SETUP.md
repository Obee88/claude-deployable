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

## Milestone 2 — coming next

GitHub Actions builds a dummy `services/hello/` container and
deploys it to a VPS over SSH. The PAT issued in step 2 of this
milestone is **not** the credential used by GHA — GHA uses repo
secrets and a deploy key. SETUP.md M2 will cover provisioning the
VPS (sudo user, Docker, GHCR `read:packages` PAT for `docker
login`, the GHA deploy key + `known_hosts`).

## Milestone 3 — coming after that

The VPS agent ships, fronted by Caddy + TLS. SETUP.md M3 adds:
issuing the read + write bearer tokens, minting the **separate**
`actions:read` PAT for the CI proxy, and adding `ops.<your-domain>`
to the Cowork outbound allowlist if your fork uses custom egress
restrictions. The `ship-a-change` skill picks up CI polling and
post-deploy health checks at this point.

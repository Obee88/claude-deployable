#!/usr/bin/env bash
# scripts/bootstrap.sh — generate .env for this clone.
#
# Copies configs/bridge.env.example to .env at the repo root, rewrites
# the placeholder allowlist path to this clone's absolute path, and
# chmod 600s the result.  The one field the script cannot fill in is
# the PAT — open the file afterwards and paste it into GH_PAT=.
#
# Refuses to overwrite an existing .env so a working setup is never
# clobbered.  Run from anywhere inside the repo.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ -z "$REPO_ROOT" ]]; then
  echo "bootstrap: must be run from inside the claude-deployable git repo" >&2
  exit 1
fi

EXAMPLE="$REPO_ROOT/configs/bridge.env.example"
ENV_FILE="$REPO_ROOT/.env"

if [[ ! -f "$EXAMPLE" ]]; then
  echo "bootstrap: missing $EXAMPLE — wrong repo?" >&2
  exit 1
fi

if [[ -e "$ENV_FILE" ]]; then
  echo "bootstrap: $ENV_FILE already exists — refusing to overwrite" >&2
  echo "         delete it first if you really want to regenerate" >&2
  exit 1
fi

# Bash parameter expansion is portable across macOS and Linux; sed -i
# is not, so avoid it.  $(cat ...) strips trailing newlines, hence the
# explicit '\n' on the printf below.
content="$(cat "$EXAMPLE")"
content="${content//\/Users\/you\/coding\/claude-deployable/$REPO_ROOT}"

printf '%s\n' "$content" > "$ENV_FILE"
chmod 600 "$ENV_FILE"

echo "wrote $ENV_FILE (mode 0600)"
echo "  CLAUDE_DEPLOYABLE_ALLOWLIST set to $REPO_ROOT"
echo
echo "next steps:"
echo "  1. \$EDITOR $ENV_FILE      # paste your GH_PAT"
echo "  2. go build -o ~/.local/bin/bridge ./cmd/bridge"

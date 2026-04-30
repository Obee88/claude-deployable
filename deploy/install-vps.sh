#!/usr/bin/env bash
# install-vps.sh — idempotent installer for the claude-deployable VPS agent.
#
# Usage (on the VPS, as root or via sudo):
#   sudo /tmp/install-vps.sh /tmp/vps-agent
#
# Where /tmp/vps-agent is the freshly-built linux/amd64 binary the
# deploy workflow scp'd up. The first arg is the only required
# argument; everything else is conventional.
#
# What this script does (idempotently — safe to re-run):
#   1. Validates we're running as root and that the binary exists.
#   2. Ensures the deploy user owns /home/deploy/bin and /home/deploy/etc.
#   3. install(1)s the binary to /home/deploy/bin/vps-agent (mode 0755,
#      owner deploy:deploy). install(1) is atomic — old binary stays
#      live until the new one is fully written.
#   4. Drops the systemd unit at /etc/systemd/system/vps-agent.service.
#   5. Drops the sudoers fragment at /etc/sudoers.d/vps-agent so the
#      deploy workflow's restart step works without a password.
#   6. systemctl daemon-reload, enable, and (if env file is in place)
#      restart. If the env file is missing, the script prints a
#      one-time note about what to do next and exits 0 — re-running
#      the script after the env file is in place will start the unit.
#
# What this script deliberately does NOT do:
#   - Create /home/deploy/etc/vps-agent.env. That file holds bearer
#     tokens; minting them is a one-time human step (SETUP.md M3).
#   - Install Caddy. The repo ships deploy/Caddyfile.example; the
#     operator pastes the relevant block into /etc/caddy/Caddyfile.
#
# Locations:
#   /home/deploy/bin/vps-agent          (binary, owned by deploy:deploy)
#   /home/deploy/etc/vps-agent.env      (env file, mode 0600 — operator places)
#   /etc/systemd/system/vps-agent.service
#   /etc/sudoers.d/vps-agent            (mode 0440 — visudo refuses other modes)

set -euo pipefail

# --- Inputs ------------------------------------------------------

BIN_SRC="${1:-}"
if [[ -z "$BIN_SRC" ]]; then
	echo "usage: $0 <path-to-vps-agent-binary>" >&2
	exit 2
fi
if [[ ! -f "$BIN_SRC" ]]; then
	echo "error: binary not found at: $BIN_SRC" >&2
	exit 2
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
	echo "error: must run as root (use sudo)" >&2
	exit 2
fi

# --- Constants ---------------------------------------------------

DEPLOY_USER="deploy"
DEPLOY_HOME="/home/${DEPLOY_USER}"
BIN_DIR="${DEPLOY_HOME}/bin"
ETC_DIR="${DEPLOY_HOME}/etc"
BIN_DST="${BIN_DIR}/vps-agent"
ENV_FILE="${ETC_DIR}/vps-agent.env"

UNIT_DST="/etc/systemd/system/vps-agent.service"
SUDOERS_DST="/etc/sudoers.d/vps-agent"

# Repo-relative paths (the deploy workflow scp's the whole deploy/
# directory, but for safety we resolve the script's own dir and
# look up siblings from there).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNIT_SRC="${SCRIPT_DIR}/vps-agent.service"

if [[ ! -f "$UNIT_SRC" ]]; then
	echo "error: vps-agent.service not found next to install-vps.sh" >&2
	echo "       expected at: $UNIT_SRC" >&2
	exit 2
fi

# --- Step 1: dirs owned by deploy --------------------------------

# The deploy user already exists from M2's setup. We don't try to
# create it; failing here is a useful signal that VPS provisioning
# was skipped.
if ! id -u "$DEPLOY_USER" >/dev/null 2>&1; then
	echo "error: user '${DEPLOY_USER}' does not exist on this VPS" >&2
	echo "       follow SETUP.md M2 §a (create deploy user) first" >&2
	exit 2
fi

install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0755 "$BIN_DIR"
install -d -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0750 "$ETC_DIR"

# --- Step 2: install the binary ----------------------------------

# install(1) is atomic — the destination is replaced via rename(2),
# so an in-flight HTTP request running against the old binary is
# unaffected until systemctl restart.
install -o "$DEPLOY_USER" -g "$DEPLOY_USER" -m 0755 "$BIN_SRC" "$BIN_DST"

# --- Step 3: install the systemd unit ----------------------------

install -o root -g root -m 0644 "$UNIT_SRC" "$UNIT_DST"

# --- Step 4: install the sudoers fragment ------------------------

# Only the two systemctl invocations the deploy workflow needs.
# Narrow scope so a stolen deploy key can't `sudo anything`.
TMP_SUDOERS="$(mktemp)"
trap 'rm -f "$TMP_SUDOERS"' EXIT
cat > "$TMP_SUDOERS" <<'EOF'
# Managed by deploy/install-vps.sh — do not edit by hand.
# Lets the deploy workflow restart the agent after a binary swap
# without password, and lets a human spot-check status. Nothing else.
deploy ALL=(root) NOPASSWD: /bin/systemctl restart vps-agent
deploy ALL=(root) NOPASSWD: /bin/systemctl status vps-agent
deploy ALL=(root) NOPASSWD: /bin/systemctl is-active vps-agent
EOF

# visudo -cf validates the syntax before we install it. A bad
# sudoers file will lock root out — never skip this check.
if ! visudo -cf "$TMP_SUDOERS" >/dev/null; then
	echo "error: sudoers fragment failed visudo validation; aborting" >&2
	exit 2
fi
install -o root -g root -m 0440 "$TMP_SUDOERS" "$SUDOERS_DST"

# --- Step 5: reload systemd, enable -----------------------------

systemctl daemon-reload
systemctl enable vps-agent.service >/dev/null

# --- Step 6: start (only if env is configured) -------------------

if [[ -f "$ENV_FILE" ]]; then
	echo "→ restarting vps-agent (env file present)"
	systemctl restart vps-agent.service
	sleep 1
	systemctl --no-pager --lines=10 status vps-agent.service || true
else
	cat <<NOTE
→ vps-agent installed but NOT started.

  Next step (one-time, as the deploy user):

    install -m 0700 -d /home/deploy/etc
    cp /tmp/vps-agent.env.example /home/deploy/etc/vps-agent.env
    chmod 0600 /home/deploy/etc/vps-agent.env
    \$EDITOR /home/deploy/etc/vps-agent.env   # set tokens, allowlist, compose dir
    sudo systemctl restart vps-agent

  Then verify:

    curl -s http://127.0.0.1:\$PORT/healthz   # ok <version>
    journalctl -u vps-agent --no-pager --lines=20

  (See SETUP.md M3 for the token-minting and Caddy steps.)
NOTE
fi

echo "✓ install-vps.sh complete"

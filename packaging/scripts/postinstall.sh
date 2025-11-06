#!/bin/sh
set -e

# Create a dedicated system user for the rendezvous server if it does not exist.
if ! getent passwd relay-tunnel >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
        useradd --system --no-create-home --shell /usr/sbin/nologin \
            --comment "relay-tunnel rendezvous server" relay-tunnel || true
    elif command -v adduser >/dev/null 2>&1; then
        adduser --system --no-create-home --group relay-tunnel || true
    fi
fi

# Ensure a group exists and owns the config dir.
if ! getent group relay-tunnel >/dev/null 2>&1; then
    groupadd --system relay-tunnel >/dev/null 2>&1 || true
fi
if [ -d /etc/relay-tunnel ]; then
    chown -R relay-tunnel:relay-tunnel /etc/relay-tunnel 2>/dev/null || true
fi

# Reload systemd so the new unit is visible. The server is NOT auto-enabled;
# the admin opts in with: systemctl enable --now relay-tunnel-server
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

exit 0

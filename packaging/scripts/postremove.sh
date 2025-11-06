#!/bin/sh
set -e

# Reload systemd after the unit file has been removed.
if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

# On full removal (not upgrade), drop the system user. Package managers pass
# different arguments; handle the common "remove"/"purge"/"0" cases.
case "$1" in
    purge|remove|0)
        if getent passwd relay-tunnel >/dev/null 2>&1; then
            if command -v userdel >/dev/null 2>&1; then
                userdel relay-tunnel >/dev/null 2>&1 || true
            elif command -v deluser >/dev/null 2>&1; then
                deluser --system relay-tunnel >/dev/null 2>&1 || true
            fi
        fi
        ;;
esac

exit 0

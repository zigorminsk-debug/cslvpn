#!/bin/sh
# Runs before removal of the package: stop the services, keep the config.
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop cslvpn-client >/dev/null 2>&1 || true
    systemctl stop cslvpn >/dev/null 2>&1 || true
    systemctl disable cslvpn-client >/dev/null 2>&1 || true
    systemctl disable cslvpn >/dev/null 2>&1 || true
fi
exit 0

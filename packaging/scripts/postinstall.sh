#!/bin/sh
# Runs after install/upgrade of the .deb/.rpm/.apk package.
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
    # The units only reference *.env files; create them so that a small edit is
    # all that is needed (client.env doubles as the unit's ConditionPathExists).
    for f in server client; do
        if [ -f "/etc/cslvpn/$f.env.example" ] && [ ! -f "/etc/cslvpn/$f.env" ]; then
            install -m 0600 -o root -g root "/etc/cslvpn/$f.env.example" "/etc/cslvpn/$f.env" || true
        fi
    done
fi

cat <<'MSG'
CSLVPN installed.

Relay server:
  1. edit /etc/cslvpn/server.env (no need to copy the example: it is in place)
  2. systemctl enable --now cslvpn
  3. open UDP/7777 and TCP/7777 in your firewall

Client:
  1. edit /etc/cslvpn/client.env (server, network, password, transport, iface)
  2. systemctl enable --now cslvpn-client

Step-by-step deployment guide:
  /usr/share/doc/cslvpn/docs/DEPLOYMENT.md
  https://github.com/zigorminsk-debug/cslvpn/blob/main/docs/DEPLOYMENT.md

Quick test without systemd:
  cslvpn server -listen :7777 -admin 127.0.0.1:7778
  cslvpn client -server <IP>:7777 -network office -password secret -create
MSG
exit 0

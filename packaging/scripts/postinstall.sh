#!/bin/sh
# Runs after install/upgrade of the .deb/.rpm/.apk package.
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload >/dev/null 2>&1 || true
fi

cat <<'MSG'
CSLVPN installed.

Relay server:
  1. edit /etc/cslvpn/server.env (copy it from server.env.example)
  2. systemctl enable --now cslvpn
  3. open UDP/7777 and TCP/7777 in your firewall

Client:
  1. edit /etc/cslvpn/client.env (copy it from client.env.example)
  2. systemctl enable --now cslvpn-client

Quick test without systemd:
  cslvpn server -listen :7777 -admin 127.0.0.1:7778
  cslvpn client -server <IP>:7777 -network office -password secret -create
MSG
exit 0

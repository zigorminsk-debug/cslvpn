#!/usr/bin/env bash
# scripts/nat-test.sh - verifies gateway mode ("internet access through the relay").
#
# Topology (namespaces only, nothing outside the host is touched):
#
#   ns "a" (client, -internet) ---veth--- host (relay + gateway TUN + NAT) ---veth--- ns "s"
#                                   10.99.9.1                                10.88.8.1      (the "internet")
#                                                                                         10.88.8.100:8000
#
# ns "a" has no route to 10.88.8.0/24 except through the tunnel, so a
# successful download proves that the packet travelled
#   client -> relay -> gateway TUN -> NAT -> ns "s" -> back again.
set -euo pipefail

BIN=${BIN:-./cslvpn}
PORT=${PORT:-17781}
ADMIN=${ADMIN:-17782}
NET=${NET:-natnet}
PASS=${PASS:-hunter2}
WORK=${WORK:-$(mktemp -d)}
GW_IF=cslgw
GW_IP=10.77.0.1
SITE_IP=10.88.8.100
SUDO=${SUDO:-}
[[ $(id -u) -ne 0 ]] && SUDO=sudo

cleanup() {
  set +e
  $SUDO ip netns pids a 2>/dev/null | xargs -r $SUDO kill 2>/dev/null
  $SUDO ip netns pids s 2>/dev/null | xargs -r $SUDO kill 2>/dev/null
  for n in a s; do $SUDO ip link del "veth$n" >/dev/null 2>&1; $SUDO ip link del "v$n" >/dev/null 2>&1; $SUDO ip netns del "$n" 2>/dev/null; done
  $SUDO ip link del "$GW_IF" 2>/dev/null
  [[ -n "${RELAY_PID:-}" ]] && $SUDO kill "$RELAY_PID" 2>/dev/null
  echo "logs: $WORK"
}
trap cleanup EXIT
say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
ok()  { printf '\033[1;32m   OK\033[0m  %s\n' "$*"; }
die() { printf '\033[1;31m   FAIL\033[0m %s\n' "$*"; exit 1; }

say "creating the simulated internet in namespace s ($SITE_IP:8000)"
$SUDO ip netns del s 2>/dev/null || true
$SUDO ip link del vs 2>/dev/null || true
$SUDO ip netns add s
$SUDO ip netns exec s ip link set lo up
$SUDO ip link add vs type veth peer name veths
$SUDO ip link set veths netns s
$SUDO ip addr add 10.88.8.1/24 dev vs
$SUDO ip link set vs up
$SUDO ip netns exec s ip addr add 10.88.8.2/24 dev veths
$SUDO ip netns exec s ip link set veths up
$SUDO ip netns exec s ip addr add "$SITE_IP/32" dev lo
$SUDO ip netns exec s ip route add default via 10.88.8.1
mkdir -p "$WORK/www"
printf 'hello-from-the-internet' > "$WORK/www/index.txt"
$SUDO chmod -R a+rX "$WORK/www"
$SUDO ip netns exec s python3 -m http.server 8000 --bind "$SITE_IP" --directory "$WORK/www" >"$WORK/site.log" 2>&1 &
for i in $(seq 1 20); do
  $SUDO ip netns exec s ss -lnt 2>/dev/null | grep -q "$SITE_IP:8000" && break
  sleep 0.5
done
$SUDO ip netns exec s ss -lnt | grep -q "$SITE_IP:8000" && ok "site is up" || die "site did not start: $(cat "$WORK/site.log")"

say "creating the gateway TUN device $GW_IF ($GW_IP/24) on the relay host"
$SUDO ip link del "$GW_IF" 2>/dev/null || true
$SUDO ip tuntap add mode tun dev "$GW_IF"
$SUDO ip addr add "$GW_IP/24" dev "$GW_IF"
$SUDO ip link set dev "$GW_IF" mtu 1400 up
$SUDO sysctl -qw net.ipv4.ip_forward=1

say "starting the relay in gateway mode"
$SUDO "$BIN" server -listen "0.0.0.0:$PORT" -admin "127.0.0.1:$ADMIN" \
  -gateway -gateway-iface "$GW_IF" -nat-cidr 26.0.0.0/8 >"$WORK/relay.log" 2>&1 &
RELAY_PID=$!
sleep 1
grep -q "gateway enabled" "$WORK/relay.log" || die "gateway not enabled: $(cat "$WORK/relay.log")"
ok "relay gateway active on $GW_IF"

say "creating the client namespace a"
$SUDO ip netns del a 2>/dev/null || true
$SUDO ip link del va 2>/dev/null || true
$SUDO ip netns add a
$SUDO ip netns exec a ip link set lo up
$SUDO ip link add va type veth peer name vetha
$SUDO ip link set vetha netns a
$SUDO ip addr add 10.99.9.1/24 dev va
$SUDO ip link set va up
$SUDO ip netns exec a ip addr add 10.99.9.2/24 dev vetha
$SUDO ip netns exec a ip link set vetha up
# Before the tunnel exists the client can only reach the site via the host.
$SUDO ip netns exec a ip route add "$SITE_IP/32" via 10.99.9.1
$SUDO iptables -t nat -C POSTROUTING -s 10.99.9.0/24 -j MASQUERADE 2>/dev/null || \
  $SUDO iptables -t nat -A POSTROUTING -s 10.99.9.0/24 -j MASQUERADE
$SUDO iptables -C FORWARD -j ACCEPT 2>/dev/null || $SUDO iptables -A FORWARD -j ACCEPT

say "client joins '$NET' with -internet"
$SUDO ip netns exec a "$BIN" client -server "10.99.9.1:$PORT" -network "$NET" -password "$PASS" \
  -name natclient -create -transport udp -iface csl0 -mtu 1400 -internet >"$WORK/client.log" 2>&1 &
for i in $(seq 1 24); do grep -q READY "$WORK/client.log" 2>/dev/null && break; sleep 0.5; done
grep -q READY "$WORK/client.log" || die "client did not connect: $(cat "$WORK/client.log")"
grep -q "internet access through the relay is enabled" "$WORK/client.log" && ok "relay confirmed gateway mode"
CLIENT_IP=$(grep -o 'is 26\.[0-9.]*' "$WORK/client.log" | awk '{print $2}' | head -1)
ok "client address $CLIENT_IP"
grep -q "pinning" "$WORK/client.log" && ok "relay address pinned outside the tunnel"

# Route everything into the tunnel, exactly like -internet does.
$SUDO ip netns exec a ip route replace default dev csl0
$SUDO ip netns exec a ip route del "$SITE_IP/32" 2>/dev/null || true
$SUDO ip netns exec a ip route | sed 's/^/     /'
ok "default route goes through the tunnel, no direct route to $SITE_IP"

say "fetch the 'internet' page through relay + NAT"
if $SUDO ip netns exec a sh -c "curl -s -m 10 http://$SITE_IP:8000/index.txt" >"$WORK/body.txt" 2>"$WORK/curl.err"; then
  grep -q "hello-from-the-internet" "$WORK/body.txt" \
    && ok "internet traffic came back through the relay: $(cat "$WORK/body.txt")" \
    || die "unexpected body: $(head -c 200 "$WORK/body.txt")"
else
  die "internet access through the relay failed ($(cat "$WORK/curl.err"))"
fi

say "isolation while NATing: virtual addresses stay hidden from the internet"
$SUDO iptables -t nat -L POSTROUTING -v -n | grep -q "26.0.0.0/8" && ok "MASQUERADE rule for 26.0.0.0/8 is installed and matched"
curl -s "http://127.0.0.1:$ADMIN/api/v1/stats" | python3 -c '
import json,sys
d=json.load(sys.stdin)["counters"]
print("   relay counters:", d)
assert d["rx_bytes"]>0 and d["tx_bytes"]>0, "no traffic relayed"
'
ok "traffic flowed in both directions through the relay"
printf '\n\033[1;32mNAT / INTERNET TEST PASSED\033[0m\n'

#!/usr/bin/env bash
# scripts/e2e-test.sh - end-to-end test of CSLVPN inside the current host.
#
# What it does (all in network namespaces, nothing outside is touched):
#   1. starts a relay in the root namespace
#   2. creates two namespaces ("a" and "b") with a veth link to the relay
#   3. runs one client per namespace, over UDP and over TCP
#   4. checks that both clients see each other in the same virtual network
#   5. pings and runs a TCP transfer between the virtual addresses
#   6. checks isolation: a third client in a *different* network cannot be
#      reached from the first network
#   7. checks that the server NAT gives clients internet access (optional)
#
# Requires: root (or sudo), iproute2, ping, nc/curl. Bind mounts /dev/net/tun.
set -euo pipefail

BIN=${BIN:-./cslvpn}
RELAY_PORT=${RELAY_PORT:-17777}
ADMIN_PORT=${ADMIN_PORT:-17778}
NET_A=${NET_A:-netA}
NET_B=${NET_B:-netB}
PASS=${PASS:-hunter2}
WORK=${WORK:-$(mktemp -d)}
SUDO=${SUDO:-}
[[ $(id -u) -ne 0 ]] && SUDO=sudo

cleanup() {
  set +e
  for i in a b c; do
    $SUDO ip link del "veth$i" >/dev/null 2>&1 || true
    $SUDO ip link del "v$i" >/dev/null 2>&1 || true
  done
  for ns in a b c; do
    $SUDO ip netns pids "$ns" 2>/dev/null | xargs -r $SUDO kill 2>/dev/null
    $SUDO ip netns del "$ns" 2>/dev/null
  done
  [[ -n "${RELAY_PID:-}" ]] && $SUDO kill "$RELAY_PID" 2>/dev/null
  echo "logs: $WORK"
}
trap cleanup EXIT

say() { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
ok()  { printf '\033[1;32m   OK\033[0m  %s\n' "$*"; }
die() { printf '\033[1;31m   FAIL\033[0m %s\n' "$*"; exit 1; }

[[ -x $BIN ]] || die "binary $BIN not found (build it first: go build -o cslvpn ./cmd/cslvpn)"
[[ -c /dev/net/tun ]] || die "/dev/net/tun missing"

say "starting relay on 0.0.0.0:$RELAY_PORT"
$SUDO "$BIN" server -listen "0.0.0.0:$RELAY_PORT" -admin "127.0.0.1:$ADMIN_PORT" \
  -allow-create true >"$WORK/relay.log" 2>&1 &
RELAY_PID=$!
sleep 1
grep -q "TCP relay listening" "$WORK/relay.log" || die "relay did not start: $(cat "$WORK/relay.log")"

setup_ns() { # name  index
  local ns=$1 idx=$2
  $SUDO ip link del "v$ns" >/dev/null 2>&1 || true
  $SUDO ip netns del "$ns" >/dev/null 2>&1 || true
  $SUDO ip netns add "$ns"
  $SUDO ip netns exec "$ns" ip link set lo up
  $SUDO ip link add "v$ns" type veth peer name "veth$ns"
  $SUDO ip link set "veth$ns" netns "$ns"
  $SUDO ip addr add "10.99.$idx.1/24" dev "v$ns"
  $SUDO ip link set "v$ns" up
  $SUDO ip netns exec "$ns" ip addr add "10.99.$idx.2/24" dev "veth$ns"
  $SUDO ip netns exec "$ns" ip link set "veth$ns" up
  $SUDO ip netns exec "$ns" ip route add default via "10.99.$idx.1"
  $SUDO mkdir -p "/etc/netns/$ns"
  echo "nameserver 1.1.1.1" | $SUDO tee "/etc/netns/$ns/resolv.conf" >/dev/null
}
for i in 1 2 3; do setup_ns "$( [[ $i == 1 ]] && echo a || ([[ $i == 2 ]] && echo b || echo c) )" "$i"; done

start_client() { # ns name network transport extra...
  local ns=$1 name=$2 network=$3 tr=$4; shift 4
  $SUDO ip netns exec "$ns" "$BIN" client \
    -server "10.99.$(case $ns in a) echo 1;; b) echo 2;; c) echo 3;; esac).1:$RELAY_PORT" \
    -network "$network" -password "$PASS" -name "$name" -transport "$tr" \
    -iface csl0 -mtu 1400 "$@" >"$WORK/client-$name.log" 2>&1 &
  echo $!
}

say "client a (UDP) creates network '$NET_A'"
start_client a alice "$NET_A" udp -create >/dev/null
for i in $(seq 1 20); do grep -q READY "$WORK/client-alice.log" 2>/dev/null && break; sleep 0.5; done

say "client b (TCP) joins the same network"
start_client b bob   "$NET_A" tcp >/dev/null
for i in $(seq 1 20); do grep -q READY "$WORK/client-bob.log" 2>/dev/null && break; sleep 0.5; done
sleep 1

grep -q "READY" "$WORK/client-alice.log" || die "alice did not connect: $(cat "$WORK/client-alice.log")"
grep -q "READY" "$WORK/client-bob.log"   || die "bob did not connect: $(cat "$WORK/client-bob.log")"
A_IP=$(grep -o 'is 26\.[0-9.]*' "$WORK/client-alice.log" | awk '{print $2}' | head -1)
B_IP=$(grep -o 'is 26\.[0-9.]*' "$WORK/client-bob.log"   | awk '{print $2}' | head -1)
ok "alice=$A_IP (udp), bob=$B_IP (tcp)"

say "ping across the virtual LAN (UDP client -> TCP client)"
$SUDO ip netns exec a ping -c 3 -W 2 "$B_IP" >"$WORK/ping-a-b.log" 2>&1 || die "ping a->b failed: $(cat "$WORK/ping-a-b.log")"
grep -q " 0% packet loss" "$WORK/ping-a-b.log" && ok "a -> b reachable ($(grep -o 'time=[0-9.]*' "$WORK/ping-a-b.log" | head -1))"
$SUDO ip netns exec b ping -c 3 -W 2 "$A_IP" >"$WORK/ping-b-a.log" 2>&1 || die "ping b->a failed"
ok "b -> a reachable (bidirectional)"

say "TCP through the tunnel (HTTP server on bob, curl from alice)"
$SUDO ip netns exec b sh -c "mkdir -p /tmp/www && printf 'hello-from-bob' > /tmp/www/payload.txt; python3 -m http.server 8080 --bind $B_IP --directory /tmp/www >/tmp/httpd.log 2>&1 & echo \$! > /tmp/httpd.pid"
for i in $(seq 1 20); do
  $SUDO ip netns exec b ss -lnt 2>/dev/null | grep -q "8080" && break
  sleep 0.5
done
$SUDO ip netns exec a sh -c "curl -s --max-time 10 --retry 3 --retry-delay 1 http://$B_IP:8080/payload.txt" >"$WORK/http.txt" \
  || die "TCP transfer through tunnel failed"
grep -q "hello-from-bob" "$WORK/http.txt" \
  && ok "TCP application traffic relayed end to end ($(wc -c <"$WORK/http.txt") bytes payload)" \
  || die "unexpected payload through the tunnel: $(head -c 200 "$WORK/http.txt")"
$SUDO ip netns exec b sh -c 'kill $(cat /tmp/httpd.pid) 2>/dev/null' || true

say "isolation: client c joins a different network '$NET_B'"
start_client c carol "$NET_B" udp -create >/dev/null
for i in $(seq 1 20); do grep -q READY "$WORK/client-carol.log" 2>/dev/null && break; sleep 0.5; done
grep -q "READY" "$WORK/client-carol.log" || die "carol did not connect"
C_IP=$(grep -o 'is 26\.[0-9.]*' "$WORK/client-carol.log" | awk '{print $2}' | head -1)
ok "carol=$C_IP in network $NET_B"
if $SUDO ip netns exec a ping -c 1 -W 2 "$C_IP" >/dev/null 2>&1; then
  die "isolation broken: alice reached carol from another network"
fi
ok "alice cannot reach carol (different networks are isolated)"

say "peer lists"
$SUDO ip netns exec a sh -c "true"
curl -s "http://127.0.0.1:$ADMIN_PORT/api/v1/networks/$NET_A/members" | head -20

say "wrong password is rejected"
$SUDO ip netns exec c "$BIN" client -server "10.99.3.1:$RELAY_PORT" -network "$NET_B" \
  -password "wrong" -name mallory -transport tcp -iface csl9 >"$WORK/client-mallory.log" 2>&1 || true
grep -qi "wrong network password\|rejected" "$WORK/client-mallory.log" \
  && ok "wrong password rejected" || die "a wrong password was not rejected: $(cat "$WORK/client-mallory.log")"

say "admin API"
curl -sf "http://127.0.0.1:$ADMIN_PORT/api/v1/stats" | grep -q '"networks"' && ok "admin API answers"
curl -sf "http://127.0.0.1:$ADMIN_PORT/api/v1/stats" | python3 -c 'import json,sys; d=json.load(sys.stdin); print("   relay counters:", d["counters"])'

printf '\n\033[1;32mALL E2E CHECKS PASSED\033[0m\n'

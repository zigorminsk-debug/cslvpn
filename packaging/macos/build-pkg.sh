#!/bin/bash
# macOS .pkg builder for CSLVPN (runs on a macOS runner).
#
#   ./packaging/macos/build-pkg.sh <binary> <version> <arch> <out.pkg>
#
# Layout of the package:
#   /usr/local/bin/cslvpn
#   /usr/local/share/cslvpn/README.md, docs/, LICENSE
#   /Library/LaunchDaemons/io.cslvpn.relay.plist (disabled by default)
#
# The client itself needs a TUN device; on macOS that means running the binary
# with root privileges (see docs/CLIENT.md). No kernel extension is installed.
set -euo pipefail

BIN=${1:?binary}
VERSION=${2:?version}
ARCH=${3:-arm64}
OUT=${4:?output.pkg}
ROOT=$(cd "$(dirname "$0")/../.." && pwd)

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

PKGROOT="$WORK/root"
SCRIPTS="$WORK/scripts"
mkdir -p "$PKGROOT/usr/local/bin" "$PKGROOT/usr/local/share/cslvpn/docs" \
         "$PKGROOT/Library/LaunchDaemons" "$SCRIPTS"

install -m 0755 "$BIN" "$PKGROOT/usr/local/bin/cslvpn"
install -m 0644 "$ROOT/README.md" "$PKGROOT/usr/local/share/cslvpn/README.md"
install -m 0644 "$ROOT/LICENSE" "$PKGROOT/usr/local/share/cslvpn/LICENSE"
cp -R "$ROOT/docs" "$PKGROOT/usr/local/share/cslvpn/"

cat > "$PKGROOT/Library/LaunchDaemons/io.cslvpn.relay.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>io.cslvpn.relay</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/cslvpn</string>
    <string>server</string>
    <string>-listen</string><string>:7777</string>
    <string>-admin</string><string>127.0.0.1:7778</string>
  </array>
  <key>RunAtLoad</key><false/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>/var/log/cslvpn-relay.log</string>
  <key>StandardErrorPath</key><string>/var/log/cslvpn-relay.err.log</string>
</dict>
</plist>
PLIST

cat > "$SCRIPTS/postinstall" <<'POST'
#!/bin/bash
# Refresh the launchd cache so the (disabled) daemon is known to the system.
/bin/launchctl bootstrap system /Library/LaunchDaemons/io.cslvpn.relay.plist 2>/dev/null || true
/bin/launchctl bootout system/io.cslvpn.relay 2>/dev/null || true
echo "CSLVPN installed: /usr/local/bin/cslvpn"
echo "Start the relay manually: sudo cslvpn server -listen :7777 -admin 127.0.0.1:7778"
echo "Or enable the daemon:     sudo launchctl enable system/io.cslvpn.relay && sudo launchctl kickstart -k system/io.cslvpn.relay"
exit 0
POST

cat > "$SCRIPTS/preinstall" <<'PRE'
#!/bin/bash
# Nothing to stop: the daemon is disabled by default.
exit 0
PRE

chmod +x "$SCRIPTS/postinstall" "$SCRIPTS/preinstall"

pkgbuild --root "$PKGROOT" \
         --scripts "$SCRIPTS" \
         --identifier io.cslvpn.pkg \
         --version "$VERSION" \
         --install-location "/" \
         --ownership recommended \
         "$OUT"

echo "built $OUT for $ARCH"

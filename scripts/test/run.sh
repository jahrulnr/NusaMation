#!/usr/bin/env bash
#
# Regression test for scripts/install.sh. Runs the installer against a fake
# release directory (a stub `curl` in PATH serves the assets) and verifies:
#   - pinned and "latest" downloads install the binary + env file
#   - re-installing is idempotent (env file is kept)
#   - a tampered binary fails checksum verification and aborts
#   - unsupported arguments and architectures fail cleanly
#   - uninstall removes the binary and unit but keeps data
#
# Usage: bash scripts/test/run.sh   (Linux only, no network needed)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALLER="${SCRIPT_DIR}/../install.sh"
TESTROOT="$(mktemp -d)"
trap 'rm -rf "$TESTROOT"' EXIT

mkdir -p "$TESTROOT/release" "$TESTROOT/fakebin" "$TESTROOT/home"

# Fake release assets (amd64 + arm64 so both arches resolve).
echo '#!/bin/sh' > "$TESTROOT/release/nusamation-linux-amd64"
echo '#!/bin/sh' > "$TESTROOT/release/nusamation-linux-arm64"
chmod +x "$TESTROOT/release/"*
cp "$SCRIPT_DIR/../../web/static/mark.png" "$TESTROOT/release/nusamation-icon.png"
(cd "$TESTROOT/release" && sha256sum nusamation-* > SHA256SUMS)

# Fake curl: maps a release download URL to a file in $FAKE_DIR.
cat > "$TESTROOT/fakebin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
out=""
while [ $# -gt 0 ]; do
	case "$1" in
		-o) out="$2"; shift 2 ;;
		-fsSL) shift ;;
		*) url="$1"; shift ;;
	esac
done
asset="${url##*/}"
[ -f "${FAKE_DIR:?}/$asset" ] || { echo "curl: missing asset $asset" >&2; exit 22; }
cp "$FAKE_DIR/$asset" "$out"
EOF
chmod +x "$TESTROOT/fakebin/curl"

# Fake systemctl: forces the "no systemd user manager" path in install_service
# so the generated unit file can be inspected on any host.
cat > "$TESTROOT/fakebin/systemctl" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
chmod +x "$TESTROOT/fakebin/systemctl"

export PATH="$TESTROOT/fakebin:$PATH"
export FAKE_DIR="$TESTROOT/release"
export HOME="$TESTROOT/home"
export NUSAMATION_VERSION=0.1.0
export NUSAMATION_NO_SERVICE=1
export NUSAMATION_REPO="local/fake"
# GitHub Actions runners hardcode XDG_CONFIG_HOME=/home/runner/.config even
# when HOME is overridden; clear it so install paths stay under $HOME.
unset XDG_CONFIG_HOME

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

# 1. Pinned install.
"$INSTALLER" >/dev/null || fail "pinned install exited non-zero"
[ -x "$HOME/.local/bin/nusamation" ] || fail "binary not installed"
[ -f "$HOME/.local/share/applications/nusamation.desktop" ] || fail "launcher not installed"
[ -f "$HOME/.local/share/icons/hicolor/512x512/apps/nusamation.png" ] || fail "icon not installed"
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
	[ -f "$HOME/.local/share/icons/hicolor/icon-theme.cache" ] || fail "icon theme cache not refreshed"
fi
if command -v desktop-file-validate >/dev/null 2>&1; then
	desktop-file-validate "$HOME/.local/share/applications/nusamation.desktop" || fail "invalid desktop entry"
fi
grep -q "NUSAMATION_ADDR=127.0.0.1:8787" "$HOME/.config/nusamation/env" || fail "env file missing/wrong"
pass "pinned install"

# 2. Idempotent re-install keeps the env file.
env_before="$(sha256sum "$HOME/.config/nusamation/env")"
"$INSTALLER" >/dev/null || fail "re-install exited non-zero"
[ "$env_before" = "$(sha256sum "$HOME/.config/nusamation/env")" ] || fail "env file was overwritten"
pass "re-install is idempotent"

# 3. Tampered binary must abort before installing.
TAMPER="$TESTROOT/tampered"
mkdir -p "$TAMPER"
cp "$TESTROOT/release/nusamation-icon.png" "$TAMPER/nusamation-icon.png"
echo "bad" > "$TAMPER/nusamation-linux-amd64"
echo "bad" > "$TAMPER/nusamation-linux-arm64"
(cd "$TAMPER" && sha256sum nusamation-* > SHA256SUMS)
echo "bad" >> "$TAMPER/nusamation-linux-amd64" # corrupt after checksums
rm -rf "$TESTROOT/home2"
if HOME="$TESTROOT/home2" FAKE_DIR="$TAMPER" "$INSTALLER" >/dev/null 2>&1; then
	fail "tampered install did not fail"
fi
[ ! -e "$TESTROOT/home2/.local/bin/nusamation" ] || fail "tampered install left a binary"
pass "tampered checksum aborts"

# 4. "latest" (unpinned) URL path.
rm -rf "$TESTROOT/home3"
if HOME="$TESTROOT/home3" NUSAMATION_VERSION="" "$INSTALLER" >/dev/null 2>&1; then
	pass "latest install"
else
	fail "latest install failed"
fi

# 5. Unknown argument fails cleanly.
if "$INSTALLER" bogus >/dev/null 2>&1; then
	fail "unknown argument did not fail"
fi
pass "unknown argument fails"

# 6. Uninstall removes binary and unit but keeps data.
mkdir -p "$TESTROOT/home4/.config/nusamation"
echo "keep" > "$TESTROOT/home4/.config/nusamation/env"
mkdir -p "$TESTROOT/home4/.nusamation"
touch "$TESTROOT/home4/.nusamation/nusamation.db"
if HOME="$TESTROOT/home4" "$INSTALLER" uninstall >/dev/null 2>&1; then
	[ ! -e "$TESTROOT/home4/.local/bin/nusamation" ] || fail "uninstall left the binary"
	[ -f "$TESTROOT/home4/.config/nusamation/env" ] || fail "uninstall removed the env file"
	[ -f "$TESTROOT/home4/.nusamation/nusamation.db" ] || fail "uninstall removed data"
	pass "uninstall keeps data"
else
	fail "uninstall exited non-zero"
fi

# 7. The service unit merges the installer's PATH (systemd user services do
# not inherit the login shell PATH). systemctl is absent in the harness, so
# only the generated unit file is checked.
if HOME="$TESTROOT/home5" NUSAMATION_NO_SERVICE= "$INSTALLER" >/dev/null 2>&1; then
	unit="$TESTROOT/home5/.config/systemd/user/nusamation.service"
	[ -f "$unit" ] || fail "unit file not written"
	expected="$(printf '%s\n' "$TESTROOT/home5/.local/bin" "$PATH" | tr ':' '\n' | awk '!seen[$0]++' | paste -sd: -)"
	got="$(sed -n 's/^Environment=PATH=//p' "$unit")"
	[ "$got" = "$expected" ] || fail "unit PATH mismatch: got=$got want=$expected"
	pass "service unit merges PATH"
else
	fail "install with service exited non-zero"
fi

echo "all installer tests passed"

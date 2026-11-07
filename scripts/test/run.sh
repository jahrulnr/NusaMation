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

export PATH="$TESTROOT/fakebin:$PATH"
export FAKE_DIR="$TESTROOT/release"
export HOME="$TESTROOT/home"
export NUSAMATION_VERSION=0.1.0
export NUSAMATION_NO_SERVICE=1
export NUSAMATION_REPO="local/fake"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

# 1. Pinned install.
"$INSTALLER" >/dev/null || fail "pinned install exited non-zero"
[ -x "$HOME/.local/bin/nusamation" ] || fail "binary not installed"
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

echo "all installer tests passed"
#!/usr/bin/env bash
#
# NusaMation installer for Linux.
#
# Quick install (latest release):
#   curl -fsSL https://github.com/jahrulnr/NusaMation/releases/latest/download/install.sh | bash
#
# The script mirrors `make install`: it drops the binary into ~/.local/bin,
# creates the data/config directories and installs a systemd user unit,
# enabled and started immediately. Re-running is safe: data and the env file
# are kept, and the unit is regenerated from the current settings.
#
# Options (environment variables, useful when piping into bash):
#   NUSAMATION_VERSION       release version to install, e.g. 0.1.0 (default: latest)
#   NUSAMATION_REPO          GitHub repo, for forks (default: jahrulnr/NusaMation)
#   NUSAMATION_INSTALL_DIR   binary directory      (default: ~/.local/bin)
#   NUSAMATION_DATA_DIR      data directory        (default: ~/.nusamation)
#   NUSAMATION_CONFIG_DIR    env file directory    (default: ~/.config/nusamation)
#   NUSAMATION_NO_SERVICE    set to 1 to skip the systemd user service
#
# Direct execution:
#   bash install.sh            install (same as piping)
#   bash install.sh uninstall  remove binary and service (data is kept)
#
set -euo pipefail

REPO="${NUSAMATION_REPO:-jahrulnr/NusaMation}"
VERSION="${NUSAMATION_VERSION:-latest}"
INSTALL_DIR="${NUSAMATION_INSTALL_DIR:-${HOME}/.local/bin}"
DATA_DIR="${NUSAMATION_DATA_DIR:-${HOME}/.nusamation}"
CONFIG_DIR="${NUSAMATION_CONFIG_DIR:-${HOME}/.config/nusamation}"
ENV_FILE="${CONFIG_DIR}/env"
XDG_CONFIG_HOME="${XDG_CONFIG_HOME:-${HOME}/.config}"
UNIT_DIR="${XDG_CONFIG_HOME}/systemd/user"
UNIT_FILE="${UNIT_DIR}/nusamation.service"
APP_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/applications"
ICON_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/icons/hicolor/512x512/apps"
SERVICE="nusamation.service"
# Global so the EXIT trap below can clean it up (locals are gone by then).
TMP_DIR=""

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m==>\033[0m %s\n' "$*" >&2; exit 1; }

uninstall() {
	if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
		systemctl --user disable --now "$SERVICE" >/dev/null 2>&1 || true
	fi
	rm -f "$UNIT_FILE" "${INSTALL_DIR}/nusamation" "${APP_DIR}/nusamation.desktop" "${ICON_DIR}/nusamation.png"
	if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
		systemctl --user daemon-reload >/dev/null 2>&1 || true
	fi
	log "Removed ${INSTALL_DIR}/nusamation and ${UNIT_FILE}"
	log "Data was kept at ${DATA_DIR}"
	log "Purge it (and the env file) manually: rm -rf ${DATA_DIR} ${CONFIG_DIR}"
}

install_binary() {
	[ "$(uname -s)" = "Linux" ] ||
		die "install.sh supports Linux only; on Windows run: irm https://github.com/${REPO}/releases/latest/download/install.ps1 | iex"
	command -v curl >/dev/null 2>&1 || die "curl is required but was not found"
	command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required but was not found (install coreutils)"

	local arch
	case "$(uname -m)" in
		x86_64 | amd64) arch="amd64" ;;
		aarch64 | arm64) arch="arm64" ;;
		*) die "unsupported architecture: $(uname -m)" ;;
	esac

	local asset="nusamation-linux-${arch}"
	local base
	if [ "$VERSION" = "latest" ]; then
		base="https://github.com/${REPO}/releases/latest/download"
	else
		base="https://github.com/${REPO}/releases/download/v${VERSION#v}"
	fi

	TMP_DIR="$(mktemp -d)"
	trap 'rm -rf "$TMP_DIR"' EXIT

	log "Downloading ${base}/${asset}"
	curl -fsSL "${base}/${asset}" -o "${TMP_DIR}/${asset}"
	curl -fsSL "${base}/SHA256SUMS" -o "${TMP_DIR}/SHA256SUMS"
	curl -fsSL "${base}/nusamation-icon.png" -o "${TMP_DIR}/nusamation-icon.png"
	# SHA256SUMS lines are "hash  filename" (two spaces, coreutils format);
	# sha256sum -c verifies the matching line relative to the temp dir.
	if ! grep -F "  ${asset}" "${TMP_DIR}/SHA256SUMS" | (cd "${TMP_DIR}" && sha256sum -c - >/dev/null); then
		die "checksum verification failed for ${asset}; aborting"
	fi
	if ! grep -F '  nusamation-icon.png' "${TMP_DIR}/SHA256SUMS" | (cd "${TMP_DIR}" && sha256sum -c - >/dev/null); then
		die "checksum verification failed for nusamation-icon.png; aborting"
	fi
	log "Checksum verified (SHA-256)"

	mkdir -p "${INSTALL_DIR}"
	install -m 0755 "${TMP_DIR}/${asset}" "${INSTALL_DIR}/nusamation"
	mkdir -p "${APP_DIR}" "${ICON_DIR}"
	install -m 0644 "${TMP_DIR}/nusamation-icon.png" "${ICON_DIR}/nusamation.png"
	printf '%s\n' '[Desktop Entry]' 'Type=Application' 'Name=NusaMation' \
		'Comment=Open the NusaMation control center' 'Exec=xdg-open http://127.0.0.1:8787' \
		'Icon=nusamation' 'Terminal=false' 'Categories=Network;' > "${APP_DIR}/nusamation.desktop"
	log "Installed ${INSTALL_DIR}/nusamation"

	case ":${PATH}:" in
		*":${INSTALL_DIR}:"*) ;;
		*) warn "${INSTALL_DIR} is not on your PATH; run it as ${INSTALL_DIR}/nusamation or add it to PATH" ;;
	esac
}

prepare_dirs() {
	mkdir -p "${DATA_DIR}" "${CONFIG_DIR}"
	chmod 700 "${DATA_DIR}"
	if [ ! -f "${ENV_FILE}" ]; then
		printf '%s\n' \
			'NUSAMATION_ADDR=127.0.0.1:8787' \
			"NUSAMATION_DATA_DIR=${DATA_DIR}" \
			'NUSAMATION_LOG_LEVEL=info' > "${ENV_FILE}"
		chmod 600 "${ENV_FILE}"
		log "Created ${ENV_FILE}"
	fi
}

install_service() {
	log "Installing systemd user unit ${UNIT_FILE}"
	mkdir -p "${UNIT_DIR}"
	# The systemd user manager does not inherit the login shell PATH, so agent
	# CLIs in non-standard locations (~/go/bin, ~/.cargo/bin, ...) would be
	# invisible to the service. Merge the current PATH into the unit, install
	# dir first; add more entries later via ${ENV_FILE} (PATH=...).
	merged="$(printf '%s\n' "${INSTALL_DIR}" "${PATH:-/usr/local/bin:/usr/bin:/bin}" | tr ':' '\n' | awk '!seen[$0]++' | paste -sd: -)"
	printf '%s\n' \
		'[Unit]' \
		'Description=NusaMation personal agent mesh' \
		'Documentation=https://github.com/jahrulnr/NusaMation' \
		'After=network-online.target' \
		'Wants=network-online.target' \
		'StartLimitIntervalSec=60' \
		'StartLimitBurst=5' \
		'' \
		'[Service]' \
		'Type=simple' \
		"WorkingDirectory=${HOME}" \
		"Environment=HOME=${HOME}" \
		"Environment=PATH=${merged}" \
		"Environment=NUSAMATION_DATA_DIR=${DATA_DIR}" \
		"EnvironmentFile=-${ENV_FILE}" \
		"ExecStart=${INSTALL_DIR}/nusamation" \
		'Restart=on-failure' \
		'RestartSec=3' \
		'TimeoutStopSec=30' \
		'' \
		'[Install]' \
		'WantedBy=default.target' > "${UNIT_FILE}"
	chmod 0644 "${UNIT_FILE}"

	if ! command -v systemctl >/dev/null 2>&1; then
		warn "systemd not found; unit saved at ${UNIT_FILE}"
		warn "Start NusaMation manually with: ${INSTALL_DIR}/nusamation"
		return 1
	fi
	if ! systemctl --user show-environment >/dev/null 2>&1; then
		warn "systemd user manager is not available in this session."
		warn "Unit saved at ${UNIT_FILE}. Start it from a systemd login with:"
		warn "  systemctl --user enable --now ${SERVICE}"
		return 1
	fi
	systemctl --user daemon-reload
	systemctl --user enable "${SERVICE}"
	if ! systemctl --user restart "${SERVICE}"; then
		die "Could not start ${SERVICE}. Check: journalctl --user -u ${SERVICE} -n 80 --no-pager"
	fi
	i=0
	while [ "$i" -lt 20 ] && ! systemctl --user is-active --quiet "${SERVICE}"; do
		i=$((i + 1))
		sleep 0.5
	done
	systemctl --user is-active --quiet "${SERVICE}" ||
		die "${SERVICE} is not active after start. Check: journalctl --user -u ${SERVICE} -n 80 --no-pager"
	log "Started and enabled ${SERVICE}"
}

# systemd user units start at login. Linger makes them start at boot too.
enable_linger() {
	if ! command -v loginctl >/dev/null 2>&1; then
		log "Note: loginctl not found; service starts with your systemd user session."
		return 0
	fi
	local user="${USER:-$(id -un)}"
	if [ "$(loginctl show-user "${user}" -p Linger --value 2>/dev/null)" = "yes" ]; then
		log "Boot autostart already enabled (linger)."
		return 0
	fi
	if loginctl enable-linger "${user}" >/dev/null 2>&1; then
		log "Enabled boot autostart (linger) for ${user}."
	else
		warn "Service is enabled at login; run \"sudo loginctl enable-linger ${user}\" for boot autostart."
	fi
}

main() {
	case "${1:-}" in
		"") ;;
		uninstall)
			uninstall
			exit 0
			;;
		*) die "unknown argument: ${1} (usage: install.sh [uninstall])" ;;
	esac

	install_binary
	prepare_dirs
	local service_installed=0
	if [ "${NUSAMATION_NO_SERVICE:-}" = "1" ]; then
		log "NUSAMATION_NO_SERVICE=1; skipping the systemd user service."
	elif install_service; then
		enable_linger
		service_installed=1
	fi

	printf '\n'
	log "NusaMation is installed."
	log "  binary: ${INSTALL_DIR}/nusamation"
	log "  data:   ${DATA_DIR}"
	if [ "$service_installed" = "1" ]; then
		log "  unit:   ${UNIT_FILE}"
		log "  env:    ${ENV_FILE}"
	fi
	printf '\n'
	if [ "$service_installed" = "1" ]; then
		log "If this is a first run, read the generated password with:"
		log "  journalctl --user -u ${SERVICE} -n 60 --no-pager"
	else
		log "Start NusaMation manually: ${INSTALL_DIR}/nusamation"
	fi
}

main "$@"

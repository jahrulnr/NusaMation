BINARY := bin/nusamation
FAKE := bin/fake-acp-agent

# User-level install (no root required).
BIN_DIR ?= $(HOME)/.local/bin
DATA_DIR ?= $(HOME)/.nusamation
CONFIG_DIR ?= $(HOME)/.config/nusamation
ENV_FILE := $(CONFIG_DIR)/env
UNIT_DIR ?= $(HOME)/.config/systemd/user
UNIT_FILE := $(UNIT_DIR)/nusamation.service
SERVICE := nusamation.service

.PHONY: all build fake run test vet fmt check clean \
	check-home install install-bin install-service prepare enable-linger \
	service-start service-stop service-restart service-status service-logs \
	uninstall purge

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/nusamation

fake:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(FAKE) ./cmd/fake-acp-agent

run:
	go run ./cmd/nusamation

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

check: fmt vet test

clean:
	rm -rf bin

# Install the binary under ~/.local/bin, then install and start a systemd user
# service. Re-running is safe: data and env config are kept and the unit is
# regenerated from the current settings.
install: install-bin install-service enable-linger
	@printf '\n'
	@printf 'NusaMation is installed.\n'
	@printf '  binary: %s\n' "$(BIN_DIR)/nusamation"
	@printf '  data:   %s\n' "$(DATA_DIR)"
	@printf '  unit:   %s\n' "$(UNIT_FILE)"
	@printf '  env:    %s\n' "$(ENV_FILE)"
	@printf '\n'
	@printf 'If this is a first run, read the generated password with:\n'
	@printf '  journalctl --user -u %s -n 60 --no-pager\n' "$(SERVICE)"

check-home:
	@if [ -z "$(HOME)" ]; then \
		printf 'HOME is not set; cannot choose the user install paths.\n'; \
		exit 1; \
	fi

install-bin: check-home build
	@install -d "$(BIN_DIR)"
	@install -m 0755 "$(BINARY)" "$(BIN_DIR)/nusamation"
	@printf 'Installed binary: %s\n' "$(BIN_DIR)/nusamation"

install-service: install-bin prepare
	@printf '%s\n' \
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
		'WorkingDirectory=$(HOME)' \
		'Environment=HOME=$(HOME)' \
		'Environment=PATH=$(BIN_DIR):/usr/local/bin:/usr/bin:/bin' \
		'Environment=NUSAMATION_DATA_DIR=$(DATA_DIR)' \
		'EnvironmentFile=-$(ENV_FILE)' \
		'ExecStart=$(BIN_DIR)/nusamation' \
		'Restart=on-failure' \
		'RestartSec=3' \
		'TimeoutStopSec=30' \
		'' \
		'[Install]' \
		'WantedBy=default.target' > "$(UNIT_FILE)"
	@chmod 0644 "$(UNIT_FILE)"
	@if ! command -v systemctl >/dev/null 2>&1; then \
		printf 'systemd not found; unit saved at %s\n' "$(UNIT_FILE)"; \
		printf 'Start NusaMation manually with: %s/nusamation\n' "$(BIN_DIR)"; \
		exit 0; \
	fi; \
	if ! systemctl --user show-environment >/dev/null 2>&1; then \
		printf 'systemd user manager is not available in this session.\n'; \
		printf 'Unit saved at %s. Start it from a systemd login with:\n' "$(UNIT_FILE)"; \
		printf '  systemctl --user enable --now %s\n' "$(SERVICE)"; \
		exit 0; \
	fi; \
	if ! systemctl --user daemon-reload; then \
		printf 'Could not reload the systemd user manager.\n'; \
		exit 1; \
	fi; \
	if ! systemctl --user enable "$(SERVICE)"; then \
		printf 'Could not enable %s for autostart.\n' "$(SERVICE)"; \
		exit 1; \
	fi; \
	if ! systemctl --user restart "$(SERVICE)"; then \
		printf '\nCould not start %s.\n' "$(SERVICE)"; \
		printf 'Check: journalctl --user -u %s -n 80 --no-pager\n' "$(SERVICE)"; \
		printf 'If port 8787 is already in use, stop the old instance first.\n'; \
		exit 1; \
	fi; \
	i=0; \
	while [ "$$i" -lt 20 ] && ! systemctl --user is-active --quiet "$(SERVICE)"; do \
		i=$$((i + 1)); sleep 0.5; \
	done; \
	if ! systemctl --user is-active --quiet "$(SERVICE)"; then \
		printf '\n%s is not active after start.\n' "$(SERVICE)"; \
		printf 'Check: journalctl --user -u %s -n 80 --no-pager\n' "$(SERVICE)"; \
		exit 1; \
	fi; \
	printf 'Started and enabled %s\n' "$(SERVICE)"

# systemd user units start at login. Linger makes them start at boot too.
enable-linger: install-service
	@if command -v loginctl >/dev/null 2>&1; then \
		user="$${USER:-$$(id -un)}"; \
		if [ "$$(loginctl show-user "$$user" -p Linger --value 2>/dev/null)" != "yes" ]; then \
			if loginctl enable-linger "$$user" >/dev/null 2>&1; then \
				printf 'Enabled boot autostart (linger) for %s.\n' "$$user"; \
			else \
				printf 'Note: service is enabled at login; run "sudo loginctl enable-linger %s" for boot autostart.\n' "$$user"; \
			fi; \
		else \
			printf 'Boot autostart already enabled (linger).\n'; \
		fi; \
	else \
		printf 'Note: loginctl not found; service starts with your systemd user session.\n'; \
	fi

prepare: check-home
	@mkdir -p "$(DATA_DIR)" "$(CONFIG_DIR)" "$(UNIT_DIR)"
	@chmod 700 "$(DATA_DIR)"
	@if [ ! -f "$(ENV_FILE)" ]; then \
		printf '%s\n' \
			'NUSAMATION_ADDR=127.0.0.1:8787' \
			"NUSAMATION_DATA_DIR=$(DATA_DIR)" \
			'NUSAMATION_LOG_LEVEL=info' > "$(ENV_FILE)"; \
		chmod 600 "$(ENV_FILE)"; \
		printf 'Created %s\n' "$(ENV_FILE)"; \
	fi

service-start:
	@systemctl --user start "$(SERVICE)"
	@systemctl --user --no-pager status "$(SERVICE)" | sed -n '1,12p'

service-stop:
	@systemctl --user stop "$(SERVICE)"

service-restart:
	@systemctl --user restart "$(SERVICE)"
	@systemctl --user --no-pager status "$(SERVICE)" | sed -n '1,12p'

service-status:
	@systemctl --user --no-pager status "$(SERVICE)"

service-logs:
	@if command -v journalctl >/dev/null 2>&1; then \
		journalctl --user -u "$(SERVICE)" -f; \
	elif [ -f "$(DATA_DIR)/logs/nusamation.log" ]; then \
		printf 'journalctl not found; following %s/logs/nusamation.log\n' "$(DATA_DIR)"; \
		tail -n 80 -f "$(DATA_DIR)/logs/nusamation.log"; \
	else \
		printf 'Neither journalctl nor %s/logs/nusamation.log is available.\n' "$(DATA_DIR)"; \
		exit 1; \
	fi

uninstall:
	@if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then \
		systemctl --user disable --now "$(SERVICE)" >/dev/null 2>&1 || true; \
	fi
	@rm -f "$(UNIT_FILE)" "$(BIN_DIR)/nusamation"
	@if command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then \
		systemctl --user daemon-reload >/dev/null 2>&1 || true; \
	fi
	@printf 'Removed %s and %s\n' "$(BIN_DIR)/nusamation" "$(UNIT_FILE)"
	@printf 'Data was kept at %s\n' "$(DATA_DIR)"
	@printf 'Run "make purge" to remove data and env config as well.\n'

purge: uninstall
	@if [ -z "$(DATA_DIR)" ] || [ "$(DATA_DIR)" = "/" ] || [ "$(DATA_DIR)" = "$(HOME)" ]; then \
		printf 'Refusing to purge unsafe DATA_DIR: %s\n' "$(DATA_DIR)"; \
		exit 1; \
	fi
	@rm -rf "$(DATA_DIR)" "$(CONFIG_DIR)"
	@printf 'Purged %s and %s\n' "$(DATA_DIR)" "$(CONFIG_DIR)"

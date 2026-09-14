PREK ?= prek
PIPX ?= pipx
PIPX_BIN_DIR ?= $(HOME)/.local/bin

.PHONY: all dep lint test build install check test-gpg

all: check

dep:
	@set -eu; \
	if command -v go >/dev/null 2>&1; then \
		echo "Go is already installed: $$(go version)"; \
	else \
		case "$$(uname -s)" in \
		Darwin) \
			command -v brew >/dev/null 2>&1 || { echo "Homebrew is required on macOS" >&2; exit 1; }; \
			brew install go; \
			;; \
		Linux) \
			command -v apt-get >/dev/null 2>&1 || { echo "apt-get is required on Ubuntu" >&2; exit 1; }; \
			sudo apt-get update; \
			sudo apt-get install -y golang-go; \
			;; \
		*) echo "unsupported operating system: $$(uname -s)" >&2; exit 1 ;; \
		esac; \
	fi; \
	if command -v "$(PIPX)" >/dev/null 2>&1; then \
		echo "pipx is already installed"; \
	else \
		case "$$(uname -s)" in \
		Darwin) \
			command -v brew >/dev/null 2>&1 || { echo "Homebrew is required on macOS" >&2; exit 1; }; \
			brew install pipx; \
			;; \
		Linux) \
			command -v apt-get >/dev/null 2>&1 || { echo "apt-get is required on Ubuntu" >&2; exit 1; }; \
			sudo apt-get update; \
			sudo apt-get install -y pipx; \
			;; \
		*) echo "unsupported operating system: $$(uname -s)" >&2; exit 1 ;; \
		esac; \
	fi; \
	$(PIPX) ensurepath; \
	export PATH="$(PIPX_BIN_DIR):$$PATH"; \
	if command -v "$(PREK)" >/dev/null 2>&1; then \
		echo "prek is already installed"; \
	else \
		$(PIPX) install prek; \
	fi; \
	$(PREK) install

lint:
	$(PREK) run --all-files

test:
	go test ./...

test-gpg:
	LJA_TEST_REAL_GPG=1 go test -run '^TestGPGRealAgent$$' ./...

build:
	go build ./...

install:
	go install ./cmd/lja

check: lint test build

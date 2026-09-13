PREK ?= prek

.PHONY: all bootstrap dep lint test build check

all: check

bootstrap:
	$(PREK) install

dep:
	@if command -v go >/dev/null 2>&1; then \
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
	fi

lint:
	$(PREK) run --all-files

test:
	go test ./...

build:
	go build ./...

check: lint test build

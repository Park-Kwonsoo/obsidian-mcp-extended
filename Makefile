BINARIES      := obsidian-mcp obsidian-indexerd
INSTALL_DIR   ?= $(HOME)/.local/bin

# Stamp version info from git so `--version` output (and any future crash
# reports) trace back to a concrete commit.
VERSION       := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS       := -s -w -X main.version=$(VERSION)

# buf-generated stubs; touched by `make gen`, consumed by every Go package
# that imports proto/indexer/v1. Wildcard expands lazily so a fresh clone
# (no *.pb.go yet) doesn't break the Makefile graph.
GEN_FILES     := $(wildcard proto/indexer/v1/*.pb.go)

DAEMON_SOCKET ?= /tmp/obsidian-indexerd.sock
DAEMON_LOG    ?= /tmp/obsidian-indexerd.log
DAEMON_PIDFILE := /tmp/obsidian-indexerd.pid

.PHONY: build install uninstall test test-all fmt vet clean tidy gen gen-clean help \
        build-mcp build-daemon install-mcp install-daemon \
        start-daemon stop-daemon restart-daemon daemon-status

gen: ## Regenerate proto stubs (model.pb.go, dto.pb.go, api.pb.go, api_grpc.pb.go)
	@command -v buf >/dev/null 2>&1 || { echo "buf not installed — see https://buf.build/docs/installation"; exit 1; }
	buf lint
	buf generate
	go mod tidy
	@echo "generated: $$(find proto -name '*.pb.go' | wc -l | tr -d ' ') files"

gen-clean: ## Remove generated proto stubs
	find proto -name '*.pb.go' -delete
	@echo "cleaned generated proto stubs"

build: gen build-mcp build-daemon ## Build both binaries into ./bin/

build-mcp: gen ## Build obsidian-mcp into ./bin/
	@mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o bin/obsidian-mcp ./cmd/obsidian-mcp
	@echo "built: bin/obsidian-mcp ($(VERSION))"

build-daemon: gen ## Build obsidian-indexerd into ./bin/
	@mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o bin/obsidian-indexerd ./cmd/obsidian-indexerd
	@echo "built: bin/obsidian-indexerd ($(VERSION))"

install: gen install-mcp install-daemon restart-daemon ## Install both binaries to $(INSTALL_DIR)/ and (re)start the daemon

install-mcp: gen ## Install obsidian-mcp to $(INSTALL_DIR)/
	@mkdir -p $(INSTALL_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(INSTALL_DIR)/obsidian-mcp ./cmd/obsidian-mcp
	@echo "installed: $(INSTALL_DIR)/obsidian-mcp ($(VERSION))"
	@echo "ensure $(INSTALL_DIR) is on your PATH"

install-daemon: gen ## Install obsidian-indexerd to $(INSTALL_DIR)/
	@mkdir -p $(INSTALL_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(INSTALL_DIR)/obsidian-indexerd ./cmd/obsidian-indexerd
	@echo "installed: $(INSTALL_DIR)/obsidian-indexerd ($(VERSION))"

uninstall: ## Remove both installed binaries
	@for b in $(BINARIES); do rm -f $(INSTALL_DIR)/$$b && echo "removed: $(INSTALL_DIR)/$$b"; done

test: gen ## Unit tests (skips golden — needs /tmp fixture vault)
	go test -race -count=1 ./internal/...

test-all: gen ## Unit + golden (requires /tmp/obsidian-mcp-bench/vaults/synth-100)
	go test -race -count=1 ./...

fmt: ## gofmt in place
	gofmt -w .

vet: gen ## go vet
	go vet ./...

tidy: ## go mod tidy
	go mod tidy

clean: gen-clean ## Remove build artifacts and generated proto stubs
	rm -rf bin/

start-daemon: install-daemon ## Launch obsidian-indexerd in the background (logs to $(DAEMON_LOG))
	@if [ -f $(DAEMON_PIDFILE) ] && kill -0 $$(cat $(DAEMON_PIDFILE)) 2>/dev/null; then \
	  echo "daemon already running (pid $$(cat $(DAEMON_PIDFILE))) at $(DAEMON_SOCKET)"; \
	  exit 0; \
	fi
	@nohup $(INSTALL_DIR)/obsidian-indexerd --socket $(DAEMON_SOCKET) >$(DAEMON_LOG) 2>&1 & echo $$! >$(DAEMON_PIDFILE)
	@sleep 0.3
	@if kill -0 $$(cat $(DAEMON_PIDFILE)) 2>/dev/null; then \
	  echo "daemon started (pid $$(cat $(DAEMON_PIDFILE))) at $(DAEMON_SOCKET) — logs: $(DAEMON_LOG)"; \
	  echo "export OBSIDIAN_MCP_DAEMON=$(DAEMON_SOCKET)  # or 'auto' to use the default socket"; \
	else \
	  echo "daemon failed to start — see $(DAEMON_LOG)"; \
	  rm -f $(DAEMON_PIDFILE); exit 1; \
	fi

stop-daemon: ## Stop the background obsidian-indexerd (SIGTERM then SIGKILL on timeout)
	@if [ ! -f $(DAEMON_PIDFILE) ]; then \
	  echo "daemon not running (no pidfile)"; \
	  rm -f $(DAEMON_SOCKET); \
	  exit 0; \
	fi; \
	PID=$$(cat $(DAEMON_PIDFILE)); \
	if [ -z "$$PID" ] || ! kill -0 $$PID 2>/dev/null; then \
	  echo "daemon not running (stale pidfile)"; \
	else \
	  kill $$PID; \
	  for i in 1 2 3 4 5; do kill -0 $$PID 2>/dev/null || break; sleep 0.2; done; \
	  kill -0 $$PID 2>/dev/null && kill -9 $$PID; \
	  echo "daemon stopped (pid $$PID)"; \
	fi; \
	rm -f $(DAEMON_PIDFILE) $(DAEMON_SOCKET)

restart-daemon: stop-daemon start-daemon ## Stop + start

daemon-status: ## Show daemon pid / socket status
	@if [ -f $(DAEMON_PIDFILE) ] && kill -0 $$(cat $(DAEMON_PIDFILE)) 2>/dev/null; then \
	  echo "running  pid=$$(cat $(DAEMON_PIDFILE))  socket=$(DAEMON_SOCKET)"; \
	else \
	  echo "not running"; \
	fi

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

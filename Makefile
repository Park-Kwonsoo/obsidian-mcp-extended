BINARY    := obsidian-mcp
PKG       := ./cmd/obsidian-mcp
INSTALL_DIR ?= $(HOME)/.local/bin

# Stamp version info from git so `--version` output (and any future crash
# reports) trace back to a concrete commit.
VERSION   := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X main.version=$(VERSION)

# buf-generated stubs; touched by `make gen`, consumed by every Go package
# that imports proto/indexer/v1. The wildcard is expanded lazily so a fresh
# clone (no *.pb.go yet) still produces something the Makefile can depend on.
GEN_FILES := $(wildcard proto/indexer/v1/*.pb.go)

.PHONY: build install uninstall test test-all fmt vet clean tidy gen gen-clean help

gen: ## Regenerate proto stubs (model.pb.go, dto.pb.go, api.pb.go, api_grpc.pb.go)
	@command -v buf >/dev/null 2>&1 || { echo "buf not installed — see https://buf.build/docs/installation"; exit 1; }
	buf lint
	buf generate
	go mod tidy
	@echo "generated: $$(find proto -name '*.pb.go' | wc -l | tr -d ' ') files"

gen-clean: ## Remove generated proto stubs
	find proto -name '*.pb.go' -delete
	@echo "cleaned generated proto stubs"

build: gen ## Build binary into ./bin/ (generates proto first if stale)
	@mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)
	@echo "built: bin/$(BINARY) ($(VERSION))"

install: gen ## Build and install to $(INSTALL_DIR)/$(BINARY)
	@mkdir -p $(INSTALL_DIR)
	go build -ldflags '$(LDFLAGS)' -o $(INSTALL_DIR)/$(BINARY) $(PKG)
	@echo "installed: $(INSTALL_DIR)/$(BINARY) ($(VERSION))"
	@echo "ensure $(INSTALL_DIR) is on your PATH"

uninstall: ## Remove the installed binary
	rm -f $(INSTALL_DIR)/$(BINARY)
	@echo "removed: $(INSTALL_DIR)/$(BINARY)"

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

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

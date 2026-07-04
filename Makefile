.PHONY: build test clean install dist

BIN_DIR := bin
DIST_DIR := dist
# Release version: pass VERSION=v1.0.0 explicitly, or fall back to git describe.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64
# Inject the build version so a binary knows its own release identity (the
# auto-updater compares it against the latest GitHub release). Kept in sync with
# scripts/package.sh's VERSIONPKG.
VERSION_LDFLAGS := -X github.com/karthikeyan5/c3/internal/version.Version=$(VERSION)

build:
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN_DIR)/c3-broker ./cmd/c3-broker
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN_DIR)/c3-claude-adapter ./cmd/c3-claude-adapter
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN_DIR)/c3-codex-adapter ./cmd/c3-codex-adapter
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN_DIR)/codex ./cmd/codex
	go build -ldflags "$(VERSION_LDFLAGS)" -o $(BIN_DIR)/migrate-legacy ./cmd/migrate-legacy

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)

install:
	go install -ldflags "$(VERSION_LDFLAGS)" ./cmd/...

# Cross-compile every platform into $(DIST_DIR)/ as release tarballs + SHA256SUMS.
# Mirrors what .github/workflows/release.yml runs on a v* tag, for local testing.
dist:
	@rm -rf $(DIST_DIR) && mkdir -p $(DIST_DIR)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		sh scripts/package.sh $$os $$arch $(VERSION) $(DIST_DIR); \
	done
	@cd $(DIST_DIR) && { sha256sum *.tar.gz > SHA256SUMS 2>/dev/null || shasum -a 256 *.tar.gz > SHA256SUMS; }
	@echo "built $(DIST_DIR)/:"; ls -1 $(DIST_DIR)

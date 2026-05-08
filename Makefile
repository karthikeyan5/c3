.PHONY: build test clean install

BIN_DIR := bin

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/c3-broker ./cmd/c3-broker
	go build -o $(BIN_DIR)/c3-claude-adapter ./cmd/c3-claude-adapter
	go build -o $(BIN_DIR)/c3-codex-adapter ./cmd/c3-codex-adapter
	go build -o $(BIN_DIR)/migrate-legacy ./cmd/migrate-legacy

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)

install:
	go install ./cmd/...

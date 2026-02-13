.PHONY: all build test clean dev fmt lint

# ──────────────────────────────────────────────
# 🏺 AgentOven — Bake production-ready AI agents
# ──────────────────────────────────────────────

all: build

# ── Build ─────────────────────────────────────

build: build-rust build-go build-python build-typescript

build-rust:
	cargo build --release

build-go:
	cd control-plane && go build -o bin/agentoven-server ./cmd/server

build-python:
	cd sdk/python && maturin build --release

build-typescript:
	cd sdk/typescript && npm run build

# ── Development ───────────────────────────────

dev-control-plane:
	cd control-plane && go run ./cmd/server

dev-ui:
	cd ui && npm run dev

# ── Test ──────────────────────────────────────

test: test-rust test-go test-python test-typescript

test-rust:
	cargo test --workspace

test-go:
	cd control-plane && go test ./...

test-python:
	cd sdk/python && pytest tests/

test-typescript:
	cd sdk/typescript && npm test

# ── Format & Lint ─────────────────────────────

fmt:
	cargo fmt --all
	cd control-plane && gofmt -w .
	cd sdk/python && ruff format .
	cd sdk/typescript && npx prettier --write src/

lint:
	cargo clippy --workspace -- -D warnings
	cd control-plane && golangci-lint run
	cd sdk/python && ruff check .
	cd sdk/typescript && npx eslint src/

# ── Docker ────────────────────────────────────

docker-build:
	docker compose -f infra/docker/docker-compose.yml build

docker-up:
	docker compose -f infra/docker/docker-compose.yml up -d

docker-down:
	docker compose -f infra/docker/docker-compose.yml down

# ── Clean ─────────────────────────────────────

clean:
	cargo clean
	cd control-plane && rm -rf bin/
	cd sdk/python && rm -rf dist/ *.so *.dylib
	cd sdk/typescript && rm -rf dist/ *.node
	cd ui && rm -rf dist/ .next/

# ── Install (development) ────────────────────

install-cli:
	cargo install --path crates/agentoven-cli

install-python:
	cd sdk/python && maturin develop

install-deps:
	cd ui && npm install
	cd sdk/typescript && npm install
	cd docs && npm install

.PHONY: all build test clean dev fmt lint dev-pro build-pro

# ──────────────────────────────────────────────
# 🏺 AgentOven — Bake production-ready AI agents
# ──────────────────────────────────────────────

all: build

# ── Build ─────────────────────────────────────

build: build-rust build-go build-dashboard build-python build-typescript

build-rust:
	cargo build --release

build-go:
	cd control-plane && go build -o bin/agentoven-server ./cmd/server

build-dashboard:
	cd control-plane/dashboard && npm install --ignore-scripts && npm run build

build-python:
	cd sdk/python && maturin build --release

build-typescript:
	cd sdk/typescript && npm run build

# ── Development ───────────────────────────────

# Run everything in dev mode (Go server + Vite HMR dashboard)
dev:
	@echo "🏺 Starting AgentOven dev mode..."
	@echo ""
	@echo "  API server  → http://localhost:8080"
	@echo "  Dashboard   → http://localhost:5173"
	@echo ""
	@$(MAKE) -j2 dev-server dev-dashboard

dev-server:
	cd control-plane && go run ./cmd/server

dev-dashboard:
	cd control-plane/dashboard && npm run dev

# ── Enterprise (Pro) ─────────────────────────

# Run the enterprise server (requires ../agentoven-pro)
dev-pro:
	@echo "🏺 Starting AgentOven Pro dev mode..."
	@echo ""
	@echo "  Pro API server → http://localhost:8080"
	@echo "  Dashboard      → http://localhost:5173"
	@echo ""
	@$(MAKE) -j2 dev-pro-server dev-dashboard

dev-pro-server:
	cd ../agentoven-pro && go run ./cmd/server

# Build the enterprise server binary
build-pro:
	cd ../agentoven-pro && go build -o bin/agentoven-pro ./cmd/server/

# Test enterprise (Pro + OSS)
test-pro:
	cd ../agentoven-pro && go test ./... -v

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

install-server:
	cd control-plane && go build -trimpath -ldflags="-s -w" -o $(GOPATH)/bin/agentoven-server ./cmd/server

install-all: install-cli install-server build-dashboard
	@echo "✅ agentoven + agentoven-server installed, dashboard built"

install-python:
	cd sdk/python && maturin develop

install-deps:
	cd control-plane/dashboard && npm install
	cd sdk/typescript && npm install

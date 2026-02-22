# ── Dashboard build stage ──────────────────────────────────────
FROM node:22-alpine AS dashboard-builder

WORKDIR /build/dashboard
COPY dashboard/package.json dashboard/package-lock.json ./
RUN npm ci --production=false
COPY dashboard/ ./
RUN npm run build

# ── Go build stage ────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Copy dashboard build output into the Go build context
COPY --from=dashboard-builder /build/dashboard/dist ./dashboard/dist

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=$(cat VERSION 2>/dev/null || echo 0.3.0)" \
    -o /agentoven-server \
    ./cmd/server

# ── Runtime stage ─────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -g '' agentoven

COPY --from=builder /agentoven-server /usr/local/bin/agentoven-server
COPY --from=builder /build/dashboard/dist /usr/share/agentoven/dashboard

USER agentoven

ENV AGENTOVEN_DASHBOARD_DIR=/usr/share/agentoven/dashboard

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["agentoven-server"]

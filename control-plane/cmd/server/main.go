// AgentOven Control Plane — the brain of the enterprise agent oven.
//
// This is the main entry point for the AgentOven control plane server.
// It provides:
//   - Agent Registry (the Menu)
//   - Model Router (ingredient routing)
//   - A2A Gateway (agent-to-agent collaboration)
//   - MCP Gateway (agent-to-tool integration)
//   - Workflow Engine (recipe execution)
//   - Multi-tenant API with cost tracking and observability

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentoven/agentoven/control-plane/internal/api"
	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/agentoven/agentoven/control-plane/internal/telemetry"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Setup structured logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	log.Info().Msg("🏺 AgentOven Control Plane starting...")

	// Load configuration
	cfg := config.Load()

	// Initialize telemetry (OpenTelemetry)
	shutdown, err := telemetry.Init(cfg.Telemetry)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize telemetry")
	}
	defer shutdown(context.Background())

	// Build the API router
	router := api.NewRouter(cfg)

	// Start HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Info().Msg("🛑 Shutting down gracefully...")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	log.Info().
		Int("port", cfg.Port).
		Str("version", cfg.Version).
		Msg("🔥 AgentOven is hot and ready!")

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("Server failed")
	}
}

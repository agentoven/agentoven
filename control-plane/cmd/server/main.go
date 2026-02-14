// AgentOven Control Plane — the brain of the enterprise agent oven.
//
// This is the main entry point for the AgentOven OSS control plane server.
// It provides:
//   - Agent Registry (the Menu)
//   - Model Router (ingredient routing)
//   - A2A Gateway (agent-to-agent collaboration)
//   - MCP Gateway (agent-to-tool integration)
//   - Workflow Engine (recipe execution)
//   - Single-kitchen, in-memory store (zero config)
//   - Dashboard UI

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/server"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Setup structured logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	log.Info().Msg("🏺 AgentOven Control Plane starting...")

	// Build the OSS server (in-memory store, single kitchen)
	ctx := context.Background()
	srv, err := server.New(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize server")
	}
	defer srv.Store.Close()
	defer srv.ShutdownFunc(ctx)

	// Start HTTP server
	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", srv.Port),
		Handler:      srv.Handler,
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	log.Info().
		Int("port", srv.Port).
		Msg("🔥 AgentOven is hot and ready!")

	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("Server failed")
	}
}

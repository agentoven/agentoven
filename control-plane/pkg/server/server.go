// Package server provides the public entry point for initializing the
// AgentOven control plane server.
//
// This package exists in pkg/ (not internal/) so that the enterprise repo
// (agentoven-pro) can import it and compose the full server with Pro overrides.
//
// Usage (OSS):
//
//	srv, err := server.New(ctx)
//	http.ListenAndServe(":8080", srv.Handler)
//
// Usage (Pro):
//
//	srv, err := server.New(ctx)
//	proHandler := tierEnforcer.Middleware(srv.Handler)
//	http.ListenAndServe(":8080", proHandler)
package server

import (
	"context"
	"fmt"
	"time"

	"net/http"

	"github.com/agentoven/agentoven/control-plane/internal/api"
	"github.com/agentoven/agentoven/control-plane/internal/api/handlers"
	"github.com/agentoven/agentoven/control-plane/internal/config"
	"github.com/agentoven/agentoven/control-plane/internal/mcpgw"
	modelrouter "github.com/agentoven/agentoven/control-plane/internal/router"
	"github.com/agentoven/agentoven/control-plane/internal/store"
	"github.com/agentoven/agentoven/control-plane/internal/telemetry"
	"github.com/agentoven/agentoven/control-plane/internal/workflow"
	"github.com/agentoven/agentoven/control-plane/pkg/models"

	"github.com/rs/zerolog/log"
)

// Config is the public configuration for the control plane server.
type Config struct {
	Port            int
	Version         string
	OTELEnabled     bool
	OTELEndpoint    string
	ServiceName     string
}

// Server holds the initialized AgentOven control plane.
type Server struct {
	// Handler is the HTTP handler with all routes and middleware.
	Handler http.Handler

	// Store is the data store (in-memory for OSS).
	// Exposed so Pro can use it in TierEnforcer and other middleware.
	Store store.Store

	// Config is the server configuration.
	Config *Config

	// Port is the port the server should listen on.
	Port int

	// ShutdownFunc should be called on graceful shutdown to flush telemetry.
	ShutdownFunc func(context.Context) error
}

// LoadConfig loads configuration from environment variables.
func LoadConfig() *Config {
	cfg := config.Load()
	return &Config{
		Port:         cfg.Port,
		Version:      cfg.Version,
		OTELEnabled:  cfg.Telemetry.Enabled,
		OTELEndpoint: cfg.Telemetry.OTLPEndpoint,
		ServiceName:  cfg.Telemetry.ServiceName,
	}
}

// New initializes all OSS control plane components and returns a ready Server.
// This is the primary entry point for both OSS and Pro main.go.
func New(ctx context.Context) (*Server, error) {
	return NewWithConfig(ctx, LoadConfig())
}

// NewWithConfig initializes the control plane with an explicit configuration.
func NewWithConfig(ctx context.Context, pubCfg *Config) (*Server, error) {
	// Build internal config from public config
	cfg := config.Load()
	if pubCfg.Port > 0 {
		cfg.Port = pubCfg.Port
	}

	// Initialize telemetry
	shutdown, err := telemetry.Init(cfg.Telemetry)
	if err != nil {
		return nil, fmt.Errorf("init telemetry: %w", err)
	}

	// OSS uses in-memory store (single kitchen, zero configuration)
	dataStore := store.NewMemoryStore()
	log.Info().Msg("✅ In-memory store initialized")

	// Seed default kitchen
	seedDefaultKitchen(ctx, dataStore)

	// Initialize services
	mr := modelrouter.NewModelRouter(dataStore)
	gw := mcpgw.NewGateway(dataStore)
	wf := workflow.NewEngine(dataStore)

	log.Info().Msg("✅ Model Router initialized")
	log.Info().Msg("✅ MCP Gateway initialized")
	log.Info().Msg("✅ Workflow Engine initialized")

	// Build handlers + API router
	h := handlers.New(dataStore, mr, gw, wf)
	router := api.NewRouter(cfg, h)

	return &Server{
		Handler:      router,
		Store:        dataStore,
		Config:       pubCfg,
		Port:         cfg.Port,
		ShutdownFunc: shutdown,
	}, nil
}

func seedDefaultKitchen(ctx context.Context, s store.Store) {
	_, err := s.GetKitchen(ctx, "default")
	if err != nil {
		k := &models.Kitchen{
			ID:          "default",
			Name:        "Default Kitchen",
			Description: "The default workspace",
			Owner:       "system",
			Plan:        models.PlanCommunity,
			CreatedAt:   time.Now().UTC(),
		}
		if err := s.CreateKitchen(ctx, k); err != nil {
			log.Warn().Err(err).Msg("Failed to seed default kitchen")
		} else {
			log.Info().Msg("✅ Default kitchen seeded")
		}
	}
}

package handlers

import "github.com/agentoven/agentoven/control-plane/pkg/contracts"

// DecisionRoute and DecisionRoutes are defined in pkg/contracts, where an edition can read them; see there.
type DecisionRoute = contracts.DecisionRoute

// DecisionRoutes is contracts.DecisionRoutes.
var DecisionRoutes = contracts.DecisionRoutes

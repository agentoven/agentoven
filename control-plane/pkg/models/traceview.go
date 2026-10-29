package models

import "time"

// TraceView is a saved way of looking at the trace list: a name and a filter, stored on the server so
// the dashboard, the CLI and the SDKs see the same views (ADR-0039). It is a bookmark, not data: it
// holds no trace and grants no access to one.
type TraceView struct {
	ID      string `json:"id"`
	Kitchen string `json:"kitchen"`
	// Owner is the subject of the person who saved it; OwnerName is what to show for them.
	Owner     string          `json:"owner"`
	OwnerName string          `json:"owner_name,omitempty"`
	Name      string          `json:"name"`
	Filter    TraceViewFilter `json:"filter"`
	// Shared makes the view visible to everyone in the kitchen. A private view is its owner's alone.
	Shared    bool      `json:"shared"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TraceViewFilter is the typed filter a view holds: the trace list's own filters and sort, and nothing
// else. The same names the list endpoint takes as query parameters.
type TraceViewFilter struct {
	Agent           string  `json:"agent,omitempty"`
	Recipe          string  `json:"recipe,omitempty"`
	Status          string  `json:"status,omitempty"`
	Model           string  `json:"model,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	Tag             string  `json:"tag,omitempty"`
	Session         string  `json:"session,omitempty"`
	Environment     string  `json:"environment,omitempty"`
	AgentVersion    string  `json:"agent_version,omitempty"`
	Query           string  `json:"q,omitempty"`
	MinCost         float64 `json:"min_cost,omitempty"`
	MaxCost         float64 `json:"max_cost,omitempty"`
	MinDuration     int64   `json:"min_duration,omitempty"`
	MaxDuration     int64   `json:"max_duration,omitempty"`
	Range           string  `json:"range,omitempty"` // 1h, 24h, 7d, 30d
	Sort            string  `json:"sort,omitempty"`  // created_at, cost, duration, tokens
	Order           string  `json:"order,omitempty"` // asc, desc
	RootsOnly       bool    `json:"roots,omitempty"`
	IncludeArchived bool    `json:"include_archived,omitempty"`
}

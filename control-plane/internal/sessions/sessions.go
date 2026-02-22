// Package sessions provides in-memory session management for multi-turn
// conversations with managed agents.
package sessions

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

// MemorySessionStore is a thread-safe in-memory implementation of SessionStore.
type MemorySessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*models.Session // key: session ID
}

// NewMemorySessionStore creates a new in-memory session store.
func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{
		sessions: make(map[string]*models.Session),
	}
}

// CreateSession stores a new session.
func (s *MemorySessionStore) CreateSession(_ context.Context, session *models.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[session.ID]; exists {
		return fmt.Errorf("session %s already exists", session.ID)
	}
	s.sessions[session.ID] = session
	return nil
}

// GetSession retrieves a session by ID.
func (s *MemorySessionStore) GetSession(_ context.Context, sessionID string) (*models.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	return session, nil
}

// UpdateSession replaces the session state.
func (s *MemorySessionStore) UpdateSession(_ context.Context, session *models.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[session.ID]; !exists {
		return fmt.Errorf("session %s not found", session.ID)
	}
	session.UpdatedAt = time.Now().UTC()
	s.sessions[session.ID] = session
	return nil
}

// ListSessions lists sessions for an agent in a kitchen.
func (s *MemorySessionStore) ListSessions(_ context.Context, kitchen, agentName string) ([]models.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []models.Session
	for _, sess := range s.sessions {
		if sess.Kitchen == kitchen && sess.AgentName == agentName {
			result = append(result, *sess)
		}
	}
	return result, nil
}

// DeleteSession removes a session.
func (s *MemorySessionStore) DeleteSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[sessionID]; !exists {
		return fmt.Errorf("session %s not found", sessionID)
	}
	delete(s.sessions, sessionID)
	return nil
}

package store

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/agentoven/agentoven/control-plane/pkg/models"
)

func (m *MemoryStore) CreateTraceView(_ context.Context, v *models.TraceView) error {
	m.mu.Lock()
	for _, o := range m.traceViews {
		if o.Kitchen == v.Kitchen && o.Owner == v.Owner && strings.EqualFold(o.Name, v.Name) {
			m.mu.Unlock()
			return &ErrConflict{Entity: "trace view", Key: v.Name}
		}
	}
	c := *v
	m.traceViews[v.ID] = &c
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) ListTraceViews(_ context.Context, kitchen, owner string) ([]models.TraceView, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []models.TraceView{}
	for _, v := range m.traceViews {
		if v.Kitchen == kitchen && (v.Shared || v.Owner == owner) {
			out = append(out, *v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemoryStore) GetTraceView(_ context.Context, kitchen, id string) (*models.TraceView, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.traceViews[id]
	if !ok || v.Kitchen != kitchen {
		return nil, &ErrNotFound{Entity: "trace view", Key: id}
	}
	c := *v
	return &c, nil
}

func (m *MemoryStore) UpdateTraceView(_ context.Context, v *models.TraceView) error {
	m.mu.Lock()
	cur, ok := m.traceViews[v.ID]
	if !ok || cur.Kitchen != v.Kitchen {
		m.mu.Unlock()
		return &ErrNotFound{Entity: "trace view", Key: v.ID}
	}
	for _, o := range m.traceViews {
		if o.ID != v.ID && o.Kitchen == v.Kitchen && o.Owner == cur.Owner && strings.EqualFold(o.Name, v.Name) {
			m.mu.Unlock()
			return &ErrConflict{Entity: "trace view", Key: v.Name}
		}
	}
	c := *cur
	c.Name, c.Filter, c.Shared, c.UpdatedAt = v.Name, v.Filter, v.Shared, time.Now().UTC()
	m.traceViews[v.ID] = &c
	m.mu.Unlock()
	m.requestSave()
	return nil
}

func (m *MemoryStore) DeleteTraceView(_ context.Context, kitchen, id string) error {
	m.mu.Lock()
	v, ok := m.traceViews[id]
	if !ok || v.Kitchen != kitchen {
		m.mu.Unlock()
		return &ErrNotFound{Entity: "trace view", Key: id}
	}
	delete(m.traceViews, id)
	m.mu.Unlock()
	m.requestSave()
	return nil
}

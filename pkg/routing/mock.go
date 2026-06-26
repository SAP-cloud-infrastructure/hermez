// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"context"
	"sync"
	"time"
)

// Mock implements Store with in-memory storage for use in unit tests.
type Mock struct {
	mu      sync.RWMutex
	configs map[string]DataplaneConfig
}

// NewMock creates an empty Mock store.
func NewMock() *Mock {
	return &Mock{configs: make(map[string]DataplaneConfig)}
}

// Get retrieves the config for a project.
func (m *Mock) Get(_ context.Context, projectID string) (*DataplaneConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cfg, ok := m.configs[projectID]
	if !ok {
		return nil, ErrNotFound
	}
	c := cfg
	return &c, nil
}

// Upsert creates or replaces the config for a project.
func (m *Mock) Upsert(_ context.Context, cfg DataplaneConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.UpdatedAt.IsZero() {
		cfg.UpdatedAt = time.Now().UTC()
	}
	m.configs[cfg.ProjectID] = cfg
	return nil
}

// Delete removes the config for a project. Idempotent.
// Returns (true, nil) if a config existed and was removed; (false, nil) if none existed.
func (m *Mock) Delete(_ context.Context, projectID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, existed := m.configs[projectID]
	delete(m.configs, projectID)
	return existed, nil
}

// Ensure Mock implements Store.
var _ Store = (*Mock)(nil)

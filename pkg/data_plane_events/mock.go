// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package data_plane_events

import (
	"context"
	"sync"
)

// Mock is an in-memory Storage implementation for tests.
// It is safe for concurrent use.
type Mock struct {
	mu      sync.Mutex
	entries map[string]bool
}

// NewMock returns an empty Mock.
func NewMock() *Mock {
	return &Mock{entries: make(map[string]bool)}
}

// Get implements Storage.
func (m *Mock) Get(_ context.Context, projectID string) (enabled, found bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.entries[projectID]
	return v, ok, nil
}

// Set implements Storage. See Storage.Set for the changed/priorEnabled semantics.
func (m *Mock) Set(_ context.Context, projectID string, enabled bool) (changed, priorEnabled bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, found := m.entries[projectID]
	// observable state of "no row" equals "row with enabled=false"
	if !found {
		if !enabled {
			return false, false, nil
		}
		m.entries[projectID] = true
		return true, false, nil
	}
	if current == enabled {
		return false, current, nil
	}
	m.entries[projectID] = enabled
	return true, current, nil
}

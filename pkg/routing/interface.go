// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"context"
	"errors"
)

// ErrNotFound is returned by Store.Get when no config exists for a project.
var ErrNotFound = errors.New("routing: config not found for project")

// Store is the persistence interface for dataplane routing configuration.
// The Postgres implementation is the production backend;
// the Mock implementation is used in unit tests.
//
// All methods receive a context so they respect request cancellation.
type Store interface {
	// Get retrieves the config for a project.
	// Returns ErrNotFound if no document exists; callers should treat that as
	// the default disabled config rather than an error visible to the client.
	Get(ctx context.Context, projectID string) (*DataplaneConfig, error)

	// Upsert creates or replaces the config for a project.
	Upsert(ctx context.Context, cfg DataplaneConfig) error

	// Delete removes the config for a project.
	// Returns (true, nil) if a config existed and was removed.
	// Returns (false, nil) if no config existed (idempotent: not an error).
	Delete(ctx context.Context, projectID string) (bool, error)
}

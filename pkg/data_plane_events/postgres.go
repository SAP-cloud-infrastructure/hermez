// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package data_plane_events

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Postgres is the production Storage implementation backed by PostgreSQL.
type Postgres struct {
	db *sql.DB
}

// NewPostgres returns a Postgres-backed Storage. The provided *sql.DB is
// expected to already have the schema applied (via easypg.Connect with
// Migrations from this package).
func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

// Get implements Storage.
func (p *Postgres) Get(ctx context.Context, projectID string) (enabled, found bool, err error) {
	row := p.db.QueryRowContext(ctx,
		`SELECT enabled FROM data_plane_events WHERE project_id = $1`, projectID)
	err = row.Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("query data_plane_events: %w", err)
	}
	return enabled, true, nil
}

// Set implements Storage atomically using a single round trip.
//
// The WHERE clause on the ON CONFLICT branch suppresses no-op writes so
// repeated PATCHes with the same value do not churn the WAL. The xmax=0
// trick distinguishes INSERT from UPDATE in the same RETURNING set:
//
//   - inserted=true              -> row was just created; priorEnabled = false.
//   - inserted=false, returned   -> WHERE matched, so prior value differed from
//     `enabled`; priorEnabled = !enabled.
//   - sql.ErrNoRows              -> conflict hit, WHERE filtered the UPDATE;
//     no observable state change.
//
// The "PATCH false on absent project" case still writes nothing because
// the ON CONFLICT DO UPDATE WHERE evaluates `false IS DISTINCT FROM false`
// = false on the freshly inserted row — but actually the INSERT itself
// would materialize a row with enabled=false, which violates the
// "no row equals enabled=false" contract. To keep that invariant, we
// short-circuit absent+disable in code rather than the DB.
func (p *Postgres) Set(ctx context.Context, projectID string, enabled bool) (changed, priorEnabled bool, err error) {
	// Preserve the "no row equals enabled=false" invariant: PATCH false on an
	// absent project must NOT materialize a row, so check first. This costs
	// one extra round trip only on the "disable on absent" path, which is
	// the cold path (default state, no-op).
	if !enabled {
		current, found, gerr := p.Get(ctx, projectID)
		if gerr != nil {
			return false, false, gerr
		}
		if !found {
			return false, false, nil
		}
		if !current {
			return false, false, nil
		}
		// found && current==true && enabled==false: real disable; fall through
		// to the upsert which will UPDATE.
	}

	row := p.db.QueryRowContext(ctx,
		`INSERT INTO data_plane_events (project_id, enabled) VALUES ($1, $2)
		 ON CONFLICT (project_id) DO UPDATE SET enabled = EXCLUDED.enabled
		   WHERE data_plane_events.enabled IS DISTINCT FROM EXCLUDED.enabled
		 RETURNING (xmax = 0) AS inserted, enabled`,
		projectID, enabled)

	var inserted bool
	var returnedEnabled bool
	scanErr := row.Scan(&inserted, &returnedEnabled)
	if errors.Is(scanErr, sql.ErrNoRows) {
		// Conflict hit but WHERE filtered the UPDATE: stored value already
		// equals `enabled`; no-op.
		return false, enabled, nil
	}
	if scanErr != nil {
		return false, false, fmt.Errorf("upsert data_plane_events: %w", scanErr)
	}
	if inserted {
		// Brand-new row; observable prior state was the default (false).
		return true, false, nil
	}
	// UPDATE branch: WHERE matched, so prior value differed from `enabled`.
	return true, !enabled, nil
}

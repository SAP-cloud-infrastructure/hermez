// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"
)

// DBMigrations contains the SQL migrations for the dataplane_config table.
// Keys must follow the golang-migrate filename convention.
var DBMigrations = map[string]string{
	"001_create_dataplane_config.up.sql": `
		CREATE TABLE IF NOT EXISTS dataplane_config (
			project_id    VARCHAR(64) PRIMARY KEY,
			enabled       BOOLEAN     NOT NULL DEFAULT FALSE,
			target_bucket TEXT        NOT NULL DEFAULT '',
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_by    VARCHAR(64) NOT NULL DEFAULT ''
		);

		-- Grant read access directly to the log-router login role provisioned by
		-- the hermes helm chart (postgres-ng seed). CREATE ROLE is intentionally
		-- omitted: the hermes user lacks CREATEROLE, so role management belongs
		-- in the chart, not in app migrations.
		GRANT SELECT ON dataplane_config TO "log-router";
	`,
	"001_create_dataplane_config.down.sql": `
		DROP TABLE IF EXISTS dataplane_config;
	`,
}

// Postgres implements Store using a PostgreSQL database.
type Postgres struct {
	db *sql.DB
}

// NewPostgres connects to postgres using env-var based connection params and
// runs any pending migrations. The env vars are:
//
//	HERMES_PG_HOSTNAME (default: localhost)
//	HERMES_PG_PORT     (default: 5432)
//	HERMES_PG_USERNAME (default: hermes)
//	HERMES_PG_PASSWORD
//	HERMES_PG_DBNAME   (default: hermes)
//	HERMES_PG_CONNECTION_OPTIONS
func NewPostgres() (*Postgres, error) {
	dbURL, err := easypg.URLFrom(easypg.URLParts{
		HostName:          osext.GetenvOrDefault("HERMES_PG_HOSTNAME", "localhost"),
		Port:              osext.GetenvOrDefault("HERMES_PG_PORT", "5432"),
		UserName:          osext.GetenvOrDefault("HERMES_PG_USERNAME", "hermes"),
		Password:          osext.GetenvOrDefault("HERMES_PG_PASSWORD", ""),
		ConnectionOptions: osext.GetenvOrDefault("HERMES_PG_CONNECTION_OPTIONS", ""),
		DatabaseName:      osext.GetenvOrDefault("HERMES_PG_DBNAME", "hermes"),
	})
	if err != nil {
		return nil, fmt.Errorf("routing: cannot build postgres URL: %w", err)
	}
	return NewPostgresFromURL(dbURL)
}

// NewPostgresFromURL connects to postgres at the given URL and runs migrations.
// Useful in tests that provide a pre-built URL.
func NewPostgresFromURL(dbURL url.URL) (*Postgres, error) {
	db, err := easypg.Connect(dbURL, easypg.Configuration{
		Migrations: DBMigrations,
	})
	if err != nil {
		return nil, fmt.Errorf("routing: cannot connect to postgres: %w", err)
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	logg.Info("routing: postgres connected and migrations applied")
	return &Postgres{db: db}, nil
}

// Get retrieves the config for a project.
func (p *Postgres) Get(ctx context.Context, projectID string) (*DataplaneConfig, error) {
	var cfg DataplaneConfig
	err := p.db.QueryRowContext(ctx,
		`SELECT project_id, enabled, target_bucket, updated_at, updated_by
		   FROM dataplane_config WHERE project_id = $1`,
		projectID,
	).Scan(&cfg.ProjectID, &cfg.Enabled, &cfg.TargetBucket, &cfg.UpdatedAt, &cfg.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("routing: cannot get config for project %s: %w", projectID, err)
	}
	return &cfg, nil
}

// Upsert creates or replaces the config for a project.
func (p *Postgres) Upsert(ctx context.Context, cfg DataplaneConfig) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO dataplane_config (project_id, enabled, target_bucket, updated_at, updated_by)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (project_id) DO UPDATE SET
		     enabled       = EXCLUDED.enabled,
		     target_bucket = EXCLUDED.target_bucket,
		     updated_at    = EXCLUDED.updated_at,
		     updated_by    = EXCLUDED.updated_by`,
		cfg.ProjectID, cfg.Enabled, cfg.TargetBucket, cfg.UpdatedAt, cfg.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("routing: cannot upsert config for project %s: %w", cfg.ProjectID, err)
	}
	return nil
}

// Delete removes the config for a project. Idempotent.
// Returns (true, nil) if a row was deleted; (false, nil) if none existed.
func (p *Postgres) Delete(ctx context.Context, projectID string) (bool, error) {
	result, err := p.db.ExecContext(ctx,
		`DELETE FROM dataplane_config WHERE project_id = $1`,
		projectID,
	)
	if err != nil {
		return false, fmt.Errorf("routing: cannot delete config for project %s: %w", projectID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("routing: cannot get rows affected after delete for project %s: %w", projectID, err)
	}
	return n > 0, nil
}

// Close releases the database connection pool.
func (p *Postgres) Close() error {
	return p.db.Close()
}

// Ensure Postgres implements Store.
var _ Store = (*Postgres)(nil)

// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package data_plane_events

// Migrations contains the schema migrations for the data_plane_events table
// in the format expected by easypg (filename keys, see go-bits/easypg).
//
// The DO block around CREATE ROLE is the SAP CC convention so that reruns
// against an existing role do not fail. The down migration intentionally
// does not DROP ROLE: helm-charts may have provisioned other login users
// inheriting from log_router_reader, and dropping the role would break them.
// Revoking the SELECT grant is sufficient to undo the up migration's effect.
//
// REVOKE ALL precedes GRANT SELECT to make the role's privilege set
// idempotent and explicit (defense in depth against an operator having
// granted extra privileges out of band).
//
// The CONNECT and USAGE grants are required because Postgres does not
// imply them from table-level privileges; without them log_router_reader
// cannot open the database or resolve the public schema. The database
// name is hardcoded to "hermes" here because easypg does not expose its
// configured DB name through migration substitution; if you deploy under
// a different DB name, edit this migration before applying.
var Migrations = map[string]string{
	"001_initial.up.sql": `
		CREATE TABLE data_plane_events (
		    project_id VARCHAR(36) PRIMARY KEY,
		    enabled    BOOLEAN NOT NULL
		);
		DO $$
		BEGIN
		    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'log_router_reader') THEN
		        CREATE ROLE log_router_reader NOLOGIN;
		    END IF;
		END
		$$;
		REVOKE ALL ON data_plane_events FROM log_router_reader;
		GRANT SELECT ON data_plane_events TO log_router_reader;
		GRANT CONNECT ON DATABASE hermes TO log_router_reader;
		GRANT USAGE ON SCHEMA public TO log_router_reader;
	`,
	"001_initial.down.sql": `
		REVOKE SELECT ON data_plane_events FROM log_router_reader;
		REVOKE USAGE ON SCHEMA public FROM log_router_reader;
		REVOKE CONNECT ON DATABASE hermes FROM log_router_reader;
		DROP TABLE data_plane_events;
	`,
}

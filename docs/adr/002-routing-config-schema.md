<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-002: Lift `dest_config` schema from log-router into hermez

- **Status:** **Superseded by ADR-007** (resource shape collapsed from `Config` to `Sink`; schema reduced from 22 columns + audit table to 10 columns; no audit table; no rate-limit / retention / grace / OTLP / DLQ surface)
- **Date:** 2026-05-19 (superseded 2026-05-20)
- **Depends on:** ADR-001
- **Superseded by:** ADR-007
- **Related:** log-router migrations `001`–`004`; keppel `easypg` pattern; archer `mgx/v2` pattern

> **Reader: this ADR is preserved for historical context only.** The schema, the table names, the column set, the audit-table design, and the package layout described below have been replaced by ADR-007. The sections that remain canonical are the **library and tooling choice** (`lib/pq` + `easypg` + `database/sql` + `sqlext`, advisory-lock migrations, env-var connection URL, pool sizing, `sqlstats` collector) and the **driver-pattern selection in `main.go`**. Read ADR-007 first; treat the schema below as the original-intent draft, not the contract.

---

## What changed (summary for the reader)

| Topic | This ADR (original intent) | ADR-007 (canonical) |
|---|---|---|
| Resource | `Config` (1 per project) | `Sink` (N per project) |
| Primary table | `routing_configs` | `routing_sinks` |
| PK | `(project_id)` | `(project_id, name)` |
| Column count | 22 (incl. rate limits, retention, grace, target_obj, sinks JSONB, lifecycle pair) | 10 (project_id, name, domain_id, description, destination, filter, kms_key_id, disabled, created_at, updated_at) |
| Audit-of-config | `routing_audit_log` side table with FK ON DELETE RESTRICT | Dropped. CADF events via `audittools.Auditor` (Phase 1.5). |
| Sink fan-out | `sinks JSONB` column on configs | One row per sink |
| `data_plane_enabled` flag | Yes | Subsumed by `disabled` |
| `enabled_at` / `disabled_at` timestamps | Yes | Captured in CADF audit events |
| `RehydrationAllowed`, `ManagementEventsEnabled` | Yes (reserved for future use) | Dropped |
| Rate limits, retention, grace, target_obj | Yes (schema-only) | Dropped — lying contract |
| `tenant_id` JSON wire tag | Reserved for hermescli back-compat | Dropped — zero shipped clients |
| Single `scoped()` builder helper | Specified, never implemented | Specified again in ADR-007 — required |

---

## Context (preserved)

ADR-001 decided hermez owns the routing-config schema. This ADR specifies
*what* schema, *how* it gets into the database, and *which library* hermez
uses for SQL.

Source data shape already exists in log-router:

| Table | Migration | Purpose |
|-------|-----------|---------|
| `dest_config` | `001_create_dest_config.up.sql` | Per-tenant routing config (PK `tenant_id`); 22 columns covering bucket/prefix/kms/endpoint/region, rate limits, feature flags, grace minutes, target object MB, retention days, lifecycle timestamps |
| `cfg_audit_log` | `002_create_cfg_audit_log.up.sql` | BIGSERIAL audit trail with FK to `dest_config(tenant_id) ON DELETE RESTRICT`; per-field change tracking |
| (alter) | `003_add_sinks.up.sql` | Adds `sinks JSONB NOT NULL DEFAULT '[]'` to `dest_config` for dynamic multi-sink fan-out |
| `metering_records` | `004_create_metering_records.up.sql` | Per-flush metering rows; `(tenant_id, service, region, sink_name, hour)` UNIQUE |

`metering_records` ownership is decided in ADR-006 (kept with log-router).
This ADR scoped to `dest_config`, `cfg_audit_log`, and `add_sinks`.

## Decision (still canonical: library and tooling)

| Concern | Choice | Justification |
|---------|--------|---------------|
| DB driver | `lib/pq` via go-bits `easypg` | hermez already depends on `go-bits`; keppel uses easypg in production; SAP CC house pattern |
| Query layer | `database/sql` + `sqlext.SimplifyWhitespace` | Sufficient for the data model (≤3 tables); no ORM dep; matches keppel for ≤5-table services |
| Migrations | `easypg.Configuration{Migrations: map[string]string}` (keppel pattern) | Embeds SQL strings in Go; uses `golang-migrate` internally with `pg_advisory_lock`; safe against concurrent replica startup |
| Migration trigger | Auto-run on hermez startup, gated by config flag (`hermes.routing_driver = "postgres"`) | Matches keppel's `easypg.Connect(dbURL, DBConfiguration())` at `cmd/api/main.go:55`. Replicas serialize via advisory lock |
| Connection URL | `easypg.URLFrom(easypg.URLParts{...})` from env vars | Keeps password out of TOML; matches keppel `internal/keppel/config.go:79-87` |
| Connection pool | `db.SetMaxOpenConns(16)` per replica; `MaxIdleConns = 4`; `ConnMaxLifetime = 5m` | Keppel default; sized for `postgresql-ng config.max_connections = 64` and N≤4 hermez replicas with safety margin |
| Observability | `prometheus.MustRegister(sqlstats.NewStatsCollector("hermes_routing", db))` | Keppel pattern; surfaces saturation and wait time as metrics |
| Driver selection | `viper.GetString("hermes.routing_driver")` switch in `configuredRoutingStore()`, mirroring `configuredStorageDriver` | Idiomatic for hermez |

These choices remain canonical under ADR-007. ADR-007 only changes the table shape; the library, tooling, migration mechanism, connection URL plumbing, pool sizing, and observability all carry forward.

## Decision (superseded: schema)

> **The schema below is the original-intent draft. Use ADR-007 §"Schema — `routing_sinks`" for the canonical table.** The column set was reduced to 10 columns; the audit table was dropped; `data_plane_enabled` and the `enabled_at`/`disabled_at` pair were collapsed into a single `disabled` boolean; `Sinks JSONB` was removed in favor of one row per sink.

```sql
-- 001_create_routing_configs.up.sql  (SUPERSEDED — see ADR-007 for routing_sinks)
CREATE TABLE routing_configs (
    project_id           VARCHAR(36) PRIMARY KEY,
    domain_id            VARCHAR(36) NOT NULL,
    display_name         TEXT NOT NULL DEFAULT '',
    -- legacy single-sink (back-compat with log-router pre-sinks era):
    bucket               TEXT NOT NULL DEFAULT '',
    prefix               TEXT NOT NULL DEFAULT '',
    kms_key_id           TEXT NOT NULL DEFAULT '',
    endpoint             TEXT NOT NULL DEFAULT '',
    region               TEXT NOT NULL DEFAULT '',
    -- rate limits:
    max_events_per_second  INTEGER NOT NULL DEFAULT 0,
    max_bytes_per_second   BIGINT  NOT NULL DEFAULT 0,
    -- feature flags:
    management_events_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    data_plane_enabled        BOOLEAN NOT NULL DEFAULT FALSE,
    rehydration_allowed       BOOLEAN NOT NULL DEFAULT FALSE,
    -- batching:
    grace_minutes        INTEGER NOT NULL DEFAULT 5,
    target_obj_mb        INTEGER NOT NULL DEFAULT 64,
    retention_days       INTEGER NOT NULL DEFAULT 365,
    -- multi-sink fan-out (lifted from migration 003):
    sinks                JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- lifecycle:
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    enabled_at           TIMESTAMPTZ,
    disabled_at          TIMESTAMPTZ
);
CREATE INDEX idx_routing_configs_domain ON routing_configs(domain_id);
CREATE INDEX idx_routing_configs_data_plane
    ON routing_configs(project_id) WHERE data_plane_enabled = true;

-- 002_create_routing_audit_log.up.sql  (SUPERSEDED — table dropped in ADR-007;
-- audit-of-config emits CADF events via audittools.Auditor instead)
CREATE TABLE routing_audit_log (
    id              BIGSERIAL PRIMARY KEY,
    project_id      VARCHAR(36) NOT NULL
        REFERENCES routing_configs(project_id) ON DELETE RESTRICT,
    actor_user_id   VARCHAR(64) NOT NULL,
    actor_project_id VARCHAR(64) NOT NULL,
    action          VARCHAR(16) NOT NULL,        -- create|update|enable|disable|delete
    field_name      TEXT,                         -- nullable for whole-row create/delete
    old_value       JSONB,
    new_value       JSONB,
    changed_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_routing_audit_log_project ON routing_audit_log(project_id, changed_at DESC);
```

### Tenant isolation rules (still canonical, table reference updated by ADR-007)

```go
const AllTenants = "*"
var ErrEmptyTenantID   = errors.New("tenant ID cannot be empty")
var ErrInvalidTenantID = errors.New("tenant ID 'unavailable' is not valid")
```

The `scoped()` query-builder helper (one centralized WHERE-clause-injection point) was specified here, not implemented in the original PR (reconciliation report flagged the drift), and **re-specified as REQUIRED in ADR-007**.

### Package layout (refined by ADR-007)

The `pkg/routing/` package layout (store.go, types.go, postgres.go, postgres_test.go, mock.go, migrations.go) survives the supersedure. The Go-string-embedded migrations approach survives. Only the SQL strings change.

The original layout listed an optional `pkg/routing/migrations/*.sql` subdirectory via `embed.FS`. That directory was never created; SQL is inlined as Go strings. ADR-007 keeps the inlined approach.

## Consequences (substantially superseded)

The original "Positive / Negative / Neutral" framing assumed a 22-column `routing_configs` schema with a side audit table. ADR-007's 10-column `routing_sinks` schema changes the cost/benefit:

- **More positive than originally claimed:** smaller test matrix, smaller migration cost, smaller "schema lying about enforcement" risk.
- **One originally-negative item removed:** "lib/pq is in maintenance mode" still applies but ADR-007 doesn't change that calculus.
- **One originally-negative item added:** Phase 1.5 needs `audittools.Auditor` wiring. Reference: `keppel/internal/keppel/auditor.go:12-36`.

## Alternatives considered (still canonical)

| Alternative | Verdict | Reason |
|-------------|---------|--------|
| `pgx/v5 + sqlx + mgx/v2` (archer pattern) | Reject | Three new top-level deps for benefit (LISTEN/NOTIFY, native types) we don't need at this scale |
| `gorp` ORM | Reject for v1 | Overkill for two tables; revisit if model grows beyond 5 tables |
| Filesystem-only migrations (log-router's `init-db.sh`) | Reject | Pushes migration responsibility outside the binary; doesn't compose with k8s rolling deploys |
| Separate `cmd/migrate` subcommand | Reject for v1 | Adds repo restructuring (no `cmd/` today); auto-on-startup with advisory lock is safe enough |
| pgx + raw SQL (no go-bits) | Reject | Loses SAP CC observability conventions (sqlstats), test helpers (WithTestDB) |

## Implementation checklist

> **See ADR-007 §"Implementation checklist (this PR)" for the canonical task list.** The original list below is preserved for the library/tooling items that survive supersedure.

- [x] Add `go-bits/easypg`, `lib/pq`, `go-bits/sqlext` to `go.mod` *(carries forward)*
- [x] Create `pkg/routing/` package *(carries forward)*
- [x] Embed migrations as Go string constants (keppel pattern) *(carries forward)*
- [x] Implement `Postgres` and `Mock` against `Store` interface *(carries forward; renamed `Sink`-shaped in ADR-007)*
- [x] Wire `configuredRoutingStore()` in `main.go` mirroring `configuredStorageDriver` *(carries forward)*
- [x] Add `[postgres]` section to `etc/hermes.conf` (and `viper.SetDefault` keys) *(carries forward)*
- [x] Bind `HERMES_PG_PASSWORD` env via `viper.BindEnv` *(carries forward)*
- [ ] **`sqlstats` Prometheus collector registration** — specified here, never wired (reconciliation report flagged); still required, carried into ADR-007's checklist by reference
- [ ] **`scoped(token)` query-builder helper** — specified here, never wired; **re-specified as REQUIRED in ADR-007**
- [ ] Verify advisory-lock behavior in easypg before relying on it for replica safety (read `easypg.Connect` source) *(carries forward)*
- [ ] Add SPDX headers per `REUSE.toml` for any new files *(carries forward)*
- [ ] `make license-headers && make check-reuse` *(carries forward)*
- [ ] Bump `main.go:26` version constant for the routing release *(done at 1.3.0; bump again on Phase 1 reshape merge)*

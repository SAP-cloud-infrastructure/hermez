<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-001: Dataplane audit-event routing, hermez owns the config

> Earlier versions of this ADR were out of date in several places (rollback, object paths, outage behaviour, migration library). Do not use them for decisions.

| | |
|---|---|
| Status | Accepted, shipped |
| Re-checked | 2026-09-26 against hermez master (6b12f7b) and log-router main (a20013b) |
| Author | Nathan Oyler |
| Reader contract | [../dataplane-config-read-contract.md](../dataplane-config-read-contract.md) |

Everything below describes the code at those two commits. Items that could not be checked in code are listed at the end under "Not verified from code".

## Context

log-router consumes CADF audit events and writes an admin copy of each event it ingests, before any per-project check runs (`internal/router/router.go:390-407`). Project owners had no way to get a copy of their own events into storage they control.

The feature adds a per-project opt-in. When a project is enabled, log-router writes a second copy of that project's events into a container in the project's own object-storage account.

## Decision

### 1. hermez stores the config, log-router reads it with SQL

- hermez owns the `dataplane_config` table, its migration, the REST API that writes it, and the CADF events for changes.
- log-router reads the table directly over Postgres. It has no HTTP client for hermez. Its only HTTP routes are `/metrics`, `/v1/signing-key` and a health check.
- No code in log-router writes `dataplane_config` (repo-wide search for INSERT/UPDATE/DELETE on it finds nothing; `internal/config/client.go:71` only SELECTs). It does write `metering_records` over the same database connection (see section 6).

### 2. Data model

One table, created by hermez migration version 1:

```sql
CREATE TABLE IF NOT EXISTS dataplane_config (
    project_id    VARCHAR(64) PRIMARY KEY,
    enabled       BOOLEAN     NOT NULL DEFAULT FALSE,
    target_bucket TEXT        NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by    VARCHAR(64) NOT NULL DEFAULT ''
);
GRANT SELECT ON dataplane_config TO "log-router";
```

| Column | Set by hermez from |
|---|---|
| `project_id` | URL path, never the request body |
| `enabled` | request body |
| `target_bucket` | request body; `hermes-audit` if `enabled=true` and the field is empty or missing |
| `updated_at` | server clock (UTC) on every PUT |
| `updated_by` | `user_id` from the Keystone token |

### 3. API

| Method | Path | Success | Other codes |
|---|---|---|---|
| GET | `/v1/projects/{project_id}/dataplane-config` | 200, stored row or `{"project_id":…,"enabled":false,…}` if none | 401/403, 500 (storage) |
| PUT | same | 200 with saved row | 400, 401, 403, 415, 500 |
| DELETE | same | 204, also when no row existed | 401, 403, 500 |

PUT rules:

- `Content-Type` must start with `application/json`, else 415.
- Body is capped at 64 KiB. Unknown JSON fields are rejected with 400. Accepted fields: `enabled`, `target_bucket`.
- A non-empty `target_bucket` must match `^[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]$` and must not contain `--`. This is checked even when `enabled=false`.
- `enabled=true` with no `target_bucket` stores `hermes-audit` (#371).
- Write is `INSERT ... ON CONFLICT (project_id) DO UPDATE`, so PUT is idempotent.
- A token without `user_id` gets 401 on PUT and DELETE.

CADF events (target type `service/hermes/dataplane-config`):

- PUT: one event per attempt that passes auth, including 400, 415 and 500 outcomes.
- DELETE: an event only when a row was removed, or when storage failed. A DELETE of a missing row emits nothing.
- Events go to the RabbitMQ queue in `HERMES_AUDIT_RABBITMQ_QUEUE_NAME`. If that is unset, hermez uses a null auditor and events are dropped.

### 4. Authorization

Policy rule in `etc/policy.json`:

```json
"project_admin": "rule:project_scope and role:audit_admin",
"cluster_viewer": "project_domain_name:cloud_domain and project_name:cloud_admin_project",
"dataplane_config:manage": "rule:project_admin or rule:cluster_viewer"
```

On top of the policy, `authDataplaneConfig` in `pkg/api/dataplane_config.go` returns 403 when the path `project_id` differs from the token's project, unless the token passes `cluster_viewer`. So:

| Caller | Can manage |
|---|---|
| `audit_admin` on project P | only P |
| token scoped to `cloud_admin_project` in `cloud_domain` | any project |

### 5. Storage in hermez

- Postgres through `database/sql`, `lib/pq` and `go.xyrillian.de/gg/pgruntime` (switched from easypg in #356).
- Migrations are an inline Go map `DBMigrations` keyed by int64 version. There is one migration, version 1. No `.sql` files.
- pgruntime serializes migrations with `SELECT version FROM schema_migrations FOR UPDATE` (a row lock), not `pg_advisory_lock`. A replica that finds the schema already migrated past its target version gets an error. hermez wraps `NewPostgres` in `must.Return`, so that replica exits.
- Pool: 16 open, 4 idle connections.
- Selected by `hermes.routing_store_driver`. Default is `postgres`. The only other accepted value is `mock` (in-memory). Any other value, including `""`, is fatal at startup.

Environment:

| Variable | Default |
|---|---|
| `HERMES_PG_HOSTNAME` | `localhost` |
| `HERMES_PG_PORT` | `5432` |
| `HERMES_PG_USERNAME` | `hermes` |
| `HERMES_PG_PASSWORD` | empty (logs a SECURITY WARNING) |
| `HERMES_PG_DBNAME` | `hermes` |
| `HERMES_PG_CONNECTION_OPTIONS` | empty |

```toml
[hermes]
routing_store_driver = "postgres"   # or "mock"; nothing else is accepted
```

### 6. How log-router uses the config

Postgres mode is on only when `LOG_ROUTER_DB_URL` is non-empty. Per event:

1. Admin copy is taken first, unconditionally, before validation, config lookup or the enabled check.
2. Config is looked up with `SELECT project_id, enabled, target_bucket FROM dataplane_config WHERE project_id = $1`. The tenant is the event's `initiator.project_id`.
3. No row or `enabled=false`: the customer copy is dropped and counted in `log_router_events_dropped_total{reason="tenant_not_enabled"}`. No log line.
4. `enabled=true`: a per-project rate limit applies (10,000 events/s, hard-coded, not settable from hermez or env), then the event goes on to processing and is buffered for the customer copy.

Mapping from the row: `enabled` becomes `DataPlaneEnabled`, `target_bucket` becomes the container (empty becomes `hermes-audit`), and the storage account is `AUTH_<project_id>`. Nothing else is read. Sinks, CEL filters, grace time, retention and OpenSearch delivery are not fed from this table.

Where objects land:

| Copy | Account | Container | Key |
|---|---|---|---|
| Admin | Swift mode: `LOG_ROUTER_SWIFT_ADMIN_ACCOUNT`, or the service user's own account if unset | `LOG_ROUTER_S3_BUCKET` (required) | `default/admin/YYYY/MM/DD/HH:00_HH:59/` |
| Project | `AUTH_<project_id>` | `target_bucket` (default `hermes-audit`) | `default/YYYY/MM/DD/HH:00_HH:59/` |

Object names under the prefix are `S{n}.json`, late-arrival shards `A{shard}_{n}.json`, plus manifest and digest objects. `LOG_ROUTER_S3_PREFIX`, if set, is prepended. The project container is created on first write with read ACL `<project_id>:*`.

Cache: in-memory per process, TTL from `LOG_ROUTER_CACHE_TTL` (default 5m, `0` disables), max 1000 entries. Only lookups that found a row are cached. Projects with no row hit Postgres on every lookup.

Metering: after each successful customer-copy flush, log-router upserts a row in `metering_records` on the same `*sql.DB` it uses for config. A metering failure does not fail the flush. The admin copy is not metered.

### 7. Database roles

- hermez connects as `HERMES_PG_USERNAME` (default `hermes`), creates the table, and grants SELECT to the login role `log-router`. There is no intermediate NOLOGIN role; the migration does not run `CREATE ROLE`.
- The `log-router` role must exist before hermez runs migration 1, otherwise the GRANT fails.
- log-router gets its credentials only from the DSN in `LOG_ROUTER_DB_URL`.
- log-router's binary runs no migrations. `metering_records` has to exist already; log-router ships its SQL (`migrations/004_create_metering_records.up.sql`) but does not apply it.

## Failure behaviour

| Situation | What happens |
|---|---|
| hermez API down | No effect on log-router. It does not call hermez. |
| Postgres down when log-router starts | 10 pings with backoff inside 60s, then fatal exit. |
| Postgres down while running, entry cached | Cached config keeps being used until its 5m TTL runs out. |
| Postgres down while running, no cached entry | Lookup returns an error. It is not treated as "disabled". On ingest: counted in `log_router_ingest_errors_total{reason="config_lookup"}` and the RabbitMQ message is requeued; after `LOG_ROUTER_RABBITMQ_MAX_REDELIVERIES` (default 3) it is rejected to the DLX if one is configured. On flush: the customer partition is skipped and stays buffered. |
| Any of the above | Admin copy continues. Admin ingest and admin flush never look at config. |
| Admin queue backed up | Admin enqueue waits at most 5s, then that admin copy is abandoned and counted. |
| Postgres down when hermez starts | `NewPostgres` fails and `must.Return` exits the process. |
| Postgres down while hermez runs | GET/PUT/DELETE return an obfuscated 500. |

Each requeue runs ingest again, so it takes another admin copy of the same event (inferred from the code order, not tested).

## Rollback and off switches

| Component | What you can do | Effect |
|---|---|---|
| One project | `PUT {"enabled":false}` or `DELETE` | Customer copy stops once log-router's cache entry for the project expires (up to 5m). |
| hermez dataplane-config API | No off switch. `routing_store_driver` takes `postgres` or `mock` only; `""` is fatal (`main.go:158-159`). `mock` is in-memory and loses all rows on restart. | Not a usable rollback without a code change. |
| log-router DB read | Remove `LOG_ROUTER_DB_URL` | log-router switches to a static config with `DataPlaneEnabled: true` for every project (`main.go:223`) and empty bucket. Empty bucket selects the fallback client, so every project's customer copy is written into the admin container under `default/YYYY/...`, next to `default/admin/...`. Metering stops. This is not "admin only". |

## Consequences

- One API and one auth model for audit reads and routing config.
- log-router does not depend on the hermez API being up. It does depend on Postgres being up at startup.
- A Postgres outage delays or dead-letters project copies for projects without a cached entry. Admin copies continue.
- Config changes reach log-router within the cache TTL (default 5m) for projects that already had a cached row. Enabling a project that had no row takes effect on the next lookup.
- Multiple hermez replicas share the table. Concurrent PUTs are serialized by Postgres; there is no leader election.
- log-router writes to the same database (`metering_records`), so "read-only consumer" is true for `dataplane_config` only.
- The `apply-lifecycle` admin tool in log-router reads the table with its own query (see the contract doc).

## Not implemented

- Bulk admin endpoint `GET /v1/dataplane-configs`.
- sqlstats collector for hermez Postgres metrics.
- Per-project rate limits, retention, grace time, CEL filters or multiple sinks via hermez. log-router has code for these, but nothing in `dataplane_config` feeds it.
- A batch lookup (`WHERE project_id = ANY($1)`) in the log-router service.
- Config history table. The CADF events are the only trail.
- log-router creating `metering_records` itself.

## Not verified from code

These appeared in earlier versions. They may be true but are deployment, process or history facts that the code does not show.

- Ship date and PR links for log-router #24 and helm-charts #12097. hermez #352 is in git history.
- The admin container being `ccadmin/master` in production. The code only reads `LOG_ROUTER_S3_BUCKET` and `LOG_ROUTER_SWIFT_ADMIN_ACCOUNT`.
- The production RabbitMQ queue name. log-router defaults to `audit-events`; `dataplane.audit` does not appear in its code.
- Whether production runs log-router in Swift mode. Per-project delivery into `AUTH_<project_id>` is implemented in the Swift backend. The in-repo chart does not set `LOG_ROUTER_SWIFT_ENABLED` or `OS_*`.
- Postgres cluster type, database name used by log-router, backups, PVC retention, and who provisions the `log-router` role. log-router's own README shows database `log-router`, which would not contain `dataplane_config`.
- Replica counts in production (the in-repo log-router chart has a 2-replica StatefulSet).
- Cache TTL overrides per region, region rollout order.
- Whether CADF events for config changes show up in `GET /v1/events`. That depends on the Logstash pipeline.
- The effective privileges of the `log-router` role beyond the SELECT grant.
- Classic vs quorum queue. The redelivery limit reads `x-delivery-count`; on a classic queue the code only sees 0 or 1 (`internal/source/rabbitmq.go:668-671`), so with the default limit of 3 a failing message would be requeued without end (inferred, not tested).

## Sources

hermez: `main.go`, `pkg/api/core.go`, `pkg/api/dataplane_config.go`, `pkg/routing/*.go`, `etc/policy.json`.
log-router (a20013b): `cmd/log-router/main.go`, `internal/config/client.go`, `internal/router/{router,flush,adminTier}.go`, `internal/audit/event.go`, `internal/sink/s3.go`, `internal/storage/{pool,swift}.go`, `internal/source/rabbitmq.go`, `internal/metering/postgres.go`.

<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Dataplane config: log-router read contract

> Earlier versions of this document were out of date (cache TTL, outage behaviour, empty `target_bucket`, batch query). Do not use them for decisions.

This is the contract between hermez (writer) and log-router (reader) for the `dataplane_config` table. It describes what the code does, re-checked on 2026-09-26 against hermez master (6b12f7b) and log-router main (a20013b). Background is in [ADR-001](adr/001-dataplane-routing.md).

log-router's `internal/config/client.go` links to this file.

## Database access

| Item | Value |
|---|---|
| Table | `dataplane_config` |
| Owner | hermez, created by migration version 1 in `pkg/routing/postgres.go` (inline Go, no `.sql` file) |
| Reader role | `log-router`, granted `SELECT ON dataplane_config` directly in that migration |
| Reader DSN | `LOG_ROUTER_DB_URL` in log-router (`cmd/log-router/main.go:48`). `lib/pq` also honours the standard `PG*` env vars (e.g. `PGPASSWORD`) for anything the DSN leaves out. |
| Reader writes | none to `dataplane_config`; log-router does upsert `metering_records` over the same connection |

The GRANT fails if the `log-router` role does not exist when hermez runs migrations 1 and 2. hermez does not create the role.

log-router runs no migrations. hermez migration 2 in `pkg/routing/postgres.go` creates `metering_records` (schema copied from log-router `migrations/004_create_metering_records.up.sql`) and grants `SELECT, INSERT, UPDATE` on it and `USAGE` on `metering_records_id_seq` to `log-router`.

## Schema

```sql
CREATE TABLE IF NOT EXISTS dataplane_config (
    project_id    VARCHAR(64) PRIMARY KEY,
    enabled       BOOLEAN     NOT NULL DEFAULT FALSE,
    target_bucket TEXT        NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by    VARCHAR(64) NOT NULL DEFAULT ''
);
```

log-router reads `project_id`, `enabled` and `target_bucket`. It never reads `updated_at` or `updated_by`.

## Queries in use

| Reader | Query |
|---|---|
| log-router service, per project | `SELECT project_id, enabled, target_bucket FROM dataplane_config WHERE project_id = $1` |
| log-router `apply-lifecycle` admin tool | `SELECT project_id, target_bucket FROM dataplane_config WHERE enabled = true AND target_bucket != '' LIMIT 10001` |

`$1` is the event's `initiator.project_id`. Both SELECT lists are explicit, so adding columns does not break them.

## What each result means

| Result | log-router does |
|---|---|
| no row | Project is disabled. Not an error. Event is dropped and counted in `log_router_events_dropped_total{reason="tenant_not_enabled"}`. Result is not cached. |
| `enabled = false` | Same as no row, except the result is cached. |
| `enabled = true` | Event is written to account `AUTH_<project_id>`, container `target_bucket`. There is no admin copy. |
| `enabled = true`, `target_bucket = ''` | Same as above with container `hermes-audit`. No warning. |

hermez writes `hermes-audit` itself when a PUT has `enabled=true` and no bucket (#371), so the empty case only occurs for rows written before that change. log-router does not validate `target_bucket`; hermez validates it on PUT (`^[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]$`, no `--`).

The container is created by the project, not by log-router: when `enabled=true` is saved, hermez creates `target_bucket` in `AUTH_<project_id>` with the caller's project-scoped token (so the project owns it and its quota applies) and makes sure `X-Container-Read` holds `<project_id>:*` and `*:<writer keystone user id>` and `X-Container-Write` holds `*:<writer keystone user id>`. The writer needs read access to chain digests. log-router writes as that non-admin writer user. It must not create the container itself and must not use an admin identity for it. Unless `hermes.dataplane_writer_user_id` is set, the writer is the user of hermez's own `[keystone]` service token, so that user must be the one log-router logs in as.

When `hermes.dataplane_assign_writer_role` is on (off by default, turned on per region), hermez also makes sure the writer user has the role `hermes.dataplane_writer_role` (default `objectstore_admin`) in the project before it creates the container. It checks with its own service token, counting the role however the writer has it (directly, through a group, inherited from the domain or a parent project, or implied), and only if the role is missing assigns it with the caller's token. Where the role is assigned inherited on the domain, as the helm chart does on every domain except the `iaas-` domains, nothing is assigned per project. That lets log-router log in with a token scoped to `<project_id>` (`LOG_ROUTER_SWIFT_PROJECT_SCOPED`), so the project owns the objects it writes. Turn both on together: without the role, the scoped login answers 401. The writer holds the role in every project that enabled dataplane and in every project of a domain where it is assigned inherited; a role assigned during a failed enable stays, and disabling leaves it in place.

If the customer removes the writer from the ACLs, deletes the container, or the project is out of quota, the object store answers 403, 404 or 413 and log-router drops the partition (tenant DLQ first, if configured) and counts it in `log_router_events_dropped_total{reason}`. It is not retried and not copied anywhere else. 401, 5xx, timeouts and network errors are retried and the events kept.

## Caching and propagation

- In-memory cache per log-router process.
- TTL: `LOG_ROUTER_CACHE_TTL`, default `5m`. `0` turns caching off.
- At most 1000 entries; when full, the entry with the earliest expiry is evicted.
- Only rows that were found are cached. No-row results go to Postgres every time.
- On expiry the entry is deleted and the next lookup queries Postgres. There is no background refresh and no push from hermez.

So after a PUT or DELETE in hermez:

| Change | Seen by log-router |
|---|---|
| Project had no row, now enabled | next lookup |
| Project had a row, changed or deleted | when the cached entry expires, up to 5m |

## Failure behaviour

| Condition | Behaviour |
|---|---|
| Postgres unreachable at log-router start | 10 pings with backoff within 60s, then fatal exit. |
| Postgres error, entry cached | Cached entry used until it expires. |
| Postgres error, no cached entry, at ingest | Error returned, not treated as disabled. Counted in `log_router_ingest_errors_total{reason="config_lookup"}`. RabbitMQ message requeued; after `LOG_ROUTER_RABBITMQ_MAX_REDELIVERIES` (default 3) rejected to the DLX if configured. |
| Postgres error, no cached entry, at flush | Customer partition skipped and kept in the buffer for the next flush. Counted in `log_router_config_lookup_errors_total`. |
| Any Postgres error | Nothing is written to a project container unless a lookup confirmed `enabled = true`. |
| `LOG_ROUTER_DB_URL` unset | Not a failure mode but worth knowing: log-router uses a static config with every project enabled and metering off. Do not run it that way where customers expect their own buckets. |

## Not implemented

- Batch lookup `WHERE project_id = ANY($1) AND enabled = TRUE`. The service only does per-project lookups. The admin tool uses the different query listed above.

## Not verified from code

- Which Postgres cluster and database name the deployed log-router DSN uses. log-router's README example uses database `log-router`; the table lives in hermez's database (default `hermes`).
- How the `log-router` role is provisioned and in what order relative to hermez.
- Effective privileges of the `log-router` role beyond the SELECT grant.
- Any commitment on future schema changes. There are two migrations today; column removals or renames are a process question, not something the code enforces.

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

log-router consumes CADF audit events. Project owners had no way to get a copy of their own events into storage they control. (log-router used to write an admin copy of every event as well. That was removed; the project's bucket is the only destination now.)

The feature adds a per-project opt-in. When a project is enabled, log-router writes that project's events into a container in the project's own object-storage account.

## Decision

### 1. hermez stores the config, log-router reads it with SQL

- hermez owns the `dataplane_config` table, its migration, the REST API that writes it, and the CADF events for changes.
- log-router reads the table directly over Postgres. It has no HTTP client for hermez. Its only HTTP routes are `/metrics`, `/v1/signing-key` and a health check.
- No code in log-router writes `dataplane_config` (repo-wide search for INSERT/UPDATE/DELETE on it finds nothing; `internal/config/client.go:71` only runs SELECT). It does write `metering_records` over the same database connection (see section 6).

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
| PUT | same | 200 with saved row | 400, 401, 403, 409, 415, 500, 502, 503 |
| DELETE | same | 204, also when no row existed | 401, 403, 500 |

PUT rules:

- `Content-Type` must start with `application/json`, else 415.
- Body is capped at 64 KiB. Unknown JSON fields are rejected with 400. Accepted fields: `enabled`, `target_bucket`.
- A non-empty `target_bucket` must match `^[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]$` and must not contain `--`. This is checked even when `enabled=false`.
- `enabled=true` with no `target_bucket` stores `hermes-audit` (#371).
- `enabled=true` creates the bucket first, see "Bucket creation" below. Nothing is saved if that fails. `enabled=false` and DELETE never touch the bucket.
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
- Migrations are an inline Go map `DBMigrations` keyed by int64 version. There are two migrations: version 1 (`dataplane_config`) and version 2 (`metering_records` for log-router). No `.sql` files.
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

### Writer role

When `hermes.dataplane_assign_writer_role` (env `HERMES_DATAPLANE_ASSIGN_WRITER_ROLE`) is on, enabling first gives the writer user the role `hermes.dataplane_writer_role` (env `HERMES_DATAPLANE_WRITER_ROLE`, default `objectstore_admin`) in the project. With that role log-router can log in with a token scoped to the project, and RGW makes the project the owner of every object it writes. It is off by default. Each region turns it on (the helm chart renders it from a value) before log-router's `LOG_ROUTER_SWIFT_PROJECT_SCOPED`; projects in `iaas-` domains enabled before then need one re-enable in between (by an admin of the project, with its current config including `target_bucket`) or a cloud admin's inherited grant on their domain, and the chart refuses log-router's flag without this one. When it is off, hermez does not talk to Keystone on enable and log-router relies on the ACL grants below.

1. The role ID is `hermes.dataplane_writer_role_id` (env `HERMES_DATAPLANE_WRITER_ROLE_ID`) if set. Otherwise Hermes looks it up by name with its own service token at startup and logs it. If that lookup fails, each enable tries again until one works and is kept; until then enabling answers 503.
2. The same checks as for the bucket come first: a token scoped to exactly `{project_id}` (else 409) and a writer user ID (else 503).
3. Hermes checks with its own service token, read only: `GET /v3/role_assignments?effective=true&role.id=<role id>&scope.project.id=<project_id>&user.id=<writer user id>`. The service user has the `service` role, which may read role assignments. `effective=true` counts the role however the writer has it: assigned directly, through a group, inherited from the project's domain or a parent project, or implied by another role. If the list is not empty the role is there, whoever assigned it, and nothing is assigned. In regions with log-router, the Hermes helm chart assigns `objectstore_admin` to the writer as an inherited role on every domain except the `iaas-` domains, so projects in those domains already have it; per-project assignments only happen in `iaas-` domains. If the check itself fails, enabling answers 503 when Keystone refuses Hermes' token (401, 403) and 502 otherwise. There is no assignment attempt after a failed check.
4. If the role is missing, hermez assigns it with the caller's own token: `PUT /v3/projects/<project_id>/users/<writer user id>/roles/<role id>`. It uses the public `identity` endpoint of the caller's catalog, and only when that is the same Keystone v3 endpoint as in Hermes' own service catalog, otherwise 502. Redirects are not followed. Hermes' service token never assigns roles.
5. A 403 on the `PUT` answers 403: the caller's token may not assign roles. The message says that a project admin (role `admin` or `role_admin` in the project) must run `openstack role add --user <writer user id> --project <project_id> <role name>` or enable dataplane themselves; after that, enabling works with the caller's token too, because step 3 then finds the role. A 401 answers 401 (token rejected, get a new one). A 404 means the writer user or the role does not exist in Keystone and answers 503, logged with both IDs. Anything else, timeouts and network errors answer 502. Nothing is created or saved on any of these.
6. A role assigned in step 4 stays even when the bucket steps below fail or the config cannot be saved. Hermes never removes it; the next enable finds it in step 3.

The Keystone requests of one enable share a 30 second deadline. The writer user holds the role in every project where it is effective: every project of a domain where it is assigned inherited, and every project where dataplane was enabled. Disabling leaves the role in place.

### Bucket creation

The project must own the bucket, so that quota and billing follow the project. On `enabled=true` hermez does this with the caller's own `X-Auth-Token`, passed through unchanged:

1. The token must be scoped to exactly `{project_id}`. A cloud admin scoped elsewhere, or a domain-scoped token, gets 409 and nothing is created or saved. (Disabling and DELETE still work cross-project for cloud admins.)
2. The public endpoint of the catalog service type `hermes.dataplane_swift_service_type` (env `HERMES_DATAPLANE_SWIFT_SERVICE_TYPE`, default `object-store-ceph`) is used. If the catalog has none, enabling fails with 503. If its path does not end in `AUTH_<project_id>`, hermez refuses to send the token there (502). Redirects are not followed. All object store requests of one enable share a 30 second deadline.
3. `HEAD` the container. If it is missing, `PUT` it. Then set `X-Container-Read` to the existing entries plus `<project_id>:*` and `*:<writer user id>`, and `X-Container-Write` to the existing entries plus `*:<writer user id>`. The writer needs read access too: log-router reads the previous digest to chain the next one, and without read access RGW answers 403 even for a missing object, so every flush would fail. The writer is the Keystone user in `hermes.dataplane_writer_user_id` (env `HERMES_DATAPLANE_WRITER_USER_ID`). If it is unset, it defaults to the user of Hermes' own Keystone service token (the `[keystone]` user, which must be the user log-router logs in as). If neither is available, enabling fails with 503.
4. The `POST` is only sent when an entry is missing. The lists are compared as sets, because RGW returns the entries in its own order. Skipping it matters for buckets that another project (the old admin writer) still owns: the operator migration already added the grants there, and a POST from the customer would fail with 403. If the grants are complete and the probe is then denied with 403, the bucket is on the operator account waiting for the ownership handover (the container is in the project's own account, so only the project or an admin can own it). hermez accepts and saves the config; the quota is not the project's until the chown.
5. The ACL is set with the same customer token: a container POST rebuilds the ACL with the requester as owner, so an admin token would hand ownership to the wrong project. Ceph RGW ignores `X-Remove-Container-Write`, so full header values are sent.
6. Creating a container does not check byte quota, writing an object does. So hermez writes a 1-byte object `.hermes-enable-probe` with the customer token and deletes it again (a 404 on the delete is fine).
7. Errors, nothing is saved on any of them:

   | Object store answer | hermez answers |
   |---|---|
   | 401 (token without an accepted role) | 403 "your token needs the objectstore_admin role in this project" |
   | 403 on the probe, for a container that already existed and whose ACLs were already complete (migrated, not handed over yet) | accepted and saved; logged at info |
   | 403 on the ACL POST or the probe, for a container that already existed (otherwise) | 409 "container <name> exists but is owned by another project; ask the operator to migrate it or choose another target_bucket" |
   | any other 403 | 403 "object storage denied the request" |
   | 400 on container PUT (too many buckets) | 409 "this project cannot create more containers" |
   | 409 | 409 "container name is not available" |
   | 413 on the probe | 409 "project has no free object storage quota" |
   | no endpoint in the catalog | 503 "object storage for dataplane is not available in this region" |
   | anything else, timeouts, network errors | 502 |

The caller needs the `objectstore_admin` role in the project; RGW rejects tokens without it. With `hermes.dataplane_assign_writer_role` on, the caller must also be allowed to assign roles in the project (role `admin` or `role_admin`), unless the writer already has the role. There is no Object Lock or versioning on the bucket.

### 6. How log-router uses the config

Postgres mode is on only when `LOG_ROUTER_DB_URL` is non-empty. Per event:

1. Config is looked up with `SELECT project_id, enabled, target_bucket FROM dataplane_config WHERE project_id = $1`. The tenant is the event's `initiator.project_id`.
2. No row or `enabled=false`: the event is dropped and counted in `log_router_events_dropped_total{reason="tenant_not_enabled"}`. No log line.
3. `enabled=true`: a per-project rate limit applies (10,000 events/s, hard-coded, not settable from hermez or env), then the event goes on to processing and is buffered for the project's bucket.

Mapping from the row: `enabled` becomes `DataPlaneEnabled`, `target_bucket` becomes the container (empty becomes `hermes-audit`), and the storage account is `AUTH_<project_id>`. Nothing else is read. Sinks, CEL filters, grace time, retention and OpenSearch delivery are not fed from this table.

Where objects land:

| Account | Container | Key |
|---|---|---|
| `AUTH_<project_id>` | `target_bucket` (default `hermes-audit`) | `default/YYYY/MM/DD/HH:00_HH:59/` |

Object names under the prefix are `S{n}.json`, late-arrival shards `A{shard}_{n}.json`, plus manifest and digest objects. `LOG_ROUTER_S3_PREFIX`, if set, is prepended. log-router does not create the container; hermez does on enable (see above). If the customer removes the writer from the read or write ACL, deletes the bucket or runs out of quota, the partition is dropped (tenant DLQ first, if configured) and counted in `log_router_events_dropped_total{reason}`.

Cache: in-memory per process, TTL from `LOG_ROUTER_CACHE_TTL` (default 5m, `0` disables), max 1000 entries. Only lookups that found a row are cached. Projects with no row hit Postgres on every lookup.

Metering: after each successful flush, log-router upserts a row in `metering_records` on the same `*sql.DB` it uses for config. A metering failure does not fail the flush.

### 7. Database roles

- hermez connects as `HERMES_PG_USERNAME` (default `hermes`), creates the table, and grants SELECT to the login role `log-router`. There is no intermediate NOLOGIN role; the migration does not run `CREATE ROLE`.
- The `log-router` role must exist before hermez runs migrations 1 and 2, otherwise the GRANTs fail.
- log-router gets its credentials only from the DSN in `LOG_ROUTER_DB_URL`.
- log-router's binary runs no migrations. hermez migration 2 creates `metering_records` (copied from log-router `migrations/004_create_metering_records.up.sql`) and grants `log-router` `SELECT, INSERT, UPDATE` on it plus `USAGE` on its id sequence.

## Failure behaviour

| Situation | What happens |
|---|---|
| hermez API down | No effect on log-router. It does not call hermez. |
| Postgres down when log-router starts | 10 pings with backoff inside 60s, then fatal exit. |
| Postgres down while running, entry cached | Cached config keeps being used until its 5m TTL runs out. |
| Postgres down while running, no cached entry | Lookup returns an error. It is not treated as "disabled". On ingest: counted in `log_router_ingest_errors_total{reason="config_lookup"}` and the RabbitMQ message is requeued; after `LOG_ROUTER_RABBITMQ_MAX_REDELIVERIES` (default 3) it is rejected to the DLX if one is configured. On flush: the customer partition is skipped and stays buffered. |
| Postgres down when hermez starts | `NewPostgres` fails and `must.Return` exits the process. |
| Postgres down while hermez runs | GET/PUT/DELETE return an obfuscated 500. |

## Rollback and off switches

| Component | What you can do | Effect |
|---|---|---|
| One project | `PUT {"enabled":false}` or `DELETE` | Delivery stops once log-router's cache entry for the project expires (up to 5m). |
| hermez dataplane-config API | No off switch. `routing_store_driver` takes `postgres` or `mock` only; `""` is fatal (`main.go:158-159`). `mock` is in-memory and loses all rows on restart. | Not a usable rollback without a code change. |
| log-router DB read | Remove `LOG_ROUTER_DB_URL` | log-router switches to a static config with `DataPlaneEnabled: true` for every project and metering off. This is not a safe rollback; the admin copy it used to fall back on no longer exists. |

## Consequences

- One API and one auth model for audit reads and routing config.
- log-router does not depend on the hermez API being up. It does depend on Postgres being up at startup.
- A Postgres outage delays or dead-letters events for projects without a cached entry.
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

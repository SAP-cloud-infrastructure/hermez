<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-001: Dataplane audit-event routing — hermez owns the config plane

- **Status:** Accepted (shipped 2026-06-25, hermez PR #352 · log-router PR #24 · helm-charts PR #12097)
- **Date:** 2026-06-29
- **Author:** Nathan Oyler
- **Supersedes:** ADR-001 through ADR-007 (draft series, 2026-05-19 → 2026-06-25)

---

## Problem

OpenStack audit events flow through RabbitMQ into log-router, which writes them
to a shared admin S3/Swift bucket (`ccadmin/master`). There was no way for
project owners to receive a copy of their own audit events in a bucket they
control.

The solution is per-project opt-in routing: a project owner enables the feature
and names a target bucket; log-router then writes events to both the admin path
and the project's own bucket.

This ADR records every decision made to ship that feature, including who owns
what, the exact data model, the API surface, the authorization model, how
log-router reads the config, the postgres access model, and how it deploys.

---

## Architecture

```
Ceph radosgw / Nova / Neutron / ...
    │ CADF events (oslo.messaging)
    ▼
RabbitMQ  ──────────────────────────────────────────────────────────────────
    │ dataplane.audit queue                                                 │
    ▼                                                                       │ notifications.info queue
log-router (StatefulSet, 2 replicas)                               logstash (3 pods)
    │   reads dataplane_config from hermez postgres (SELECT only)          │
    │   writes to Swift/Ceph RGW via Keystone token (OS_* env vars)        │
    │                                                                       ▼
    ├──→ hermes-audit / events/_admin/_Default/.../ANN_0.json    OpenSearch (hermes index)
    └──→ <target_bucket> / events/<project>/.../ANN_0.json               │
         (when enabled=true for that project)                              ▼
                                                                    hermez API (/v1/events)
```

Config plane:
```
operator / project owner
    │ PUT /v1/projects/{project_id}/dataplane-config  (audit_admin role required)
    ▼
hermez-api  ──→  hermez postgres  (dataplane_config table)
                      │
                      └──→  log-router reads on each routing decision (SELECT, TTL-cached)
```

---

## Decision 1: Hermez owns the config plane

Hermez is the source of truth for per-project routing config. The schema,
migrations, CRUD API, and CADF audit events for config changes all live in
this repository.

Log-router is a **read-only consumer**. It never writes to the hermez database.

**Why hermez, not log-router:**
- Log-router is a high-throughput writer optimized for data-plane throughput.
  Bundling a tenant CRUD API in a writer creates two release cadences in one binary.
- Hermez is already the tenant-facing API for audit reads. Config is a
  write-side companion to the same domain — one URL, one auth model, one doc.
- Limes precedent: in SAP CC, the service that serves the read API owns the
  config for what gets read. Hermez plays the same role for audit routing that
  Limes plays for quota.

---

## Decision 2: Data model — one table, five columns

```sql
CREATE TABLE dataplane_config (
    project_id    VARCHAR(64)  PRIMARY KEY,
    enabled       BOOLEAN      NOT NULL DEFAULT FALSE,
    target_bucket TEXT         NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_by    VARCHAR(64)  NOT NULL DEFAULT ''
);
```

- `project_id` — Keystone project UUID from the URL path (never the token body)
- `enabled` — kill switch; `false` = route to admin path only
- `target_bucket` — Ceph/S3 bucket name; required when `enabled=true`; validated at write time
- `updated_at` — server-stamped on every PUT; client value ignored
- `updated_by` — Keystone user UUID from the token at write time

**What was deliberately omitted from go-live:**
- Rate limits, retention days, grace minutes — deferred; no enforcement existed
- CEL filter expressions — deferred; no evaluator in log-router yet
- Multiple sinks per project — deferred; bucket fan-out is a future ADR
- Config change history table — CADF events on PUT/DELETE are the audit trail
- `container_format` field — hardcoded to `"hermes"` server-side; Ceph dislikes
  underscores in account names, so `_Default` was replaced; eliminating the
  field removes a decision surface from customers

**Storage backend: PostgreSQL via easypg, not OpenSearch**

Config writes are transactional; OpenSearch's 1-second refresh lag means a
PUT followed immediately by GET could return stale state. The Helm chart already
provisions a `postgresql-ng` instance. Config is a relational concern, not a
search concern.

Library stack: `database/sql` + `lib/pq` + `go-bits/easypg` (connection +
golang-migrate-based migrations with `pg_advisory_lock` serialization).

Env vars consumed by hermez at startup:
```
HERMES_PG_HOSTNAME        (default: localhost)
HERMES_PG_PORT            (default: 5432)
HERMES_PG_USERNAME        (default: hermes)
HERMES_PG_PASSWORD
HERMES_PG_DBNAME          (default: hermes)
HERMES_PG_CONNECTION_OPTIONS
```

Config key in `hermes.conf`:
```toml
[hermes]
routing_store_driver = "postgres"   # or "mock" for tests
```

---

## Decision 3: API surface — three endpoints

All under `/v1/projects/{project_id}/dataplane-config`.

| Method | Status codes | Description |
|--------|-------------|-------------|
| GET    | 200 | Returns config if it exists; returns `{enabled:false}` default if not. Never 404. |
| PUT    | 200, 400, 403, 415 | Idempotent create-or-replace. Strict JSON (unknown fields → 400). |
| DELETE | 204, 403 | Idempotent remove. Deleting non-existent config → 204. |

**PUT validation:**
- `Content-Type: application/json` required (else 415)
- Body capped at 64 KiB
- `target_bucket` required when `enabled=true`
- Bucket name must match `^[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]$` (RFC-1123 subset, S3 rules)
- No consecutive hyphens (`--`) in bucket name
- Unknown JSON fields rejected

**Package layout:**
```
pkg/routing/
  interface.go   — Store interface + ErrNotFound
  types.go       — DataplaneConfig struct + DefaultDataplaneConfig()
  postgres.go    — Postgres implementation + embedded DBMigrations map
  mock.go        — In-memory mock for tests
pkg/api/
  dataplane_config.go       — GET/PUT/DELETE handlers
  dataplane_config_test.go  — handler-level tests
```

---

## Decision 4: Authorization

Policy rule:
```json
"project_admin":             "rule:project_scope and role:audit_admin",
"dataplane_config:manage":   "rule:project_admin"
```

- The caller must have the `audit_admin` Keystone role on the **project matching the URL path**.
- The token's `project_id` must equal the path `project_id`. Cross-project access → 403. This is enforced in the handler (`authDataplaneConfig`), not the policy file.
- The `audit_admin` role is seeded by the hermes Helm chart's `keystone-seed.yaml` when `logRouter.enabled: true`. Operators assign it to project users who should be able to toggle routing.

---

## Decision 5: Log-router reads hermez postgres directly (no HTTP)

Log-router reads `dataplane_config` with a single parameterized SELECT.
It does NOT call the hermez REST API.

**Why direct SQL, not HTTP:**

| Alternative | Reason rejected |
|-------------|----------------|
| HTTP GET `/v1/projects/:id/dataplane-config` | Hermez-api must be healthy for log-router to route. Creates a circular dependency: log-router fails if hermez is down, even though postgres is fine. |
| RabbitMQ control-plane topic | New infra dep for a problem TTL already handles |
| Postgres LISTEN/NOTIFY | Optimization; deferred. Revisit if 5-min propagation is user-visible. |

Direct SQL: log-router connects to the same postgres database as hermez using
a separate `log-router` user with SELECT-only access. If postgres is down, both
services fail together — which is honest. If hermez-api is down, log-router
continues routing normally.

**Read query:**
```sql
SELECT project_id, enabled, target_bucket
FROM dataplane_config
WHERE project_id = $1;
```

Interpretation:
- Zero rows → treat as disabled. Not an error. Route to admin path only.
- Row with `enabled=false` → same. Route to admin path only.
- Row with `enabled=true` → route to both admin path and `target_bucket`.

The ccadmin/master admin path is unconditional — it does not consult this config
and is never affected by a postgres outage.

**Caching:**

Log-router caches per-project results with a configurable TTL:
```
LOG_ROUTER_CACHE_TTL   (default: 5m)
```

Lazily populated on first `GetConfig` call per tenant. Stale entries re-query
on next access after TTL. Set to `30s` in qa-de-1 for faster validation.

**Failure modes:**

| Condition | Log-router behavior |
|-----------|---------------------|
| Postgres unreachable at startup | Exit. Helm liveness probe restarts. Postgres deploys before hermez/log-router (helm init ordering). |
| Postgres unreachable mid-flight | Serve from cache until TTL expires; log errors. Admin path unaffected. |
| `target_bucket` empty on `enabled=true` row | Should never happen (validated at write time). Treat as disabled; log warning with project_id. |
| Log-router tries to write (impossible) | `permission denied`. Defense-in-depth. |

**Full contract:** `docs/dataplane-config-read-contract.md`

---

## Decision 6: Postgres access model — one database, two roles

**One postgres-ng cluster. One database (`hermes`). Two postgres login roles.**

| Role | Owns | Access to other tables |
|------|------|------------------------|
| `hermes` | `dataplane_config` | — |
| `log-router` | `metering_records` | SELECT on `dataplane_config` (granted by hermez migration 001) |

Hermez migration `001_create_dataplane_config.up.sql`:
```sql
CREATE TABLE IF NOT EXISTS dataplane_config ( ... );

GRANT SELECT ON dataplane_config TO "log-router";
```

The grant is **direct** to the `log-router` login role — no NOLOGIN intermediary
role. The `hermes` user owns the table and can issue the grant without
`CREATEROLE`. The `log-router` role must be provisioned by the helm chart's
postgres-ng seed before hermez starts.

**Why one database, not two:**
- Postgres cross-database queries are expensive; though not needed today, keeping
  routing and metering in one DB preserves the option.
- One `pg_dump`, one backup policy, one connection-pool budget.
- Table-level GRANTs enforce the same access boundary as separate databases would,
  with less operational overhead.

**Metering stays in log-router:**
`metering_records` is hot-write on every flush (one upsert per batch of events).
Routing that write through hermez adds latency to the critical path. Log-router
keeps its metering writer; the table lives in the same database but is owned by
the `log-router` role. Hermez has no grants on `metering_records`.

---

## Decision 7: Deploy model — single coordinated deploy

The original design planned four independent phases (postgres first, hermez code
second, log-router third, house-cleaning fourth). In practice all three changes
deployed together into qa-de-1:

| PR | Repo | What it did |
|----|------|-------------|
| #352 | hermez | Dataplane-config API + postgres migration + CADF audit events |
| #24 | log-router | Swift/Ceph RGW storage backend + hermez postgres config read |
| #12097 | helm-charts | postgres-ng, log-router StatefulSet, HERMES_PG_* env, policy rule fix |

The phased approach guided design (postgres validated before consumers, each
change independently reversible) but execution collapsed into one coordinated
push because qa-de-1 was the first region and there was no production traffic to
protect.

**Schema migration safety:**

`easypg.Connect` acquires `pg_advisory_lock` before running migrations.
Multiple hermez replicas starting simultaneously: the first acquires the lock
and runs migration 001; the others wait, see the schema already at version 1,
and proceed. Migration IDs are keyed on the filename string in `DBMigrations`.

**Migration rule: additive only.** No DROP, RENAME, or type change on existing
columns. Log-router's SELECT list is explicit (`project_id, enabled, target_bucket`)
so new columns added by future hermez migrations are invisible and non-breaking.
Non-additive changes ship in two steps: add new column → log-router learns it →
remove old column (two separate PRs, two separate deploys).

**Region order for remaining regions:**

| Step | Region | Soak |
|------|--------|------|
| ✓ | qa-de-1 | validated |
| 2 | eu-de-1 | 24h |
| 3 | eu-de-2 | 12h |
| 4 | na-us-1 | 12h |
| 5 | ap-jp-2 | 12h |
| 6 | All remaining | parallel after #5 |

**Rollback:**

| What broke | Rollback action |
|------------|-----------------|
| Hermez dataplane-config API | Set `routing_store_driver = ""` in config; hermez skips postgres on restart |
| Log-router routing decisions | Remove `LOG_ROUTER_DB_URL`; log-router falls back to static config (all events to admin path only) |
| Postgres schema | Data preserved; PVC retained on pod removal (`resource-policy: keep`) |

---

## Consequences

### Positive

- Hermez is now a complete tenant-facing API for audit: reads (OpenSearch) and
  routing config (postgres). One URL prefix, one auth model.
- Log-router has no HTTP dependency on hermez for routing decisions. A hermez-api
  outage does not affect the data plane.
- The `audit_admin` Keystone role and policy rules live with the API that enforces
  them — no split-brain.
- CADF events for every PUT/DELETE provide a tamper-evident config change log
  accessible via the existing `GET /v1/events` API.

### Negative / Accepted risk

- Hermez gained state. Multiple hermez replicas share one postgres. Write
  serialization is handled by postgres; no leader election in hermez.
- Hermez now has an operational postgres surface: backups, migrations, connection
  monitoring. Mitigation: `postgresql-ng` + pgbackup + pgmetrics trio per SAP CC
  house pattern.
- Log-router has a hard dependency on hermez-postgres. This is the same class of
  dependency it already has for metering writes.

### Deferred (future ADR)

- CEL filter expressions on events per project
- Multiple sinks per project
- Rate limits and retention policies
- Bulk admin endpoint (`GET /v1/dataplane-configs` for operators)
- Auto-disable after inactivity TTL
- `docs/operators/db-roles.md` — formal access matrix for operators
- `sqlstats` Prometheus collector for hermez postgres connection metrics

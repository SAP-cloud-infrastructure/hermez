<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-007: Dataplane routing config API — go-live scope

- **Status:** Accepted (shipped 2026-06-25 in hermez PR #352)
- **Date:** 2026-06-25
- **Author:** Nathan Oyler
- **Supersedes:** References in ADR-002 and ADR-003 to a "Sink" resource; those ADRs are now superseded by this document for the go-live feature set
- **Related:** ADR-001 (stateful boundary), ADR-004 (inter-service contract), log-router `docs/dataplane-config-read-contract.md`

---

## Context

We have a Tuesday go-live deadline to ship the dataplane audit event routing feature. The full "Sink" CRUD surface described in draft notes (multiple sinks, CEL filters, rate limits, retention) is correct long-term but is out of scope for go-live. This ADR records the minimal implementation that ships and locks in the decisions made to get there.

The architecture at go-live:

```
Ceph radosgw  →  RabbitMQ  →  log-router  ─→  ccadmin/master bucket (always)
                                            └→  project bucket         (when enabled=true)
```

Hermez owns the config plane. Log-router is a read-only consumer.

---

## Decision

### 1. Data model: one table, one row per project

```sql
CREATE TABLE dataplane_config (
    project_id    VARCHAR(64) PRIMARY KEY,
    enabled       BOOLEAN     NOT NULL DEFAULT FALSE,
    target_bucket TEXT        NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by    VARCHAR(64) NOT NULL DEFAULT ''
);
```

- **`project_id`** — Keystone project UUID, from the path variable (not the token body)
- **`enabled`** — kill switch; FALSE = route only to admin path
- **`target_bucket`** — customer's S3/Ceph bucket name; required when `enabled=true`, empty otherwise
- **`updated_at`** — server-stamped; never trusted from the client
- **`updated_by`** — user UUID from the Keystone token at write time

**`container_format` is NOT stored or exposed.** It will always be `"hermes"` server-side. The discussion about `_Default` vs `hermes` (Ceph disliking underscores) was resolved by hardcoding `"hermes"` and eliminating the field entirely from the API surface. This removes a decision surface from customers and avoids the transitional tech debt.

### 2. Storage backend: PostgreSQL via easypg

PostgreSQL was chosen over OpenSearch despite Hermez's existing OpenSearch client. The reasons:

1. **Consistency semantics** — Config writes are transactional. OpenSearch's 1-second refresh lag means a customer who PUTs and immediately polls might see stale state.
2. **ADR precedent** — ADR-001 through ADR-004 all assumed postgres. The Helm chart already provisions a `postgresql-ng` instance in the hermes deployment.
3. **Operational simplicity** — A config table is a relational concern, not a search concern.

Library stack: `database/sql` + `lib/pq` (driver) + `easypg` (connection + golang-migrate based migrations).

Env vars consumed by hermez at startup:
- `HERMES_PG_HOSTNAME` (default: `localhost`)
- `HERMES_PG_PORT` (default: `5432`)
- `HERMES_PG_USERNAME` (default: `hermes`)
- `HERMES_PG_PASSWORD`
- `HERMES_PG_DBNAME` (default: `hermes`)
- `HERMES_PG_CONNECTION_OPTIONS`

Config key added to `hermes.conf`:
```toml
[hermes]
routing_store_driver = "postgres"  # or "mock" for tests
```

### 3. API surface

Three endpoints, all under `/v1/projects/{project_id}/`:

| Method | Path | Status codes |
|--------|------|--------------|
| GET    | `/v1/projects/{project_id}/dataplane-config` | 200 |
| PUT    | `/v1/projects/{project_id}/dataplane-config` | 200, 400, 403, 415 |
| DELETE | `/v1/projects/{project_id}/dataplane-config` | 204, 403 |

**GET** returns the config if it exists, or the default `{enabled: false}` document if not. Never returns 404.

**PUT** is idempotent create-or-replace. Strict JSON decoding (unknown fields → 400). Validation:
- `target_bucket` required when `enabled=true`
- Bucket name must match `^[a-z0-9][a-z0-9\-]{1,61}[a-z0-9]$` (RFC-1123 subset, S3 rules)
- `Content-Type: application/json` required
- Body capped at 64 KiB

**DELETE** is idempotent. Deleting non-existent config → 204, not 404.

### 4. Authorization

Policy rule: `"dataplane_config:manage": "rule:project_admin"`

Where `project_admin` is defined as:
```json
"project_admin": "rule:project_scope and role:audit_admin"
```

The `audit_admin` role is seeded by the hermes Helm chart's `keystone-seed.yaml` when `logRouter.enabled: true`. Cloud operators assign this role to project users who should be able to toggle dataplane routing.

**Cross-project access is rejected with 403.** The path `project_id` must match the token's `project_id` in `Context.Auth`. This is enforced in the handler, not in the policy file.

### 5. Log-router read contract

Log-router reads the `dataplane_config` table directly using the `log-router`
PostgreSQL login role. Hermez's migration 001 grants SELECT to this role directly:

```sql
GRANT SELECT ON dataplane_config TO "log-router";
```

The `log-router` login role is provisioned by the helm chart's postgres-ng seed.
Hermez does not need `CREATEROLE` to issue this grant — it only requires
ownership of the `dataplane_config` table, which the `hermes` migration user has.

**Note:** the original design proposed a `log_router_reader` NOLOGIN intermediary
role (GRANT role → GRANT role-membership → login role). This was simplified: a
direct GRANT to the login role achieves identical permissions with less ceremony.

Log-router does NOT run any migrations against the hermez database. The `hermes`
migration runner owns the schema. Log-router's `LOG_ROUTER_DB_URL` points at the
same database, but the `log-router` user has no CREATE privilege and cannot alter
any hermez-owned tables.

The full read contract is documented in `docs/dataplane-config-read-contract.md`.

### 6. CORS

The `server.go` CORS configuration now includes `PUT` and `DELETE` in `AllowedMethods`. Previously only `GET` and `HEAD` were allowed; the dashboard (Elektra) needs write access.

### 7. Package structure

```
pkg/routing/
  interface.go   — Store interface + ErrNotFound
  types.go       — DataplaneConfig struct + DefaultDataplaneConfig()
  postgres.go    — Postgres implementation + DBMigrations
  mock.go        — In-memory mock for tests
pkg/api/
  dataplane_config.go       — GET/PUT/DELETE handlers
  dataplane_config_test.go  — 9 handler-level tests
```

---

## Consequences

### Positive

- Ships by Tuesday with minimal blast radius on existing event-query code
- Customers get a clear on/off toggle with meaningful error messages
- Log-router has a simple SELECT contract with no HTTP dependency on hermez
- Policy enforcement is consistent with existing hermez conventions

### Negative / Accepted risk

- No history of config changes (who toggled what when). Acceptable for go-live; the CADF event for the PUT/DELETE itself is the audit trail.
- No multi-sink support at go-live. Customers who want multiple destinations must wait for a future ADR.
- The `GRANT SELECT ON dataplane_config TO "log-router"` in migration 001 requires the `log-router` login role to already exist. This role is provisioned by the helm chart's postgres-ng seed, which runs before the hermez pod starts. If the chart deploy sequence is wrong, hermez logs a warning on migration startup (GRANT to non-existent role) but continues; log-router will receive `permission denied` on first SELECT, fail closed, and treat all projects as disabled until the role is seeded and hermez restarts. That is the correct safe default.

---

## Deferred (future ADR)

- CEL filter expressions on events
- Multiple sinks per project
- Rate limits and retention policies
- Bulk admin endpoint (`GET /v1/dataplane-configs` for operators)
- Auto-disable after TTL

<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Data-Plane Events Toggle

This page documents the per-project toggle that controls whether data-plane
events for a project are delivered to the project's immutable Ceph compliance
bucket. The bucket itself is operator-managed and is **not** customer-supplied.
The customer surface is a single boolean per project.

## API surface

Two endpoints, both authenticated via Keystone and authorized via
`policy.json`:

| Method | Path                                            | Policy rule                  |
|--------|-------------------------------------------------|------------------------------|
| `GET`  | `/v1/projects/{project_id}/data-plane-events`   | `data_plane_events:show`     |
| `PATCH`| `/v1/projects/{project_id}/data-plane-events`   | `data_plane_events:update`   |

The URL `project_id` is the source of truth. A token whose
`project_id` does not match the URL is rejected with `403 Forbidden`;
cross-tenant administration is out of scope for this API.

### `GET` — read current state

```bash
curl -H "X-Auth-Token: $TOKEN" \
  https://hermes.example.com/v1/projects/$PROJECT_ID/data-plane-events
```

```json
{"enabled": false}
```

A project that has never been configured returns `{"enabled": false}`.

### `PATCH` — toggle state

```bash
curl -X PATCH \
  -H "X-Auth-Token: $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}' \
  https://hermes.example.com/v1/projects/$PROJECT_ID/data-plane-events
```

```json
{"enabled": true}
```

Request constraints:

* `Content-Type: application/json` is required (`415` otherwise).
* Body is capped at 64 KiB (`413` otherwise).
* Unknown fields are rejected (`400`).
* `enabled` is required and must be a boolean.

### No-op semantics

`PATCH` is a strict no-op when the requested state already matches the
stored state. In that case Hermes does **not** write to Postgres and does
**not** emit a CADF audit event. The response still reflects the current
state.

Because "no row in the table" and "row with `enabled=false`" are observably
indistinguishable through `GET`, `PATCH {"enabled": false}` against a project
that has no row is also a no-op.

## Storage

The toggle is persisted in PostgreSQL. The schema is intentionally minimal:

```sql
CREATE TABLE data_plane_events (
    project_id VARCHAR(36) PRIMARY KEY,
    enabled    BOOLEAN     NOT NULL
);
```

The `VARCHAR(36)` width matches the Archer convention and accommodates a
UUID with hyphens. Project IDs in this table never include dashes are
restricted to Keystone-style identifiers; the handler enforces a tighter
`[A-Za-z0-9_-]{1,36}` regex on the URL.

### Postgres dependency (hard-coupled)

Hermes hard-depends on PostgreSQL at startup. If Postgres is unreachable,
the binary fails to start and the supervised process (Kubernetes
container, systemd unit, etc.) will CrashLoop. **This affects ALL Hermes
endpoints, including `/v1/events`** — the data-plane-events feature shares
the same process and the OpenSearch-backed read paths cannot serve
traffic until the Postgres connection succeeds.

A lazy-connect mode that allows the read path to remain available during
a Postgres outage is a planned Phase-2 follow-up; v1 prioritises a single
audited write path.

### Postgres role for the log router

The log-router component (a separate service that consumes this flag to
decide whether to ship a project's data-plane events to its compliance
bucket) reads this table directly. Hermes' migration creates a dedicated,
login-less **group role**, scrubs any prior privileges, and grants the
minimum needed:

```sql
CREATE ROLE log_router_reader NOLOGIN;
REVOKE ALL ON data_plane_events FROM log_router_reader;
GRANT SELECT ON data_plane_events TO log_router_reader;
GRANT CONNECT ON DATABASE hermes TO log_router_reader;
GRANT USAGE  ON SCHEMA public    TO log_router_reader;
```

**`NOLOGIN` does not waive authentication.** `log_router_reader` is a
group role: nobody can connect *as* `log_router_reader` directly,
because Postgres rejects login attempts against `NOLOGIN` roles. The
log-router connects with a **separate login user** (provisioned by the
helm chart, with its own password) that has been granted membership in
this group:

```sql
-- provisioned out of band by the helm chart, not by Hermes:
CREATE ROLE log_router_app_user LOGIN PASSWORD '...';
GRANT log_router_reader TO log_router_app_user;
```

The login user authenticates with credentials and *inherits* the
SELECT/CONNECT/USAGE grants from `log_router_reader`. This split is
deliberate: Hermes' migration declares the privilege set declaratively
(easy to audit, easy to re-run), while credential management stays with
the helm chart and the operator. Rotating the log-router's password
does not require re-running the Hermes migration; revoking the
log-router's access entirely is a single `REVOKE log_router_reader FROM
log_router_app_user`.

The role is created with an idempotent `DO` block so the migration can be
re-run safely. The `REVOKE ALL` precedes `GRANT SELECT` so any privilege
granted out of band by an operator is scrubbed back to the intended
minimum. The down migration revokes the grants but does **not** drop the
role, since other login users may already inherit from it.

The database name in `GRANT CONNECT ON DATABASE hermes` is hardcoded.
If you deploy Hermes against a database with a different name, edit
`pkg/data_plane_events/migrations.go` before applying.

### Migrations are up-only at runtime

Hermes embeds the migrations via `easypg.Connect`, which **only runs Up
migrations on startup**. The Down migration in `001_initial.down.sql`
exists for manual rollback through the `migrate` CLI:

```bash
migrate -path pkg/data_plane_events -database "$DB_URL" down 1
```

Do not rely on Hermes to revert a schema change — it never will.

### "Immutable" defined

The compliance bucket that receives data-plane events is configured for
WORM (write-once-read-many) via S3 Object-Lock retention managed
out-of-band by the bucket-management tier. Hermes only owns the toggle;
it does not provision the bucket, configure retention, or delete
already-delivered events. Disabling the toggle prevents *future*
deliveries; it does not undo prior writes to the bucket.

### Activation latency

The log-router consumes this table on a periodic refresh, so toggle
changes do not take effect instantly. Operators should communicate to
customers that changes typically take effect within a few minutes,
bounded by the log-router's refresh interval (operator-tunable; see the
log-router runbook for the configured value in your region).

## Project deletion

Project deletion in Keystone is **not** synced to this table. If a
project is deleted, its row remains; if its UUID is later reused (rare
but possible), the new project would inherit the previous toggle state.
A reaper job that prunes orphan rows is a planned Phase-2 follow-up.

## Configuration

### `hermes.conf`

```toml
[postgres]
hostname = "localhost"
port     = "5432"
username = "hermes"
database = "hermes"
# password may also be supplied via HERMES_DB_PASSWORD
```

The Postgres password may be set via the `HERMES_DB_PASSWORD` environment
variable, which takes precedence over `postgres.password` in the config
file.

### RabbitMQ — required for audit emission

When `HERMES_RABBITMQ_QUEUE_NAME` is set, Hermes emits a CADF audit event
to RabbitMQ on every state-changing `PATCH`. The connection is configured
**only** via environment variables:

| Variable                     | Default     | Notes                              |
|------------------------------|-------------|------------------------------------|
| `HERMES_RABBITMQ_HOSTNAME`   | `localhost` |                                    |
| `HERMES_RABBITMQ_PORT`       | `5672`      |                                    |
| `HERMES_RABBITMQ_USERNAME`   | `guest`     |                                    |
| `HERMES_RABBITMQ_PASSWORD`   | `guest`     |                                    |
| `HERMES_RABBITMQ_QUEUE_NAME` | *required*  | absence aborts startup in production |

Hermes follows a strict-startup posture: in production
(`keystone_driver != "mock"`), if `HERMES_RABBITMQ_QUEUE_NAME` is unset
the process exits before binding the HTTP listener. This is intentional
— silently dropping audit events is not an acceptable failure mode for
this surface, and gating on the env var rather than the keystone driver
catches the helm-chart footgun where `keystone_driver = "mock"`
accidentally renders in a production values file.

### Audit emit non-blocking

The CADF audit emit is performed asynchronously: each state-changing
`PATCH` schedules a goroutine that calls `audittools.Auditor.Record`,
guarded by a 1024-slot semaphore. The go-bits Auditor has a hard-coded
20-deep internal channel; under sustained RabbitMQ backpressure that
channel fills and `Record` blocks indefinitely. Without a handler-side
bound, every PATCH would spawn a goroutine that blocks forever, growing
memory until the process OOMs.

Cap 1024 is generous relative to expected toggle traffic (PATCH per
project, rare): drops only happen under genuinely pathological backlog.
When the cap is reached, additional emits are dropped and the
`hermes_data_plane_events_audit_drops_total` counter is incremented. Any
non-zero drop count is operationally significant — it means RabbitMQ
delivery is far enough behind that audit events have been lost. **Alert
on any non-zero value of this counter** and investigate the upstream
RabbitMQ delivery; do not treat drops as routine.

This handler-side semaphore is interim. The proper fix is in
[sapcc/go-bits PR #273](https://github.com/sapcc/go-bits/pull/273),
which adds a storage queue inside the auditor itself. Once that lands
and Hermes adopts it, the handler-side semaphore and drop counter
become redundant and will be removed.

### Mock mode

For local development and tests, set `keystone_driver = "mock"` in
`hermes.conf` and leave `HERMES_RABBITMQ_QUEUE_NAME` unset. Hermes uses
an in-memory toggle store and a null auditor; no Postgres or RabbitMQ
connection is opened.

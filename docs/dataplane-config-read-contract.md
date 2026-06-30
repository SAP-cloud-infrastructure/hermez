<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Dataplane Config — Log-Router Read Contract

This document is the authoritative contract between hermez (writer) and log-router (reader)
for the `dataplane_config` table. Log-router must implement against this spec; hermez
must not break it without a migration and coordinated release.

---

## Database

| Item | Value |
|------|-------|
| Host | PostgreSQL cluster provisioned by the `hermes` Helm chart |
| Database | `hermes` (configurable via helm values) |
| Table | `dataplane_config` |
| Login role | `log-router` (provisioned by the Helm chart's postgres-ng seed) |
| Access | Direct `GRANT SELECT ON dataplane_config TO "log-router"` in hermez migration 001 |

Log-router connects to postgres using the `log-router` login role. Hermez's migration 001
grants this role SELECT on `dataplane_config` directly — no intermediary NOLOGIN role.
The `log-router` user has SELECT only — no INSERT, UPDATE, DELETE, or access to any
other hermez-owned table.

---

## Table schema

```sql
CREATE TABLE dataplane_config (
    project_id    VARCHAR(64) PRIMARY KEY,
    enabled       BOOLEAN     NOT NULL DEFAULT FALSE,
    target_bucket TEXT        NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by    VARCHAR(64) NOT NULL DEFAULT ''
);
```

---

## Reading a single project's config

```sql
SELECT project_id, enabled, target_bucket
FROM dataplane_config
WHERE project_id = $1;
```

Parameters: `$1` = Keystone project UUID (string).

**If the query returns zero rows:** treat as disabled. The project has not opted in.
Do NOT surface this as an error. Route only to the `ccadmin/master` bucket.

**If the query returns a row with `enabled = false`:** same as missing — route only
to the `ccadmin/master` bucket. Do not route to `target_bucket`.

**If the query returns a row with `enabled = true`:** route to both `ccadmin/master`
and `target_bucket`. The `target_bucket` value is validated by hermez at write time
(RFC-1123 subset, 3–63 chars, lowercase + digits + hyphens). Trust it.

---

## Batch lookup (optional optimisation)

If log-router processes events for many projects in one batch, a `WHERE project_id = ANY($1)`
query is safe:

```sql
SELECT project_id, enabled, target_bucket
FROM dataplane_config
WHERE project_id = ANY($1)
  AND enabled = TRUE;
```

Projects absent from the result → disabled.

---

## Caching

Log-router SHOULD cache results with a TTL of 30 seconds (configurable). This prevents
hammering postgres on every event.

Cache invalidation is best-effort. Eventual consistency of up to 30 seconds is acceptable
for a routing-toggle; operators and customers understand that toggling takes effect within
a short window, not instantly.

When the cache entry expires, re-query postgres. There is no push notification from hermez.

---

## Failure modes

| Condition | Required behaviour |
|-----------|-------------------|
| Postgres unreachable | **Fail closed** — treat all projects as disabled. Route only to `ccadmin/master`. Log the error. Do NOT spray events into a bucket you cannot confirm is opted-in. |
| Query returns unexpected error | Same as unreachable — fail closed. |
| `target_bucket` empty on an `enabled=true` row | Should not happen (hermez validates at write time). Treat as disabled; log a warning with the project_id. |

The ccadmin/master routing path is unconditional and must never consult this config.
A hermez/postgres outage stops project-bucket routing only; the admin path is unaffected.

---

## Schema stability

Hermez will not remove or rename existing columns without a coordination notice and a
postgres migration. Log-router may safely rely on `project_id`, `enabled`, and
`target_bucket` remaining stable.

New columns may be added in future migrations. Log-router's `SELECT` list is explicit
(not `SELECT *`) so additions are non-breaking.

---

## Credentials

The `log-router` postgres login role is provisioned by the hermes Helm chart's
`postgres-ng` seed values. Hermez's migration 001 grants it SELECT on `dataplane_config`
directly:

```sql
GRANT SELECT ON dataplane_config TO "log-router";
```

The chart must:

1. Create the `log-router` login role with a password (via postgres-ng seed).
2. Ensure the role exists before hermez starts — hermez's `GRANT ... TO "log-router"`
   requires the role to exist at migration time. The postgres-ng seed runs before the
   hermez pod starts, so ordering is handled by the helm chart's init sequence.

Log-router does **not** run any migrations against the hermez database. It connects
to postgres, queries `dataplane_config`, and does nothing else schema-related.

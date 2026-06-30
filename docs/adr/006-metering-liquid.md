<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-006: Metering and LIQUID stay in log-router; shared postgres database

- **Status:** Accepted (2026-06-29)
- **Date:** 2026-05-19 (revised 2026-06-29)
- **Depends on:** ADR-001, ADR-004

---

## Context

ADR-001 moved routing-config ownership to hermez. ADR-004 specifies that
log-router reads routing config directly from the shared postgres. Two
follow-up questions arose:

1. Does `metering_records` (written from log-router's flush path, scraped by Limes via LIQUID) also move to hermez?
2. How is the postgres laid out — one cluster, one database, multiple roles?

## Decision

### Metering ownership: stays in log-router

`metering_records` and the LIQUID API stay with log-router. log-router
keeps the writer code. The table lives in the same postgres database as
hermez's `dataplane_config` but is owned by the `log-router` postgres role.

Reconsider in 6 months once production load on metering is observed.

**Why not move metering to hermez:**
- `metering_records` is hot-write on every flush — routing through hermez adds latency to a critical path
- Failure on the metering write path should not affect hermez query availability
- Hermez stays focused on read-heavy + low-write workloads

**Why not eliminate metering:**
- LIQUID's contract requires queryable historical usage; Prometheus counters reset on pod restart and are not billing-grade

### Database layout: one database, two postgres roles

**One postgres-ng cluster. One database (`hermes`). Two postgres roles
(`hermes`, `log-router`). Table-level GRANTs scope access.**

| Table | Owned by | log-router access | hermes access |
|-------|----------|-------------------|---------------|
| `dataplane_config` | `hermes` | SELECT (granted by migration 001) | Full (owner) |
| `metering_records` | `log-router` | Full (owner) | None |

- Hermez migration 001 creates `dataplane_config` and immediately grants SELECT to `log-router`
- Log-router migration 004 creates `metering_records` (unchanged, only connection string differs)
- `hermes` has no grants on `metering_records` and never touches it

### What actually shipped

In hermez migration `001_create_dataplane_config.up.sql`:

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

Note: the original draft specified a `log_router_reader` NOLOGIN role as an
intermediate. This was simplified: the direct GRANT to the `log-router` login
role is sufficient and requires no `CREATEROLE` privilege on the `hermes` user.

In log-router (migration 004, unchanged):

```sql
-- metering_records table, owned by log-router user
```

### Helm chart postgres-ng values (canonical)

```yaml
logRouter:
  hermesDb:
    user: log-router        # login role name
postgresql:
  databases:
    hermes: {}              # single shared database
  users:
    hermes: {}              # owns dataplane_config; runs hermez migrations
    log-router: {}          # owns metering_records; SELECT on dataplane_config
```

### Migration ownership

| Who | Migration runner | Tables created |
|-----|-----------------|----------------|
| hermez | `easypg.Connect` on startup (advisory-lock protected) | `dataplane_config` |
| log-router | log-router's own migration runner on startup | `metering_records` |

Log-router MUST NOT run any migrations against hermez's tables. The
`log-router` postgres role has no CREATE privilege on hermez-owned tables —
any attempt produces `permission denied` immediately.

## Consequences

### Positive

- log-router keeps its existing metering writer with no code changes (connection string only)
- Single postgres deployment per region, single backup policy, single connection budget
- Routing-vs-metering isolation enforced by postgres role permissions

### Negative

- log-router imports `database/sql` and `lib/pq` for both metering writes and routing reads
- Two-role access matrix must be documented for operators

### Neutral

- Connection-pool sizing: hermez and log-router share one postgres; pool sizes must stay within `max_connections` budget

## Re-evaluation triggers

Reopen if:
- LIQUID load grows beyond what the shared postgres cluster can serve
- A SAP CC billing platform standardizes a metering data plane
- log-router needs to call other write APIs in hermez (would justify HTTP metering writes)

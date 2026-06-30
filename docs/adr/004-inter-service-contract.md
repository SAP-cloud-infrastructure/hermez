<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-004: log-router reads hermez routing config directly from postgres

- **Status:** Accepted (shipped 2026-06-25)
- **Date:** 2026-05-19 (revised 2026-06-29)
- **Depends on:** ADR-001, ADR-007
- **Supersedes:** the earlier draft that proposed an HTTP-pull contract with TTL+ETag

---

## Context

ADR-001 split ownership: hermez owns the routing-config schema and CRUD API;
log-router consumes config to make routing decisions on the data plane.
ADR-007 defines the shipped `dataplane_config` table and the go-live scope.

This ADR specifies how log-router reads that table.

The earlier draft proposed an HTTP-pull contract (TTL cache + ETag +
fail-closed startup probe + new Keystone role). It was rejected during design
review: the "no shared DB" rule it assumed does not exist in any prior ADR —
it was self-imposed. Both services live in the same helm release, deploy
together, and share operational ownership; the layering boundary HTTP would
have enforced was imaginary.

## Decision

### log-router connects directly to the shared postgres instance

log-router reads hermez's `dataplane_config` table using the `log-router`
postgres login role, which has `SELECT` on the table granted by hermez's
migration 001.

```
LOG_ROUTER_DB_URL=postgres://log-router:<pw>@hermes-postgresql.<ns>.svc:5432/hermes
```

Same database hermez writes to. Different postgres user. No migrations run
from log-router against this database — hermez owns the schema entirely.

### Access grant

Hermez migration `001_create_dataplane_config.up.sql` contains:

```sql
GRANT SELECT ON dataplane_config TO "log-router";
```

This is a direct grant to the login role. No NOLOGIN intermediary role is
used — the `hermes` migration user owns the table and can grant directly.
The `log-router` user is provisioned by the helm chart's postgres-ng seed
before the hermez pod starts.

log-router can only SELECT. Any accidental INSERT/UPDATE/DELETE triggers
`permission denied` immediately — caught in CI, never reaches production.

### Read query

```sql
SELECT project_id, enabled, target_bucket
FROM dataplane_config
WHERE project_id = $1;
```

Behaviour:
- **Zero rows** → treat as disabled. Not an error. Route only to `ccadmin/master`.
- **Row with enabled=false** → same as missing. Route only to `ccadmin/master`.
- **Row with enabled=true** → route to both `ccadmin/master` and `target_bucket`.

The full contract is in `docs/dataplane-config-read-contract.md`.

### What log-router does NOT own

- No schema migrations against the hermez database
- No INSERT/UPDATE/DELETE on any hermez table
- No metering writes to the hermez database (metering has a separate table owned by log-router; see ADR-006)

### Cache behavior

log-router caches results with a configurable TTL (default 5 minutes via
`LOG_ROUTER_CACHE_TTL`). Cache is lazily populated on first GetConfig call
per tenant. Stale entries re-query postgres on next access after TTL.

Set `LOG_ROUTER_CACHE_TTL=30s` for faster propagation during qa-de-1 testing.

| Operation | log-router behavior |
|-----------|---------------------|
| Startup | No prefetch. Cache populated lazily. |
| GetConfig — cache miss | SELECT from dataplane_config |
| GetConfig — cache hit, fresh | Return cached |
| GetConfig — cache hit, stale | Re-SELECT |
| Hermez updates a row via API | log-router sees it within one TTL window |

### Failure modes

| Scenario | Behavior |
|----------|----------|
| Postgres unreachable at startup | log-router exits. Helm liveness probe restarts. ADR-005 deploy ordering ensures postgres comes first. |
| Postgres unreachable mid-flight | Serve from cache until TTL expires; log errors. Admin path (ccadmin/master) is unaffected — it does not consult this config. |
| Hermez writes a malformed config row | Validation lives in hermez at write time. log-router trusts what's in the table. Malformed rows log a warning and treat the project as disabled. |
| log-router tries to write (impossible — SELECT-only) | `permission denied`. Defense-in-depth. |

### Why not HTTP

| Alternative | Verdict | Reason |
|-------------|---------|--------|
| HTTP pull with TTL+ETag | Reject | Requires hermez-api to be healthy for log-router to route events. Circular dependency. |
| RabbitMQ control-plane topic | Reject | New infra dep for a problem the TTL already handles |
| Postgres LISTEN/NOTIFY | Defer | Pure optimization; revisit if 5-min propagation is too slow |

## Consequences

### Positive

- log-router has no HTTP dependency on hermez for routing decisions
- No new auth surface — log-router doesn't need a Keystone token for config reads
- Postgres-down is the same failure log-router already has (for metering)
- Consistent with how SAP CC services share postgres (keppel/limes pattern)

### Negative

- log-router has a hard dependency on hermes-postgres being reachable. This is
  the same class of dependency it already has for metering (ADR-006).
- Schema changes need coordination: a hermez migration that renames
  `dataplane_config.target_bucket` breaks log-router's SELECT until it deploys.
  Mitigation: additive-only migrations (ADR-005). Renames ship in two steps.

## Re-evaluation triggers

Reopen if:
- log-router and hermez separate into different helm releases
- A second consumer of dataplane config appears (then HTTP API is cheaper than distributing credentials)
- 5-minute propagation becomes a user-visible problem (add LISTEN/NOTIFY)

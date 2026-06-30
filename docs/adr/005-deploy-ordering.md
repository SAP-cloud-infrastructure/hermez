<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-005: Phased rollout — postgres first, hermez code second, log-router third

- **Status:** Accepted — phases collapsed (shipped 2026-06-25 as hermez PR #352, log-router PR #24, helm-charts PR #12097)
- **Date:** 2026-05-19 (revised 2026-06-29)
- **Depends on:** ADR-001, ADR-007

---

## What shipped

The multi-phase plan described in the original draft below was designed for
a zero-downtime regional rollout where postgres, hermez, and log-router came
up independently. In practice all three changes shipped together into qa-de-1:

| Change | PR | Status |
|--------|----|--------|
| Hermez dataplane-config API + postgres migration | hermez #352 | Merged |
| log-router Swift/Ceph storage backend | log-router #24 | Open (CI passing) |
| Helm chart: postgres-ng, log-router StatefulSet, HERMES_PG_* env, policy fix | helm-charts #12097 | Open |

The phased approach was correct in intent — postgres deployed first, hermez
API validated in qa-de-1 before log-router consumed it — but it happened
within a single coordinated deploy rather than across sequential weeks.

### Deploy sequence actually used (qa-de-1)

```
1. helm-charts PR #12097 included postgres-ng + hermez-api HERMES_PG_* env
2. hermez-api pod restarted; migration 001 ran; dataplane_config table created;
   GRANT SELECT on dataplane_config TO "log-router" applied
3. dataplane-config API validated live: GET/PUT/DELETE all returned correct responses
4. cc-demo project enabled: PUT with enabled=true, target_bucket="cc-demo-audit"
5. log-router pods running (2/2) — consuming RabbitMQ, writing to admin path
6. log-router PR #24 still open — per-tenant routing to cc-demo-audit pending
```

### Schema migration safety (still canonical)

`easypg.Connect` uses `pg_advisory_lock` for migration serialization. Hermez
replicas race to apply migration 001; the loser waits, then sees the schema
already at version 1 and proceeds without re-running it.

Migrations MUST be additive-only: ADD COLUMN, ADD INDEX, ADD TABLE. Never
DROP, RENAME, or change column type. Non-additive changes ship in two steps
(add new column; log-router learns it; remove old column).

### Rollback

| What broke | Rollback action |
|------------|-----------------|
| hermez dataplane-config API | Set `routing_store_driver = ""` in config; hermez skips postgres on restart |
| log-router routing decisions | Disable LOG_ROUTER_DB_URL; log-router falls back to static config (all events to admin path) |
| postgres schema | Data preserved; PVC retained on pod removal (resource-policy: keep) |

---

## Original phased plan (preserved for context)

The plan below describes the intent as designed. It was not fully executed as
described — the phases collapsed into concurrent work — but the principles
(each phase independently reversible, postgres validated before consumers, etc.)
guided the actual rollout.

### Four phases

```
PHASE 0 — Log-router house-cleaning
  Remove internal/api/config_handler.go, migrations 001/002/003.
  log-router focuses on data-plane writing only.

PHASE A — Helm chart prep (postgres only)
  postgresql-ng provisioned. No consumers yet. 24h soak.

PHASE B — Hermez code (routing API)
  hermez PR-2: postgres implementation + migrations + dataplane-config API
  Activated when routing_store_driver = "postgres" in config.

PHASE C — Log-router service (data-plane consumer)
  LOG_ROUTER_DB_URL points at hermez postgres.
  log-router connects; reads dataplane_config; routes per-tenant.
```

### Decoupled feature flags

```yaml
logRouter:
  enabled: true              # log-router StatefulSet
  postgresql:
    enabled: true            # postgresql-ng + pgbackup + pgmetrics
hermes:
  routingApiEnabled: true    # HERMES_PG_* env vars on hermes-api
```

### Additive-only migration rule

No migration may DROP, RENAME, or change a column type. log-router's SELECT
list is explicit (`SELECT project_id, enabled, target_bucket`) so new columns
added by future hermez migrations are invisible and non-breaking.

### Region order

| Step | Region | Soak |
|------|--------|------|
| 1 | qa-de-1 | 24h |
| 2 | eu-de-1 | 24h |
| 3 | eu-de-2 | 12h |
| 4 | na-us-1 | 12h |
| 5 | ap-jp-2 | 12h |
| 6 | All remaining | parallel after #5 |

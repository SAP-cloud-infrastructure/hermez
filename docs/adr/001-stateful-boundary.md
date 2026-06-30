<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-001: Hermez owns audit-routing configuration (stateful boundary)

- **Status:** Accepted (shipped 2026-06-25 in hermez PR #352)
- **Date:** 2026-05-19
- **Author:** Nathan Oyler (with assistance from /do router)
- **Stakeholders:** Hermez maintainers, log-router maintainers, CC observability team
- **Related:** ADR-007 (canonical go-live scope); helm-charts PR #12097; log-router PR #24

---

## Context

Hermez today is a stateless query layer over OpenSearch. CLAUDE.md states:
> Hermes is the **query layer only** — it reads from OpenSearch. It does not ingest events directly.

The CC observability team is adding a per-tenant audit-routing capability:
customers configure which audit events get written to which Ceph/S3 destinations.
The data-plane writer is **log-router**.

PR #11330 bundled a `postgresql-ng` dependency and a log-router
StatefulSet under the **hermes Helm chart**. The chart placement is correct —
postgres is on the hermes chart — but the *code* ownership of the schema,
the CRUD HTTP API, the migrations, and the audit log of config changes lived
in log-router. This ADR records the decision to move that ownership into hermez.

### Forces

| Force | Implication |
|-------|-------------|
| Hermez is the public, tenant-facing query API for audit data already | Tenants reach hermez to read audit events; routing config is a write-side companion to the same domain |
| log-router is a writer that should not own a tenant API | Writers are throughput-optimized; CRUD APIs are latency-and-correctness-optimized |
| Limes precedent for quota/usage | Limes owns the API for quota config across services. Hermez playing the same role for audit-routing is a familiar pattern in CC |
| Hermez has no DB today | Adding postgres is a meaningful architectural delta — must be decided deliberately |

## Decision

**Hermez becomes the source of truth for audit-routing configuration.** The
schema, the migrations, the CRUD HTTP API, the policy rules (`audit_admin` role),
and the CADF audit events for config changes all live in this repository.

**log-router becomes a read-only consumer** of this configuration. It connects
directly to the same postgres instance with a SELECT-only grant on `dataplane_config`
(see ADR-004 for the contract).

The chart-level placement (postgres-ng under the hermes chart) **is preserved** —
that part was already correct. What changed is the code ownership.

## What shipped (go-live scope, ADR-007)

ADR-007 narrowed the scope significantly from the original design:

- **Table**: `dataplane_config` (5 columns: project_id, enabled, target_bucket, updated_at, updated_by) — not the 22-column `routing_configs` originally planned
- **API**: 3 endpoints (GET/PUT/DELETE `/v1/projects/{id}/dataplane-config`) — not the 8-endpoint routing/configs surface
- **No audit side-table**: CADF events via `audittools.Auditor` are the audit trail; no `routing_audit_log` table
- **No rate limits, retention, or CEL filters**: deferred to a future ADR
- **Access grant**: direct `GRANT SELECT ON dataplane_config TO "log-router"` in hermez migration 001 — no NOLOGIN role indirection

## Consequences

### Positive

- Hermez became a complete public-facing API surface for audit (read + config).
- log-router shrank significantly and now focuses on its single responsibility: high-throughput writing.
- The `audit_admin` Keystone role and policy rules live with the API that enforces them.
- Both services share one postgres cluster with role-scoped access, matching the SAP CC house pattern.

### Negative

- Hermez gained state. Multiple replicas share one postgres. Mitigation: postgres handles write serialization; easypg advisory lock prevents concurrent migration runs.
- Adding postgres adds operational surface (backups, migrations, connection pools, monitoring).

### Neutral

- CADF event ingestion path (RabbitMQ → log-router → Swift/S3) is unchanged.
- WAL state stays with log-router (bbolt on PVC).

## Alternatives considered

### Alternative A — Keep config ownership in log-router

Rejected because: log-router becomes a multi-purpose service (writer + tenant CRUD API). Tenants need two URLs for audit operations. UX regression.

### Alternative B — Shared postgres, both services read/write

Rejected because: two writers split authoritative ownership; schema migrations require coordinated rollouts of two services.

### Alternative C — A new dedicated config service (`hermes-config`)

Rejected because: operationally heavier; same data domain as hermez; YAGNI.

## Compliance

- Supersedes CLAUDE.md "query layer only" — this ADR is the deliberate architectural change that adjusts that statement.
- All open questions from the original draft are resolved in ADR-002 through ADR-007.

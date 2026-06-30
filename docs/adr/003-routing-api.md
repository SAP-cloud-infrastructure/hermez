<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# ADR-003: Routing-config HTTP API and authorization

- **Status:** **Superseded by ADR-007** (URL changed from `/v1/routing/configs/{project_id}` to `/v1/projects/{id}/sinks`; endpoint set reduced from 8 to 5; `POST /enable` and `POST /disable` dropped in favor of PATCH on `disabled`; `audit_admin`-only writes replaced by tenant-CRUD-able `project_admin`; CEL compile-check at write time deferred until log-router consumes typed)
- **Date:** 2026-05-19 (superseded 2026-05-20)
- **Depends on:** ADR-001, ADR-002
- **Superseded by:** ADR-007
- **Related:** log-router `internal/api/config_handler.go` (701 LoC source); existing hermez `pkg/api/core.go`, `pkg/api/events.go`

> **Reader: this ADR is preserved for historical context only.** The endpoint set, the URL scheme, the lifecycle endpoints, the authorization model, and the CEL compile-check decision below have been replaced by ADR-007. The sections that remain canonical are the **httpapi.Compose integration pattern**, the **package layout under `pkg/api/`**, the **CORS expansion**, the **content-type / body-cap enforcement**, the **per-test `prometheus.NewPedanticRegistry()` pattern**, and the **table-driven test pattern** that mirrors `api_test.go`. Read ADR-007 first; treat the endpoint table and policy.json snippet below as the original-intent draft, not the contract.

---

## What changed (summary for the reader)

| Topic | This ADR (original intent) | ADR-007 (canonical) |
|---|---|---|
| URL prefix | `/v1/routing/configs` | `/v1/projects/{id}/sinks` |
| Resource in URL | `configs` keyed by `{project_id}` | `sinks` parented to `projects/{id}` |
| Endpoint count | 8 (list, get, create, patch, enable, disable, audit-log, delete) | 5 (list, get, create, patch, delete) |
| Lifecycle | `POST /enable`, `POST /disable` (separate RPCs) | PATCH on `disabled: bool` (single mutation surface) |
| `GET /audit-log` endpoint | Yes (queries `routing_audit_log` table) | Dropped. Customers query `GET /v1/events?target_type=routing/sink&target_id=...` |
| Authorization on writes | `audit_admin` only (operator-only v1) | `project_admin` (tenant-CRUD-able per architecture explainer §10) |
| Authorization on reads | `project_viewer` / `domain_viewer` / `audit_admin` | `cluster_viewer` / `project_viewer` |
| CEL filter compile-check at write time | Yes; vendor `google/cel-go` directly into hermez | Deferred. `filter` is opaque string; log-router validates at consume-time. |
| Body size cap | 64 KiB (specified here) | 64 KiB (re-specified; current code drift at 1 MiB is fixed) |
| Content-Type 415 | Specified here | Re-specified in ADR-007 — required (current code accepts any) |
| URL-vs-body project_id mismatch | Reject 400 | Re-specified in ADR-007 — required (current code does not enforce) |
| Audit-log emission via `audittools.Auditor` | Specified here | Phase 1.5 — same mechanism, after the API surface lands |
| `routing_validation.go` separate file | Specified here | Layout decision; either inline in handlers or separate file is acceptable. ADR-007 doesn't pin. |

---

## Context (preserved)

ADR-001 placed the routing-config CRUD API in hermez. ADR-002 specified the
schema. This ADR specified the HTTP surface, the authorization model, and how
the new API plugs into hermez's existing `httpapi.Compose` pattern.

log-router already implements this API at `~/gh/log-router/internal/api/config_handler.go`.
That implementation was the reference; we lift its endpoint shape and validation
logic, adapt it to hermez's idioms (`gopherpolicy`, `respondwith`, `httpapi.Compose`,
the `ReturnESJSON` exception is N/A here — postgres returns clean JSON).

## Decision (superseded: endpoint surface)

> **The endpoint table below is the original-intent draft. Use ADR-007 §"Endpoints (5)" for the canonical surface.**

URL prefix: `/v1/routing/`. The `/v1/events` routes stay untouched.

| Method | Path | Policy rule | Description |
|--------|------|-------------|-------------|
| GET    | `/v1/routing/configs`             | `routing:list`         | List configs (admin: all; tenant: own only) |
| GET    | `/v1/routing/configs/{project_id}` | `routing:show`        | Single config (tenant: must match token) |
| POST   | `/v1/routing/configs`             | `routing:create`       | Create — body specifies `project_id` |
| PATCH  | `/v1/routing/configs/{project_id}` | `routing:update`      | Partial update (PATCH semantics from log-router `config_handler.go:285-336`) |
| POST   | `/v1/routing/configs/{project_id}/enable`  | `routing:enable`  | Set `enabled_at`, `data_plane_enabled=true` |
| POST   | `/v1/routing/configs/{project_id}/disable` | `routing:disable` | Set `disabled_at`, `data_plane_enabled=false` |
| GET    | `/v1/routing/configs/{project_id}/audit-log` | `routing:list` | Per-config audit log |
| DELETE | `/v1/routing/configs/{project_id}` | `routing:delete`      | Remove (FK ON DELETE RESTRICT preserves audit log) |

**ADR-007 collapses this to 5 endpoints under `/v1/projects/{id}/sinks` with PATCH-on-`disabled` replacing the enable/disable RPCs and the audit-log endpoint replaced by a CADF query.**

## Decision (superseded: authorization model)

> **Use ADR-007 §"Authorization model — tenant-CRUD-able" for the canonical rules.**

Original-intent stance: writes are operator-only via `audit_admin`. The four arguments listed in the original ADR (Elektra deferred → no UI → operators-via-curl; CEL evaluator hardening cost; PR #11330 already seeds `audit_admin`; alignment with log-router's `cluster_admin` posture) all assumed Elektra was deferred and CEL was on the critical path.

**The architecture explainer §10 closed Open Q2 in favor of customer-CRUD-able**, and ADR-007 defers CEL altogether (filter is an opaque string). Both legs of the original argument fall away. ADR-007 re-specifies the authorization model as `project_admin` for writes, `cluster_viewer or project_viewer` for reads, with `audit_admin` removed from the routing-write gate.

## Decision (still canonical: integration pattern)

These pieces of the original ADR survive supersedure and remain the contract under ADR-007:

### Package layout

```
pkg/api/
  ├─ core.go               // unchanged (V1API for events)
  ├─ events.go             // unchanged
  ├─ routing.go            // RoutingAPI implements httpapi.API
  ├─ routing_handlers.go   // CRUD handlers (5 endpoints per ADR-007)
  ├─ routing_validation.go // input validation (optional separate file)
  └─ routing_test.go       // table-driven tests, mirrors api_test.go
```

`api.Server` (`pkg/api/server.go:22`) gets a third positional arg — the `routing.Store` — and adds `NewRoutingAPI(validator, store)` to the `httpapi.Compose` list (`server.go:31-35`).

### CORS expansion

`pkg/api/server.go:41-45` extends from `GET, HEAD` to also allow `POST, PUT, PATCH, DELETE`. Existing allowed headers (`X-Auth-Token`, `Content-Type`, `Accept`) suffice.

(Elektra integration is deferred; CORS is widened proactively because it is cheap and reversible. If a security review wants tighter v1, narrow CORS to GET/HEAD and re-open it when a UI lands.)

### Filter validation at write time (superseded — deferred)

> The original decision was to **compile via `google/cel-go` at write time** and vendor the dep directly. **ADR-007 supersedes this:** `filter` is an opaque string; log-router validates at consume-time; the `cel-go` dep is not added in this PR.
>
> Original rationale (operator-only writes → no malicious-expression threat model → compile-check is the only ergonomics question) is preserved; ADR-007 reaches the opposite conclusion because the dep buys nothing until log-router actually evaluates the expression. CEL evaluator hardening was already deferred; deferring the compile-check itself just defers the dep.

### Validation rules

The structural validation rules (sink-name regex, content-type, body cap, URL-vs-body mismatch rejection) carry forward into ADR-007. The drifted ranges (`grace_minutes`, `target_obj_mb`, `retention_days`, `max_events_per_second`) are moot because ADR-007 drops those columns entirely.

| Rule | Status under ADR-007 |
|---|---|
| `sinkNamePattern = ^[a-zA-Z0-9][a-zA-Z0-9_-]*$` | Carries forward |
| Sink names unique within scope | Now `(project_id, name)` PK in DB; uniqueness enforced by Postgres |
| Sink buckets unique within a config | Dropped — `Config` doesn't exist; one row per sink |
| Per-sink-type required fields (s3 / otlp / dlq) | Dropped — Phase 1 is S3 only; future types add via URI scheme prefix |
| `grace_minutes ∈ [1, 60]` | Dropped — column removed |
| `target_obj_mb ∈ [1, 256]` | Dropped — column removed |
| `retention_days ∈ [1, 3650]` | Dropped — column removed |
| `max_events_per_second ≥ 0` | Dropped — column removed |
| `project_id` and `domain_id` shape | Carries forward (ADR-007: Keystone identifier shape, ≤ 64 chars) |
| Body cap 64 KiB | **Re-specified in ADR-007** — current code drift at 1 MiB is fixed |
| Content-Type 415 enforcement | **Re-specified in ADR-007** — currently not enforced |
| URL-vs-body `project_id` mismatch → 400 | **Re-specified in ADR-007** — currently not enforced |

### Audit-log emission (superseded mechanism, same destination)

Original mechanism: write to `routing_audit_log` table from each handler, with actor identity from `gopherpolicy.Token`, AND emit a CADF event via `audittools.Auditor`.

**ADR-007 keeps only the second leg.** The side table is dropped; CADF events via `audittools.Auditor` are the single audit-of-config mechanism, wired in Phase 1.5.

Reference (still canonical): `/Users/I810033/gh/keppel/internal/keppel/auditor.go:12-36`.

### Metrics

`prometheus.MustRegister` for routing-storage error counters; per-test `prometheus.NewPedanticRegistry()` to avoid registry conflicts. Both carry forward into ADR-007 unchanged.

### CORS, request size limits, content type (carries forward)

- Accept only `Content-Type: application/json`; reject others with 415. *(re-specified in ADR-007)*
- Body size cap at 64 KiB. *(re-specified in ADR-007)*
- Reject path-mismatch: if URL `{project_id}` differs from body `project_id`, return 400. *(re-specified in ADR-007)*

## Consequences (substantially superseded)

The original "Positive / Negative / Neutral" listed `google/cel-go` as a transitive dep cost and `audit_admin`-only as a v1 simplification. ADR-007 reverses both: no `cel-go` dep, no operator-only stance.

Net effect under ADR-007:
- **Smaller dep footprint** (no `cel-go`).
- **Larger test surface** (tenant writers, not just operators) but smaller code surface (5 endpoints, not 8; no CEL pipeline).
- **CORS expansion still proactively wider than v1 strictly needs** — no UI exists yet, but an Elektra successor or admin UI is the realistic v2 audience.

## Alternatives considered (still canonical for the URL-prefix question)

| Alternative | Verdict | Reason |
|-------------|---------|--------|
| URL prefix `/v1/configs` (no `routing/`) | Reject | Conflicts with hermez config-the-server-config |
| URL prefix `/v1/tenants` (matches log-router's current API) | Reject | "tenant" is overloaded in OpenStack |
| Skip CEL validation at write time | **Adopted by ADR-007** | UX cost (silent breakage) is real, but log-router can compile-check at consume-time and surface errors via `log_router_config_fetch_failures_total`; dep cost avoided in hermez |
| Separate roles per endpoint (`routing:write_filter`, `routing:write_sinks`) | Reject for v1 | YAGNI; one role per CRUD verb is enough |
| GraphQL surface | Reject | Inconsistent with rest of hermez; no client requests it |
| URL prefix `/v1/projects/{id}/sinks` (Keystone-project-parented) | **Adopted by ADR-007** | Matches GCP `projects/{p}/sinks/{name}`; matches architecture explainer §10 |

The original ADR did not consider `/v1/projects/{id}/sinks` as an alternative — the panel synthesis introduced it, drawing on the GCP vendor reference and the architecture explainer §10 closure.

## Implementation checklist

> **See ADR-007 §"Implementation checklist (this PR)" for the canonical task list.** The items below are the subset that survives supersedure.

- [x] Add `pkg/api/routing.go`, `routing_handlers.go`, `routing_test.go` *(carries forward; renamed for `Sink` shape)*
- [x] Update `pkg/api/server.go` to accept `routing.Store` *(carries forward)*
- [x] Update `etc/policy.json` *(re-do per ADR-007: drop `routing:enable`, `routing:disable`, `audit_admin`-on-write; add tenant-CRUD-able rules)*
- [x] Update `etc/permissive-policy.json` and `pkg/test/policy.json` *(re-do same)*
- [x] Add fixtures for table-driven tests *(rename `routing_*.json` → `sink_*.json` per ADR-007)*
- [x] CORS expansion in `server.go:41-45` *(carries forward)*
- [ ] ~~Wire CEL filter validation~~ *(deferred by ADR-007)*
- [ ] **Add `audittools.Auditor` setup** *(Phase 1.5 per ADR-007)*
- [x] Bump `main.go:26` version *(done at 1.3.0; bump again on Phase 1 reshape merge)*
- [ ] Update `docs/users/hermez-v1-reference.md` *(per ADR-007's new endpoint set)*
- [ ] Update `docs/users/api-example.md` *(per ADR-007's new endpoint set)*

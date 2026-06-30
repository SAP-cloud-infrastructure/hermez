# Hermez Architecture Decision Records

## What lives here

Architecture Decision Records (ADRs) for Hermez. Each ADR captures a single
decision, its rationale, alternatives considered, and consequences. ADRs are
immutable once accepted: superseded decisions get a new ADR that references
the old one.

## Index

| # | Title | Status | Supersedes |
|---|-------|--------|------------|
| 001 | Stateful boundary — Hermez owns audit-routing configuration | Proposed | — |
| 002 | Routing-config schema — lift from log-router into hermez | Superseded by 007 | — |
| 003 | Routing-config HTTP API — package layout and authorization | Superseded by 007 | — |
| 004 | Inter-service contract — log-router consumes hermez via HTTP | Proposed | — |
| 005 | Deploy ordering — phased postgres-then-log-router rollout | Proposed | — |
| 006 | Metering and LIQUID — staying with log-router for now | Proposed | — |
| 007 | Routing API contract — `Sink` resource, project-parented URL, PATCH-disabled | Proposed | 002, 003 |

## Cross-repo references

- log-router ADR-004 (audit-router pipeline): `~/gh/log-router/docs/adr/004-audit-router.md`
- log-router ADR-005 (LIQUID API, no shared-DB): `~/gh/log-router/docs/adr/005-liquid-api.md`
- helm-charts PR #11330 (the integration we are splitting): https://github.com/sapcc/helm-charts/pull/11330
- Internal issue: https://github.wdf.sap.corp/sap-cloud-infrastructure/observability-issues/issues/765

## Conventions

- File name: `NNN-kebab-case-title.md`
- Status: `Proposed | Accepted | Superseded by NNN | Rejected`
- Use evidence: cite `path:line` for code claims; cite ADR or PR for cross-cutting claims.
- Decisions are scoped narrowly: a decision that affects two concerns gets two ADRs.

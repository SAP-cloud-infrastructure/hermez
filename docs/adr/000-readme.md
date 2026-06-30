<!--
SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company

SPDX-License-Identifier: Apache-2.0
-->

# Hermez Architecture Decision Records

## What lives here

Architecture Decision Records (ADRs) for Hermez. Each ADR captures a decision,
its rationale, alternatives considered, and consequences.

## Index

| # | Title | Status |
|---|-------|--------|
| 001 | Dataplane audit-event routing — hermez owns the config plane | **Accepted** |

## Historical drafts (2026-05-19 → 2026-06-25)

The `001-dataplane-routing.md` above supersedes a draft series (ADR-001 through
ADR-007) that was written during the design phase before go-live. Those drafts
are preserved in git history on the `feat/log-router-integration` branch for
reference but are not the canonical record. ADR-001 contains everything.

## Cross-repo references

- log-router PR #24: Swift/Ceph storage backend + hermez postgres config read
- helm-charts PR #12097: postgres-ng, log-router StatefulSet, HERMES_PG_* env
- Hermez PR #352: dataplane-config API (merged, live in qa-de-1)

## Conventions

- File name: `NNN-kebab-case-title.md`
- Status: `Proposed | Accepted | Superseded by NNN | Rejected`
- Use evidence: cite `path:line` for code claims; cite ADR or PR for cross-cutting claims.

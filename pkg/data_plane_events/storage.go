// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package data_plane_events implements the per-project toggle that controls
// whether data-plane events for a given project are delivered to the
// project's immutable Ceph compliance bucket. The bucket itself is
// operator-managed out-of-band; the only customer-visible control is a
// boolean per project.
//
// Note: package name uses underscores for clarity; renaming to
// "dataplaneevents" was deferred to avoid touching every import site.
//
// Phase-2 follow-up: project deletion in Keystone is not synced to this
// table; orphan rows accumulate. A reaper job is planned but out of scope
// for v1.
package data_plane_events

import "context"

// Storage abstracts the per-project boolean toggle so that handlers can be
// tested without a real Postgres instance.
//
// The default state for any project is enabled=false. Storage implementations
// MUST treat "no row" and "row with enabled=false" as observably equivalent;
// callers rely on this to keep the audit log clean (see the Set contract).
type Storage interface {
	// Get returns the current enabled flag for projectID.
	// found is true only when an explicit row exists; if found is false, enabled
	// MUST be returned as false (the default state).
	Get(ctx context.Context, projectID string) (enabled, found bool, err error)

	// Set upserts the toggle for projectID atomically. The changed return value
	// is true iff the operation actually transitioned observable state, where
	// state is "what Get returns". This means:
	//   - enabled=true on a project with no row or enabled=false  -> changed=true
	//   - enabled=false on a project with enabled=true            -> changed=true
	//   - enabled=true on a project already enabled=true          -> changed=false (no write)
	//   - enabled=false on a project with no row                  -> changed=false (no write)
	//
	// priorEnabled reflects the observable state BEFORE this call (defaulting
	// to false when no row existed). Handlers emit a CADF audit event only
	// when changed is true; priorEnabled feeds the audit attachment.
	Set(ctx context.Context, projectID string, enabled bool) (changed, priorEnabled bool, err error)
}

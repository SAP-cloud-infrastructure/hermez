// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package routing manages per-project dataplane routing configuration.
// Hermez is the authoritative writer; log-router is a read-only consumer
// via direct postgres SELECT (see docs/dataplane-config-read-contract.md).
package routing

import (
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/must"
)

// DataplaneConfig holds the routing configuration for a single project.
// When Enabled is true, log-router routes dataplane events from Ceph RGW
// into the project's TargetBucket in addition to the shared admin bucket.
type DataplaneConfig struct {
	ProjectID    string    `json:"project_id"`
	Enabled      bool      `json:"enabled"`
	TargetBucket string    `json:"target_bucket,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	UpdatedBy    string    `json:"updated_by"`
}

// DefaultDataplaneConfig returns the default (disabled) config for a project
// that has no stored configuration. Callers should set ProjectID on the result.
func DefaultDataplaneConfig(projectID string) DataplaneConfig {
	return DataplaneConfig{
		ProjectID: projectID,
		Enabled:   false,
	}
}

// Render implements the audittools.Target interface so DataplaneConfig can be
// used directly in audittools.Event.Target. The full config is attached as JSON
// so audit consumers can see exactly what was written.
func (c DataplaneConfig) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:   "service/hermes/dataplane-config",
		ID:        c.ProjectID,
		ProjectID: c.ProjectID,
		Attachments: []cadf.Attachment{
			must.Return(cadf.NewJSONAttachment("payload", map[string]any{
				"enabled":       c.Enabled,
				"target_bucket": c.TargetBucket,
			})),
		},
	}
}

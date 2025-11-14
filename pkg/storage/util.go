// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"errors"
	"strings"

	"github.com/sapcc/go-api-declarations/cadf"
)

// Tenant validation errors
var (
	ErrEmptyTenantID   = errors.New("tenant ID cannot be empty")
	ErrInvalidTenantID = errors.New("tenant ID 'unavailable' is not valid for queries")
)

// CADFFieldMapping maps API field names to OpenSearch CADF index fields.
// The .keyword suffix is used for exact-match queries and aggregations, avoiding text analysis and tokenization.
// This mapping is shared across all storage backends to ensure consistency in CADF event querying.
var CADFFieldMapping = map[string]string{
	"time":           "eventTime",
	"action":         "action.keyword",
	"outcome":        "outcome.keyword",
	"request_path":   "requestPath.keyword",
	"observer_id":    "observer.id.keyword",
	"observer_type":  "observer.typeURI.keyword",
	"target_id":      "target.id.keyword",
	"target_type":    "target.typeURI.keyword",
	"initiator_id":   "initiator.id.keyword",
	"initiator_type": "initiator.typeURI.keyword",
	"initiator_name": "initiator.name.keyword",
}

// DeduplicateEvents removes duplicate events by ID while preserving order.
// First occurrence of each event is kept. This handles cases where the same
// event exists in multiple indexes during index migration or multi-index queries.
func DeduplicateEvents(events []*cadf.Event) []*cadf.Event {
	if len(events) == 0 {
		return events
	}

	seen := make(map[string]struct{}, len(events))
	result := make([]*cadf.Event, 0, len(events))

	for _, event := range events {
		if event == nil {
			continue // Skip nil events
		}

		if _, ok := seen[event.ID]; !ok {
			seen[event.ID] = struct{}{}
			result = append(result, event)
		}
	}

	return result
}

// indexName returns the single consolidated datastream name.
// Tenant isolation is now enforced at document level via tenant_ids field,
// not through separate per-tenant indexes.
func indexName() string {
	return "hermes"
}

// validateTenantID ensures the tenant ID is valid for querying.
// Returns an error if the tenant ID is empty or equals "unavailable".
//
func validateTenantID(tenantID string) error {
	if tenantID == "" {
		return ErrEmptyTenantID
	}
	if tenantID == "unavailable" {
		return ErrInvalidTenantID
	}
	return nil
}

// TruncateSlashPath truncates slash-separated paths to maxDepth levels.
// This is used for hierarchical attribute values like "service/compute/instance".
// If maxDepth is 0 or the path has no slashes, returns the path unchanged.
// Example: TruncateSlashPath("service/compute/instance", 2) returns "service/compute"
func TruncateSlashPath(path string, maxDepth int) string {
	if maxDepth == 0 || !strings.Contains(path, "/") {
		return path
	}

	parts := strings.Split(path, "/")
	if len(parts) <= maxDepth {
		return path
	}

	return strings.Join(parts[:maxDepth], "/")
}

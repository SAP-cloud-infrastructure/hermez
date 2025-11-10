// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"fmt"
	"strings"

	"github.com/sapcc/go-api-declarations/cadf"
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

// indexName generates the index name for a given tenantID.
// If tenantID is empty, queries use the audit-* wildcard (cross-tenant).
// When a tenantID is provided, only audit-<tenantID>* is queried.
func indexName(tenantID string) string {
	index := "audit-*"
	if tenantID != "" {
		index = fmt.Sprintf("audit-%s*", tenantID)
	}
	return index
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

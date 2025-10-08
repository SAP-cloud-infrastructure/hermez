// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"github.com/sapcc/go-api-declarations/cadf"
)

// RemoveDuplicates removes duplicates from a slice of strings while preserving the order.
func RemoveDuplicates(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	var result []string
	for _, v := range s {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			result = append(result, v)
		}
	}

	return result
}

// DeduplicateEvents removes duplicate events by ID while preserving order.
// First occurrence of each event is kept. This is used during hybrid query
// mode when the same event might exist in both old and new indexes.
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

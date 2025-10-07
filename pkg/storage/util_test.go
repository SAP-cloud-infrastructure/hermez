// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"reflect"
	"testing"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/stretchr/testify/assert"
)

func TestRemoveDuplicates(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "No duplicates",
			input:    []string{"apple", "banana", "cherry"},
			expected: []string{"apple", "banana", "cherry"},
		},
		{
			name:     "With duplicates",
			input:    []string{"apple", "banana", "cherry", "apple", "cherry"},
			expected: []string{"apple", "banana", "cherry"},
		},
		{
			name:     "All duplicates",
			input:    []string{"apple", "apple", "apple"},
			expected: []string{"apple"},
		},
		{
			name:     "Single element",
			input:    []string{"apple"},
			expected: []string{"apple"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := RemoveDuplicates(tt.input)
			if !reflect.DeepEqual(output, tt.expected) {
				t.Errorf("got %v, want %v", output, tt.expected)
			}
		})
	}
}

func TestDeduplicateEvents(t *testing.T) {
	tests := []struct {
		name     string
		input    []*cadf.Event
		expected int // Expected number of unique events
	}{
		{
			name: "no duplicates",
			input: []*cadf.Event{
				{ID: "event-1"},
				{ID: "event-2"},
				{ID: "event-3"},
			},
			expected: 3,
		},
		{
			name: "with duplicates",
			input: []*cadf.Event{
				{ID: "event-1"},
				{ID: "event-2"},
				{ID: "event-1"}, // Duplicate
				{ID: "event-3"},
			},
			expected: 3,
		},
		{
			name: "all duplicates",
			input: []*cadf.Event{
				{ID: "event-1"},
				{ID: "event-1"},
				{ID: "event-1"},
			},
			expected: 1,
		},
		{
			name:     "empty slice",
			input:    []*cadf.Event{},
			expected: 0,
		},
		{
			name:     "nil slice",
			input:    nil,
			expected: 0,
		},
		{
			name: "with nil events",
			input: []*cadf.Event{
				{ID: "event-1"},
				nil,
				{ID: "event-2"},
				nil,
			},
			expected: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := DeduplicateEvents(tt.input)

			assert.Len(t, result, tt.expected)

			// Verify no duplicates in result
			seen := make(map[string]bool)
			for _, event := range result {
				assert.False(t, seen[event.ID], "Found duplicate ID: %s", event.ID)
				seen[event.ID] = true
			}
		})
	}
}

func TestDeduplicateEvents_PreservesOrder(t *testing.T) {
	input := []*cadf.Event{
		{ID: "event-3", EventTime: "2025-01-03"},
		{ID: "event-1", EventTime: "2025-01-01"},
		{ID: "event-3", EventTime: "2025-01-03-DIFFERENT"}, // Duplicate, should be dropped
		{ID: "event-2", EventTime: "2025-01-02"},
	}

	result := DeduplicateEvents(input)

	// Should preserve first occurrence order
	assert.Len(t, result, 3)
	assert.Equal(t, "event-3", result[0].ID)
	assert.Equal(t, "event-1", result[1].ID)
	assert.Equal(t, "event-2", result[2].ID)

	// First occurrence of event-3 should be kept
	assert.Equal(t, "2025-01-03", result[0].EventTime)
}

func TestDeduplicateEvents_HandlesNilInMiddle(t *testing.T) {
	input := []*cadf.Event{
		{ID: "event-1"},
		nil,
		nil,
		{ID: "event-2"},
		nil,
		{ID: "event-1"}, // Duplicate
	}

	result := DeduplicateEvents(input)

	assert.Len(t, result, 2)
	assert.Equal(t, "event-1", result[0].ID)
	assert.Equal(t, "event-2", result[1].ID)
}

// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sapcc/hermes/pkg/storage"
)

func Test_GetEvent(t *testing.T) {
	eventID := "7be6c4ff-b761-5f1f-b234-f5d41616c2cd"
	event, err := GetEvent(context.Background(), eventID, "", storage.Mock{})
	require.Nil(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "7be6c4ff-b761-5f1f-b234-f5d41616c2cd", event.ID)
	assert.NotEmpty(t, event.Outcome)
	assert.NotEmpty(t, event.EventTime)
	assert.NotEmpty(t, event.Action)
}

func Test_GetEvents(t *testing.T) {
	events, total, err := GetEvents(context.Background(), &EventFilter{}, "", storage.Mock{})
	require.Nil(t, err)
	require.NotNil(t, events)
	assert.Equal(t, len(events), 4)
	assert.True(t, total >= len(events))
	for _, event := range events {
		assert.NotEmpty(t, event.ID)
		assert.NotEmpty(t, event.Outcome)
		assert.NotEmpty(t, event.Time)
		assert.NotEmpty(t, event.Initiator.ID)
		assert.NotEmpty(t, event.Initiator.Name)
		assert.NotEmpty(t, event.Initiator.TypeURI)
	}
	assert.NotEqual(t, events[0].ID, events[1].ID)
	assert.NotEqual(t, events[0].ID, events[2].ID)
}

func Test_GetAttributes(t *testing.T) {
	attributes, err := GetAttributes(context.Background(), &AttributeFilter{QueryName: "action"}, "", storage.Mock{})
	require.Nil(t, err)
	require.NotNil(t, attributes)
	assert.Equal(t, len(attributes), 6)
}

// Test_storageFilter_WindowBounds verifies the offset+limit window check,
// including the overflow-safe form. Mock.MaxLimit() is 100.
func Test_storageFilter_WindowBounds(t *testing.T) {
	store := storage.Mock{}

	// Within bounds: accepted.
	_, err := storageFilter(&EventFilter{Offset: 50, Limit: 50}, store)
	assert.NoError(t, err, "offset+limit == MaxLimit must be accepted")

	// Limit alone exceeds the maximum: rejected with the ErrWindowExceeded
	// sentinel so the API layer can map it to a 400 (client error), not a 500.
	_, err = storageFilter(&EventFilter{Offset: 0, Limit: 101}, store)
	assert.Error(t, err, "limit above MaxLimit must be rejected")
	assert.True(t, errors.Is(err, ErrWindowExceeded), "window error must wrap ErrWindowExceeded")

	// Offset pushes the window past the maximum: rejected.
	_, err = storageFilter(&EventFilter{Offset: 60, Limit: 50}, store)
	assert.Error(t, err, "offset+limit above MaxLimit must be rejected")

	// Overflow guard: an offset near the top of the uint range must be rejected,
	// not silently wrapped into a small in-bounds sum. With the naive
	// offset+limit form this would overflow to a tiny value and pass.
	const nearMax = ^uint(0) - 5 // math.MaxUint - 5
	_, err = storageFilter(&EventFilter{Offset: nearMax, Limit: 50}, store)
	assert.Error(t, err, "a near-MaxUint offset must be rejected, not wrapped")
}

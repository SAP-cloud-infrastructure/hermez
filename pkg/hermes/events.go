// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package hermes

import (
	"context"
	"fmt"

	"github.com/jinzhu/copier"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/hermes/pkg/storage"
)

// ListEvent contains high-level data about an event, intended as a list item
//
//	The JSON annotations here are for the JSON to be returned by the API
type ListEvent struct {
	ID          string            `json:"id"`
	Time        string            `json:"eventTime"`
	Action      string            `json:"action"`
	Outcome     string            `json:"outcome"`
	RequestPath string            `json:"requestPath"`
	Initiator   ResourceRef       `json:"initiator"`
	Target      ResourceRef       `json:"target"`
	Observer    ResourceRef       `json:"observer"`
	Attachments []cadf.Attachment `json:"attachments,omitempty"`
}

// ResourceRef is an embedded struct for ListEvents (eg. Initiator, Target, Observer)
type ResourceRef struct {
	TypeURI string `json:"typeURI"`
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
}

// EventFilter maps to the filtering/paging/sorting allowed by the API for Events
type EventFilter struct {
	ObserverType  string
	TargetType    string
	TargetID      string
	InitiatorID   string
	InitiatorType string
	InitiatorName string
	Action        string
	Outcome       string
	Search        string
	RequestPath   string
	Time          map[string]string
	Offset        uint
	Limit         uint
	Sort          []FieldOrder
	Details       bool // Additional Detail for eventsList func which includes attachments.
}

// FieldOrder is an embedded struct for Event Filtering
type FieldOrder struct {
	Fieldname string
	Order     string // asc or desc
}

// AttributeFilter maps to the filtering allowed by the API for Attributes
type AttributeFilter struct {
	QueryName string
	MaxDepth  uint
	Limit     uint
}

// GetEvents returns a list of matching events (with filtering)
func GetEvents(ctx context.Context, filter *EventFilter, tenantID string, eventStore storage.Storage) ([]*ListEvent, int, error) {
	storageFilter, err := storageFilter(filter, eventStore)
	if err != nil {
		return nil, 0, err
	}

	logg.Debug("hermes.GetEvents: tenant id is %s", tenantID)
	eventDetails, total, err := eventStore.GetEvents(ctx, storageFilter, tenantID)
	if err != nil {
		return nil, 0, err
	}

	events, err := eventsList(eventDetails, filter.Details)
	if err != nil {
		return nil, 0, err
	}
	return events, total, err
}

func storageFilter(filter *EventFilter, eventStore storage.Storage) (*storage.EventFilter, error) {
	// As per the documentation, the default limit is 10
	if filter.Limit == 0 {
		filter.Limit = 10
	}

	if filter.Offset+filter.Limit > eventStore.MaxLimit() {
		return nil, fmt.Errorf("offset %d plus limit %d exceeds the maximum of %d",
			filter.Offset, filter.Limit, eventStore.MaxLimit())
	}

	var storageFieldOrder []storage.FieldOrder
	err := copier.Copy(&storageFieldOrder, &filter.Sort)
	if err != nil {
		panic("Could not copy storage field order.")
	}
	storageFilter := storage.EventFilter{
		ObserverType:  filter.ObserverType,
		InitiatorID:   filter.InitiatorID,
		InitiatorType: filter.InitiatorType,
		InitiatorName: filter.InitiatorName,
		TargetType:    filter.TargetType,
		TargetID:      filter.TargetID,
		Action:        filter.Action,
		Outcome:       filter.Outcome,
		Search:        filter.Search,
		RequestPath:   filter.RequestPath,
		Time:          filter.Time,
		Offset:        filter.Offset,
		Limit:         filter.Limit,
		Sort:          storageFieldOrder,
	}
	return &storageFilter, nil
}

// eventsList Construct ListEvents
func eventsList(eventDetails []*cadf.Event, details bool) ([]*ListEvent, error) {
	var events []*ListEvent
	for _, storageEvent := range eventDetails {
		event := ListEvent{
			Initiator: ResourceRef{
				TypeURI: storageEvent.Initiator.TypeURI,
				ID:      storageEvent.Initiator.ID,
				Name:    storageEvent.Initiator.Name,
			},
			Target: ResourceRef{
				TypeURI: storageEvent.Target.TypeURI,
				ID:      storageEvent.Target.ID,
			},
			ID:          storageEvent.ID,
			Action:      string(storageEvent.Action),
			Outcome:     string(storageEvent.Outcome),
			RequestPath: storageEvent.RequestPath,
			Time:        storageEvent.EventTime,
			Observer: ResourceRef{
				TypeURI: storageEvent.Observer.TypeURI,
				ID:      storageEvent.Observer.ID,
				Name:    storageEvent.Observer.Name,
			},
		}
		if details {
			event.Attachments = storageEvent.Attachments
		}
		copiedInitiator := storageEvent.Initiator              // Create a copy of the Initiator
		err := copier.Copy(&event.Initiator, &copiedInitiator) // Use the copy as the source for the copy
		if err != nil {
			return nil, err
		}

		events = append(events, &event)
	}
	return events, nil
}

// GetEvent returns the CADF detail for event with the specified ID
func GetEvent(ctx context.Context, eventID, tenantID string, eventStore storage.Storage) (*cadf.Event, error) {
	event, err := eventStore.GetEvent(ctx, eventID, tenantID)

	return event, err
}

// GetAttributes No Logic here, but handles mock implementation for eventStore
func GetAttributes(ctx context.Context, filter *AttributeFilter, tenantID string, eventStore storage.Storage) ([]string, error) {
	attributeFilter := storage.AttributeFilter{
		QueryName: filter.QueryName,
		MaxDepth:  filter.MaxDepth,
		Limit:     filter.Limit,
	}
	attribute, err := eventStore.GetAttributes(ctx, &attributeFilter, tenantID)

	return attribute, err
}

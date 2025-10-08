// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"

	"github.com/sapcc/go-api-declarations/cadf"
)

// Status contains Prometheus status strings
// TODO: Determine if we want a similar setup for Elasticsearch.
type Status string

const (
	// StatusSuccess means success
	StatusSuccess Status = "success"
	// StatusError means error
	StatusError = "error"
)

// ErrorType enumerates different Prometheus error types
type ErrorType string

const (
	// ErrorNone means no error
	ErrorNone ErrorType = ""
	// ErrorTimeout means that a timeout occurred while processing the request
	ErrorTimeout = "timeout"
	// ErrorCanceled means that the query was cancelled (to protect the service from malicious requests)
	ErrorCanceled = "canceled"
	// ErrorExec means unspecified error happened during query execution
	ErrorExec = "execution"
	// ErrorBadData means the API parameters where invalid
	ErrorBadData = "bad_data"
	// ErrorInternal means some unspecified internal error happened
	ErrorInternal = "internal"
)

// Response encapsulates a generic response of a Prometheus API
type Response struct {
	Status    Status    `json:"status"`
	Data      []any     `json:"data,omitempty"`
	ErrorType ErrorType `json:"errorType,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Storage is an interface that wraps the underlying event storage mechanism.
// Because it is an interface, the real implementation can be mocked away in unit tests.
type Storage interface {
	/********** requests to ElasticSearch **********/
	GetEvents(ctx context.Context, filter *EventFilter, tenantID string) ([]*cadf.Event, int, error)
	GetEvent(ctx context.Context, eventID, tenantID string) (*cadf.Event, error)
	GetAttributes(ctx context.Context, filter *AttributeFilter, tenantID string) ([]string, error)
	MaxLimit() uint
}

// FieldOrder maps the sort Fieldname and Order
type FieldOrder struct {
	Fieldname string
	Order     string // asc or desc
}

// EventFilter is similar to hermes.EventFilter, but using IDs instead of names
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
}

// AttributeFilter contains parameters for filtering by attributes
type AttributeFilter struct {
	QueryName string
	MaxDepth  uint
	Limit     uint
}

// Thanks to the tool at https://mholt.github.io/json-to-go/

// eventListwithTotal contains JSON annotations for parsing the result from ElasticSearch
type eventListWithTotal struct {
	Total  int          `json:"total"`
	Events []cadf.Event `json:"events"`
}

// AttributeValueList is used for holding unique attributes
type AttributeValueList []AttributeValue

// AttributeValue contains the return values for an attribute call.
type AttributeValue struct {
	Value string `json:"value"`
	Count int64  `json:"-"` // Json to not include it in JSON return
}

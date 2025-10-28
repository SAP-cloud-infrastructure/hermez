// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/logg"
	"github.com/spf13/viper"
)

// OpenSearch contains an opensearchapi.Client we pass around after init.
type OpenSearch struct {
	osClient *opensearchapi.Client
}

func (os *OpenSearch) client() *opensearchapi.Client {
	// Lazy initialisation - don't connect to OpenSearch until we need to
	if os.osClient == nil {
		os.init()
	}
	return os.osClient
}

func (os *OpenSearch) init() {
	logg.Debug("Initializing OpenSearch()")

	var url = viper.GetString("opensearch.url")
	var username = viper.GetString("opensearch.username")
	var password = viper.GetString("opensearch.password")
	logg.Debug("Using OpenSearch URL: %s", url)
	logg.Debug("Using OpenSearch Username: %s", username)

	// Create custom HTTP transport with optimized connection pooling.
	// Default http.Transport has MaxIdleConnsPerHost=2 which is too low for production.
	// These settings are based on opensearch-go documentation recommendations.
	// ResponseHeaderTimeout is configurable via opensearch.response_header_timeout (seconds).
	// If not set, defaults to 5 seconds. Increase for high-latency environments.
	responseHeaderTimeout := viper.GetInt("opensearch.response_header_timeout")
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = 5
	}
	transport := &http.Transport{
		MaxIdleConns:          100,              // Total idle connections across all hosts
		MaxIdleConnsPerHost:   10,               // Idle connections per host (opensearch-go recommended)
		MaxConnsPerHost:       0,                // Unlimited active connections (0 = no limit)
		IdleConnTimeout:       90 * time.Second, // How long idle connections stay open
		ResponseHeaderTimeout: time.Duration(responseHeaderTimeout) * time.Second, // Timeout waiting for response headers
		ExpectContinueTimeout: 1 * time.Second,  // Timeout for 100-continue responses
		// DisableKeepAlives: false (default) - Keep-alive enabled for connection reuse
	}

	// Create client configuration
	config := opensearchapi.Config{
		Client: opensearch.Config{
			Addresses: []string{url},
			Transport: transport, // Use custom transport with better pooling
		},
	}

	// Add basic auth if credentials are provided
	if username != "" && password != "" {
		config.Client.Username = username
		config.Client.Password = password
	}

	var err error
	os.osClient, err = opensearchapi.NewClient(config)
	if err != nil {
		// TODO - Add instrumentation here for failed opensearch connection
		panic(err)
	}
}

// osFieldMapping is an alias to the shared CADFFieldMapping for consistency.
// The field mapping is defined in util.go to ensure consistency across storage backends.
var osFieldMapping = CADFFieldMapping

// buildBoolQuery constructs a bool query JSON string from filters
func buildBoolQuery(filter *EventFilter) map[string]any {
	boolQuery := map[string]any{
		"bool": map[string]any{
			"must":     []any{},
			"filter":   []any{},
			"must_not": []any{},
		},
	}

	boolClause := boolQuery["bool"].(map[string]any)

	// Helper to add filter or negation
	addFilter := func(value, fieldName string) {
		if strings.HasPrefix(value, "!") {
			// Negation: add to must_not
			value = value[1:]
			boolClause["must_not"] = append(boolClause["must_not"].([]any), map[string]any{
				"term": map[string]any{fieldName: value},
			})
		} else {
			// Normal filter: add to filter
			boolClause["filter"] = append(boolClause["filter"].([]any), map[string]any{
				"term": map[string]any{fieldName: value},
			})
		}
	}

	if filter.ObserverType != "" {
		addFilter(filter.ObserverType, osFieldMapping["observer_type"])
	}
	if filter.TargetType != "" {
		addFilter(filter.TargetType, osFieldMapping["target_type"])
	}
	if filter.TargetID != "" {
		addFilter(filter.TargetID, osFieldMapping["target_id"])
	}
	if filter.InitiatorType != "" {
		addFilter(filter.InitiatorType, osFieldMapping["initiator_type"])
	}
	if filter.InitiatorID != "" {
		addFilter(filter.InitiatorID, osFieldMapping["initiator_id"])
	}
	if filter.InitiatorName != "" {
		addFilter(filter.InitiatorName, osFieldMapping["initiator_name"])
	}
	if filter.Action != "" {
		addFilter(filter.Action, osFieldMapping["action"])
	}
	if filter.Outcome != "" {
		addFilter(filter.Outcome, osFieldMapping["outcome"])
	}
	if filter.RequestPath != "" {
		addFilter(filter.RequestPath, osFieldMapping["request_path"])
	}

	// Time range filters
	if len(filter.Time) > 0 {
		rangeQuery := map[string]any{osFieldMapping["time"]: map[string]any{}}
		timeRange := rangeQuery[osFieldMapping["time"]].(map[string]any)

		for key, value := range filter.Time {
			switch key {
			case "lt":
				timeRange["lt"] = value
			case "lte":
				timeRange["lte"] = value
			case "gt":
				timeRange["gt"] = value
			case "gte":
				timeRange["gte"] = value
			}
		}

		boolClause["filter"] = append(boolClause["filter"].([]any), map[string]any{"range": rangeQuery})
	}

	// Full-text search
	if filter.Search != "" {
		boolClause["must"] = append(boolClause["must"].([]any), map[string]any{
			"query_string": map[string]any{
				"query": filter.Search,
			},
		})
	}

	return boolQuery
}

// GetEvents grabs events for a given tenantID with filtering.
func (os OpenSearch) GetEvents(ctx context.Context, filter *EventFilter, tenantID string) ([]*cadf.Event, int, error) {
	index := indexName(tenantID)
	logg.Debug("Looking for events in index %s", index)

	// Build the query
	query := buildBoolQuery(filter)

	// Build the complete search body
	searchBody := map[string]any{
		"query": query,
	}

	// Add sorting
	var sortArray []any
	if filter.Sort != nil {
		for _, fieldOrder := range filter.Sort {
			sortOrder := "desc"
			if fieldOrder.Order == "asc" {
				sortOrder = "asc"
			}
			sortArray = append(sortArray, map[string]any{
				osFieldMapping[fieldOrder.Fieldname]: map[string]any{"order": sortOrder},
			})
		}
	}

	// Always sort by time descending as default
	sortArray = append(sortArray, map[string]any{
		osFieldMapping["time"]: map[string]any{"order": "desc"},
	})
	searchBody["sort"] = sortArray

	// Add pagination
	offset := min(filter.Offset, math.MaxInt32)
	limit := min(filter.Limit, math.MaxInt32)
	searchBody["from"] = offset
	searchBody["size"] = limit

	// Convert to JSON
	bodyJSON, err := json.Marshal(searchBody)
	if err != nil {
		return nil, 0, err
	}

	logg.Debug("OpenSearch query: %s", string(bodyJSON))

	// Execute search
	searchResp, err := os.client().Search(ctx, &opensearchapi.SearchReq{
		Indices: []string{index},
		Body:    bytes.NewReader(bodyJSON),
	})

	if err != nil {
		if osErr, ok := errext.As[*opensearch.StructError](err); ok {
			errdetails, _ := json.Marshal(osErr) //nolint:errcheck
			logg.Error("OpenSearch failed with error %s", errdetails)
		} else {
			logg.Error("Unknown error occurred: %v", err)
		}
		return nil, 0, err
	}

	logg.Debug("Got %d hits", searchResp.Hits.Total.Value)

	// Parse events from hits
	var events []*cadf.Event
	for _, hit := range searchResp.Hits.Hits {
		var de cadf.Event
		err := json.Unmarshal(hit.Source, &de)
		if err != nil {
			return nil, 0, err
		}
		events = append(events, &de)
	}

	total := searchResp.Hits.Total.Value

	return events, total, nil
}

// GetEvent Returns EventDetail for a single event.
func (os OpenSearch) GetEvent(ctx context.Context, eventID, tenantID string) (*cadf.Event, error) {
	index := indexName(tenantID)
	logg.Debug("Looking for event %s in index %s", eventID, index)

	// Build term query for exact ID match
	queryBody := map[string]any{
		"query": map[string]any{
			"term": map[string]any{
				"id": eventID,
			},
		},
	}

	bodyJSON, err := json.Marshal(queryBody)
	if err != nil {
		return nil, err
	}

	logg.Debug("Query: %s", string(bodyJSON))

	searchResp, err := os.client().Search(ctx, &opensearchapi.SearchReq{
		Indices: []string{index},
		Body:    bytes.NewReader(bodyJSON),
	})

	if err != nil {
		logg.Debug("Query failed: %s", err.Error())
		return nil, err
	}

	total := searchResp.Hits.Total.Value
	logg.Debug("Results: %d", total)

	if total > 0 {
		hit := searchResp.Hits.Hits[0]
		var de cadf.Event
		err := json.Unmarshal(hit.Source, &de)
		return &de, err
	}
	return nil, nil
}

// GetAttributes Return all unique attributes available for filtering
func (os OpenSearch) GetAttributes(ctx context.Context, filter *AttributeFilter, tenantID string) ([]string, error) {
	index := indexName(tenantID)

	logg.Debug("Looking for unique attributes for %s in index %s", filter.QueryName, index)

	// Map query name to OpenSearch field
	var osName string
	if val, ok := osFieldMapping[filter.QueryName]; ok {
		osName = val
	} else {
		osName = filter.QueryName
	}
	logg.Debug("Mapped Queryname: %s --> %s", filter.QueryName, osName)

	limit := min(filter.Limit, math.MaxInt32)

	// Build aggregation query
	searchBody := map[string]any{
		"size": 0, // We don't need the actual documents, just aggregations
		"aggs": map[string]any{
			"attributes": map[string]any{
				"terms": map[string]any{
					"field": osName,
					"size":  limit,
				},
			},
		},
	}

	bodyJSON, err := json.Marshal(searchBody)
	if err != nil {
		return nil, err
	}

	logg.Debug("OpenSearch aggregation query: %s", string(bodyJSON))

	searchResp, err := os.client().Search(ctx, &opensearchapi.SearchReq{
		Indices: []string{index},
		Body:    bytes.NewReader(bodyJSON),
	})

	if err != nil {
		if osErr, ok := errext.As[*opensearch.StructError](err); ok {
			errdetails, _ := json.Marshal(osErr) //nolint:errcheck
			logg.Error("OpenSearch failed with error %s", errdetails)
		} else {
			logg.Error("Unknown error occurred: %v", err)
		}
		return nil, err
	}

	// Parse aggregations
	var aggResult struct {
		Attributes struct {
			Buckets []struct {
				Key      any   `json:"key"`
				DocCount int64 `json:"doc_count"`
			} `json:"buckets"`
		} `json:"attributes"`
	}

	if err := json.Unmarshal(searchResp.Aggregations, &aggResult); err != nil {
		logg.Error("Failed to parse aggregations: %w", err)
		return nil, err
	}

	logg.Debug("Number of Buckets: %d", len(aggResult.Attributes.Buckets))

	// Enforce lower and upper bound before converting to int
	var maxDepth int
	if filter.MaxDepth > 0 && filter.MaxDepth <= math.MaxInt32 {
		maxDepth = int(filter.MaxDepth)
	} else {
		maxDepth = int(math.MaxInt32)
	}

	var unique []string
	for _, bucket := range aggResult.Attributes.Buckets {
		logg.Debug("key: %v count: %d", bucket.Key, bucket.DocCount)

		// Convert key to string
		attribute := fmt.Sprintf("%v", bucket.Key)

		// Hierarchical Depth Handling
		attribute = TruncateSlashPath(attribute, maxDepth)

		unique = append(unique, attribute)
	}

	slices.Sort(unique)
	unique = slices.Compact(unique)
	return unique, nil
}

// MaxLimit grabs the configured maxlimit for results
func (os OpenSearch) MaxLimit() uint {
	maxLimit := viper.GetInt("opensearch.max_result_window")
	if maxLimit < 0 {
		return 0
	}
	return uint(maxLimit)
}

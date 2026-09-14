// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestBuildBoolQuery_TenantFiltering(t *testing.T) {
	emptyFilter := &EventFilter{}

	// Normal tenant ID should include tenant_ids filter with correct value
	query := buildBoolQuery(emptyFilter, "some-project-id")
	boolClause := query["bool"].(map[string]any)
	filters := boolClause["filter"].([]any)
	assert.Len(t, filters, 1, "expected exactly one tenant filter")
	termFilter := filters[0].(map[string]any)["term"].(map[string]any)
	assert.Equal(t, "some-project-id", termFilter["tenant_ids"], "tenant_ids filter should match the provided tenant ID")

	// AllTenants should omit tenant_ids filter
	query = buildBoolQuery(emptyFilter, AllTenants)
	boolClause = query["bool"].(map[string]any)
	filters = boolClause["filter"].([]any)
	assert.Empty(t, filters, "expected no tenant_ids filter for AllTenants")
}

func TestOpenSearchRejectsPartialResults(t *testing.T) {
	tests := []struct {
		name string
		call func(*OpenSearch) error
	}{
		{
			name: "events",
			call: func(os *OpenSearch) error {
				_, _, err := os.GetEvents(context.Background(), &EventFilter{Limit: 1}, "tenant-a")
				return err
			},
		},
		{
			name: "event",
			call: func(os *OpenSearch) error {
				_, err := os.GetEvent(context.Background(), "event-id", "tenant-a")
				return err
			},
		},
		{
			name: "attributes",
			call: func(os *OpenSearch) error {
				_, err := os.GetAttributes(context.Background(), &AttributeFilter{QueryName: "action", Limit: 1}, "tenant-a")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os := openSearchTestClient(t, `{"timed_out":false,"_shards":{"failed":1},"hits":{"total":{"value":1},"hits":[]}}`, func(map[string]any) {})
			assert.ErrorIs(t, tt.call(os), ErrPartialResults)
		})
	}
}

// TestOpenSearchTimeoutResponsesAreRejected verifies that the real OpenSearch
// v4 client decodes a successful HTTP response with timed_out=true and that
// neither query path can return the partial payload as a successful result.
func TestOpenSearchTimeoutResponsesAreRejected(t *testing.T) {
	tests := []struct {
		name     string
		response string
		call     func(*OpenSearch) (any, error)
	}{
		{
			name: "events",
			response: `{
				"timed_out": true,
				"_shards": {"failed": 0},
				"hits": {"total": {"value": 1}, "hits": []}
			}`,
			call: func(os *OpenSearch) (any, error) {
				events, _, err := os.GetEvents(context.Background(), &EventFilter{Limit: 1}, "tenant-a")
				return events, err
			},
		},
		{
			name: "attributes",
			response: `{
				"timed_out": true,
				"_shards": {"failed": 0},
				"aggregations": {"attributes": {"buckets": [{"key": "partial", "doc_count": 1}]}}
			}`,
			call: func(os *OpenSearch) (any, error) {
				attributes, err := os.GetAttributes(context.Background(), &AttributeFilter{QueryName: "action", Limit: 1}, "tenant-a")
				return attributes, err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os := openSearchTestClient(t, tt.response, func(map[string]any) {})
			result, err := tt.call(os)

			assert.ErrorIs(t, err, ErrPartialResults)
			assert.Nil(t, result, "timed-out searches must not return a partial result")
		})
	}
}

func TestOpenSearchAttributeLimitIsCapped(t *testing.T) {
	previousMaxLimit := viper.GetInt("opensearch.max_result_window")
	viper.Set("opensearch.max_result_window", 7)
	t.Cleanup(func() { viper.Set("opensearch.max_result_window", previousMaxLimit) })

	os := openSearchTestClient(t, `{"timed_out":false,"_shards":{"failed":0},"aggregations":{"attributes":{"buckets":[]}}}`, func(body map[string]any) {
		aggs := body["aggs"].(map[string]any)["attributes"].(map[string]any)["terms"].(map[string]any)
		assert.Equal(t, float64(7), aggs["size"])
	})
	attributes, err := os.GetAttributes(context.Background(), &AttributeFilter{QueryName: "action", Limit: 100}, "tenant-a")
	assert.NoError(t, err)
	assert.Empty(t, attributes)
}

func openSearchTestClient(t *testing.T, response string, checkBody func(map[string]any)) *OpenSearch {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("allow_partial_search_results"); got != "false" {
			t.Errorf("allow_partial_search_results = %q, want false", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode search request: %v", err)
		}
		checkBody(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(response)); err != nil {
			t.Errorf("write search response: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client, err := opensearchapi.NewClient(opensearchapi.Config{Client: opensearch.Config{Addresses: []string{server.URL}}})
	if err != nil {
		t.Fatalf("create OpenSearch test client: %v", err)
	}
	os := &OpenSearch{osClient: client}
	os.initOnce.Do(func() {})
	return os
}

func TestBuildGetEventQuery_TenantFiltering(t *testing.T) {
	// Normal tenant: query should have bool.must with event ID and bool.filter with tenant_ids
	query := buildGetEventQuery("some-event-id", "some-project-id")
	boolClause := query["query"].(map[string]any)["bool"].(map[string]any)

	// Verify event ID in must clause
	mustClauses := boolClause["must"].([]any)
	assert.Len(t, mustClauses, 1, "expected one must clause for event ID")
	eventTerm := mustClauses[0].(map[string]any)["term"].(map[string]any)
	assert.Equal(t, "some-event-id", eventTerm["id"], "must clause should match the event ID")

	// Verify tenant filter content
	filterClauses := boolClause["filter"].([]any)
	assert.Len(t, filterClauses, 1, "expected one filter clause for tenant_ids")
	tenantTerm := filterClauses[0].(map[string]any)["term"].(map[string]any)
	assert.Equal(t, "some-project-id", tenantTerm["tenant_ids"], "filter should match the provided tenant ID")

	// AllTenants: query should NOT have filter key, but must still have event ID
	query = buildGetEventQuery("some-event-id", AllTenants)
	boolClause = query["query"].(map[string]any)["bool"].(map[string]any)
	_, hasFilter := boolClause["filter"]
	assert.False(t, hasFilter, "expected no tenant_ids filter for AllTenants")

	mustClauses = boolClause["must"].([]any)
	assert.Len(t, mustClauses, 1, "must clause should still be present for AllTenants")
}

func TestBuildGetAttributesQuery_TenantFiltering(t *testing.T) {
	// Normal tenant: search body should have query with tenant filter and aggs
	body := buildGetAttributesQuery("action.keyword", 100, "some-project-id")

	// Verify aggs present and correct
	aggs := body["aggs"].(map[string]any)["attributes"].(map[string]any)["terms"].(map[string]any)
	assert.Equal(t, "action.keyword", aggs["field"], "aggregation should use the provided field name")
	assert.Equal(t, uint(100), aggs["size"], "aggregation should use the provided limit")

	// Verify tenant filter content
	queryClause := body["query"].(map[string]any)["bool"].(map[string]any)
	filterClauses := queryClause["filter"].([]any)
	assert.Len(t, filterClauses, 1)
	tenantTerm := filterClauses[0].(map[string]any)["term"].(map[string]any)
	assert.Equal(t, "some-project-id", tenantTerm["tenant_ids"], "filter should match the provided tenant ID")

	// AllTenants: search body should NOT have "query" key but should still have aggs
	body = buildGetAttributesQuery("action.keyword", 100, AllTenants)
	_, hasQuery := body["query"]
	assert.False(t, hasQuery, "expected no query for AllTenants")

	_, hasAggs := body["aggs"]
	assert.True(t, hasAggs, "expected aggs in AllTenants search body")
}

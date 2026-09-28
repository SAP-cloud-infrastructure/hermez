// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/errmask"
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

// TestOpenSearchRejectsPartialResults verifies that a search response with
// _shards.failed > 0 maps to ErrPartialResults on every query path, both with
// the error mask hermes configures (shard failures masked, detected from the
// response body) and with the v5 default mask (shard failures returned by the
// client as *opensearchapi.PartialSearchError).
func TestOpenSearchRejectsPartialResults(t *testing.T) {
	calls := []struct {
		name string
		call func(*OpenSearch) error
	}{
		{
			name: "events",
			call: func(os *OpenSearch) error {
				_, _, err := os.GetEvents(t.Context(), &EventFilter{Limit: 1}, "tenant-a")
				return err
			},
		},
		{
			name: "event",
			call: func(os *OpenSearch) error {
				_, err := os.GetEvent(t.Context(), "event-id", "tenant-a")
				return err
			},
		},
		{
			name: "attributes",
			call: func(os *OpenSearch) error {
				_, err := os.GetAttributes(t.Context(), &AttributeFilter{QueryName: "action", Limit: 1}, "tenant-a")
				return err
			},
		},
	}
	masks := []struct {
		name string
		mask *errmask.ErrorMask // nil keeps the mask set by newOpenSearchClient
	}{
		{name: "hermes mask", mask: nil},
		{name: "v5 default mask", mask: errmask.New(errmask.Empty)},
	}

	const response = `{
		"timed_out": false,
		"_shards": {"total": 2, "successful": 1, "failed": 1, "failures": [{"shard": 0, "index": "hermes", "reason": {"type": "exception", "reason": "boom"}}]},
		"hits": {"total": {"value": 1}, "hits": []},
		"aggregations": {"attributes": {"buckets": [{"key": "partial", "doc_count": 1}]}}
	}`

	for _, m := range masks {
		for _, c := range calls {
			t.Run(m.name+"/"+c.name, func(t *testing.T) {
				os := openSearchTestClient(t, response, func(map[string]any) {})
				if m.mask != nil {
					if err := os.osClient.SetErrorMask(errmask.All, *m.mask); err != nil {
						t.Fatalf("set error mask: %v", err)
					}
				}
				assert.ErrorIs(t, c.call(os), ErrPartialResults)
			})
		}
	}
}

// TestOpenSearchParsesResponses checks hit totals (object and integer form),
// event decoding and attribute aggregation parsing on a complete response.
func TestOpenSearchParsesResponses(t *testing.T) {
	for _, total := range []string{`{"value": 42, "relation": "eq"}`, `42`} {
		t.Run("events total "+total, func(t *testing.T) {
			os := openSearchTestClient(t, `{
				"timed_out": false,
				"_shards": {"total": 1, "successful": 1, "failed": 0},
				"hits": {"total": `+total+`, "hits": [{"_index": "hermes", "_id": "a", "_source": {"id": "7d4f", "action": "create"}}]}
			}`, func(map[string]any) {})
			events, gotTotal, err := os.GetEvents(t.Context(), &EventFilter{Limit: 1}, "tenant-a")
			assert.NoError(t, err)
			assert.Equal(t, 42, gotTotal)
			if assert.Len(t, events, 1) {
				assert.Equal(t, "7d4f", events[0].ID)
				assert.Equal(t, "create", string(events[0].Action))
			}
		})
	}

	t.Run("event", func(t *testing.T) {
		os := openSearchTestClient(t, `{
			"timed_out": false,
			"_shards": {"total": 1, "successful": 1, "failed": 0},
			"hits": {"total": {"value": 1}, "hits": [{"_index": "hermes", "_id": "a", "_source": {"id": "7d4f"}}]}
		}`, func(map[string]any) {})
		event, err := os.GetEvent(t.Context(), "7d4f", "tenant-a")
		assert.NoError(t, err)
		if assert.NotNil(t, event) {
			assert.Equal(t, "7d4f", event.ID)
		}
	})

	t.Run("attributes", func(t *testing.T) {
		os := openSearchTestClient(t, `{
			"timed_out": false,
			"_shards": {"total": 1, "successful": 1, "failed": 0},
			"hits": {"total": {"value": 5}, "hits": []},
			"aggregations": {"attributes": {"doc_count_error_upper_bound": 0, "sum_other_doc_count": 0, "buckets": [
				{"key": "compute/server", "doc_count": 3},
				{"key": "compute/server/volume", "doc_count": 1},
				{"key": "block-storage", "doc_count": 1}
			]}}
		}`, func(map[string]any) {})
		attributes, err := os.GetAttributes(t.Context(), &AttributeFilter{QueryName: "target_type", Limit: 10, MaxDepth: 2}, "tenant-a")
		assert.NoError(t, err)
		assert.Equal(t, []string{"block-storage", "compute/server"}, attributes)
	})
}

// TestOpenSearchTimeoutResponsesAreRejected verifies that the real OpenSearch
// v5 client decodes a successful HTTP response with timed_out=true and that
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
				events, _, err := os.GetEvents(t.Context(), &EventFilter{Limit: 1}, "tenant-a")
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
				attributes, err := os.GetAttributes(t.Context(), &AttributeFilter{QueryName: "action", Limit: 1}, "tenant-a")
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
	attributes, err := os.GetAttributes(t.Context(), &AttributeFilter{QueryName: "action", Limit: 100}, "tenant-a")
	assert.NoError(t, err)
	assert.Empty(t, attributes)
}

// openSearchTestClient returns an OpenSearch backed by the production client
// config, pointed at a test server that answers every search with response.
func openSearchTestClient(t *testing.T, response string, checkBody func(map[string]any)) *OpenSearch {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hermes/_search" {
			// The v5 transport health-checks the seed URL and polls its node
			// stats in the background. Node discovery must stay off.
			if strings.HasPrefix(r.URL.Path, "/_nodes/http") || r.URL.Path == "/_nodes" {
				t.Errorf("unexpected node discovery request: %s %s", r.Method, r.URL)
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
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

	client, err := newOpenSearchClient(server.URL, "", "", http.DefaultTransport)
	if err != nil {
		t.Fatalf("create OpenSearch test client: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close OpenSearch test client: %v", err)
		}
	})
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

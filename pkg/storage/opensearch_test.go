// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"

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

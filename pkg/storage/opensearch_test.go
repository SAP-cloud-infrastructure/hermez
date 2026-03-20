// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildBoolQuery_TenantFiltering(t *testing.T) {
	emptyFilter := &EventFilter{}

	// Normal tenant ID should include tenant_ids filter
	query := buildBoolQuery(emptyFilter, "some-project-id")
	boolClause := query["bool"].(map[string]any)
	filters := boolClause["filter"].([]any)
	assert.NotEmpty(t, filters, "expected tenant_ids filter for normal tenant ID")

	// AllTenants should omit tenant_ids filter
	query = buildBoolQuery(emptyFilter, AllTenants)
	boolClause = query["bool"].(map[string]any)
	filters = boolClause["filter"].([]any)
	assert.Empty(t, filters, "expected no tenant_ids filter for AllTenants")
}

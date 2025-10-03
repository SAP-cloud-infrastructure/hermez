// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"errors"
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/assert"
)

func TestStorageError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *StorageError
		expected string
	}{
		{
			name: "error with cause",
			err: &StorageError{
				Code:    ErrorCodeQuerySyntax,
				Message: "Invalid query syntax",
				Cause:   errors.New("parse exception"),
			},
			expected: "QUERY_SYNTAX_ERROR: Invalid query syntax (caused by: parse exception)",
		},
		{
			name: "error without cause",
			err: &StorageError{
				Code:    ErrorCodeIndexNotFound,
				Message: "Index not found",
			},
			expected: "INDEX_NOT_FOUND: Index not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, "error message", tt.err.Error(), tt.expected)
		})
	}
}

func TestStorageError_Unwrap(t *testing.T) {
	cause := errors.New("underlying error")
	err := &StorageError{
		Code:    ErrorCodeTimeout,
		Message: "Query timeout",
		Cause:   cause,
	}

	assert.DeepEqual(t, "unwrapped error", errors.Unwrap(err), cause)
}

func TestStorageError_ToAPIResponse(t *testing.T) {
	tests := []struct {
		name     string
		err      *StorageError
		expected map[string]any
	}{
		{
			name: "error without details",
			err: &StorageError{
				Code:    ErrorCodeQuerySyntax,
				Message: "Invalid query syntax",
			},
			expected: map[string]any{
				"error": map[string]any{
					"code":    "QUERY_SYNTAX_ERROR",
					"message": "Invalid query syntax",
				},
			},
		},
		{
			name: "error with details",
			err: &StorageError{
				Code:    ErrorCodeResourceExhausted,
				Message: "Query requires too much memory",
				Details: map[string]any{
					"hint": "Try using more specific filters",
				},
			},
			expected: map[string]any{
				"error": map[string]any{
					"code":    "RESOURCE_EXHAUSTED",
					"message": "Query requires too much memory",
					"details": map[string]any{
						"hint": "Try using more specific filters",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.DeepEqual(t, "API response", tt.err.ToAPIResponse(), tt.expected)
		})
	}
}

func TestNewQuerySyntaxError(t *testing.T) {
	tests := []struct {
		name       string
		cause      error
		queryField string
		expected   *StorageError
	}{
		{
			name:       "with query field",
			cause:      errors.New("parse error"),
			queryField: "search",
			expected: &StorageError{
				Code:       ErrorCodeQuerySyntax,
				Message:    "Invalid query syntax in field 'search'",
				HTTPStatus: http.StatusBadRequest,
				Details: map[string]any{
					"field": "search",
				},
				Cause: errors.New("parse error"),
			},
		},
		{
			name:  "without query field",
			cause: errors.New("parse error"),
			expected: &StorageError{
				Code:       ErrorCodeQuerySyntax,
				Message:    "The search query contains invalid syntax",
				HTTPStatus: http.StatusBadRequest,
				Details:    map[string]any{},
				Cause:      errors.New("parse error"),
			},
		},
		{
			name:       "with parse_exception hint",
			cause:      errors.New("parse_exception: failed to parse"),
			queryField: "search",
			expected: &StorageError{
				Code:       ErrorCodeQuerySyntax,
				Message:    "Invalid query syntax in field 'search'",
				HTTPStatus: http.StatusBadRequest,
				Details: map[string]any{
					"field": "search",
					"hint":  "Please check for unmatched quotes, brackets, or invalid operators",
				},
				Cause: errors.New("parse_exception: failed to parse"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NewQuerySyntaxError(tt.cause, tt.queryField)
			assert.DeepEqual(t, "error code", result.Code, tt.expected.Code)
			assert.DeepEqual(t, "error message", result.Message, tt.expected.Message)
			assert.DeepEqual(t, "HTTP status", result.HTTPStatus, tt.expected.HTTPStatus)
			assert.DeepEqual(t, "error details", result.Details, tt.expected.Details)
		})
	}
}

func TestNewIndexNotFoundError(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		expected string
	}{
		{
			name:     "with tenant ID",
			tenantID: "project-123",
			expected: "No audit events found for this project",
		},
		{
			name:     "without tenant ID",
			tenantID: "",
			expected: "No audit events found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewIndexNotFoundError(errors.New("index not found"), tt.tenantID)
			assert.DeepEqual(t, "error message", err.Message, tt.expected)
			assert.DeepEqual(t, "HTTP status", err.HTTPStatus, http.StatusNotFound)
		})
	}
}

func TestNewTimeoutError(t *testing.T) {
	cause := errors.New("timeout")
	err := NewTimeoutError(cause)

	assert.DeepEqual(t, "error code", err.Code, ErrorCodeTimeout)
	assert.DeepEqual(t, "HTTP status", err.HTTPStatus, http.StatusGatewayTimeout)
	assert.DeepEqual(t, "hint present", err.Details["hint"] != nil, true)
}

func TestNewResourceExhaustedError(t *testing.T) {
	tests := []struct {
		name         string
		resourceType string
		expectedMsg  string
		expectedHint string
	}{
		{
			name:         "memory resource",
			resourceType: "memory",
			expectedMsg:  "Query requires too much memory to execute",
			expectedHint: "Try using more specific filters to reduce the result set",
		},
		{
			name:         "result window resource",
			resourceType: "result_window",
			expectedMsg:  "Query result set is too large",
			expectedHint: "Use pagination with smaller limit values",
		},
		{
			name:         "unknown resource",
			resourceType: "unknown",
			expectedMsg:  "Query exceeded resource limits",
			expectedHint: "Try narrowing your search criteria",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewResourceExhaustedError(errors.New("resource exhausted"), tt.resourceType)
			assert.DeepEqual(t, "error message", err.Message, tt.expectedMsg)
			assert.DeepEqual(t, "error hint", err.Details["hint"].(string), tt.expectedHint)
			assert.DeepEqual(t, "HTTP status", err.HTTPStatus, http.StatusBadRequest)
		})
	}
}

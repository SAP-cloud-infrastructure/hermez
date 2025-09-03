// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"errors"
	"net/http"
	"testing"

	elastic "github.com/olivere/elastic/v7"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/errext"
)

func TestWrapElasticsearchError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		tenantID     string
		searchQuery  string
		expectedCode ErrorCode
		expectedHTTP int
	}{
		{
			name: "nil error",
			err:  nil,
		},
		{
			name: "parse error",
			err: &elastic.Error{
				Status: http.StatusBadRequest,
			},
			searchQuery:  "invalid:query",
			expectedCode: ErrorCodeQuerySyntax,
			expectedHTTP: http.StatusBadRequest,
		},
		{
			name: "index not found",
			err: &elastic.Error{
				Status: http.StatusNotFound,
			},
			tenantID:     "project-123",
			expectedCode: ErrorCodeIndexNotFound,
			expectedHTTP: http.StatusNotFound,
		},
		{
			name: "timeout error",
			err: &elastic.Error{
				Status: http.StatusRequestTimeout,
			},
			expectedCode: ErrorCodeTimeout,
			expectedHTTP: http.StatusGatewayTimeout,
		},
		{
			name: "rate limit error",
			err: &elastic.Error{
				Status: http.StatusTooManyRequests,
			},
			expectedCode: ErrorCodeTooManyRequests,
			expectedHTTP: http.StatusTooManyRequests,
		},
		{
			name: "service unavailable",
			err: &elastic.Error{
				Status: http.StatusServiceUnavailable,
			},
			expectedCode: ErrorCodeConnectionFailure,
			expectedHTTP: http.StatusServiceUnavailable,
		},
		{
			name: "too many buckets error",
			err: &elastic.Error{
				Status: http.StatusBadRequest,
			},
			expectedCode: ErrorCodeQuerySyntax, // Will be treated as generic bad request
			expectedHTTP: http.StatusBadRequest,
		},
		{
			name:         "connection refused",
			err:          errors.New("connection refused"),
			expectedCode: ErrorCodeConnectionFailure,
			expectedHTTP: http.StatusServiceUnavailable,
		},
		{
			name:         "timeout in error message",
			err:          errors.New("request timeout"),
			expectedCode: ErrorCodeConnectionFailure,
			expectedHTTP: http.StatusServiceUnavailable,
		},
		{
			name:         "unknown error",
			err:          errors.New("something went wrong"),
			expectedCode: ErrorCodeInternalError,
			expectedHTTP: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := wrapElasticsearchError(tt.err, tt.tenantID, tt.searchQuery)

			if tt.err == nil {
				assert.DeepEqual(t, "nil result", result, nil)
				return
			}

			storageErr, ok := errext.As[*StorageError](result)
			assert.DeepEqual(t, "is StorageError", ok, true)
			assert.DeepEqual(t, "error code", storageErr.Code, tt.expectedCode)
			assert.DeepEqual(t, "HTTP status", storageErr.HTTPStatus, tt.expectedHTTP)
		})
	}
}

func TestWrapElasticsearchError_PreservesContext(t *testing.T) {
	elasticErr := &elastic.Error{
		Status: http.StatusBadRequest,
	}

	// Test that the original error is preserved
	wrapped := wrapElasticsearchError(elasticErr, "tenant-123", "search query")
	storageErr, ok := errext.As[*StorageError](wrapped)
	assert.DeepEqual(t, "is StorageError", ok, true)
	assert.DeepEqual(t, "has cause", storageErr.Cause != nil, true)
}

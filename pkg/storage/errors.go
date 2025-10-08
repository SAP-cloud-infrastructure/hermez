// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"fmt"
	"net/http"
	"strings"
)

// ErrorCode represents standardized error codes for storage operations
type ErrorCode string

const (
	// ErrorCodeQuerySyntax indicates malformed query syntax
	ErrorCodeQuerySyntax ErrorCode = "QUERY_SYNTAX_ERROR"
	// ErrorCodeIndexNotFound indicates the requested index doesn't exist
	ErrorCodeIndexNotFound ErrorCode = "INDEX_NOT_FOUND"
	// ErrorCodeTimeout indicates the query took too long to execute
	ErrorCodeTimeout ErrorCode = "QUERY_TIMEOUT"
	// ErrorCodeTooManyRequests indicates rate limiting
	ErrorCodeTooManyRequests ErrorCode = "TOO_MANY_REQUESTS"
	// ErrorCodeConnectionFailure indicates inability to connect to storage
	ErrorCodeConnectionFailure ErrorCode = "CONNECTION_FAILURE"
	// ErrorCodeInternalError indicates an unexpected storage error
	ErrorCodeInternalError ErrorCode = "INTERNAL_ERROR"
	// ErrorCodeResourceExhausted indicates query resource limits exceeded
	ErrorCodeResourceExhausted ErrorCode = "RESOURCE_EXHAUSTED"
)

// StorageError provides structured error information for storage operations.
// It wraps the underlying error while providing user-friendly messages.
type StorageError struct {
	// Code is a machine-readable error code
	Code ErrorCode
	// Message is a user-friendly error message
	Message string
	// HTTPStatus is the suggested HTTP status code for this error
	HTTPStatus int
	// Details provides additional context (optional)
	Details map[string]any
	// Cause is the underlying error
	Cause error
}

// Error implements the error interface
func (e *StorageError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap implements the errors.Unwrap interface
func (e *StorageError) Unwrap() error {
	return e.Cause
}

// ToAPIResponse creates a safe response for API consumers
func (e *StorageError) ToAPIResponse() map[string]any {
	response := map[string]any{
		"error": map[string]any{
			"code":    string(e.Code),
			"message": e.Message,
		},
	}

	if len(e.Details) > 0 {
		if errorMap, ok := response["error"].(map[string]any); ok {
			errorMap["details"] = e.Details
		}
	}
	return response
}

// NewQuerySyntaxError creates an error for malformed queries
func NewQuerySyntaxError(cause error, queryField string) *StorageError {
	message := "The search query contains invalid syntax"
	details := make(map[string]any)

	if queryField != "" {
		message = fmt.Sprintf("Invalid query syntax in field '%s'", queryField)
		details["field"] = queryField
	}

	// Extract helpful hints from Elasticsearch error if available
	if cause != nil && strings.Contains(cause.Error(), "parse_exception") {
		details["hint"] = "Please check for unmatched quotes, brackets, or invalid operators"
	}

	return &StorageError{
		Code:       ErrorCodeQuerySyntax,
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
		Details:    details,
		Cause:      cause,
	}
}

// NewIndexNotFoundError creates an error when the index doesn't exist
func NewIndexNotFoundError(cause error, tenantID string) *StorageError {
	message := "No audit events found for this project"
	if tenantID == "" {
		message = "No audit events found"
	}

	return &StorageError{
		Code:       ErrorCodeIndexNotFound,
		Message:    message,
		HTTPStatus: http.StatusNotFound,
		Details: map[string]any{
			"hint": "This project may not have any audit events yet, or you may not have access to view them",
		},
		Cause: cause,
	}
}

// NewTimeoutError creates an error for query timeouts
func NewTimeoutError(cause error) *StorageError {
	return &StorageError{
		Code:       ErrorCodeTimeout,
		Message:    "The query took too long to execute. Please try narrowing your search criteria",
		HTTPStatus: http.StatusGatewayTimeout,
		Details: map[string]any{
			"hint": "Try using more specific filters or a smaller time range",
		},
		Cause: cause,
	}
}

// NewRateLimitError creates an error for rate limiting
func NewRateLimitError(cause error) *StorageError {
	return &StorageError{
		Code:       ErrorCodeTooManyRequests,
		Message:    "Too many requests. Please wait a moment before trying again",
		HTTPStatus: http.StatusTooManyRequests,
		Cause:      cause,
	}
}

// NewConnectionError creates an error for connection failures
func NewConnectionError(cause error) *StorageError {
	return &StorageError{
		Code:       ErrorCodeConnectionFailure,
		Message:    "Unable to retrieve audit events. Please try again later",
		HTTPStatus: http.StatusServiceUnavailable,
		Details: map[string]any{
			"hint": "The audit service is temporarily unavailable",
		},
		Cause: cause,
	}
}

// NewResourceExhaustedError creates an error for resource limit violations
func NewResourceExhaustedError(cause error, resourceType string) *StorageError {
	message := "Query exceeded resource limits"
	details := make(map[string]any)

	switch resourceType {
	case "memory":
		message = "Query requires too much memory to execute"
		details["hint"] = "Try using more specific filters to reduce the result set"
	case "result_window":
		message = "Query result set is too large"
		details["hint"] = "Use pagination with smaller limit values"
	default:
		details["hint"] = "Try narrowing your search criteria"
	}

	return &StorageError{
		Code:       ErrorCodeResourceExhausted,
		Message:    message,
		HTTPStatus: http.StatusBadRequest,
		Details:    details,
		Cause:      cause,
	}
}

// NewInternalError creates a generic internal error
func NewInternalError(cause error) *StorageError {
	return &StorageError{
		Code:       ErrorCodeInternalError,
		Message:    "An unexpected error occurred while processing your request",
		HTTPStatus: http.StatusInternalServerError,
		Cause:      cause,
	}
}

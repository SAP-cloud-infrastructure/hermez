<!--
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
SPDX-License-Identifier: Apache-2.0
-->

# Hermes Error Handling Examples

This document provides examples of the error messages returned by Hermes when Elasticsearch queries fail.

## Error Response Format

All storage-related errors follow a consistent JSON structure:

```json
{
  "error": {
    "code": "ERROR_CODE",
    "message": "User-friendly error message",
    "details": {
      "hint": "Helpful suggestion for resolving the issue"
    }
  }
}
```

## Common Error Scenarios

### 1. Query Syntax Error (400 Bad Request)

When a search query contains invalid syntax:

```json
{
  "error": {
    "code": "QUERY_SYNTAX_ERROR",
    "message": "Invalid query syntax in field 'search'",
    "details": {
      "field": "search",
      "hint": "Please check for unmatched quotes, brackets, or invalid operators"
    }
  }
}
```

### 2. Index Not Found (404 Not Found)

When no audit events exist for a project:

```json
{
  "error": {
    "code": "INDEX_NOT_FOUND",
    "message": "No audit events found for this project",
    "details": {
      "hint": "This project may not have any audit events yet, or you may not have access to view them"
    }
  }
}
```

### 3. Query Timeout (504 Gateway Timeout)

When a query takes too long to execute:

```json
{
  "error": {
    "code": "QUERY_TIMEOUT",
    "message": "The query took too long to execute. Please try narrowing your search criteria",
    "details": {
      "hint": "Try using more specific filters or a smaller time range"
    }
  }
}
```

### 4. Rate Limiting (429 Too Many Requests)

When too many requests are made:

```json
{
  "error": {
    "code": "TOO_MANY_REQUESTS",
    "message": "Too many requests. Please wait a moment before trying again"
  }
}
```

### 5. Resource Exhausted (400 Bad Request)

When a query exceeds resource limits:

```json
{
  "error": {
    "code": "RESOURCE_EXHAUSTED",
    "message": "Query result set is too large",
    "details": {
      "hint": "Use pagination with smaller limit values"
    }
  }
}
```

### 6. Service Unavailable (503 Service Unavailable)

When the storage backend is unavailable:

```json
{
  "error": {
    "code": "CONNECTION_FAILURE",
    "message": "Unable to retrieve audit events. Please try again later",
    "details": {
      "hint": "The audit service is temporarily unavailable"
    }
  }
}
```

## Benefits

1. **Clear Error Codes**: Machine-readable codes allow clients to handle errors programmatically
2. **User-Friendly Messages**: Non-technical users can understand what went wrong
3. **Actionable Hints**: Suggestions help users resolve issues without support
4. **Consistent Format**: All errors follow the same structure for easier parsing
5. **Enhanced Metrics**: Specific error types are tracked for better monitoring

## Implementation Details

- Error details are logged internally for debugging while safe messages are returned to users
- HTTP status codes are appropriately mapped to error types
- Prometheus metrics track specific error types: `hermes_storage_errors_by_type_count{error_code="..."}`
- The `hermes_storage_errors_count` metric provides overall error counting alongside the detailed type-specific metrics
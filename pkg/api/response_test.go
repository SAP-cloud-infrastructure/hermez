// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/sapcc/hermes/pkg/storage"
)

func TestRespondWithStorageError_IncrementsBothMetrics(t *testing.T) {
	// Create a new registry to avoid conflicts with global metrics
	reg := prometheus.NewPedanticRegistry()

	// Create local versions of the metrics for testing
	testStorageErrorsCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "test_hermes_storage_errors_count",
		Help: "Test counter for storage errors",
	})
	testStorageErrorsCounterVec := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "test_hermes_storage_errors_by_type_count",
			Help: "Test counter for storage errors by type",
		},
		[]string{"error_code"},
	)

	// Register the test metrics
	reg.MustRegister(testStorageErrorsCounter, testStorageErrorsCounterVec)

	// Temporarily replace the global metrics with our test metrics
	originalCounter := storageErrorsCounter
	originalCounterVec := storageErrorsCounterVec
	storageErrorsCounter = testStorageErrorsCounter
	storageErrorsCounterVec = testStorageErrorsCounterVec
	defer func() {
		storageErrorsCounter = originalCounter
		storageErrorsCounterVec = originalCounterVec
	}()

	// Create a test storage error
	storageErr := &storage.StorageError{
		Code:       storage.ErrorCodeConnectionFailure,
		HTTPStatus: http.StatusServiceUnavailable,
		Message:    "Failed to connect to storage backend",
		Cause:      nil,
	}

	// Create a test HTTP request and response recorder
	w := httptest.NewRecorder()

	// Call RespondWithStorageError
	handled := RespondWithStorageError(w, storageErr)

	// Verify the error was handled
	if !handled {
		t.Fatal("Expected RespondWithStorageError to handle the storage error")
	}

	// Verify HTTP response
	resp := w.Result()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status code %d, got %d", http.StatusServiceUnavailable, resp.StatusCode)
	}

	// Verify the legacy counter was incremented
	legacyCount := testutil.ToFloat64(testStorageErrorsCounter)
	if legacyCount != 1 {
		t.Errorf("Expected legacy counter to be 1, got %f", legacyCount)
	}

	// Verify the new counter vector was incremented with the correct label
	vecCount := testutil.ToFloat64(testStorageErrorsCounterVec.WithLabelValues(string(storage.ErrorCodeConnectionFailure)))
	if vecCount != 1 {
		t.Errorf("Expected counter vector with label '%s' to be 1, got %f", storage.ErrorCodeConnectionFailure, vecCount)
	}

	// Test with another error to ensure both metrics continue to increment
	storageErr2 := &storage.StorageError{
		Code:       storage.ErrorCodeQuerySyntax,
		HTTPStatus: http.StatusBadRequest,
		Message:    "Invalid query",
		Cause:      nil,
	}

	w2 := httptest.NewRecorder()
	RespondWithStorageError(w2, storageErr2)

	// Verify the legacy counter incremented again
	legacyCount2 := testutil.ToFloat64(testStorageErrorsCounter)
	if legacyCount2 != 2 {
		t.Errorf("Expected legacy counter to be 2 after second error, got %f", legacyCount2)
	}

	// Verify the new counter vector was incremented with the different label
	vecCount2 := testutil.ToFloat64(testStorageErrorsCounterVec.WithLabelValues(string(storage.ErrorCodeQuerySyntax)))
	if vecCount2 != 1 {
		t.Errorf("Expected counter vector with label '%s' to be 1, got %f", storage.ErrorCodeQuerySyntax, vecCount2)
	}

	// The original ConnectionError counter should still be 1
	vecCountOriginal := testutil.ToFloat64(testStorageErrorsCounterVec.WithLabelValues(string(storage.ErrorCodeConnectionFailure)))
	if vecCountOriginal != 1 {
		t.Errorf("Expected counter vector with label '%s' to still be 1, got %f", storage.ErrorCodeConnectionFailure, vecCountOriginal)
	}
}

func TestRespondWithStorageError_NonStorageError(t *testing.T) {
	// Test that non-storage errors are handled by the fallback
	w := httptest.NewRecorder()
	normalErr := io.EOF

	handled := RespondWithStorageError(w, normalErr)
	if !handled {
		t.Fatal("Expected RespondWithStorageError to handle the error via fallback")
	}

	// Verify it returns a text error response
	resp := w.Result()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("Expected status code %d for non-storage error, got %d", http.StatusInternalServerError, resp.StatusCode)
	}
}

func TestRespondWithStorageError_NilError(t *testing.T) {
	// Test that nil errors return false
	w := httptest.NewRecorder()

	handled := RespondWithStorageError(w, nil)
	if handled {
		t.Fatal("Expected RespondWithStorageError to return false for nil error")
	}
}

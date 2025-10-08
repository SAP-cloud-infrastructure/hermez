// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/hermes/pkg/storage"
)

// ReturnESJSON is a custom response helper that preserves Elasticsearch URL formatting.
// This is needed because Elasticsearch requires literal & characters in URLs, but Go's
// JSON marshaler escapes them as \u0026.
//
// Example:
//
//	events := []Event{...}
//	ReturnESJSON(w, http.StatusOK, map[string]any{
//		"events": events,
//		"total":  len(events),
//	})
func ReturnESJSON(w http.ResponseWriter, code int, data any) {
	payload, err := json.MarshalIndent(&data, "", "  ")
	if err != nil {
		respondwith.ErrorText(w, err)
		return
	}

	// Replace escaped ampersands with literal ones for Elasticsearch compatibility
	payload = bytes.ReplaceAll(payload, []byte("\\u0026"), []byte("&"))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, err = w.Write(payload)
	if err != nil {
		// It's too late to write this as a 5xx response since we've already
		// started writing a 2xx response, so this can only be logged.
		logg.Error("Issue with writing payload when returning JSON: %s", err.Error())
	}
}

// getProtocol determines the protocol (http or https) for building URLs.
func getProtocol(req *http.Request) string {
	protocol := "http"
	if req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https" {
		protocol = "https"
	}
	return protocol
}

// RespondWithStorageError checks if the error is a StorageError and responds appropriately.
// It returns true if the error was handled (response was written), false otherwise.
func RespondWithStorageError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}

	// Check if it's a StorageError
	if storageErr, ok := errext.As[*storage.StorageError](err); ok {
		// Log the full error with cause for debugging
		logg.Error("Storage error occurred: %s", storageErr.Error())

		// Track storage errors in both metrics for backward compatibility
		storageErrorsCounter.Inc()                                             // Legacy metric: total count
		storageErrorsCounterVec.WithLabelValues(string(storageErr.Code)).Inc() // New metric: by error type

		// Return the user-friendly error response
		ReturnESJSON(w, storageErr.HTTPStatus, storageErr.ToAPIResponse())
		return true
	}

	// Fall back to standard error handling
	return respondwith.ErrorText(w, err)
}

// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
)

// ReturnESJSON is a custom response helper that preserves storage backend URL formatting.
// This is needed because OpenSearch requires literal & characters in URLs, but Go's
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

	// Replace escaped ampersands with literal ones for storage backend compatibility
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

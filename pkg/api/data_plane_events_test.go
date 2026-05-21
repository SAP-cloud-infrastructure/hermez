// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/mock"

	"github.com/sapcc/hermes/pkg/data_plane_events"
)

// waitForAuditCount polls the MockAuditor up to a deadline for at least
// `want` accumulated audit events. Audit emission is asynchronous
// (goroutine + semaphore) to avoid blocking the HTTP handler on RabbitMQ
// backpressure; tests must therefore wait briefly for the goroutine to
// call Record. We accumulate across drains because RecordedEvents()
// removes events on read.
func waitForAuditCount(t *testing.T, auditor *audittools.MockAuditor, want int) int {
	t.Helper()
	got := 0
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got += len(auditor.RecordedEvents())
		if got >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	return got
}

func newDPETestRouter(t *testing.T, auth map[string]string, store data_plane_events.Storage, auditor audittools.Auditor) http.Handler {
	t.Helper()
	// Save and restore the package-level prometheus registerer so subtests
	// don't leak metric registrations into each other.
	origRegisterer := prometheus.DefaultRegisterer
	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()
	t.Cleanup(func() { prometheus.DefaultRegisterer = origRegisterer })
	validator := mock.NewValidator(mock.NewEnforcer(), auth)
	dpeAPI := NewDataPlaneEventsAPI(validator, store, auditor)
	// methodNotAllowedAPI must come AFTER dpeAPI so the router has all routes
	// when MethodNotAllowedHandler is installed.
	return httpapi.Compose(dpeAPI, methodNotAllowedAPI{})
}

func doDPE(t *testing.T, h http.Handler, method, path string, body any, contentType string) (resp *http.Response, respBody []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("X-Auth-Token", "valid-token")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	resp = w.Result()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	_ = resp.Body.Close()
	respBody = out
	return resp, respBody
}

func TestDataPlaneEventsGet_DefaultsFalseForUnknownProject(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, body := doDPE(t, router, http.MethodGet, "/v1/projects/proj-1/data-plane-events", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var got dataPlaneEventsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, body)
	}
	if got.Enabled {
		t.Errorf("enabled = true, want false for unknown project")
	}
}

func TestDataPlaneEventsPatch_TrueOnAbsent_CreatesAndEmits(t *testing.T) {
	store := data_plane_events.NewMock()
	auditor := audittools.NewMockAuditor()
	auth := map[string]string{"project_id": "proj-1", "user_id": "u-1", "user_name": "alice"}
	router := newDPETestRouter(t, auth, store, auditor)

	resp, body := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": true}, "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	var got dataPlaneEventsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, body)
	}
	if !got.Enabled {
		t.Errorf("enabled = false, want true")
	}
	if got := waitForAuditCount(t, auditor, 1); got != 1 {
		t.Errorf("recorded %d audit events, want 1", got)
	}
	enabled, found, err := store.Get(context.Background(), "proj-1")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	if !enabled || !found {
		t.Errorf("after PATCH true: stored enabled=%v found=%v, want true,true", enabled, found)
	}
}

func TestDataPlaneEventsPatch_TrueOnAlreadyTrue_NoOp(t *testing.T) {
	store := data_plane_events.NewMock()
	if _, _, err := store.Set(context.Background(), "proj-1", true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	auditor := audittools.NewMockAuditor()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, auditor)

	resp, body := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": true}, "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if events := auditor.RecordedEvents(); len(events) != 0 {
		t.Errorf("recorded %d audit events on no-op, want 0", len(events))
	}
}

func TestDataPlaneEventsPatch_FalseOnAbsent_NoOp(t *testing.T) {
	// Spec: "no row" and "row with enabled=false" are observably equivalent.
	// PATCH false on absent project must NOT write a row and MUST NOT emit a CADF event.
	store := data_plane_events.NewMock()
	auditor := audittools.NewMockAuditor()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, auditor)

	resp, body := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": false}, "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if events := auditor.RecordedEvents(); len(events) != 0 {
		t.Errorf("recorded %d audit events on no-op, want 0", len(events))
	}
	if _, found, err := store.Get(context.Background(), "proj-1"); err != nil {
		t.Fatalf("store.Get: %v", err)
	} else if found {
		t.Errorf("PATCH false on absent must not materialize a row")
	}
}

func TestDataPlaneEventsPatch_FalseOnTrue_DisablesAndEmits(t *testing.T) {
	store := data_plane_events.NewMock()
	if _, _, err := store.Set(context.Background(), "proj-1", true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	auditor := audittools.NewMockAuditor()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, auditor)

	resp, body := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": false}, "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
	if got := waitForAuditCount(t, auditor, 1); got != 1 {
		t.Errorf("recorded %d audit events, want 1", got)
	}
}

func TestDataPlaneEventsPatch_RejectsUnknownFields(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]any{"enabled": true, "bucket": "x"}, "application/json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDataPlaneEventsPatch_RejectsMissingEnabled(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]any{}, "application/json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestDataPlaneEventsPatch_RejectsWrongContentType(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": true}, "text/plain")
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", resp.StatusCode)
	}
}

func TestDataPlaneEventsPatch_RejectsOversizedBody(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	// Build a >64 KiB request body. JSON shape doesn't matter; the cap fires before parsing completes.
	huge := strings.Repeat("a", 65*1024)
	bodyBytes := []byte(`{"enabled":true,"x":"` + huge + `"}`)
	req := httptest.NewRequest(http.MethodPatch, "/v1/projects/proj-1/data-plane-events", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Auth-Token", "valid-token")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Result().StatusCode)
	}
}

func TestDataPlaneEvents_RejectsCrossTenant(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "OTHER"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodGet, "/v1/projects/proj-1/data-plane-events", nil, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET cross-tenant: status = %d, want 403", resp.StatusCode)
	}
	resp, _ = doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": true}, "application/json")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PATCH cross-tenant: status = %d, want 403", resp.StatusCode)
	}
}

func TestDataPlaneEvents_RejectsInvalidProjectID(t *testing.T) {
	store := data_plane_events.NewMock()
	router := newDPETestRouter(t, map[string]string{"project_id": "proj-1"}, store, audittools.NewMockAuditor())

	tt := []struct {
		name string
		path string
	}{
		{"slash_in_id", "/v1/projects/abc%2Fdef/data-plane-events"},
		{"unavailable_literal", "/v1/projects/unavailable/data-plane-events"},
		{"too_long", "/v1/projects/" + strings.Repeat("a", 65) + "/data-plane-events"},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := doDPE(t, router, http.MethodGet, tc.path, nil, "")
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 400 or 404", resp.StatusCode)
			}
		})
	}
}

// T1.2 — application/json with charset parameter is the JS fetch and
// python-requests default. The handler must use mime.ParseMediaType and
// accept it.
func TestDataPlaneEventsPatch_AcceptsJSONWithCharset(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1", "user_id": "u-1", "user_name": "alice"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, body := doDPE(t, router, http.MethodPatch, "/v1/projects/proj-1/data-plane-events",
		map[string]bool{"enabled": true}, "application/json; charset=utf-8")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}
}

// T1.5 / T1.6 — a token whose scope claim is empty (e.g. domain-only or
// cluster_viewer-shape) must NOT be allowed to read a project's toggle.
// The fail-closed cross-tenant guard rejects on `scoped != projectID`,
// which catches empty as well as mismatched scope.
func TestDataPlaneEventsGet_EmptyScopeIsForbidden(t *testing.T) {
	store := data_plane_events.NewMock()
	// project_id deliberately empty in the auth map.
	auth := map[string]string{"user_id": "u-1", "user_name": "alice"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodGet, "/v1/projects/proj-1/data-plane-events", nil, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("empty-scope GET: status = %d, want 403", resp.StatusCode)
	}
}

// T1.5 — cluster_viewer-style token (domain=cloud_domain, project=cloud_admin_project,
// no project_id scope on the URL project) must be 403 on the toggle endpoint.
// We simulate the post-policy behaviour: the policy no longer authorises
// cluster_viewer on data_plane_events:show, so any token without
// project_admin / project_viewer fails Require() and returns 403 either at
// the policy step or the cross-tenant guard. Either way the contract is
// "cluster_viewer cannot read this".
func TestDataPlaneEventsGet_ClusterViewerForbidden(t *testing.T) {
	store := data_plane_events.NewMock()
	// project_id="" forces the cross-tenant fail-closed branch even if
	// the policy were ever loosened.
	auth := map[string]string{
		"user_id":             "u-1",
		"user_name":           "alice",
		"project_domain_name": "cloud_domain",
		"project_name":        "cloud_admin_project",
	}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodGet, "/v1/projects/proj-1/data-plane-events", nil, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cluster_viewer-shape GET: status = %d, want 403", resp.StatusCode)
	}
}

// T1.6 — a wildcard '*' in the URL must be rejected by validateProjectID
// before reaching policy/storage. This guards against a future regression
// in the regex.
func TestDataPlaneEvents_WildcardInURLRejected(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	resp, _ := doDPE(t, router, http.MethodGet, "/v1/projects/%2A/data-plane-events", nil, "")
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wildcard project_id: status = %d, want 400 or 404", resp.StatusCode)
	}
}

// T1.12 — POST on a registered path returns 405 with an Allow header
// listing the supported methods (RFC 7231 §6.5.5).
func TestDataPlaneEvents_PostReturns405WithAllow(t *testing.T) {
	store := data_plane_events.NewMock()
	auth := map[string]string{"project_id": "proj-1"}
	router := newDPETestRouter(t, auth, store, audittools.NewMockAuditor())

	req := httptest.NewRequest(http.MethodPost, "/v1/projects/proj-1/data-plane-events",
		bytes.NewReader([]byte(`{"enabled":true}`)))
	req.Header.Set("X-Auth-Token", "valid-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	resp := w.Result()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST: status = %d, want 405", resp.StatusCode)
	}
	allow := resp.Header.Get("Allow")
	if allow == "" {
		t.Fatalf("missing Allow header on 405 response")
	}
	if !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodPatch) {
		t.Errorf("Allow = %q; want it to contain GET and PATCH", allow)
	}
}

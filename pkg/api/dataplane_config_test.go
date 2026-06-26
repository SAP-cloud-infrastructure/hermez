// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	policy "github.com/databus23/goslo.policy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/mock"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/hermes/pkg/routing"
	"github.com/sapcc/hermes/pkg/storage"
	"github.com/sapcc/hermes/pkg/test"
)

const testProjectID = "test-project-1"
const dataplaneConfigPath = "/v1/projects/" + testProjectID + "/dataplane-config"

// updateEvent and deleteEvent build cadf.Event values that match what MockAuditor records after normalization.
// MockAuditor.normalize zeroes out ID, EventTime, TypeURI, EventType, Observer, and the standard Initiator.
// What remains and must match: Action, Outcome, Reason, RequestPath, Target (including Attachments).
func updateEvent(reasonCode int, target cadf.Resource) cadf.Event {
	outcome := cadf.FailureOutcome
	if reasonCode >= 200 && reasonCode < 300 {
		outcome = cadf.SuccessOutcome
	}
	return cadf.Event{
		Action:  cadf.UpdateAction,
		Outcome: outcome,
		Reason: cadf.Reason{
			ReasonType: "HTTP",
			ReasonCode: strconv.Itoa(reasonCode),
		},
		RequestPath: dataplaneConfigPath,
		Target:      target,
	}
}

func deleteEvent(reasonCode int, target cadf.Resource) cadf.Event {
	outcome := cadf.FailureOutcome
	if reasonCode >= 200 && reasonCode < 300 {
		outcome = cadf.SuccessOutcome
	}
	return cadf.Event{
		Action:  cadf.DeleteAction,
		Outcome: outcome,
		Reason: cadf.Reason{
			ReasonType: "HTTP",
			ReasonCode: strconv.Itoa(reasonCode),
		},
		RequestPath: dataplaneConfigPath,
		Target:      target,
	}
}

// dataplaneTarget builds the cadf.Resource for DataplaneConfig.Render() for testProjectID.
func dataplaneTarget(attachments []cadf.Attachment) cadf.Resource {
	return cadf.Resource{
		TypeURI:     "service/hermes/dataplane-config",
		ID:          testProjectID,
		ProjectID:   testProjectID,
		Attachments: attachments,
	}
}

// payloadAttachment builds the JSON attachment that DataplaneConfig.Render() adds.
func payloadAttachment(enabled bool, targetBucket string) []cadf.Attachment {
	return []cadf.Attachment{
		must.Return(cadf.NewJSONAttachment("payload", map[string]any{
			"enabled":       enabled,
			"target_bucket": targetBucket,
		})),
	}
}

// setupDataplaneTest creates a handler where the mock token is scoped to testProjectID.
// This is needed because authDataplaneConfig enforces path-vs-token project match.
// Returns the handler, the routing mock store, and the mock auditor for event assertions.
func setupDataplaneTest(t *testing.T) (http.Handler, *routing.Mock, *audittools.MockAuditor) {
	t.Helper()

	policyBytes, err := os.ReadFile("../test/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	policyRules := make(map[string]string)
	if err := json.Unmarshal(policyBytes, &policyRules); err != nil {
		t.Fatal(err)
	}
	policyEnforcer, err := policy.NewEnforcer(policyRules)
	if err != nil {
		t.Fatal(err)
	}
	viper.Set("hermes.PolicyEnforcer", policyEnforcer)

	validator := mock.NewValidator(mock.NewEnforcer(), map[string]string{
		"project_id": testProjectID,
		"user_id":    "user-abc",
	})
	routingStore := routing.NewMock()
	mockAuditor := audittools.NewMockAuditor()

	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()

	v1API := NewV1API(validator, storage.Mock{}, routingStore, mockAuditor)
	return httpapi.Compose(v1API, NewVersionAPI(v1API.VersionData()), NewMetricsAPI()), routingStore, mockAuditor
}

// putJSON issues a PUT to dataplaneConfigPath with a JSON body and returns the recorder.
func putJSON(t *testing.T, handler http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, dataplaneConfigPath, bytes.NewReader(b))
	req.Header.Set("X-Auth-Token", "something")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// TestDataplaneConfig_GetDefault proves GET returns the disabled default when no config exists.
func TestDataplaneConfig_GetDefault(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/projects/" + testProjectID + "/dataplane-config",
		ExpectStatusCode: http.StatusOK,
		ExpectJSON:       "fixtures/dataplane-config-default.json",
	}.Check(t, handler)
	// GET does not emit audit events.
	auditor.ExpectEvents(t /* none */)
}

// TestDataplaneConfig_PutAndGet proves PUT persists and a subsequent GET returns the saved config.
// Also asserts the audit event is emitted with UpdateAction and success outcome.
func TestDataplaneConfig_PutAndGet(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	path := "/v1/projects/" + testProjectID + "/dataplane-config"

	// PUT valid config
	rec := putJSON(t, handler, map[string]any{
		"enabled":       true,
		"target_bucket": "my-audit-bucket",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the response contains the expected fields.
	var resp routing.DataplaneConfig
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("PUT response is not valid JSON: %s", err)
	}
	if resp.ProjectID != testProjectID {
		t.Errorf("PUT response project_id: want %q, got %q", testProjectID, resp.ProjectID)
	}
	if !resp.Enabled {
		t.Error("PUT response enabled: want true, got false")
	}
	if resp.TargetBucket != "my-audit-bucket" {
		t.Errorf("PUT response target_bucket: want %q, got %q", "my-audit-bucket", resp.TargetBucket)
	}
	if resp.UpdatedBy != "user-abc" {
		t.Errorf("PUT response updated_by: want %q, got %q", "user-abc", resp.UpdatedBy)
	}
	if resp.UpdatedAt.IsZero() {
		t.Error("PUT response updated_at must be set")
	}

	// Assert one audit event was emitted with UpdateAction and success.
	auditor.ExpectEvents(t, updateEvent(
		http.StatusOK,
		dataplaneTarget(payloadAttachment(true, "my-audit-bucket")),
	))

	// GET after PUT must return the same config.
	getReq := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	getReq.Header.Set("X-Auth-Token", "something")
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET after PUT expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var getResp routing.DataplaneConfig
	if err := json.Unmarshal(getRec.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("GET response is not valid JSON: %s", err)
	}
	if getResp.ProjectID != testProjectID || !getResp.Enabled || getResp.TargetBucket != "my-audit-bucket" {
		t.Errorf("GET after PUT mismatch: %+v", getResp)
	}
}

// TestDataplaneConfig_PutEnabledMissingBucket proves PUT with enabled=true and no target_bucket → 400.
// A failure audit event must still be emitted.
func TestDataplaneConfig_PutEnabledMissingBucket(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	rec := putJSON(t, handler, map[string]any{
		"enabled": true,
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	auditor.ExpectEvents(t, updateEvent(
		http.StatusBadRequest,
		dataplaneTarget(payloadAttachment(false, "")),
	))
}

// TestDataplaneConfig_PutInvalidBucketName proves PUT with a bucket name that fails RFC-1123 → 400.
func TestDataplaneConfig_PutInvalidBucketName(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	cases := []string{
		"UPPERCASE",
		"has space",
		"ab",         // too short (< 3 chars)
		"-start",     // starts with hyphen
		"my--bucket", // consecutive hyphens (prohibited by S3 and Ceph RGW)
	}
	for _, bucket := range cases {
		rec := putJSON(t, handler, map[string]any{
			"enabled":       true,
			"target_bucket": bucket,
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("bucket %q: expected 400, got %d", bucket, rec.Code)
		}
	}
	// One failure event per invalid bucket attempt.
	nilCfgTarget := dataplaneTarget(payloadAttachment(false, ""))
	expected := make([]cadf.Event, len(cases))
	for i := range expected {
		expected[i] = updateEvent(http.StatusBadRequest, nilCfgTarget)
	}
	auditor.ExpectEvents(t, expected...)
}

// TestDataplaneConfig_PutUnknownField proves PUT rejects unknown JSON fields with 400.
func TestDataplaneConfig_PutUnknownField(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	rec := putJSON(t, handler, map[string]any{
		"enabled":       false,
		"target_bucket": "",
		"extra_field":   "should-fail",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	auditor.ExpectEvents(t, updateEvent(
		http.StatusBadRequest,
		dataplaneTarget(payloadAttachment(false, "")),
	))
}

// TestDataplaneConfig_DeleteAndGet proves DELETE removes config; subsequent GET returns the default.
// Also asserts a delete audit event is emitted.
func TestDataplaneConfig_DeleteAndGet(t *testing.T) {
	handler, routingStore, auditor := setupDataplaneTest(t)
	path := "/v1/projects/" + testProjectID + "/dataplane-config"

	// Seed a config to delete
	if err := routingStore.Upsert(t.Context(), routing.DataplaneConfig{
		ProjectID:    testProjectID,
		Enabled:      true,
		TargetBucket: "my-audit-bucket",
		UpdatedAt:    time.Now().UTC(),
		UpdatedBy:    "user-abc",
	}); err != nil {
		t.Fatalf("failed to seed routing config: %s", err)
	}

	// DELETE
	delReq := httptest.NewRequest(http.MethodDelete, path, http.NoBody)
	delReq.Header.Set("X-Auth-Token", "something")
	delRec := httptest.NewRecorder()
	handler.ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE expected 204, got %d: %s", delRec.Code, delRec.Body.String())
	}

	// Assert delete audit event.
	auditor.ExpectEvents(t, deleteEvent(
		http.StatusNoContent,
		dataplaneTarget(payloadAttachment(false, "")),
	))

	// GET after DELETE must return the default (disabled) config.
	test.APIRequest{
		Method:           "GET",
		Path:             path,
		ExpectStatusCode: http.StatusOK,
		ExpectJSON:       "fixtures/dataplane-config-default.json",
	}.Check(t, handler)
}

// TestDataplaneConfig_DeleteNonExistent proves DELETE on non-existent config is idempotent (204)
// and does NOT emit an audit event (no resource existed to delete).
func TestDataplaneConfig_DeleteNonExistent(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	req := httptest.NewRequest(http.MethodDelete, "/v1/projects/"+testProjectID+"/dataplane-config", http.NoBody)
	req.Header.Set("X-Auth-Token", "something")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	// No event — the resource never existed.
	auditor.ExpectEvents(t /* none */)
}

// TestDataplaneConfig_CrossProjectForbidden proves that a token scoped to
// testProjectID cannot access a different project's config (403).
func TestDataplaneConfig_CrossProjectForbidden(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t) // token project_id = testProjectID

	differentProject := "other-project-99"
	otherPath := "/v1/projects/" + differentProject + "/dataplane-config"

	// GET and DELETE use NoBody.
	for _, method := range []string{"GET", "DELETE"} {
		req := httptest.NewRequest(method, otherPath, http.NoBody)
		req.Header.Set("X-Auth-Token", "something")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s to different project: expected 403, got %d", method, rec.Code)
		}
	}

	// PUT to a different project also requires a body (Content-Type enforcement happens
	// after the project-ID check, but providing valid JSON is cleaner than relying on ordering).
	putBody, err := json.Marshal(map[string]any{"enabled": false})
	if err != nil {
		t.Fatal(err)
	}
	putReq := httptest.NewRequest(http.MethodPut, otherPath, bytes.NewReader(putBody))
	putReq.Header.Set("X-Auth-Token", "something")
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	handler.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusForbidden {
		t.Errorf("PUT to different project: expected 403, got %d", putRec.Code)
	}

	// 403 happens before the handler body runs — no audit events from our code.
	auditor.ExpectEvents(t /* none */)
}

// TestDataplaneConfig_DisabledPutAcceptsEmptyBucket proves PUT with enabled=false
// does not require target_bucket (routing is off, bucket is irrelevant).
func TestDataplaneConfig_DisabledPutAcceptsEmptyBucket(t *testing.T) {
	handler, _, auditor := setupDataplaneTest(t)
	rec := putJSON(t, handler, map[string]any{
		"enabled": false,
	})
	if rec.Code != http.StatusOK {
		t.Errorf("PUT disabled without bucket: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	auditor.ExpectEvents(t, updateEvent(
		http.StatusOK,
		dataplaneTarget(payloadAttachment(false, "")),
	))
}

// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	policy "github.com/databus23/goslo.policy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/viper"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/mock"

	"github.com/sapcc/hermes/pkg/routing"
	"github.com/sapcc/hermes/pkg/storage"
	"github.com/sapcc/hermes/pkg/test"
)

func setupTest(t *testing.T) http.Handler {
	// load test policy (where everything is allowed)
	policyBytes, err := os.ReadFile("../test/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	policyRules := make(map[string]string)
	err = json.Unmarshal(policyBytes, &policyRules)
	if err != nil {
		t.Fatal(err)
	}
	policyEnforcer, err := policy.NewEnforcer(policyRules)
	if err != nil {
		t.Fatal(err)
	}
	viper.Set("hermes.PolicyEnforcer", policyEnforcer)

	// create test driver with the domains and projects from start-data.sql
	validator := mock.NewValidator(mock.NewEnforcer(), nil)
	storageInterface := storage.Mock{}
	routingStore := routing.NewMock()

	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()

	// Create API compositions using httpapi
	v1API := NewV1API(validator, storageInterface, routingStore, audittools.NewNullAuditor())
	versionAPI := NewVersionAPI(v1API.VersionData())
	metricsAPI := NewMetricsAPI()

	// Compose all APIs using httpapi
	router := httpapi.Compose(
		v1API,
		versionAPI,
		metricsAPI,
	)

	return router
}

func Test_API(t *testing.T) {
	tt := []struct {
		name       string
		method     string
		path       string
		statuscode int
		json       string
	}{
		{"Metadata", "GET", "/v1/", http.StatusOK, "fixtures/api-metadata.json"},
		{"EventDetails", "GET", "/v1/events/7be6c4ff-b761-5f1f-b234-f5d41616c2cd", http.StatusOK, "fixtures/event-details.json"},
		{"EventList", "GET", "/v1/events?event_type=identity.project.deleted&offset=10", http.StatusOK, "fixtures/event-list.json"},
		{"Attributes", "GET", "/v1/attributes/resource_type?limit=10", http.StatusOK, "fixtures/attributes.json"},
		{"AttributesKnownName", "GET", "/v1/attributes/action?limit=10", http.StatusOK, "fixtures/attributes.json"},
		{"AttributesUnknownName", "GET", "/v1/attributes/observer.id.keyword", http.StatusBadRequest, ""},
		{"AttributesLimitExceedsMax", "GET", "/v1/attributes/action?limit=99999", http.StatusBadRequest, ""},
		{"InvalidEventID", "GET", "/v1/events/invalid-uuid", http.StatusBadRequest, ""},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			router := setupTest(t)

			test.APIRequest{
				Method:           tc.method,
				Path:             tc.path,
				ExpectStatusCode: tc.statuscode,
				ExpectJSON:       tc.json,
			}.Check(t, router)
		})
	}
}

func TestListEvents_ParameterParsing(t *testing.T) {
	validTimeStr := time.Now().UTC().Format(time.RFC3339)
	anotherValidTimeStr := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)

	tt := []struct {
		name             string
		path             string // Query string part
		expectStatusCode int
		// ExpectJSON will be "" for Bad Request (plain text error from http.Error)
		// For the one OK case, we won't specify ExpectJSON to avoid diffs with the mock.
		expectJSON string
	}{
		// --- Minimal "Loop Runs" Case (will get static data from mock) ---
		// This ensures the loop itself doesn't panic and can process a valid item.
		// We don't check the body because the mock returns static data.
		// TODO: Add a real test fixture for this.
		{"Sort_MinimalValidToRunLoop", "?sort=time", http.StatusOK, ""},

		// --- Sort Parameter Parsing Errors ---
		{"Sort_InvalidField", "?sort=invalidfield", http.StatusBadRequest, ""},
		{"Sort_InvalidDirection", "?sort=time:wrongdir", http.StatusBadRequest, ""},
		// Cases leading to empty sortElement or sortfield
		{"Sort_EmptyElementMiddle_FromCut", "?sort=time,,initiator_id", http.StatusBadRequest, ""},
		{"Sort_EmptyElementLeading_FromCut", "?sort=,time", http.StatusBadRequest, ""},
		{"Sort_OnlyCommas_FromCut", "?sort=,,", http.StatusBadRequest, ""},
		{"Sort_EmptyFieldNameExplicit", "?sort=:asc", http.StatusBadRequest, ""},

		// --- Time Parameter Parsing Errors ---
		// Minimal valid case for time loop
		{"Time_MinimalValidToRunLoop", "?time=lt:" + validTimeStr, http.StatusOK, ""},

		// Invalid cases
		{"Time_InvalidOperator", "?time=xx:" + validTimeStr, http.StatusBadRequest, ""},
		{"Time_DuplicateOperator", "?time=lt:" + validTimeStr + ",lt:" + anotherValidTimeStr, http.StatusBadRequest, ""},
		{"Time_MissingValue", "?time=lt:", http.StatusBadRequest, ""},
		{"Time_InvalidFormat", "?time=lt:not-a-time-format", http.StatusBadRequest, ""},
		// Cases leading to empty timeElement or operator
		{"Time_EmptyElementMiddle_FromCut", "?time=lt:" + validTimeStr + ",,gte:" + anotherValidTimeStr, http.StatusBadRequest, ""},
		{"Time_EmptyElementLeading_FromCut", "?time=,lt:" + validTimeStr, http.StatusBadRequest, ""},
		{"Time_OnlyCommas_FromCut", "?time=,,", http.StatusBadRequest, ""},
		{"Time_EmptyOperatorNameExplicit", "?time=:" + validTimeStr, http.StatusBadRequest, ""},
	}

	router := setupTest(t)

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			req := test.APIRequest{
				Method:           "GET",
				Path:             "/v1/events" + tc.path,
				ExpectStatusCode: tc.expectStatusCode,
			}
			// Only set ExpectJSON if we actually expect a specific JSON fixture (which we don't here for 200s)
			// For 400s, http.Error writes plain text, so ExpectJSON should be "" or nil.
			if tc.expectStatusCode != http.StatusOK {
				req.ExpectJSON = "" // For http.Error, response is not JSON
			}

			req.Check(t, router)
		})
	}
}

// TestCrossTenantWildcardIsolation is a security regression test for
// cross-tenant access control.
//
// A token scoped to project "tenant-a" must NOT be able to read another
// tenant's events by passing ?project_id=tenant-b. The override is only
// permitted for cloud admins that satisfy the cluster_viewer rule; getIndexID
// enforces this. Here the enforcer forbids cluster_viewer, so the override
// attempt must be rejected rather than silently serving tenant-b's data.
func TestCrossTenantWildcardIsolation(t *testing.T) {
	auth := map[string]string{"project_id": "tenant-a"}
	store := &scopeCapturingStorage{}

	// Non-admin (cluster_viewer forbidden) attempting to override the tenant:
	// must be denied before the storage layer receives any tenant scope.
	router := setupTestWithScopeAndStorage(t, auth, store, "cluster_viewer")
	rec := doGet(t, router, "/v1/events?project_id=tenant-b")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant override status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if len(store.tenantIDs) != 0 {
		t.Fatalf("cross-tenant override reached storage with scopes %v", store.tenantIDs)
	}

	// Same token WITHOUT an override query param stays within its own scope: OK.
	store = &scopeCapturingStorage{}
	router = setupTestWithScopeAndStorage(t, auth, store, "cluster_viewer")
	rec = doGet(t, router, "/v1/events")
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped query within own tenant should succeed, got status %d", rec.Code)
	}
	if got, want := store.tenantIDs, []string{"tenant-a"}; !slices.Equal(got, want) {
		t.Errorf("scoped request used tenant IDs %v, want %v", got, want)
	}

	// A genuine cloud admin (cluster_viewer allowed) MAY override the tenant.
	store = &scopeCapturingStorage{}
	router = setupTestWithScopeAndStorage(t, auth, store) // nothing forbidden => cluster_viewer allowed
	rec = doGet(t, router, "/v1/events?project_id=tenant-b")
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud admin override should succeed, got status %d", rec.Code)
	}
	if got, want := store.tenantIDs, []string{"tenant-b"}; !slices.Equal(got, want) {
		t.Errorf("admin override used tenant IDs %v, want %v", got, want)
	}
}

// TestSecurityResponseHeaders verifies the security headers set by ReturnESJSON.
func TestSecurityResponseHeaders(t *testing.T) {
	router := setupTest(t)
	rec := doGet(t, router, "/v1/events/7be6c4ff-b761-5f1f-b234-f5d41616c2cd")

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("expected X-Content-Type-Options: nosniff, got %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control: no-store (audit data must not be cached), got %q", got)
	}
}

// TestInputValidation_Rejections confirms malformed path/query input is rejected
// before reaching storage. Locks in UUID validation and attribute/limit checks.
func TestInputValidation_Rejections(t *testing.T) {
	router := setupTest(t)

	tt := []struct {
		name       string
		path       string
		expectCode int
	}{
		{"NonUUIDEventID", "/v1/events/not-a-uuid", http.StatusBadRequest},
		{"UnknownAttribute", "/v1/attributes/observer.id.keyword", http.StatusBadRequest},
		{"AttributeLimitExceedsMax", "/v1/attributes/action?limit=99999999", http.StatusBadRequest},
		{"NegativeOffsetRejected", "/v1/events?offset=-1", http.StatusBadRequest},
		// Pagination window over the storage max is a CLIENT error -> 400 with an
		// actionable message, NOT an obfuscated 500. Mock.MaxLimit() is 100.
		{"WindowExceededIsClientError", "/v1/events?limit=101", http.StatusBadRequest},
		{"SearchTooLong", "/v1/events?search=" + strings.Repeat("a", storage.MaxSearchQueryLength+1), http.StatusBadRequest},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, router, tc.path)
			if rec.Code != tc.expectCode {
				t.Errorf("%s: expected status %d, got %d", tc.path, tc.expectCode, rec.Code)
			}
		})
	}
}

// TestGetAttributes_DefaultLimitRespectsStorageMaximum preserves the established
// omitted/zero limit behavior on installations whose result window is below the
// historic 10000 default. Explicit oversized values remain client errors.
func TestGetAttributes_DefaultLimitRespectsStorageMaximum(t *testing.T) {
	store := &attributeLimitCapturingStorage{maxLimit: 100}
	router := setupTestWithScopeAndStorage(t, map[string]string{"project_id": "tenant-a"}, store)

	for _, tc := range []struct {
		name       string
		path       string
		statusCode int
		wantLimit  uint
	}{
		{"OmittedLimit", "/v1/attributes/action", http.StatusOK, 100},
		{"ZeroLimit", "/v1/attributes/action?limit=0", http.StatusOK, 100},
		{"ExplicitLimitExceedsMaximum", "/v1/attributes/action?limit=101", http.StatusBadRequest, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store.limits = nil
			rec := doGet(t, router, tc.path)
			if rec.Code != tc.statusCode {
				t.Fatalf("expected status %d, got %d", tc.statusCode, rec.Code)
			}
			if tc.wantLimit == 0 {
				if len(store.limits) != 0 {
					t.Errorf("storage received limits %v for rejected request", store.limits)
				}
				return
			}
			if len(store.limits) != 1 || store.limits[0] != tc.wantLimit {
				t.Errorf("storage received limits %v, want [%d]", store.limits, tc.wantLimit)
			}
		})
	}
}

func TestRejectOverCapacityMiddleware(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	handler := rejectOverCapacityMiddleware(1)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
		close(finished)
	}))

	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("over-capacity status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After = %q, want %q", got, "1")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
}

// TestEventID_NewlineSanitized verifies the CR/LF sanitization contract on
// event IDs. A trailing newline (log-injection / header-splitting vector) must
// be stripped and the underlying UUID handled safely — never passed through to
// storage verbatim and never a 5xx. The handler strips \r/\n then UUID-parses,
// so a valid UUID with a trailing %0a resolves to the same event as the clean
// UUID.
func TestEventID_NewlineSanitized(t *testing.T) {
	router := setupTest(t)

	clean := doGet(t, router, "/v1/events/7be6c4ff-b761-5f1f-b234-f5d41616c2cd")
	injected := doGet(t, router, "/v1/events/7be6c4ff-b761-5f1f-b234-f5d41616c2cd%0a")

	if injected.Code >= 500 {
		t.Fatalf("newline-injected event ID caused a server error (%d); input was not sanitized safely", injected.Code)
	}
	if injected.Code != clean.Code {
		t.Errorf("newline should be stripped so the injected ID behaves like the clean UUID: clean=%d injected=%d",
			clean.Code, injected.Code)
	}
}

// doGet issues an authenticated GET against the handler and returns the recorder.
func doGet(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	req.Header.Set("X-Auth-Token", "something")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// timeoutStorage is a storage.Storage stub whose query paths report a backend
// timeout via storage.ErrPartialResults. It models the OpenSearch "timed_out":
// true case so we can assert the API surfaces incompleteness rather than a
// truncated 200.
type timeoutStorage struct{}

func (timeoutStorage) GetEvents(_ context.Context, _ *storage.EventFilter, _ string) ([]*cadf.Event, int, error) {
	return nil, 0, storage.ErrPartialResults
}
func (timeoutStorage) GetEvent(_ context.Context, _, _ string) (*cadf.Event, error) {
	return nil, storage.ErrPartialResults
}
func (timeoutStorage) GetAttributes(_ context.Context, _ *storage.AttributeFilter, _ string) ([]string, error) {
	return nil, storage.ErrPartialResults
}
func (timeoutStorage) MaxLimit() uint { return 100 }

// TestBackendTimeout_IsNotSilentlyTruncated asserts that a storage timeout
// becomes an explicit 504 Gateway Timeout, never a 200 with a partial/short
// page. For an audit trail, silently returning an incomplete result set (and a
// deflated total that would break NextURL pagination) is a correctness defect,
// so the failure must be visible to the caller.
func TestBackendTimeout_IsNotSilentlyTruncated(t *testing.T) {
	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()
	v1API := NewV1API(mock.NewValidator(mock.NewEnforcer(), nil), timeoutStorage{}, routing.NewMock(), audittools.NewNullAuditor())
	router := httpapi.Compose(v1API, NewVersionAPI(v1API.VersionData()), NewMetricsAPI())

	for _, tc := range []struct {
		name string
		path string
	}{
		{"EventsList", "/v1/events"},
		{"Attributes", "/v1/attributes/action?limit=10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := doGet(t, router, tc.path)
			if rec.Code != http.StatusGatewayTimeout {
				t.Errorf("%s: a backend timeout must surface as 504, got %d (a 200 would silently hide events)", tc.path, rec.Code)
			}
			if strings.Contains(rec.Body.String(), storage.ErrPartialResults.Error()) {
				t.Errorf("%s: partial-result backend detail leaked to the client", tc.name)
			}
		})
	}
}

func setupTestWithScopeAndStorage(t *testing.T, auth map[string]string, store storage.Storage, forbidRules ...string) http.Handler {
	t.Helper()
	enforcer := mock.NewEnforcer()
	for _, rule := range forbidRules {
		enforcer.Forbid(rule)
	}
	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()
	v1API := NewV1API(mock.NewValidator(enforcer, auth), store, routing.NewMock(), audittools.NewNullAuditor())
	return httpapi.Compose(v1API, NewVersionAPI(v1API.VersionData()), NewMetricsAPI())
}

type scopeCapturingStorage struct {
	tenantIDs []string
}

func (s *scopeCapturingStorage) GetEvents(_ context.Context, _ *storage.EventFilter, tenantID string) ([]*cadf.Event, int, error) {
	s.tenantIDs = append(s.tenantIDs, tenantID)
	return nil, 0, nil
}

func (*scopeCapturingStorage) GetEvent(context.Context, string, string) (*cadf.Event, error) {
	return nil, nil
}

func (*scopeCapturingStorage) GetAttributes(context.Context, *storage.AttributeFilter, string) ([]string, error) {
	return nil, nil
}

func (*scopeCapturingStorage) MaxLimit() uint { return 100 }

type attributeLimitCapturingStorage struct {
	limits   []uint
	maxLimit uint
}

func (*attributeLimitCapturingStorage) GetEvents(context.Context, *storage.EventFilter, string) ([]*cadf.Event, int, error) {
	return nil, 0, nil
}

func (*attributeLimitCapturingStorage) GetEvent(context.Context, string, string) (*cadf.Event, error) {
	return nil, nil
}

func (s *attributeLimitCapturingStorage) GetAttributes(_ context.Context, filter *storage.AttributeFilter, _ string) ([]string, error) {
	s.limits = append(s.limits, filter.Limit)
	return []string{}, nil
}

func (s *attributeLimitCapturingStorage) MaxLimit() uint { return s.maxLimit }

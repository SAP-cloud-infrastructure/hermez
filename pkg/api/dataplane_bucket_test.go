// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/mock"

	"github.com/sapcc/hermes/pkg/routing"
	"github.com/sapcc/hermes/pkg/storage"
)

const (
	testWriterUserID = "writer-user-1"
	testCallerToken  = "caller-token-xyz"
	testWriterGrant  = "*:" + testWriterUserID
	testContainer    = "/swift/v1/AUTH_" + testProjectID + "/hermes-audit"
	testProbeObject  = testContainer + "/" + dataplaneProbeObject
)

// rgwCall is one request received by the fake object store.
type rgwCall struct {
	Method string
	Path   string
	Token  string
	Read   string
	Write  string
	Body   string
}

// fakeRGW is an httptest stand-in for the Ceph RGW Swift API. It keeps the
// state of one container and its objects and answers like RGW does.
type fakeRGW struct {
	*httptest.Server
	mu sync.Mutex
	// Exists, Read and Write are the container state.
	Exists bool
	Read   string
	Write  string
	// Objects holds the object paths that exist.
	Objects map[string]bool
	// Fail maps "METHOD container" or "METHOD object" to the status that
	// request answers instead.
	Fail  map[string]int
	calls []rgwCall
}

func newFakeRGW(t *testing.T) *fakeRGW {
	t.Helper()
	f := &fakeRGW{Objects: map[string]bool{}, Fail: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.calls = append(f.calls, rgwCall{
			Method: r.Method,
			Path:   r.URL.Path,
			Token:  r.Header.Get("X-Auth-Token"),
			Read:   r.Header.Get("X-Container-Read"),
			Write:  r.Header.Get("X-Container-Write"),
			Body:   string(body),
		})
		kind := "container"
		if strings.Count(strings.TrimPrefix(r.URL.Path, "/swift/v1/"), "/") > 1 {
			kind = "object"
		}
		if status, ok := f.Fail[r.Method+" "+kind]; ok {
			w.WriteHeader(status)
			return
		}
		switch r.Method + " " + kind {
		case "HEAD container":
			if !f.Exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("X-Container-Read", f.Read)
			w.Header().Set("X-Container-Write", f.Write)
			w.WriteHeader(http.StatusNoContent)
		case "PUT container":
			f.Exists = true
			w.WriteHeader(http.StatusCreated)
		case "POST container":
			f.Read = r.Header.Get("X-Container-Read")
			f.Write = r.Header.Get("X-Container-Write")
			w.WriteHeader(http.StatusNoContent)
		case "PUT object":
			f.Objects[r.URL.Path] = true
			w.WriteHeader(http.StatusCreated)
		case "DELETE object":
			if !f.Objects[r.URL.Path] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			delete(f.Objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRGW) Calls() []rgwCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rgwCall(nil), f.calls...)
}

// Methods returns "METHOD path" for every call, in order.
func (f *fakeRGW) Methods() []string {
	return callMethods(f.Calls())
}

// callMethods returns "METHOD path" for every call, in order.
func callMethods(calls []rgwCall) []string {
	var out []string
	for _, c := range calls {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

// fakeKeystone is a validator whose tokens carry a ProviderClient with a
// service catalog that points at the fake object store.
type fakeKeystone struct {
	inner       *mock.Validator[*mock.Enforcer]
	wantService string
	// endpoint returns the catalog URL for a token's project.
	endpoint func(projectID string) string
	// identityEndpoint is the catalog URL of the identity service; empty means
	// the catalog has none.
	identityEndpoint string
}

func (k fakeKeystone) CheckToken(r *http.Request) *gopherpolicy.Token {
	token := k.inner.CheckToken(r)
	pc := &gophercloud.ProviderClient{TokenID: testCallerToken}
	pc.EndpointLocator = func(opts gophercloud.EndpointOpts) (string, error) {
		if opts.Type == "identity" && k.identityEndpoint != "" {
			return k.identityEndpoint, nil
		}
		if opts.Type != k.wantService {
			// Like openstack.V3EndpointURL.
			return "", &gophercloud.ErrEndpointNotFound{}
		}
		return k.endpoint(token.Context.Auth["project_id"]), nil
	}
	token.ProviderClient = pc
	return token
}

func newFakeKeystone(rgw *fakeRGW, auth map[string]string, forbiddenRules ...string) fakeKeystone {
	enforcer := mock.NewEnforcer()
	for _, rule := range forbiddenRules {
		enforcer.Forbid(rule)
	}
	return fakeKeystone{
		inner:       mock.NewValidator(enforcer, auth),
		wantService: defaultDataplaneServiceType,
		endpoint: func(projectID string) string {
			return rgw.URL + "/swift/v1/AUTH_" + projectID
		},
	}
}

// setupDataplaneTestWith builds the API against the fake object store with the
// given writer user id and token auth values.
func setupDataplaneTestWith(t *testing.T, rgw *fakeRGW, writerID string, auth map[string]string, forbiddenRules ...string) (http.Handler, *routing.Mock, *audittools.MockAuditor) {
	t.Helper()
	return setupDataplaneTestKeystone(t, newFakeKeystone(rgw, auth, forbiddenRules...), writerID)
}

// setupDataplaneTestKeystone builds the API with the given validator.
func setupDataplaneTestKeystone(t *testing.T, validator gopherpolicy.Validator, writerID string) (http.Handler, *routing.Mock, *audittools.MockAuditor) {
	t.Helper()
	return setupDataplaneTestRole(t, validator, writerID, nil)
}

// setupDataplaneTestRole builds the API with the given validator. configure,
// if not nil, adjusts the writer role setup that NewV1API made from the config
// (off by default).
func setupDataplaneTestRole(t *testing.T, validator gopherpolicy.Validator, writerID string, configure func(*dataplaneWriterRole)) (http.Handler, *routing.Mock, *audittools.MockAuditor) {
	t.Helper()
	routingStore := routing.NewMock()
	mockAuditor := audittools.NewMockAuditor()

	prometheus.DefaultRegisterer = prometheus.NewPedanticRegistry()

	v1API := NewV1API(validator, storage.Mock{}, routingStore, mockAuditor, nil, nil)
	v1API.provider.dataplaneBucket = dataplaneBucketConfig{WriterUserID: writerID, ServiceType: defaultDataplaneServiceType}
	if configure != nil {
		configure(v1API.provider.dataplaneWriterRole)
	}
	return httpapi.Compose(v1API, NewVersionAPI(v1API.VersionData()), NewMetricsAPI()), routingStore, mockAuditor
}

func scopedAuth(projectID string) map[string]string {
	return map[string]string{"project_id": projectID, "user_id": "user-abc"}
}

func assertStored(t *testing.T, store *routing.Mock, want bool) {
	t.Helper()
	cfg, err := store.Get(t.Context(), testProjectID)
	switch {
	case !want && err == nil && cfg.Enabled:
		t.Errorf("config was saved as enabled, expected nothing saved")
	case want && (err != nil || !cfg.Enabled):
		t.Errorf("expected enabled config to be saved, got %v, %v", cfg, err)
	}
}

func assertResponse(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantBody string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, wantStatus, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), wantBody) {
		t.Errorf("body = %q, want it to contain %q", rec.Body, wantBody)
	}
}

func assertNoCalls(t *testing.T, rgw *fakeRGW) {
	t.Helper()
	if calls := rgw.Calls(); len(calls) != 0 {
		t.Errorf("object store got %d requests, want 0: %+v", len(calls), calls)
	}
}

// TestEnableDataplane_WrongScopeRejected proves a token scoped to another
// project (a cloud admin) cannot enable dataplane: nothing is created or saved.
func TestEnableDataplane_WrongScopeRejected(t *testing.T) {
	rgw := newFakeRGW(t)
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth("cloud-admin-project"))

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusConflict, "token scoped to the project")
	assertNoCalls(t, rgw)
	assertStored(t, store, false)
}

// TestEnableDataplane_DomainScopedTokenRejected proves a domain-scoped token
// (no project_id) cannot enable dataplane.
func TestEnableDataplane_DomainScopedTokenRejected(t *testing.T) {
	rgw := newFakeRGW(t)
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, map[string]string{"domain_id": "default", "user_id": "user-abc"})

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusConflict, "token scoped to the project")
	assertNoCalls(t, rgw)
	assertStored(t, store, false)
}

// TestEnableDataplane_CreatesContainerACLAndProbe proves a missing container is
// created, gets the read and write ACLs for the project and the writer, and
// the quota probe object is written and deleted, all with the caller's token.
func TestEnableDataplane_CreatesContainerACLAndProbe(t *testing.T) {
	rgw := newFakeRGW(t)
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth(testProjectID))

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusOK, `"enabled": true`)

	want := []string{
		"HEAD " + testContainer,
		"PUT " + testContainer,
		"POST " + testContainer,
		"PUT " + testProbeObject,
		"DELETE " + testProbeObject,
	}
	if got := rgw.Methods(); !slices.Equal(got, want) {
		t.Fatalf("object store calls = %q, want %q", got, want)
	}
	calls := rgw.Calls()
	for _, c := range calls {
		if c.Token != testCallerToken {
			t.Errorf("%s %s used token %q, want the caller's token", c.Method, c.Path, c.Token)
		}
	}
	if got, want := calls[2].Read, testProjectID+":*,"+testWriterGrant; got != want {
		t.Errorf("X-Container-Read = %q, want %q", got, want)
	}
	if got := calls[2].Write; got != testWriterGrant {
		t.Errorf("X-Container-Write = %q, want %q", got, testWriterGrant)
	}
	if got := calls[3].Body; len(got) != 1 {
		t.Errorf("probe object has %d bytes, want 1", len(got))
	}
	if len(rgw.Objects) != 0 {
		t.Errorf("probe object was not deleted: %v", rgw.Objects)
	}
	assertStored(t, store, true)
}

// TestEnableDataplane_MergesExistingACLs proves an existing container is not
// recreated and its other ACL entries survive, without duplicates.
func TestEnableDataplane_MergesExistingACLs(t *testing.T) {
	rgw := newFakeRGW(t)
	rgw.Exists = true
	rgw.Read = "other-project:*"
	rgw.Write = "*:someone-else, " + testWriterGrant
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth(testProjectID))

	rec := putJSON(t, handler, map[string]any{"enabled": true, "target_bucket": "hermes-audit"})
	assertResponse(t, rec, http.StatusOK, `"enabled": true`)

	want := []string{
		"HEAD " + testContainer,
		"POST " + testContainer,
		"PUT " + testProbeObject,
		"DELETE " + testProbeObject,
	}
	if got := rgw.Methods(); !slices.Equal(got, want) {
		t.Fatalf("object store calls = %q, want %q", got, want)
	}
	if want := "*:someone-else," + testWriterGrant; rgw.Write != want {
		t.Errorf("write ACL = %q, want %q", rgw.Write, want)
	}
	if want := "other-project:*," + testProjectID + ":*," + testWriterGrant; rgw.Read != want {
		t.Errorf("read ACL = %q, want %q", rgw.Read, want)
	}
	assertStored(t, store, true)
}

// TestEnableDataplane_SkipsPOSTWhenACLsComplete proves a container that already
// has every grant, in whatever order RGW returns them, is not POSTed: a POST
// would fail on a container another project still owns.
func TestEnableDataplane_SkipsPOSTWhenACLsComplete(t *testing.T) {
	rgw := newFakeRGW(t)
	rgw.Exists = true
	rgw.Read = testWriterGrant + ",other-project:*, " + testProjectID + ":*"
	rgw.Write = testWriterGrant + ",*:someone-else"
	// A POST would fail like it does on a container owned by another project.
	rgw.Fail["POST container"] = http.StatusForbidden
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth(testProjectID))

	for range 2 { // second run proves idempotence
		rec := putJSON(t, handler, map[string]any{"enabled": true})
		assertResponse(t, rec, http.StatusOK, `"enabled": true`)
	}
	for _, c := range rgw.Calls() {
		if c.Method == http.MethodPost || (c.Method == http.MethodPut && c.Path == testContainer) {
			t.Errorf("unexpected %s %s on a container whose ACLs are complete", c.Method, c.Path)
		}
	}
	assertStored(t, store, true)
}

// TestEnableDataplane_AcceptsMigratedBucketNotHandedOver proves a container the
// operator migrated (ACLs complete) but that is still owned by the operator
// account is accepted when the probe is denied: no POST, config saved.
func TestEnableDataplane_AcceptsMigratedBucketNotHandedOver(t *testing.T) {
	rgw := newFakeRGW(t)
	rgw.Exists = true
	rgw.Read = testProjectID + ":*," + testWriterGrant
	rgw.Write = testWriterGrant
	rgw.Fail["PUT object"] = http.StatusForbidden
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth(testProjectID))

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusOK, `"enabled": true`)

	want := []string{"HEAD " + testContainer, "PUT " + testProbeObject}
	if got := rgw.Methods(); !slices.Equal(got, want) {
		t.Fatalf("object store calls = %q, want %q", got, want)
	}
	assertStored(t, store, true)
}

// TestEnableDataplane_ObjectStoreErrors proves RGW failures map to clear API
// errors and nothing is saved.
func TestEnableDataplane_ObjectStoreErrors(t *testing.T) {
	const (
		roleMsg   = "your token needs the objectstore_admin role in this project"
		deniedMsg = "object storage denied the request"
		ownedMsg  = "container hermes-audit exists but is owned by another project; ask the operator to migrate it or choose another target_bucket"
		quotaMsg  = "project has no free object storage quota"
	)
	tests := []struct {
		name       string
		exists     bool
		fail       string
		status     int
		wantStatus int
		wantBody   string
	}{
		{"HeadUnauthorized", true, "HEAD container", http.StatusUnauthorized, http.StatusForbidden, roleMsg},
		{"HeadForbidden", true, "HEAD container", http.StatusForbidden, http.StatusForbidden, deniedMsg},
		{"CreateUnauthorized", false, "PUT container", http.StatusUnauthorized, http.StatusForbidden, roleMsg},
		{"CreateForbidden", false, "PUT container", http.StatusForbidden, http.StatusForbidden, deniedMsg},
		{"CreateTooManyBuckets", false, "PUT container", http.StatusBadRequest, http.StatusConflict, "this project cannot create more containers"},
		{"CreateNameTaken", false, "PUT container", http.StatusConflict, http.StatusConflict, "container name is not available"},
		{"CreateServerError", false, "PUT container", http.StatusInternalServerError, http.StatusBadGateway, "unexpected status 500"},
		{"ACLUnauthorized", true, "POST container", http.StatusUnauthorized, http.StatusForbidden, roleMsg},
		{"ACLForbiddenOnExisting", true, "POST container", http.StatusForbidden, http.StatusConflict, ownedMsg},
		{"ACLForbiddenAfterCreate", false, "POST container", http.StatusForbidden, http.StatusForbidden, deniedMsg},
		{"ProbeQuota", false, "PUT object", http.StatusRequestEntityTooLarge, http.StatusConflict, quotaMsg},
		{"ProbeQuotaExisting", true, "PUT object", http.StatusRequestEntityTooLarge, http.StatusConflict, quotaMsg},
		{"ProbeForbiddenOnExisting", true, "PUT object", http.StatusForbidden, http.StatusConflict, ownedMsg},
		{"ProbeDeleteServerError", false, "DELETE object", http.StatusServiceUnavailable, http.StatusBadGateway, "unexpected status 503"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rgw := newFakeRGW(t)
			rgw.Exists = tc.exists
			rgw.Fail[tc.fail] = tc.status
			handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth(testProjectID))

			rec := putJSON(t, handler, map[string]any{"enabled": true})
			assertResponse(t, rec, tc.wantStatus, tc.wantBody)
			assertStored(t, store, false)
		})
	}
}

// TestEnableDataplane_ProbeAlreadyGone proves a 404 on deleting the probe
// object does not fail the enable.
func TestEnableDataplane_ProbeAlreadyGone(t *testing.T) {
	rgw := newFakeRGW(t)
	rgw.Fail["DELETE object"] = http.StatusNotFound
	handler, store, _ := setupDataplaneTestWith(t, rgw, testWriterUserID, scopedAuth(testProjectID))

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusOK, `"enabled": true`)
	assertStored(t, store, true)
}

// TestEnableDataplane_EndpointMissing proves a region without the object store
// in the catalog gets 503 and nothing is sent or saved.
func TestEnableDataplane_EndpointMissing(t *testing.T) {
	rgw := newFakeRGW(t)
	ks := newFakeKeystone(rgw, scopedAuth(testProjectID))
	ks.wantService = "some-other-object-store"
	handler, store, _ := setupDataplaneTestKeystone(t, ks, testWriterUserID)

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusServiceUnavailable, "object storage for dataplane is not available in this region")
	assertNoCalls(t, rgw)
	assertStored(t, store, false)
}

// TestEnableDataplane_EndpointOtherAccountRefused proves the caller's token is
// never sent to an endpoint that is not the project's own account.
func TestEnableDataplane_EndpointOtherAccountRefused(t *testing.T) {
	for name, endpoint := range map[string]func(*fakeRGW) string{
		"OtherProject": func(rgw *fakeRGW) string { return rgw.URL + "/swift/v1/AUTH_other-project" },
		"ProjectAsPrefix": func(rgw *fakeRGW) string {
			return rgw.URL + "/swift/v1/AUTH_" + testProjectID + "-evil"
		},
		"NoAccount": func(rgw *fakeRGW) string { return rgw.URL + "/swift/v1" },
	} {
		t.Run(name, func(t *testing.T) {
			rgw := newFakeRGW(t)
			ks := newFakeKeystone(rgw, scopedAuth(testProjectID))
			ks.endpoint = func(string) string { return endpoint(rgw) }
			handler, store, _ := setupDataplaneTestKeystone(t, ks, testWriterUserID)

			rec := putJSON(t, handler, map[string]any{"enabled": true})
			assertResponse(t, rec, http.StatusBadGateway, "cannot determine the object storage endpoint")
			assertNoCalls(t, rgw)
			assertStored(t, store, false)
		})
	}
}

// TestEnableDataplane_RedirectNotFollowed proves a redirect from the object
// store does not carry the caller's token to another server.
func TestEnableDataplane_RedirectNotFollowed(t *testing.T) {
	other := newFakeRGW(t)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+testContainer, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	ks := newFakeKeystone(other, scopedAuth(testProjectID))
	ks.endpoint = func(projectID string) string { return redirector.URL + "/swift/v1/AUTH_" + projectID }
	handler, store, _ := setupDataplaneTestKeystone(t, ks, testWriterUserID)

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusBadGateway, "unexpected status 307")
	assertNoCalls(t, other)
	assertStored(t, store, false)
}

// TestEnableDataplane_WriterUnset proves enabling fails loudly, and touches
// nothing, when the writer user id is not configured.
func TestEnableDataplane_WriterUnset(t *testing.T) {
	rgw := newFakeRGW(t)
	handler, store, _ := setupDataplaneTestWith(t, rgw, "", scopedAuth(testProjectID))

	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, http.StatusServiceUnavailable, "not configured")
	assertNoCalls(t, rgw)
	assertStored(t, store, false)
}

// TestDisableDataplane_DoesNotTouchBucket proves enabled=false never calls the
// object store, even for a token scoped elsewhere.
func TestDisableDataplane_DoesNotTouchBucket(t *testing.T) {
	rgw := newFakeRGW(t)
	handler, _, _ := setupDataplaneTestWith(t, rgw, "", scopedAuth("cloud-admin-project"))

	rec := putJSON(t, handler, map[string]any{"enabled": false})
	assertResponse(t, rec, http.StatusOK, `"enabled": false`)
	assertNoCalls(t, rgw)
}

func TestMergeACL(t *testing.T) {
	tests := []struct {
		existing string
		add      []string
		want     string
	}{
		{"", []string{"*:w"}, "*:w"},
		{"*:a", []string{"*:w"}, "*:a,*:w"},
		{"*:w,*:a", []string{"*:w"}, "*:w,*:a"},
		{" *:a , ,*:a", []string{"*:w"}, "*:a,*:w"},
		{"p:*", []string{"p:*", "*:w"}, "p:*,*:w"},
	}
	for _, tc := range tests {
		if got := mergeACL(tc.existing, tc.add...); got != tc.want {
			t.Errorf("mergeACL(%q, %q) = %q, want %q", tc.existing, tc.add, got, tc.want)
		}
	}
}

func TestSameACL(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"*:w,p:*", "p:*, *:w", true},
		{"*:w,p:*", "p:*,*:w,p:*", true},
		{"*:w", "*:w,p:*", false},
		{"*:w", "", false},
	}
	for _, tc := range tests {
		if got := sameACL(tc.a, tc.b); got != tc.want {
			t.Errorf("sameACL(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestEnsureDataplaneBucket_RejectsUnsafeNames proves the provisioning step
// checks the names it puts into the object store URL itself, even when called
// without the handler's validation: nothing is sent to the object store.
func TestEnsureDataplaneBucket_RejectsUnsafeNames(t *testing.T) {
	rgw := newFakeRGW(t)
	p := &v1Provider{dataplaneBucket: dataplaneBucketConfig{WriterUserID: testWriterUserID}}
	for _, c := range []struct{ project, bucket string }{
		{testProjectID, "../other"},
		{testProjectID, "a/b"},
		{testProjectID, "hermes-audit?x=1"},
		{"bad/project", "hermes-audit"},
	} {
		token := &gopherpolicy.Token{}
		token.Context.Auth = scopedAuth(c.project)
		berr := p.ensureDataplaneBucket(t.Context(), token, c.project, c.bucket)
		if berr == nil || berr.Status != http.StatusBadRequest {
			t.Errorf("project %q bucket %q: got %v, want 400", c.project, c.bucket, berr)
		}
	}
	assertNoCalls(t, rgw)
}

// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/spf13/viper"
)

const (
	testRoleName     = "objectstore_admin"
	testRoleID       = "role-id-42"
	testServiceToken = "hermes-service-token"
	// testGrant is the Keystone path of the writer's role assignment.
	testGrant = "/v3/projects/" + testProjectID + "/users/" + testWriterUserID + "/roles/" + testRoleID
	// testCheck is the service token's read of the same assignment.
	testCheck     = "/v3/role_assignments?effective=true&role.id=" + testRoleID + "&scope.project.id=" + testProjectID + "&user.id=" + testWriterUserID
	testRoleList  = "/v3/roles?name=" + testRoleName
	testAdminHint = "openstack role add --user " + testWriterUserID + " --project " + testProjectID + " " + testRoleName
)

// dropConnection as a Fail status makes the fake Keystone close the
// connection without an answer.
const dropConnection = -1

// fakeIdentity is an httptest stand-in for the parts of Keystone v3 that the
// writer role step uses: the role list, the role assignment list, and one
// project role grant. Calls record the request URI (path and query).
type fakeIdentity struct {
	*httptest.Server
	mu sync.Mutex
	// Assigned is whether the writer has the role in the project.
	Assigned bool
	// Fail maps "METHOD kind" (kind: roles, assignments, grant) to the status
	// that request answers instead.
	Fail  map[string]int
	calls []rgwCall
}

func newFakeIdentity(t *testing.T) *fakeIdentity {
	t.Helper()
	f := &fakeIdentity{Fail: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, rgwCall{Method: r.Method, Path: r.URL.RequestURI(), Token: r.Header.Get("X-Auth-Token")})
		kind := "grant"
		switch r.URL.Path {
		case "/v3/roles":
			kind = "roles"
		case "/v3/role_assignments":
			kind = "assignments"
		}
		if status, ok := f.Fail[r.Method+" "+kind]; ok {
			if status == dropConnection {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			w.WriteHeader(status)
			return
		}
		switch {
		case r.Method == http.MethodGet && kind == "roles":
			var roles []map[string]string
			if r.URL.Query().Get("name") == testRoleName {
				roles = append(roles, map[string]string{"id": testRoleID, "name": testRoleName})
			}
			writeJSON(t, w, map[string]any{"roles": roles, "links": map[string]any{}})
		case r.Method == http.MethodGet && kind == "assignments":
			assignments := []map[string]any{}
			if f.Assigned {
				assignments = append(assignments, map[string]any{
					"role":  map[string]string{"id": testRoleID},
					"scope": map[string]any{"project": map[string]string{"id": testProjectID}},
					"user":  map[string]string{"id": testWriterUserID},
				})
			}
			writeJSON(t, w, map[string]any{"role_assignments": assignments, "links": map[string]any{}})
		case r.URL.Path != testGrant:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut:
			f.Assigned = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			f.Assigned = false
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func writeJSON(t *testing.T, w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Error(err)
	}
}

func (f *fakeIdentity) Calls() []rgwCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rgwCall(nil), f.calls...)
}

// Count returns how many calls went to "METHOD uri".
func (f *fakeIdentity) Count(methodURI string) int {
	n := 0
	for _, m := range callMethods(f.Calls()) {
		if m == methodURI {
			n++
		}
	}
	return n
}

// serviceClient is Hermes' own identity client against the fake Keystone.
func (f *fakeIdentity) serviceClient() *gophercloud.ServiceClient {
	return &gophercloud.ServiceClient{
		ProviderClient: &gophercloud.ProviderClient{TokenID: testServiceToken},
		Endpoint:       f.URL + "/v3/",
	}
}

// writerRoleOn turns the role assignment on against the fake Keystone, with
// the role ID already resolved.
func writerRoleOn(ks *fakeIdentity) func(*dataplaneWriterRole) {
	return func(r *dataplaneWriterRole) {
		r.Assign = true
		r.RoleName = testRoleName
		r.roleID = testRoleID
		r.Service = ks.serviceClient()
	}
}

// setupWriterRoleTest builds the API against the fake object store and the
// fake Keystone. The caller's catalog has the fake Keystone as identity
// endpoint, like Keystone's catalog: versioned, without a trailing slash.
func setupWriterRoleTest(t *testing.T, rgw *fakeRGW, ks *fakeIdentity, auth map[string]string, writerID string, configure func(*dataplaneWriterRole)) (handler http.Handler, stored func() bool) {
	t.Helper()
	validator := newFakeKeystone(rgw, auth)
	validator.identityEndpoint = ks.URL + "/v3"
	handler, store, _ := setupDataplaneTestRole(t, validator, writerID, configure)
	stored = func() bool {
		cfg, err := store.Get(t.Context(), testProjectID)
		return err == nil && cfg.Enabled
	}
	return handler, stored
}

// enable sends PUT enabled=true and checks the answer.
func enable(t *testing.T, handler http.Handler, wantStatus int, wantBody string) *httptest.ResponseRecorder {
	t.Helper()
	rec := putJSON(t, handler, map[string]any{"enabled": true})
	assertResponse(t, rec, wantStatus, wantBody)
	return rec
}

func assertIdentityCalls(t *testing.T, ks *fakeIdentity, want ...string) {
	t.Helper()
	if got := callMethods(ks.Calls()); !slices.Equal(got, want) {
		t.Errorf("Keystone calls = %q, want %q", got, want)
	}
}

// assertTokens checks that every read went out with the service token and
// every grant with the caller's token.
func assertTokens(t *testing.T, ks *fakeIdentity) {
	t.Helper()
	for _, c := range ks.Calls() {
		want := testCallerToken
		if c.Method == http.MethodGet {
			want = testServiceToken
		}
		if c.Token != want {
			t.Errorf("%s %s used token %q, want %q", c.Method, c.Path, c.Token, want)
		}
	}
}

func assertBucketCreated(t *testing.T, rgw *fakeRGW, want bool) {
	t.Helper()
	if got := slices.Contains(rgw.Methods(), "PUT "+testContainer); got != want {
		t.Errorf("bucket created = %v, want %v; object store calls %q", got, want, rgw.Methods())
	}
}

func assertSaved(t *testing.T, stored func() bool, want bool) {
	t.Helper()
	if got := stored(); got != want {
		t.Errorf("enabled config saved = %v, want %v", got, want)
	}
}

// TestWriterRole_GateOff proves that with hermes.dataplane_assign_writer_role
// off, enabling does not talk to Keystone at all and works as before.
func TestWriterRole_GateOff(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, func(r *dataplaneWriterRole) {
		writerRoleOn(ks)(r)
		r.Assign = false
	})

	enable(t, handler, http.StatusOK, `"enabled": true`)
	assertIdentityCalls(t, ks)
	assertBucketCreated(t, rgw, true)
	assertSaved(t, stored, true)
}

// TestWriterRole_PresentSkipsGrant proves that when the service token sees the
// role (assigned by an admin, or on a re-enable), a caller who may not assign
// roles can still enable: no grant is attempted.
func TestWriterRole_PresentSkipsGrant(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	ks.Assigned = true
	ks.Fail["PUT grant"] = http.StatusForbidden
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))

	enable(t, handler, http.StatusOK, `"enabled": true`)
	assertIdentityCalls(t, ks, "GET "+testCheck)
	assertTokens(t, ks)
	assertBucketCreated(t, rgw, true)
	assertSaved(t, stored, true)
}

// TestWriterRole_AssignsWhenMissing proves a missing role is assigned with the
// caller's token on the URL of the project, the writer user and the role,
// after a check with the service token, and then the bucket is created.
func TestWriterRole_AssignsWhenMissing(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))

	enable(t, handler, http.StatusOK, `"enabled": true`)
	assertIdentityCalls(t, ks, "GET "+testCheck, "PUT "+testGrant)
	assertTokens(t, ks)
	if !ks.Assigned {
		t.Error("the role was not assigned")
	}
	assertBucketCreated(t, rgw, true)
	assertSaved(t, stored, true)
}

// TestWriterRole_GrantErrors proves how a failed grant with the caller's token
// is answered, and that nothing is created or saved on any of them.
func TestWriterRole_GrantErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantStatus int
		wantBody   string
		// notBody must not be in the answer.
		notBody string
	}{
		{"Forbidden", http.StatusForbidden, http.StatusForbidden, testAdminHint, ""},
		{"Unauthorized", http.StatusUnauthorized, http.StatusUnauthorized, "your token was rejected by the identity service", "openstack role add"},
		{"NotFound", http.StatusNotFound, http.StatusServiceUnavailable, "role assignment is not configured", "openstack role add"},
		{"ServerError", http.StatusServiceUnavailable, http.StatusBadGateway, "identity service returned unexpected status 503", ""},
		{"ConnectionDropped", dropConnection, http.StatusBadGateway, "identity service request failed", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rgw, ks := newFakeRGW(t), newFakeIdentity(t)
			ks.Fail["PUT grant"] = tc.status
			handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))

			rec := enable(t, handler, tc.wantStatus, tc.wantBody)
			if tc.notBody != "" && strings.Contains(rec.Body.String(), tc.notBody) {
				t.Errorf("body %q must not contain %q", rec.Body, tc.notBody)
			}
			assertIdentityCalls(t, ks, "GET "+testCheck, "PUT "+testGrant)
			assertNoCalls(t, rgw)
			assertSaved(t, stored, false)
		})
	}
}

// TestWriterRole_CheckErrors proves a failed check with the service token is
// never followed by a grant: 503 when Hermes' own token is refused (a setup
// problem), 502 otherwise.
func TestWriterRole_CheckErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantStatus int
		wantBody   string
	}{
		{"Unauthorized", http.StatusUnauthorized, http.StatusServiceUnavailable, "role assignment is not configured"},
		{"Forbidden", http.StatusForbidden, http.StatusServiceUnavailable, "role assignment is not configured"},
		{"ServerError", http.StatusInternalServerError, http.StatusBadGateway, "cannot check the dataplane writer role"},
		{"ConnectionDropped", dropConnection, http.StatusBadGateway, "cannot check the dataplane writer role"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rgw, ks := newFakeRGW(t), newFakeIdentity(t)
			ks.Fail["GET assignments"] = tc.status
			handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))

			enable(t, handler, tc.wantStatus, tc.wantBody)
			assertIdentityCalls(t, ks, "GET "+testCheck)
			assertNoCalls(t, rgw)
			assertSaved(t, stored, false)
		})
	}
}

// TestWriterRole_KeystoneDown proves an unreachable Keystone answers 502 and
// nothing is created or saved.
func TestWriterRole_KeystoneDown(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))
	ks.Close()

	enable(t, handler, http.StatusBadGateway, "cannot check the dataplane writer role")
	assertNoCalls(t, rgw)
	assertSaved(t, stored, false)
}

// TestWriterRole_Preconditions proves that with the gate on, a token not
// scoped to exactly the project (a cloud admin's, a domain-scoped one) or a
// missing writer user never reaches Keystone or the object store.
func TestWriterRole_Preconditions(t *testing.T) {
	tests := []struct {
		name       string
		auth       map[string]string
		writerID   string
		wantStatus int
		wantBody   string
	}{
		{"CloudAdminOtherProject", scopedAuth("cloud-admin-project"), testWriterUserID, http.StatusConflict, "token scoped to the project"},
		{"DomainScoped", map[string]string{"domain_id": "default", "user_id": "user-abc"}, testWriterUserID, http.StatusConflict, "token scoped to the project"},
		{"WriterUnset", scopedAuth(testProjectID), "", http.StatusServiceUnavailable, "not configured"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rgw, ks := newFakeRGW(t), newFakeIdentity(t)
			// cluster_viewer is allowed by the mock enforcer, so the
			// cross-project token passes authDataplaneConfig.
			handler, stored := setupWriterRoleTest(t, rgw, ks, tc.auth, tc.writerID, writerRoleOn(ks))

			enable(t, handler, tc.wantStatus, tc.wantBody)
			assertIdentityCalls(t, ks)
			assertNoCalls(t, rgw)
			assertSaved(t, stored, false)
		})
	}
}

// TestWriterRole_KeptWhenBucketFails proves the role stays when the bucket
// step fails, whether this request assigned it or it was there before: no
// DELETE is sent, and the caller gets the bucket error.
func TestWriterRole_KeptWhenBucketFails(t *testing.T) {
	tests := []struct {
		name      string
		preset    bool
		wantCalls []string
	}{
		{"AssignedByThisRequest", false, []string{"GET " + testCheck, "PUT " + testGrant}},
		{"AssignedBefore", true, []string{"GET " + testCheck}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rgw, ks := newFakeRGW(t), newFakeIdentity(t)
			ks.Assigned = tc.preset
			rgw.Fail["PUT object"] = http.StatusRequestEntityTooLarge
			handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))

			enable(t, handler, http.StatusConflict, "project has no free object storage quota")
			assertIdentityCalls(t, ks, tc.wantCalls...)
			if !ks.Assigned {
				t.Error("the role is gone after a failed bucket step")
			}
			assertSaved(t, stored, false)
		})
	}
}

// TestWriterRole_ReEnableGrants proves the upgrade path for projects enabled
// while the switch was off: once it is on, enabling the already enabled
// project again checks and assigns the role like a first enable.
func TestWriterRole_ReEnableGrants(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	var role *dataplaneWriterRole
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, func(r *dataplaneWriterRole) {
		writerRoleOn(ks)(r)
		r.Assign = false
		role = r
	})

	enable(t, handler, http.StatusOK, `"enabled": true`)
	assertIdentityCalls(t, ks)
	assertSaved(t, stored, true)

	role.Assign = true
	enable(t, handler, http.StatusOK, `"enabled": true`)
	assertIdentityCalls(t, ks, "GET "+testCheck, "PUT "+testGrant)
	assertTokens(t, ks)
	if !ks.Assigned {
		t.Error("re-enable did not assign the role")
	}
	assertSaved(t, stored, true)
}

// TestWriterRole_LazyResolution proves a role lookup that failed at startup is
// tried again on enable (503 while it keeps failing, nothing touched), and
// once it works the ID is kept and not looked up again.
func TestWriterRole_LazyResolution(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	ks.Fail["GET roles"] = http.StatusInternalServerError
	var role *dataplaneWriterRole
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, func(r *dataplaneWriterRole) {
		writerRoleOn(ks)(r)
		r.roleID = ""
		role = r
	})
	role.resolveAtStartup(t.Context())
	if got := ks.Count("GET " + testRoleList); got != 1 {
		t.Fatalf("startup looked up the role %d times, want 1", got)
	}

	enable(t, handler, http.StatusServiceUnavailable, "role assignment is not configured")
	if got := ks.Count("GET " + testRoleList); got != 2 {
		t.Errorf("enable after a failed startup lookup looked up the role %d times in total, want 2", got)
	}
	assertNoCalls(t, rgw)
	assertSaved(t, stored, false)

	delete(ks.Fail, "GET roles")
	enable(t, handler, http.StatusOK, `"enabled": true`)
	enable(t, handler, http.StatusOK, `"enabled": true`)
	if got := ks.Count("GET " + testRoleList); got != 3 {
		t.Errorf("role was looked up %d times in total, want 3 (cached after the first success)", got)
	}
	if !ks.Assigned {
		t.Error("the role was not assigned after the lookup worked")
	}
	assertSaved(t, stored, true)
}

// TestWriterRole_ConfiguredIDSkipsLookup proves hermes.dataplane_writer_role_id
// (env HERMES_DATAPLANE_WRITER_ROLE_ID) is used as is, without a lookup by
// name.
func TestWriterRole_ConfiguredIDSkipsLookup(t *testing.T) {
	t.Cleanup(viper.Reset)
	viper.Set("hermes.dataplane_assign_writer_role", true)
	viper.Set("hermes.dataplane_writer_role", testRoleName)
	viper.Set("hermes.dataplane_writer_role_id", " "+testRoleID+" ")
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	ks.Fail["GET roles"] = http.StatusInternalServerError
	var role *dataplaneWriterRole
	handler, stored := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, func(r *dataplaneWriterRole) {
		r.Service = ks.serviceClient()
		role = r
	})
	role.resolveAtStartup(t.Context())

	enable(t, handler, http.StatusOK, `"enabled": true`)
	assertIdentityCalls(t, ks, "GET "+testCheck, "PUT "+testGrant)
	assertSaved(t, stored, true)
}

// TestWriterRole_EndpointRefused proves the caller's token is only sent to the
// Keystone Hermes itself uses: another identity endpoint in the caller's
// catalog, or none, gets 502 and no grant request.
func TestWriterRole_EndpointRefused(t *testing.T) {
	for name, catalog := range map[string]func(ks, other *fakeIdentity) string{
		"OtherHost": func(_, other *fakeIdentity) string { return other.URL + "/v3" },
		"OtherPath": func(ks, _ *fakeIdentity) string { return ks.URL + "/evil/v3" },
		"Missing":   func(_, _ *fakeIdentity) string { return "" },
	} {
		t.Run(name, func(t *testing.T) {
			rgw, ks, other := newFakeRGW(t), newFakeIdentity(t), newFakeIdentity(t)
			validator := newFakeKeystone(rgw, scopedAuth(testProjectID))
			validator.identityEndpoint = catalog(ks, other)
			handler, store, _ := setupDataplaneTestRole(t, validator, testWriterUserID, writerRoleOn(ks))

			enable(t, handler, http.StatusBadGateway, "cannot determine the identity endpoint")
			assertIdentityCalls(t, ks, "GET "+testCheck)
			assertIdentityCalls(t, other)
			assertNoCalls(t, rgw)
			assertStored(t, store, false)
		})
	}
}

// TestWriterRole_DisableDoesNotTouchKeystone proves enabled=false neither
// checks nor removes the role.
func TestWriterRole_DisableDoesNotTouchKeystone(t *testing.T) {
	rgw, ks := newFakeRGW(t), newFakeIdentity(t)
	ks.Assigned = true
	handler, _ := setupWriterRoleTest(t, rgw, ks, scopedAuth(testProjectID), testWriterUserID, writerRoleOn(ks))

	rec := putJSON(t, handler, map[string]any{"enabled": false})
	assertResponse(t, rec, http.StatusOK, `"enabled": false`)
	assertIdentityCalls(t, ks)
	assertNoCalls(t, rgw)
}

func TestIdentityV3Endpoint(t *testing.T) {
	for _, in := range []string{
		"https://identity.example.com/v3",
		"https://identity.example.com/v3/",
		"https://identity.example.com",
		"https://identity.example.com/",
	} {
		got, err := identityV3Endpoint(in)
		if err != nil || got != "https://identity.example.com/v3/" {
			t.Errorf("identityV3Endpoint(%q) = %q, %v", in, got, err)
		}
	}
}

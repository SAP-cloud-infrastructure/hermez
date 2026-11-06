// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gophercloud/gophercloud/v2"
)

// fakeRoleList serves GET /v3/roles with the given roles, or the given status
// when it is not 200. It records the query of the last request.
func fakeRoleList(t *testing.T, status int, roles []map[string]string, query, token *string) *gophercloud.ServiceClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/roles" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*query = r.URL.RawQuery
		*token = r.Header.Get("X-Auth-Token")
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"roles": roles, "links": map[string]any{}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return &gophercloud.ServiceClient{
		ProviderClient: &gophercloud.ProviderClient{TokenID: "service-token"},
		Endpoint:       srv.URL + "/v3/",
	}
}

func TestResolveRoleID(t *testing.T) {
	role := func(id, name string) map[string]string { return map[string]string{"id": id, "name": name} }
	tests := []struct {
		name    string
		status  int
		roles   []map[string]string
		want    string
		wantErr bool
	}{
		{"one match", http.StatusOK, []map[string]string{role("r1", "objectstore_admin")}, "r1", false},
		{"no match", http.StatusOK, nil, "", true},
		{"other name ignored", http.StatusOK, []map[string]string{role("r2", "objectstore_viewer")}, "", true},
		{"ambiguous", http.StatusOK, []map[string]string{role("r1", "objectstore_admin"), role("r3", "objectstore_admin")}, "", true},
		{"forbidden", http.StatusForbidden, nil, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var query, token string
			client := fakeRoleList(t, tt.status, tt.roles, &query, &token)
			got, err := ResolveRoleID(t.Context(), client, " objectstore_admin ")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			// Only global roles: filter by name, never by domain.
			if query != "name=objectstore_admin" {
				t.Errorf("query = %q, want only the name filter", query)
			}
			if token != "service-token" {
				t.Errorf("token = %q, want the service token", token)
			}
		})
	}
}

func TestResolveRoleIDEmptyName(t *testing.T) {
	if _, err := ResolveRoleID(t.Context(), nil, "  "); err == nil {
		t.Error("want an error for an empty role name")
	}
}

func TestHasProjectRole(t *testing.T) {
	assignment := func(user, project, role string) map[string]any {
		return map[string]any{
			"user":  map[string]string{"id": user},
			"scope": map[string]any{"project": map[string]string{"id": project}},
			"role":  map[string]string{"id": role},
		}
	}
	tests := []struct {
		name        string
		status      int
		assignments []map[string]any
		want        bool
		wantErr     bool
	}{
		{"assigned", http.StatusOK, []map[string]any{assignment("w", "p", "r")}, true, false},
		{"not assigned", http.StatusOK, nil, false, false},
		{"unrelated assignment ignored", http.StatusOK, []map[string]any{assignment("w", "other", "r")}, false, false},
		{"forbidden", http.StatusForbidden, nil, false, true},
		{"server error", http.StatusInternalServerError, nil, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var query, token string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v3/role_assignments" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				query, token = r.URL.RawQuery, r.Header.Get("X-Auth-Token")
				if tt.status != http.StatusOK {
					w.WriteHeader(tt.status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"role_assignments": tt.assignments, "links": map[string]any{}}); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(srv.Close)
			client := &gophercloud.ServiceClient{
				ProviderClient: &gophercloud.ProviderClient{TokenID: "service-token"},
				Endpoint:       srv.URL + "/v3/",
			}

			got, err := HasProjectRole(t.Context(), client, "w", "p", "r")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
			if want := "effective=true&role.id=r&scope.project.id=p&user.id=w"; query != want {
				t.Errorf("query = %q, want %q", query, want)
			}
			if token != "service-token" {
				t.Errorf("token = %q, want the service token", token)
			}
		})
	}
}

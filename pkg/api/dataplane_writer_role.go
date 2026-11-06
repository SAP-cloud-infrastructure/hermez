// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/utils"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/hermes/pkg/identity"
)

// dataplaneRoleTimeout bounds all Keystone requests of one enable.
const dataplaneRoleTimeout = 30 * time.Second

// dataplaneWriterRole gives the log-router writer user a role in the
// customer's project on enable, so log-router can log in with a token scoped
// to the project and the objects it writes are owned by the project.
//
// Hermes' own service token is only used to read: to look up the role ID and
// to check whether the writer already has the role. Assigning is done with the
// caller's token.
type dataplaneWriterRole struct {
	// Assign turns the role assignment on. When false, enabling does not talk
	// to Keystone.
	Assign bool
	// RoleName is the name of the role, looked up when no ID is configured.
	RoleName string
	// Service is Hermes' own identity v3 client. Nil without the keystone
	// driver. Its endpoint is the only one the caller's token is sent to.
	Service *gophercloud.ServiceClient

	mu sync.Mutex
	// roleID is the configured or resolved role ID; empty until resolved.
	roleID string
}

// newDataplaneWriterRole returns the writer role setup. A non-empty roleID
// skips the lookup by name.
func newDataplaneWriterRole(assign bool, roleName, roleID string) *dataplaneWriterRole {
	return &dataplaneWriterRole{
		Assign:   assign,
		RoleName: strings.TrimSpace(roleName),
		roleID:   strings.TrimSpace(roleID),
	}
}

// RoleID returns the role ID, looking it up by name with the service token
// when it is not known yet. A failed lookup is tried again on the next call.
func (r *dataplaneWriterRole) RoleID(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.roleID != "" {
		return r.roleID, nil
	}
	if r.Service == nil {
		return "", fmt.Errorf("cannot look up role %q without the keystone driver", r.RoleName)
	}
	id, err := identity.ResolveRoleID(ctx, r.Service, r.RoleName)
	if err != nil {
		return "", err
	}
	r.roleID = id
	logg.Info("dataplane writer role %s has ID %s", r.RoleName, id)
	return id, nil
}

// resolveAtStartup looks up the role ID once at startup, so a problem shows up
// in the log right away. Enabling tries again if this fails.
func (r *dataplaneWriterRole) resolveAtStartup(ctx context.Context) {
	if !r.Assign {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, dataplaneRoleTimeout)
	defer cancel()
	id, err := r.RoleID(ctx)
	if err != nil {
		logg.Error("hermes.dataplane_assign_writer_role is on, but the dataplane writer role cannot be resolved: %s; enabling dataplane answers 503 until a lookup on enable works", err)
		return
	}
	logg.Info("dataplane writer role assignment is on: enabling dataplane gives the writer user role %s (ID %s) in the project, with the caller's token", r.RoleName, id)
}

// ensureDataplaneWriterRole makes sure the writer user has the writer role in
// the project. An assignment made here stays, even when a later enable step
// fails: the next enable finds it, and taking it back could race with a
// concurrent enable of the same project that relies on it.
func (p *v1Provider) ensureDataplaneWriterRole(ctx context.Context, token *gopherpolicy.Token, projectID string) *bucketError {
	r := p.dataplaneWriterRole
	if r == nil || !r.Assign {
		return nil
	}
	if berr := p.checkEnablePreconditions(token, projectID); berr != nil {
		return berr
	}
	writerID := p.dataplaneBucket.WriterUserID

	ctx, cancel := context.WithTimeout(ctx, dataplaneRoleTimeout)
	defer cancel()

	roleID, err := r.RoleID(ctx)
	if err != nil {
		logg.Error("dataplane-config PUT: hermes.dataplane_assign_writer_role is on but the role cannot be resolved, cannot enable dataplane for project %s: %s", projectID, err)
		return &bucketError{http.StatusServiceUnavailable, "dataplane writer role assignment is not configured on this Hermes instance"}
	}

	// Check with Hermes' own token, which may read assignments. A caller who
	// may not read them still gets through when the role is already there.
	present, err := identity.HasProjectRole(ctx, r.Service, writerID, projectID, roleID)
	if err != nil {
		logg.Error("dataplane-config PUT: cannot check role %s of the writer user %s in project %s with the service token: %s", roleID, writerID, projectID, err)
		if gophercloud.ResponseCodeIs(err, http.StatusUnauthorized) || gophercloud.ResponseCodeIs(err, http.StatusForbidden) {
			return &bucketError{http.StatusServiceUnavailable, "dataplane writer role assignment is not configured on this Hermes instance"}
		}
		return &bucketError{http.StatusBadGateway, "cannot check the dataplane writer role"}
	}
	if present {
		return nil
	}

	// Assign with the caller's token, only at the Keystone Hermes itself uses.
	if token.ProviderClient == nil || token.ProviderClient.EndpointLocator == nil {
		logg.Error("dataplane-config PUT: token for project %s has no service catalog access", projectID)
		return &bucketError{http.StatusBadGateway, "cannot determine the identity endpoint"}
	}
	endpoint, err := token.ProviderClient.EndpointLocator(gophercloud.EndpointOpts{
		Type:         "identity",
		Availability: gophercloud.AvailabilityPublic,
	})
	if err != nil {
		logg.Error("dataplane-config PUT: cannot look up the identity endpoint for project %s: %s", projectID, err)
		return &bucketError{http.StatusBadGateway, "cannot determine the identity endpoint"}
	}
	identityV3, err := identityV3Endpoint(endpoint)
	if err != nil || identityV3 != r.Service.Endpoint {
		logg.Error("dataplane-config PUT: refusing identity endpoint %q for project %s: not %q", endpoint, projectID, r.Service.Endpoint)
		return &bucketError{http.StatusBadGateway, "cannot determine the identity endpoint"}
	}
	client := p.bucketHTTPClient
	if client == nil {
		client = defaultBucketHTTPClient
	}
	keystone := callerClient{client: client, tokenID: token.ProviderClient.TokenID, projectID: projectID, service: "identity service"}
	assignment := identityV3 + "projects/" + url.PathEscape(projectID) +
		"/users/" + url.PathEscape(writerID) +
		"/roles/" + url.PathEscape(roleID)

	put, berr := keystone.do(ctx, http.MethodPut, assignment, nil, nil)
	if berr != nil {
		return berr
	}
	switch {
	case isSuccess(put.StatusCode):
		logg.Info("dataplane-config PUT: assigned role %s (%s) to the writer user %s in project %s", r.RoleName, roleID, writerID, projectID)
		return nil
	case put.StatusCode == http.StatusForbidden:
		logg.Info("dataplane-config PUT: caller may not assign role %s in project %s", r.RoleName, projectID)
		return &bucketError{http.StatusForbidden, fmt.Sprintf(
			"your token may not assign roles in this project, so Hermes cannot give the dataplane writer user the %[1]s role; "+
				"a project admin (role admin or role_admin in the project) must run: openstack role add --user %[2]s --project %[3]s %[1]s "+
				"(or enable dataplane themselves); after that, enabling works with your token too",
			r.RoleName, writerID, projectID)}
	case put.StatusCode == http.StatusUnauthorized:
		return &bucketError{http.StatusUnauthorized, "your token was rejected by the identity service; get a new token"}
	case put.StatusCode == http.StatusNotFound:
		logg.Error("dataplane-config PUT: Keystone answered 404 on assigning role %s to user %s in project %s: the writer user or the role does not exist (check hermes.dataplane_writer_user_id and hermes.dataplane_writer_role)", roleID, writerID, projectID)
		return &bucketError{http.StatusServiceUnavailable, "dataplane writer role assignment is not configured on this Hermes instance"}
	default:
		logg.Error("dataplane-config PUT: PUT %s for project %s returned %d", assignment, projectID, put.StatusCode)
		return &bucketError{http.StatusBadGateway, fmt.Sprintf("identity service returned unexpected status %d", put.StatusCode)}
	}
}

// identityV3Endpoint turns a catalog identity endpoint into the base URL of
// the v3 API, ending in "/v3/", the same way gophercloud's NewIdentityV3 does.
func identityV3Endpoint(endpoint string) (string, error) {
	base, err := utils.BaseEndpoint(endpoint)
	if err != nil {
		return "", err
	}
	return gophercloud.NormalizeURL(base) + "v3/", nil
}

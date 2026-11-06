// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/roles"
)

// ResolveRoleID returns the ID of the global Keystone role with the given
// name. It fails when there is no such role or more than one.
func ResolveRoleID(ctx context.Context, client *gophercloud.ServiceClient, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("no role name given")
	}
	if client == nil {
		return "", errors.New("no Keystone client")
	}
	// Without a domain_id filter Keystone lists only global roles, which are
	// the ones a project admin can assign.
	page, err := roles.List(client, roles.ListOpts{Name: name}).AllPages(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot list roles named %q: %w", name, err)
	}
	found, err := roles.ExtractRoles(page)
	if err != nil {
		return "", fmt.Errorf("cannot read the roles named %q: %w", name, err)
	}
	var ids []string
	for _, r := range found {
		// The name filter is exact, check anyway so a Keystone that ignores
		// it cannot hand us an unrelated role.
		if r.Name == name && r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	switch len(ids) {
	case 0:
		return "", fmt.Errorf("there is no global role named %q", name)
	case 1:
		return ids[0], nil
	default:
		return "", fmt.Errorf("there are %d global roles named %q", len(ids), name)
	}
}

// HasProjectRole reports whether the user has the role in the project,
// directly, through a group or inherited, as a token scoped to the project
// would show it. It only reads role assignments.
func HasProjectRole(ctx context.Context, client *gophercloud.ServiceClient, userID, projectID, roleID string) (bool, error) {
	if client == nil {
		return false, errors.New("no Keystone client")
	}
	effective := true
	page, err := roles.ListAssignments(client, roles.ListAssignmentsOpts{
		UserID:         userID,
		ScopeProjectID: projectID,
		RoleID:         roleID,
		Effective:      &effective,
	}).AllPages(ctx)
	if err != nil {
		return false, fmt.Errorf("cannot list role assignments of user %s in project %s: %w", userID, projectID, err)
	}
	found, err := roles.ExtractRoleAssignments(page)
	if err != nil {
		return false, fmt.Errorf("cannot read role assignments of user %s in project %s: %w", userID, projectID, err)
	}
	// The filters are exact; check anyway so a Keystone that ignores one
	// cannot make an unrelated assignment count.
	for _, a := range found {
		if a.Role.ID == roleID && a.Scope.Project.ID == projectID && a.User.ID == userID {
			return true, nil
		}
	}
	return false, nil
}

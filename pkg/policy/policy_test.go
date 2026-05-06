// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package policy

import (
	"testing"

	"encoding/json"
	"os"

	policy "github.com/databus23/goslo.policy"
	"github.com/stretchr/testify/assert"

	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/hermes/pkg/util"
)

// Setup test
func GetEnforcer() *policy.Enforcer {
	const path = "../../etc/policy.json"

	policyenforcer, err := util.LoadPolicyFile(path)

	if err != nil {
		return nil
	}
	return policyenforcer
}

func Test_Policy_AuditViewerTrue(t *testing.T) {
	enforcer := GetEnforcer()
	c := policy.Context{
		Roles: []string{
			"audit_viewer",
		},
		// Auth will only have one entry
		Auth: map[string]string{
			// "domain_id":           "ca1b267e149d4e44bf53d28d1c8d6bc9",
			"project_id": "7a09c05926ec452ca7992af4aa03c31d",
		},
		Request: map[string]string{
			"project_id": "7a09c05926ec452ca7992af4aa03c31d", "domain_id": "ca1b267e149d4e44bf53d28d1c8d6bc9",
		},
		Logger: logg.Debug,
	}
	assert.True(t, enforcer.Enforce("event:show", c))
}

func Test_Policy_UnknownRoleFalse(t *testing.T) {
	enforcer := GetEnforcer()
	c := policy.Context{
		Roles: []string{
			"unknown_role",
		},
		Auth: map[string]string{
			"domain_id": "ca1b267e149d4e44bf53d28d1c8d6bc9",
			// "project_id":          "7a09c05926ec452ca7992af4aa03c31d",
		},
		Request: map[string]string{
			"domain_id": "ca1b267e149d4e44bf53d28d1c8d6bc9",
		},
		Logger: logg.Debug,
	}
	assert.False(t, enforcer.Enforce("event:show", c))
}

func Test_Policy_ProjectNoDomain(t *testing.T) {
	enforcer := GetEnforcer()
	c := policy.Context{
		Roles: []string{
			"audit_viewer",
		},
		Auth: map[string]string{
			"domain_id": "ca1b267e149d4e44bf53d28d1c8d6bc9",
		},
		Request: map[string]string{
			"domain_id": "ca1b267e149d4e44bf53d28d1c8d6bc9",
		},
		Logger: logg.Debug,
	}
	assert.True(t, enforcer.Enforce("event:show", c))
}

func Test_Policy_ProjectNoProject(t *testing.T) {
	enforcer := GetEnforcer()
	c := policy.Context{
		Roles: []string{
			"audit_viewer",
		},
		Auth: map[string]string{
			"domain_id": "ca1b267e149d4e44bf53d28d1c8d6bc9",
		},
		Request: map[string]string{
			"project_id": "7a09c05926ec452ca7992af4aa03c31d",
		},
		Logger: logg.Debug,
	}
	assert.False(t, enforcer.Enforce("event:show", c))
}

func TestPolicy(t *testing.T) {
	var keystonePolicy map[string]string

	file, err := os.Open("../../etc/policy.json")
	if err != nil {
		t.Fatal("Failed to open policy file: ", err)
	}
	if err := json.NewDecoder(file).Decode(&keystonePolicy); err != nil {
		t.Fatal("Failed to decode policy file: ", err)
	}

	auditContext := policy.Context{
		Roles: []string{"audit_viewer"},
		Auth:  map[string]string{"project_id": "7a09c05926ec452ca7992af4aa03c31d"},
		Request: map[string]string{
			"project_id": "7a09c05926ec452ca7992af4aa03c31d",
			"domain_id":  "ca1b267e149d4e44bf53d28d1c8d6bc9"},
	}

	serviceContext := policy.Context{
		Roles: []string{"service"},
	}

	enforcer, err := policy.NewEnforcer(keystonePolicy)
	if err != nil {
		t.Fatal("Failed to parse policy ", err)
	}
	if !enforcer.Enforce("event:show", auditContext) {
		t.Error("Event Show check should have returned true")
	}

	if enforcer.Enforce("non_existent_rule", serviceContext) {
		t.Error("Non existent rule should not pass")
	}
	// if !enforcer.Enforce("cloud_admin", adminContext) {
	//	t.Error("cloud_admin check should pass")
	//}
	// if !enforcer.Enforce("service_admin_or_owner", adminContext) {
	//	t.Error("service_admin_or_owner should pass for admin")
	//}
	// if !enforcer.Enforce("service_admin_or_owner", userContext) {
	//	t.Error("service_admin_or_owner should pass for owner")
	//}
	// userContext.Request["user_id"] = "u-2"
	// if enforcer.Enforce("service_admin_or_owner", userContext) {
	//	t.Error("service_admin_or_owner should pass for non owning user")
	//}
}

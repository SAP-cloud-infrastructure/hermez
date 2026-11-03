// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"

	"github.com/sapcc/hermes/pkg/identity"
)

func TestConfigureDataplaneWriter(t *testing.T) {
	svc := identity.ServiceUser{ID: "svc-id", Name: "hermes", Domain: "Default"}
	tests := []struct {
		name       string
		configured string
		svc        identity.ServiceUser
		want       string
	}{
		{"configured value wins over the service user", "cfg-id", svc, "cfg-id"},
		{"service user is used when unset", "", svc, "svc-id"},
		{"whitespace-only counts as unset", "  ", svc, "svc-id"},
		{"configured value is trimmed", " cfg-id ", svc, "cfg-id"},
		{"whitespace-only and no service user ends empty", " ", identity.ServiceUser{}, ""},
		{"nothing available stays empty", "", identity.ServiceUser{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(viper.Reset)
			viper.Set("hermes.dataplane_writer_user_id", tt.configured)
			configureDataplaneWriter(tt.svc)
			if got := viper.GetString("hermes.dataplane_writer_user_id"); got != tt.want {
				t.Errorf("viper has %q, want %q", got, tt.want)
			}
		})
	}
}

// TestReadConfigWriterRoleEnv proves the writer role settings are read from
// their environment variables, and the defaults when they are unset.
func TestReadConfigWriterRoleEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hermes.conf")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	get := func() (assign bool, role, roleID string) {
		return viper.GetBool("hermes.dataplane_assign_writer_role"),
			viper.GetString("hermes.dataplane_writer_role"),
			viper.GetString("hermes.dataplane_writer_role_id")
	}

	t.Run("defaults", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		setDefaultConfig()
		readConfig(&path)
		if assign, role, roleID := get(); assign || role != "objectstore_admin" || roleID != "" {
			t.Errorf("got (%v, %q, %q), want (false, \"objectstore_admin\", \"\")", assign, role, roleID)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Cleanup(viper.Reset)
		t.Setenv("HERMES_DATAPLANE_ASSIGN_WRITER_ROLE", "true")
		t.Setenv("HERMES_DATAPLANE_WRITER_ROLE", "other_role")
		t.Setenv("HERMES_DATAPLANE_WRITER_ROLE_ID", "role-id-1")
		setDefaultConfig()
		readConfig(&path)
		if assign, role, roleID := get(); !assign || role != "other_role" || roleID != "role-id-1" {
			t.Errorf("got (%v, %q, %q), want (true, \"other_role\", \"role-id-1\")", assign, role, roleID)
		}
	})
}

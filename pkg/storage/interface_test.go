// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateTenantID(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		wantErr  error
	}{
		{"valid UUID", "b3b70c8271a845709f9a03030e705da7", nil},
		{"AllTenants wildcard", AllTenants, nil},
		{"empty", "", ErrEmptyTenantID},
		{"unavailable", "unavailable", ErrInvalidTenantID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTenantID(tt.tenantID)
			assert.Equal(t, tt.wantErr, err)
		})
	}
}

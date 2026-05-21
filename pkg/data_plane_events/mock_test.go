// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package data_plane_events

import (
	"context"
	"testing"
)

// Note on `new(true)` / `new(false)`: in Go 1.22+, `new(expr)` returns a
// pointer to the expression's value (not the zero value of its type), so
// `new(true)` is `*bool` pointing to `true`. This is the form the
// `modernize` linter prefers; an earlier reviewer's claim that
// `new(true)` returns the zero value reflected pre-1.22 semantics.

func TestMockStorage(t *testing.T) {
	const pid = "proj-1"
	ctx := context.Background()

	tt := []struct {
		name             string
		seed             *bool // nil means "no row"
		setEnabled       bool
		wantChanged      bool
		wantPriorEnabled bool
		wantAfter        bool
		wantFound        bool
	}{
		{"absent_then_true_creates", nil, true, true, false, true, true},
		{"absent_then_false_noop", nil, false, false, false, false, false},
		{"true_then_true_noop", new(true), true, false, true, true, true},
		{"true_then_false_disables", new(true), false, true, true, false, true},
		{"false_then_true_enables", new(false), true, true, false, true, true},
		// Seeding "false" via Set on absent is a no-op (false_on_absent), so
		// the row never exists and the second Set(false) is also a no-op.
		// wantFound is therefore false — this row exercises the contract
		// that "no row" is observably equivalent to "enabled=false".
		{"false_then_false_noop", new(false), false, false, false, false, false},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMock()
			if tc.seed != nil {
				if _, _, err := m.Set(ctx, pid, *tc.seed); err != nil {
					t.Fatalf("seed Set: %v", err)
				}
			}
			gotChanged, gotPrior, err := m.Set(ctx, pid, tc.setEnabled)
			if err != nil {
				t.Fatalf("Set: %v", err)
			}
			if gotChanged != tc.wantChanged {
				t.Errorf("changed: got %v, want %v", gotChanged, tc.wantChanged)
			}
			if gotPrior != tc.wantPriorEnabled {
				t.Errorf("priorEnabled: got %v, want %v", gotPrior, tc.wantPriorEnabled)
			}
			gotAfter, gotFound, err := m.Get(ctx, pid)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if gotAfter != tc.wantAfter {
				t.Errorf("enabled after: got %v, want %v", gotAfter, tc.wantAfter)
			}
			if gotFound != tc.wantFound {
				t.Errorf("found after: got %v, want %v", gotFound, tc.wantFound)
			}
		})
	}
}

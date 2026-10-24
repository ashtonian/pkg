//go:build unit

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
)

// BuildBaseConstraints must emit the tenant predicate whenever
// a tenant column is configured — an empty tenant ID yields an impossible
// predicate (tenant_col = ”), never an unscoped query.
func TestBuildBaseConstraints_FailClosedOnEmptyTenant(t *testing.T) {
	tests := []struct {
		name       string
		tenantCol  string
		tenantID   string
		deleteCol  string
		wantTenant bool
		wantValue  string
	}{
		{"tenant col + tenant id", "tenant_id", "t-A", "deleted_at", true, "t-A"},
		{"tenant col + EMPTY tenant id emits impossible predicate", "tenant_id", "", "deleted_at", true, ""},
		{"no tenant col skips tenant predicate", "", "t-A", "deleted_at", false, ""},
		{"no tenant col + empty id", "", "", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := filter.BuildBaseConstraints(tt.tenantCol, tt.tenantID, tt.deleteCol, false)

			var tenantConstraint *filter.PolicyConstraint
			for _, c := range pc.Constraints {
				if c.Field == tt.tenantCol && tt.tenantCol != "" {
					tenantConstraint = c
				}
			}
			if tt.wantTenant {
				if tenantConstraint == nil {
					t.Fatalf("tenant constraint missing: %+v", pc.Constraints)
				}
				if tenantConstraint.Operator != filter.OpEq {
					t.Errorf("operator = %v, want OpEq", tenantConstraint.Operator)
				}
				if tenantConstraint.Value != tt.wantValue {
					t.Errorf("value = %v, want %q", tenantConstraint.Value, tt.wantValue)
				}
			} else if tenantConstraint != nil {
				t.Errorf("unexpected tenant constraint: %+v", tenantConstraint)
			}
		})
	}
}

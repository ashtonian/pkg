package filter

// Merge combines two PolicyConstraints with AND semantics.
func (pc *PolicyConstraints) Merge(other *PolicyConstraints) *PolicyConstraints {
	if other == nil || other.IsEmpty() {
		return pc
	}
	if pc == nil || pc.IsEmpty() {
		return other
	}
	return &PolicyConstraints{
		And: []*PolicyConstraints{pc, other},
	}
}

// RepositoryScope is the floor of a repository read: the request tenant when
// the table is tenant-scoped, and soft-delete masking.
type RepositoryScope struct {
	TenantColumn string
	// TenantID is the tenant key as the tenant column holds it: a string or
	// a canonical UUID string, or an int64 / uint64 for an integer column.
	// parse.Resource normalizes Scope.Tenant to it. Nil or "" admits no row
	// (BuildBaseConstraints).
	TenantID       any
	DeleteColumn   string
	IncludeDeleted bool
}

// ComposeConstraints is the policy tree a repository read applies, the same
// on every backend: governance when present — it scopes
// tenancy itself, grants included — otherwise the tenant floor; soft-delete
// masking; then authorization. The parts are ANDed under a new root, so
// neither input is modified and a cached governance tree is never extended.
func ComposeConstraints(scope RepositoryScope, governance, authz *PolicyConstraints) *PolicyConstraints {
	root := &PolicyConstraints{}
	if governance != nil && !governance.IsEmpty() {
		root.And = append(root.And, governance)
		if scope.DeleteColumn != "" && !scope.IncludeDeleted {
			root.And = append(root.And, BuildBaseConstraints("", nil, scope.DeleteColumn, false))
		}
	} else {
		root.And = append(root.And, BuildBaseConstraints(scope.TenantColumn, scope.TenantID, scope.DeleteColumn, scope.IncludeDeleted))
	}
	if authz != nil && !authz.IsEmpty() {
		root.And = append(root.And, authz)
	}
	return root
}

// BuildBaseConstraints builds PolicyConstraints for tenant isolation and soft-delete.
//
// tenantID is the tenant key in the tenant column's type (RepositoryScope).
//
// Fail-closed tenant scoping: when tenantCol is set, the tenant floor is
// ALWAYS emitted. Without a tenant — nil or "" — it is UnknownConstraint,
// which admits no row on every tenant column type, instead of widening the
// query to every tenant or failing as a request error. Repos additionally
// reject the missing tenant up front with a typed error; the floor here is
// defense in depth for any caller that bypasses that gate.
func BuildBaseConstraints(tenantCol string, tenantID any, deleteCol string, includeSoftDeleted bool) *PolicyConstraints {
	pc := &PolicyConstraints{}
	switch {
	case tenantCol == "":
	case tenantID == nil || tenantID == "":
		pc.Constraints = append(pc.Constraints, UnknownConstraint())
	default:
		pc.Constraints = append(pc.Constraints, &PolicyConstraint{
			Field: tenantCol, Operator: OpEq, Value: tenantID,
		})
	}
	if deleteCol != "" && !includeSoftDeleted {
		pc.Constraints = append(pc.Constraints, &PolicyConstraint{
			Field: deleteCol, Operator: OpNull, Value: true,
		})
	}
	return pc
}

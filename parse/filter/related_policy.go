package filter

import (
	"fmt"
	"time"
)

// relatedPolicies is the server's policy for each policy-scoped relation's
// target table (RelationScope.Related), compiled at one instant: however
// often a statement names a table, its relative-time tokens admit the same
// related rows, and the same as the statement's own policy.
type relatedPolicies struct {
	byTable map[string]*PolicyConstraints
	// now is the instant the policies' relative-time tokens resolve
	// against; zero until the statement being built fixes it (at).
	now time.Time
}

// at is r resolved at now, unless r already holds the instant its scope
// named (RelationScope.Now).
func (r relatedPolicies) at(now time.Time) relatedPolicies {
	if r.now.IsZero() {
		r.now = now
	}
	return r
}

// on is nested, the filter a relation predicate on f holds its target's
// rows to, ANDed with the policy for f's target table; nil nested is the
// policy alone. A scope holding no policy for the table is
// ErrRelatedPolicyRequired.
func (r relatedPolicies) on(f *FieldSchema, nested *FilterNode) (*FilterNode, error) {
	table := f.Relation.RelatedTable()
	pc, ok := r.byTable[table]
	if !ok {
		return nil, fmt.Errorf("relation %s on %s: %w", f.ExternalName, table, ErrRelatedPolicyRequired)
	}
	ast, err := applyPolicyConstraints(f.Related, &FilterAST{Root: nested}, pc, r.now)
	if err != nil {
		return nil, fmt.Errorf("relation %s: %w", f.ExternalName, err)
	}
	return ast.Root, nil
}

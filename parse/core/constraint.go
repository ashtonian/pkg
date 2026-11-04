package core

// Declared constraints shared by schema discovery and patch diagnostics.
const (
	ConstraintEnum     = "enum"
	ConstraintMinLen   = "minlen"
	ConstraintMaxLen   = "maxlen"
	ConstraintMin      = "min"
	ConstraintMax      = "max"
	ConstraintMinItems = "minitems"
	ConstraintMaxItems = "maxitems"
)

// ConstraintError identifies a declared constraint and its schema limit.
// Enum limits are the comma-separated allowlist; bounds retain schema text.
type ConstraintError struct {
	Constraint string
	Limit      string
	detail     string
}

// NewConstraintError retains the value-specific diagnostic as Error's text.
func NewConstraintError(constraint, limit, detail string) *ConstraintError {
	return &ConstraintError{Constraint: constraint, Limit: limit, detail: detail}
}

func (e *ConstraintError) Error() string {
	if e.detail != "" {
		return e.detail
	}
	return e.Constraint + " constraint: " + e.Limit
}

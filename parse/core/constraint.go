package core

// Declared constraints shared by schema discovery and patch diagnostics
// (patch.ConstraintError); each is also the struct tag that declares it
// (`minlen:"3"`).
const (
	ConstraintEnum     = "enum"
	ConstraintMinLen   = "minlen"
	ConstraintMaxLen   = "maxlen"
	ConstraintMin      = "min"
	ConstraintMax      = "max"
	ConstraintMinItems = "minitems"
	ConstraintMaxItems = "maxitems"
)

// constraintTagKeys are the constraint tags readConstraints reads.
func constraintTagKeys() []string {
	keys := []string{ConstraintEnum, ConstraintMinLen, ConstraintMaxLen, ConstraintMin, ConstraintMax, ConstraintMinItems, ConstraintMaxItems}
	return keys
}

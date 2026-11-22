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

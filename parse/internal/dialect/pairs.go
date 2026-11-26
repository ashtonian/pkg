package dialect

// PairRows is what a relation insert without a target table selects its
// pairs from: every owner key crossed with every value key, each set bound
// as one parameter and read back as rows (patch.RelationDialect.RelationRows).
type PairRows struct {
	// Sources and Targets are the expressions the select list writes the
	// pair table's source and target columns from, in column order, and
	// Tenant its tenant column's: empty when the pairs carry no tenant.
	Sources, Targets []string
	Tenant           string
	// From is the FROM clause, its leading space included.
	From string
	// Where are the conditions the select needs whatever the statement
	// adds, ANDed before its own: SQLite's true, which parts an upsert's ON
	// CONFLICT from a join's ON.
	Where []string
}

// PairTenant is the tenant a relation insert writes its pairs for, in the
// two forms a dialect may bind it: Value, as a statement binds a parameter,
// and Record, as a JSON record of the pair table's row holds it — a
// database scalar, or an error for a value that is none. Record is called
// only by a dialect that reads the tenant from such a record.
type PairTenant struct {
	Value  any
	Record func() (any, error)
}

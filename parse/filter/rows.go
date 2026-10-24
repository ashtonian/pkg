package filter

// Rows is the row reader the Scan helpers ([ScanFacetUnion],
// [IncludeQuery.Scan]) consume. *database/sql.Rows and pgx.Rows both satisfy
// it; the caller owns Close.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

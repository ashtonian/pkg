package filtertest

import (
	"slices"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// FoldRecord is the `$ilike` case fixture: names whose letters lower-case
// differently from how Unicode case folding equates them. Its table is
// "fold_records".
type FoldRecord struct {
	ID   string `json:"id"   db:"id"   parse:"filter" filter_ops:"eq,in"`
	Name string `json:"name" db:"name" parse:"filter" filter_ops:"like,ilike"`
}

// FoldRecords is the fixture data: the long s, the final sigma and the
// micro sign, which case folding equates with s, σ and μ and lower-casing
// does not; the dotted capital I, which lower-cases to i; and an ASCII
// name every backend folds.
func FoldRecords() []FoldRecord {
	return []FoldRecord{
		{ID: "a", Name: "ſ"},
		{ID: "b", Name: "ς"},
		{ID: "c", Name: "µ"},
		{ID: "d", Name: "İ"},
		{ID: "e", Name: "Sigma"},
	}
}

// FoldCases are `$ilike` filters on FoldRecords that every backend answers
// alike: each character is compared lower-cased, which equates no
// non-ASCII pair here, and ASCII letters fold everywhere.
func FoldCases() []Case { return slices.Clone(foldCases) }

var foldCases = []Case{
	{Name: "long s is no s", Filter: `{"name":{"$ilike":"s"}}`, Want: ids("")},
	{Name: "final sigma is no sigma", Filter: `{"name":{"$ilike":"σ"}}`, Want: ids("")},
	{Name: "micro sign is no mu", Filter: `{"name":{"$ilike":"μ"}}`, Want: ids("")},
	{Name: "ascii letters fold", Filter: `{"name":{"$ilike":"sIGMA"}}`, Want: ids("e")},
	{Name: "like does not fold", Filter: `{"name":{"$like":"sigma"}}`, Want: ids("")},
}

// FoldUnicodeCases are `$ilike` filters on FoldRecords whose answer
// lower-cases a non-ASCII letter: PostgreSQL under pg_c_utf8 and the
// reference evaluator answer them; SQLite and the C collation lower-case
// ASCII only.
func FoldUnicodeCases() []Case { return slices.Clone(foldUnicodeCases) }

var foldUnicodeCases = []Case{
	{Name: "dotted capital I lower-cases to i", Filter: `{"name":{"$ilike":"i"}}`, Want: ids("d")},
}

// FoldSchema returns FoldRecord's generated filter schema.
func FoldSchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[FoldRecord](core.NewSchemaGenerator())
}

// RunFold evaluates every FoldCases case on backend, which evaluates
// against FoldRecords; with unicode set, the FoldUnicodeCases too.
func RunFold(t *testing.T, unicode bool, backend Backend) {
	schema, err := FoldSchema()
	if err != nil {
		t.Fatalf("filtertest: fold schema: %v", err)
	}
	cases := FoldCases()
	if unicode {
		cases = append(cases, FoldUnicodeCases()...)
	}
	run(t, filter.NewFilterParser(schema, core.DefaultConfig()), cases, backend)
}

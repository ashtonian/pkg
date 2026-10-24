//go:build unit

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Regression tests for the 2026-09-30 review's filter findings.

type docRow struct {
	ID   string         `json:"id" db:"id" parse:"sort"`
	Doc  map[string]any `json:"doc" db:"doc" parse:"filter"`
	Nums []uint64       `json:"nums" db:"nums" parse:"filter"`
}

// N02: a JSON number some backend would round is rejected, not answered
// differently per backend.
func TestFilter_JSONNumbersStayInThePortableDomain(t *testing.T) {
	p, err := parse.NewResource[docRow]("doc_rows")
	require.NoError(t, err)

	for _, bad := range []string{
		`{"doc":{"n":18446744073709551615}}`,
		`{"doc":{"$contains":{"deep":[1, 9223372036854775808]}}}`,
		`{"doc":{"n":0.10000000000000001}}`,
		`{"nums":{"$contains":[18446744073709551615]}}`,
	} {
		_, err := p.ParseFilter(bad)
		requireCode(t, err, filter.ECodeValueTypeMismatch.String())
	}
	for _, ok := range []string{
		`{"doc":{"n":9223372036854775807}}`,
		`{"doc":{"n":-9223372036854775808}}`,
		`{"doc":{"n":0.1}}`,
		`{"doc":{"n":1.50}}`,
		`{"doc":{"n":2e3}}`,
		`{"nums":{"$contains":[9223372036854775807]}}`,
	} {
		_, err := p.ParseFilter(ok)
		assert.NoError(t, err, ok)
	}
}

func TestPortableDecimal(t *testing.T) {
	for lit, want := range map[string]bool{
		"0.1": true, "1.50": true, "2e3": true, "-0.0": true, "1.7976931348623157e308": true,
		"0.10000000000000001": false, "1e400": false, "1e-400": false,
		"9007199254740993.0": false, "3.141592653589793238": false,
	} {
		assert.Equal(t, want, core.PortableDecimal(lit), lit)
	}
}

// N11: a facet over a field whose external name is not an SQL identifier
// executes.
func TestFacet_ExternalNameIsNotAnAlias(t *testing.T) {
	type labelled struct {
		Label string `json:"display-name" db:"label" parse:"filter,facet"`
	}
	s, err := filter.GenerateFilterSchema[labelled](core.NewSchemaGenerator())
	require.NoError(t, err)
	fq, err := filter.BuildFacetQuery(s, filter.SQLiteDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "display-name"}})
	require.NoError(t, err)

	db := dbtest.SQLite(t)
	_, err = db.Exec(`CREATE TABLE labelleds (label TEXT); INSERT INTO labelleds VALUES ('a'), ('a'), ('b')`)
	require.NoError(t, err)
	rows, err := db.Query(fq.SQL, fq.Args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	counts := map[string]int{}
	for rows.Next() {
		var field, key string
		var count int
		require.NoError(t, rows.Scan(&field, &key, &count))
		assert.Equal(t, "display-name", field)
		counts[key] = count
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]int{"a": 2, "b": 1}, counts)
}

type nullable struct {
	ID    string  `json:"id,omitempty" db:"id"`
	Name  *string `json:"name"  parse:"filter" filter_ops:"not_null"`
	Other *string `json:"other" parse:"filter" filter_ops:"eq"`
}

// N13: not_null has a wire spelling, and every NULL test is one capability.
func TestFilter_NotNullIsAWireOperator(t *testing.T) {
	p, err := parse.NewResource[nullable]("nullables")
	require.NoError(t, err)

	for filterJSON, want := range map[string]filter.FilterOperator{
		`{"name":{"$not_null":true}}`:  filter.OpNotNull,
		`{"name":{"$not_null":false}}`: filter.OpNull,
		`{"name":{"$null":false}}`:     filter.OpNotNull,
		`{"name":{"$exists":false}}`:   filter.OpNull,
	} {
		f, parseErr := p.ParseFilter(filterJSON)
		require.NoError(t, parseErr, filterJSON)
		assert.Equal(t, want, f.AST.Root.Op, filterJSON)
	}

	_, err = p.ParseFilter(`{"other":{"$not_null":true}}`)
	requireCode(t, err, filter.ECodeInvalidOperator.String())
}

func TestQueryParams_NotNullSuffix(t *testing.T) {
	type row struct {
		ID      string  `json:"id,omitempty" db:"id"`
		Name    *string `json:"name"     parse:"filter" filter_ops:"not_null"`
		NameNot *string `json:"name_not" parse:"filter" filter_ops:"null"`
	}
	p, err := parse.NewResource[row]("rows")
	require.NoError(t, err)
	qp := filter.NewQueryParamParser(p.FilterSchema())

	ast, err := qp.Parse(url.Values{"name_not_null": {"true"}})
	require.NoError(t, err)
	assert.Equal(t, "name", ast.Root.Field.ExternalName, "the split naming a real field wins")
	assert.Equal(t, filter.OpNotNull, ast.Root.Op)
}

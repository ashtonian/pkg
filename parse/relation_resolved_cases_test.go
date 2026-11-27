//go:build integration

package parse_test

import (
	"database/sql"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Relations resolved outside SQL, held to what SQL answers: a backend that
// holds rows in memory resolves a filter's relation predicates
// (filter.ResolveMemberships) and includes (filter.ResolveInclude) through
// lookups, then evaluates the filter on each row. Each case runs through
// two lookups over one database: its tables loaded into memory, answered
// by filter.MatchLookupRow and filter.Evaluate (governedMemory), and the
// database running the SQL each lookup renders (sqlMembershipLookup).

// resolvedLookup is a store that answers every lookup a relation resolves
// through.
type resolvedLookup interface {
	filter.RelationLookup
	filter.IncludeLookup
}

// resolvedStores loads tables into memory and names each lookup.
func resolvedStores(t *testing.T, db *sql.DB, d filter.Dialect, tables ...string) (governedMemory, map[string]resolvedLookup) {
	t.Helper()
	store := governedMemory{}
	for _, table := range tables {
		store[table] = append(store[table], queryRows[map[string]any](t, db, "SELECT * FROM "+d.QuoteIdent(table))...)
	}
	lookups := map[string]resolvedLookup{
		"memory": store,
		"sql":    sqlMembershipLookup{t: t, d: d, db: db},
	}
	return store, lookups
}

// assertResolvedFilters requires each filter document to select the rows
// of r outside SQL that it selects in SQL on r's provider, through every
// lookup.
func assertResolvedFilters[T any](t *testing.T, store governedMemory, lookups map[string]resolvedLookup, r *parse.Resource[T], docs ...string) {
	t.Helper()
	types := resourceKeyTypes(t, r)
	for _, doc := range docs {
		var want []string
		for _, item := range crxPage(t, r, url.Values{filter.ParamFilter: {doc}}).Items {
			key, err := r.Key(&item)
			require.NoError(t, err)
			want = append(want, keyText(t, types, key))
		}
		slices.Sort(want)
		for name, lookup := range lookups {
			t.Run(r.Table()+" "+doc+" "+name, func(t *testing.T) {
				req, err := r.ParseList(url.Values{filter.ParamFilter: {doc}})
				require.NoError(t, err)
				resolved, err := filter.ResolveMemberships(t.Context(), req.Filter(), lookup)
				require.NoError(t, err)
				var got []string
				for _, row := range store[r.Table()] {
					truth, err := filter.Evaluate(resolved.AST().Root, governedRow(r.Table(), row))
					require.NoError(t, err)
					if truth == filter.True {
						got = append(got, keyText(t, types, rowKey(r, row)))
					}
				}
				slices.Sort(got)
				assert.Equal(t, want, got)
			})
		}
	}
}

// assertResolvedIncludes requires each include of every row of r outside
// SQL to name the owners and targets SQL on r's provider names, through
// every lookup.
func assertResolvedIncludes[T any](t *testing.T, lookups map[string]resolvedLookup, r *parse.Resource[T], fields ...string) {
	t.Helper()
	page := crxPage(t, r, url.Values{filter.ParamInclude: {strings.Join(fields, ",")}})
	owners := make([]core.Key, len(page.Items))
	for i := range page.Items {
		key, err := r.Key(&page.Items[i])
		require.NoError(t, err)
		owners[i] = key
	}
	schema := r.FilterSchema()
	for _, field := range fields {
		for name, lookup := range lookups {
			t.Run(r.Table()+" includes "+field+" "+name, func(t *testing.T) {
				rows, err := filter.ResolveInclude(t.Context(), schema.Fields[field], owners, lookup)
				require.NoError(t, err)
				assert.Empty(t, rows.Truncated)
				want := page.Relations[field]
				if want == nil {
					want = map[string][]string{}
				}
				assert.Equal(t, want, rows.IDs)
			})
		}
	}
}

// resourceKeyTypes are the types of r's key columns, as its filter schema
// declares them.
func resourceKeyTypes[T any](t *testing.T, r *parse.Resource[T]) []core.TypeInfo {
	t.Helper()
	schema := r.FilterSchema()
	var types []core.TypeInfo
	for _, column := range r.PrimaryKeyColumns() {
		var field *filter.FieldSchema
		for _, f := range schema.Fields {
			if f.Column == column && f.Relation == nil && f.Path == nil {
				field = f
			}
		}
		require.NotNil(t, field, "key column %s has no filter field", column)
		types = append(types, core.TypeInfo{Type: field.DataType, NumberKind: field.NumberKind})
	}
	return types
}

// rowKey is a stored row's key, a value per key column of r.
func rowKey[T any](r *parse.Resource[T], row map[string]any) core.Key {
	columns := r.PrimaryKeyColumns()
	key := make(core.Key, len(columns))
	for i, c := range columns {
		key[i] = row[c]
	}
	return key
}

func keyText(t *testing.T, types []core.TypeInfo, key core.Key) string {
	t.Helper()
	text, err := core.KeyTextOf(types, key)
	require.NoError(t, err)
	return text
}

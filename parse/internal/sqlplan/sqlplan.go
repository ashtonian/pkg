// Package sqlplan compiles parse's composed requests (internal/backend) to
// SQL for a dialect: a list's page, fragment, count, facet and include
// statements; an update's Current, Conflict, Lock and Owner, then its
// relation statements and rereads once its owners' keys are known; and an
// update batch's chunks. Each statement is compiled here and nowhere else:
// the SQL executor (internal/sqlexec) runs what it compiles, and the root
// package's SQL builders (parse.Resource.BuildList, BuildFacets,
// IncludeQueries, BuildUpdate, BuildUpdateBatch) return it. It reads a
// resource through its descriptor (backend.Resource) and its SQL text
// (backend.SQLResource), and imports neither the root package nor the
// executor.
package sqlplan

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// Statement is one parameterized SQL statement.
type Statement struct {
	SQL  string
	Args []any
}

// sqlOf is the SQL text res's statements are compiled around, which the
// root package renders for a provider with a dialect.
func sqlOf(res *backend.Resource) (*backend.SQLResource, error) {
	if res.SQL == nil {
		return nil, fmt.Errorf("parse: %s: the resource has no SQL for a dialect", res.Table)
	}
	return res.SQL, nil
}

// keyOrder is the ORDER BY list that reads res's key columns in their
// types' one order (filter.KeyOrder), as the root package compares keys.
func keyOrder(d filter.Dialect, res *backend.Resource) string {
	order := make([]string, len(res.Key))
	for i, column := range res.Key {
		order[i] = filter.KeyOrder(d, d.QuoteIdent(res.Table)+"."+d.QuoteIdent(column), res.KeyTypes[i])
	}
	return strings.Join(order, ", ")
}

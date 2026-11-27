//go:build unit || concurrency

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

// onSQLite and onPostgres compile for SQLite and PostgreSQL without a
// database (providertest.Compile), for a test of what a resource parses
// and builds rather than of what its statements read.
var (
	onSQLite   = providertest.Compile(filter.SQLiteDialect{})   //nolint:gochecknoglobals // one provider per dialect: Relate joins resources of one provider only
	onPostgres = providertest.Compile(filter.PostgresDialect{}) //nolint:gochecknoglobals // one provider per dialect: Relate joins resources of one provider only
)

// computedDialect is a dialect the computed columns compile for, with its
// expressions.
type computedDialect struct {
	dialect                  filter.Dialect
	email, groupIDs, contact string
}

// computedDialects are the dialects the computed columns compile for, with
// each one's expressions.
func computedDialects() []computedDialect {
	dialects := []computedDialect{
		{filter.PostgresDialect{}, parsetest.PgEmailExpr, parsetest.PgGroupIDsExpr, parsetest.PgContactExpr},
		{filter.SQLiteDialect{}, parsetest.SqliteEmailExpr, parsetest.SqliteGroupIDsExpr, parsetest.SqliteContactExpr},
	}
	return dialects
}

// queryParams is filter.NewQueryParamParser over schema with options it
// accepts.
func queryParams(t testing.TB, schema *filter.ResourceSchema, opts ...filter.QueryParamOption) *filter.QueryParamParser {
	t.Helper()
	qp, err := filter.NewQueryParamParser(schema, opts...)
	require.NoError(t, err)
	return qp
}

// newSchema builds a test schema, which must be valid, holding at most
// maxOperations operations (0: any number).
func newSchema(resource string, maxOperations int, paths map[string]*patch.PathSchema) *patch.PatchSchema {
	s, err := patch.NewPatchSchema(resource, paths)
	if err != nil {
		panic(err)
	}
	s.MaxOperations = maxOperations
	return s
}

// pathOf is the path schema s declares under key, or nil.
func pathOf(s *patch.PatchSchema, key string) *patch.PathSchema {
	ps, _ := s.Path(key)
	return ps
}

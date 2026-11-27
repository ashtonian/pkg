//go:build unit || integration || concurrency

package parse_test

import (
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// computedDialects are the dialects the computed columns compile for, with
// each one's expressions.
var computedDialects = []struct {
	dialect                  filter.Dialect
	email, groupIDs, contact string
}{
	{filter.PostgresDialect{}, parsetest.PgEmailExpr, parsetest.PgGroupIDsExpr, parsetest.PgContactExpr},
	{filter.SQLiteDialect{}, parsetest.SqliteEmailExpr, parsetest.SqliteGroupIDsExpr, parsetest.SqliteContactExpr},
}

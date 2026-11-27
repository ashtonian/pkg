//go:build unit

package parsehttp_test

import (
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

// onSQLite is the provider the tests' resources compile for: the handlers
// under test parse requests and write answers, and run no statement.
var onSQLite = providertest.Compile(filter.SQLiteDialect{}) //nolint:gochecknoglobals // one provider: Relate joins resources of one provider only

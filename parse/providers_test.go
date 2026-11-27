//go:build unit || integration || concurrency

package parse_test

import (
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

// onSQLite and onPostgres compile for SQLite and PostgreSQL without a
// database (providertest.Compile), for a test of what a resource parses
// and builds rather than of what its statements read.
var (
	onSQLite   = providertest.Compile(filter.SQLiteDialect{})   //nolint:gochecknoglobals // one provider per dialect: Relate joins resources of one provider only
	onPostgres = providertest.Compile(filter.PostgresDialect{}) //nolint:gochecknoglobals // one provider per dialect: Relate joins resources of one provider only
)

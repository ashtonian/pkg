//go:build integration

package filter_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestNullableWrappers_Postgres(t *testing.T) {
	runNullableWrappers(t, visitDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, timestampType: "TIMESTAMPTZ", stamp: func(at sql.NullTime) any { return at }})
}

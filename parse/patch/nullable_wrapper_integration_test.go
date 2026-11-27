//go:build integration

package patch_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestPatch_NullableWrapperColumnsPostgres(t *testing.T) {
	runNullableWrapperPatches(t, wrapperDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, timestampType: "TIMESTAMPTZ", stamp: func(at sql.NullTime) any { return at }})
}

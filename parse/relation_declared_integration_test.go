//go:build integration

package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestDeclaredRelations_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TIMESTAMPTZ") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationCases(t, relationResources(t), filter.PostgresDialect{}, func(t *testing.T, st parse.Statement) []string {
		rows, err := db.Query(st.SQL, st.Args...)
		require.NoError(t, err, st.SQL)
		var ids []string
		for rows.Next() {
			cols, err := rows.Columns()
			require.NoError(t, err)
			vals := make([]any, len(cols))
			for i := range vals {
				vals[i] = new(any)
			}
			require.NoError(t, rows.Scan(vals...))
			ids = append(ids, (*vals[0].(*any)).(string))
		}
		require.NoError(t, errors.Join(rows.Err(), rows.Close()))
		return ids
	})
}

func TestDeclaredRelations_Memberships_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TIMESTAMPTZ") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationMemberships(t, relationResources(t), filter.PostgresDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestDeclaredRelations_Includes_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TIMESTAMPTZ") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationIncludes(t, relationResources(t), filter.PostgresDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestDeclaredRelations_Facets_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TIMESTAMPTZ") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationFacets(t, relationResources(t), filter.PostgresDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestDeclaredRelations_Writes_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	for _, stmt := range relationFixtureSQL(`'2026-01-01T00:00:00Z'`, "TIMESTAMPTZ") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runRelationWrites(t, relationResources(t), filter.PostgresDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

func TestElementPredicates_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	for _, stmt := range orderFixtureSQL("JSONB") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	runElementCases(t, filter.PostgresDialect{}, func(t *testing.T, q string, args []any) [][]any {
		return queryRows(t, db, q, args)
	})
}

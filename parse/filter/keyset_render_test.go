//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// rankKeys orders by a computed score, highest first, then by id: a
// keyset no ListQuery sort declares.
func rankKeys() []filter.CursorKey {
	return []filter.CursorKey{
		{Name: "score", SQLExpr: "score", Column: "score", DataType: core.FieldTypeFloat, Direction: filter.SortDesc},
		{Name: "label", SQLExpr: "items.label", Column: "label", DataType: core.FieldTypeString, Direction: filter.SortAsc, Nullable: true},
		{Name: "id", SQLExpr: "items.id", Column: "id", DataType: core.FieldTypeString, Direction: filter.SortAsc},
	}
}

func rankToken(score, label, id any) *filter.PageToken {
	return &filter.PageToken{Fields: []filter.CursorField{
		{Name: "score", Value: score, Direction: filter.SortDesc},
		{Name: "label", Value: label, Direction: filter.SortAsc},
		{Name: "id", Value: id, Direction: filter.SortAsc},
	}}
}

func TestOrderByKeys_NoKeysIsNoClause(t *testing.T) {
	assert.Empty(t, filter.OrderByKeys(filter.PostgresDialect{}, nil, false))
}

// The walk backwards is the walk forwards inverted, NULL placement too.
func TestOrderByKeys_ReverseInvertsEveryKey(t *testing.T) {
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		forward := filter.OrderByKeys(d, rankKeys(), false)
		backward := filter.OrderByKeys(d, rankKeys(), true)
		assert.Contains(t, forward, "score DESC")
		assert.Contains(t, forward, "ASC NULLS LAST")
		assert.Contains(t, backward, "score ASC")
		assert.Contains(t, backward, "DESC NULLS FIRST")
	}
}

// A token that does not match the keyset is refused before anything is
// bound.
func TestSeekPredicate_RefusesAForeignToken(t *testing.T) {
	for name, token := range map[string]*filter.PageToken{
		"no token":           nil,
		"too few fields":     {Fields: rankToken(0.5, "a", "b").Fields[:2]},
		"another key":        {Fields: []filter.CursorField{{Name: "score", Value: 0.5, Direction: filter.SortDesc}, {Name: "name", Value: "a", Direction: filter.SortAsc}, {Name: "id", Value: "b", Direction: filter.SortAsc}}},
		"another direction":  {Fields: []filter.CursorField{{Name: "score", Value: 0.5, Direction: filter.SortAsc}, {Name: "label", Value: "a", Direction: filter.SortAsc}, {Name: "id", Value: "b", Direction: filter.SortAsc}}},
		"a mistyped value":   rankToken("high", "a", "b"),
		"null on a non-null": rankToken(0.5, "a", nil),
	} {
		t.Run(name, func(t *testing.T) {
			bound := 0
			_, err := filter.SeekPredicate(filter.PostgresDialect{}, rankKeys(), token, func(any) string {
				bound++
				return "$x"
			})
			assertParseCode(t, err, filter.ECodeInvalidCursor)
			assert.Zero(t, bound)
		})
	}
}

// Every non-null value is bound once, in key order and normalized, and a
// null boundary is matched with IS NULL, never bound.
func TestSeekPredicate_BindsEachValueOnce(t *testing.T) {
	for _, tt := range []struct {
		name  string
		token *filter.PageToken
		want  []any
	}{
		{"every value", rankToken(float32(0.5), "a", "b"), []any{0.5, "a", "b"}},
		{"a null label", rankToken(2.0, nil, "b"), []any{2.0, "b"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var args []any
			where, err := filter.SeekPredicate(filter.PostgresDialect{}, rankKeys(), tt.token, func(v any) string {
				args = append(args, v)
				return filter.PostgresDialect{}.Placeholder(len(args))
			})
			require.NoError(t, err)
			assert.Equal(t, tt.want, args)
			assert.NotContains(t, where, "'", "values are bound, never spliced")
		})
	}
}

// Keyset walks over computed and member keys on SQLite.
func TestKeysetRender_SQLite(t *testing.T) {
	runKeysetRender(t, renderDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, docType: "TEXT"})
}

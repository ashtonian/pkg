//go:build unit

package filter_test

import (
	"database/sql/driver"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type pointerCursorNumber struct{ n int64 }

func (n *pointerCursorNumber) Value() (driver.Value, error) { return n.n, nil }

type pointerCursorText struct{ s string }

func (s *pointerCursorText) MarshalText() ([]byte, error) { return []byte(s.s), nil }

func TestCursorValuePointerMethods(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input any
		want  any
	}{
		{"valuer value", pointerCursorNumber{42}, int64(42)},
		{"valuer pointer", &pointerCursorNumber{42}, int64(42)},
		{"text value", pointerCursorText{"key"}, "key"},
		{"text pointer", &pointerCursorText{"key"}, "key"},
		{"nil valuer", (*pointerCursorNumber)(nil), nil},
		{"nil text", (*pointerCursorText)(nil), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filter.CursorValue(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestGeneratedPointerMethodSortCanCreateCursor(t *testing.T) {
	type row struct {
		ID   pointerCursorNumber `json:"id" db:"id" parse:"sort"`
		Name *pointerCursorText  `json:"name" db:"name" parse:"sort"`
	}
	schema, err := filter.GenerateSortSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	sort, err := filter.ParseSort(schema, "name,id")
	require.NoError(t, err)
	plan := filter.CursorPlan{Keys: filter.Keyset(sort, filter.CursorKey{})}
	value := row{ID: pointerCursorNumber{42}, Name: &pointerCursorText{"key"}}
	token, err := plan.Token(false, func(col string) (any, error) { return filter.CursorValueByTag(value, "db", col) })
	require.NoError(t, err)
	require.Equal(t, "key", token.Fields[0].Value)
	require.Equal(t, int64(42), token.Fields[1].Value)
	encoded, err := filter.EncodeCursor(token)
	require.NoError(t, err)
	decoded, err := filter.DecodeCursor(encoded)
	require.NoError(t, err)
	require.NoError(t, filter.ValidateCursor(decoded, plan.Keys))
}

//go:build unit

package patch_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// composeSchema has a scalar, a counter, a stamp, a native array and a
// document whose members a patch writes.
func composeSchema(t *testing.T) *patch.PatchSchema {
	t.Helper()
	ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove}
	s, err := patch.NewPatchSchema("things", map[string]*patch.PathSchema{
		"/name":       {AllowedOps: ops, DataType: core.FieldTypeString, SQLColumn: "name"},
		"/rank":       {AllowedOps: ops, DataType: core.FieldTypeInt, SQLColumn: "rank"},
		"/tags":       {AllowedOps: append(ops, patch.PatchOpArrayAppend), DataType: core.FieldTypeArray, SQLColumn: "tags", GoType: reflect.TypeFor[[]string]()},
		"/doc":        {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLColumn: "doc"},
		"/doc/**":     {AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: "doc"},
		"/version":    {AllowedOps: ops, DataType: core.FieldTypeInt, SQLColumn: "version"},
		"/updated_at": {AllowedOps: ops, DataType: core.FieldTypeTimestamp, SQLColumn: "updated_at"},
	})
	require.NoError(t, err)
	return s
}

func composeParse(t *testing.T, body string) *patch.ParsedPatch {
	t.Helper()
	parsed, err := patch.NewParser(composeSchema(t), nil).Parse([]byte(body))
	require.NoError(t, err)
	return parsed
}

// storedRow reads values, a row's stored values by column.
func storedRow(values map[string]any) patch.StoredRow {
	return func(column string) (any, bool) {
		v, ok := values[column]
		return v, ok
	}
}

// A composed update assigns its columns in the order BuildPatchSQL's SET
// clause does, and none, the stamp and counters included, when the patch
// assigns no column of its own.
func TestComposedPatch_ColumnsAreTheSetClauses(t *testing.T) {
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	opts := []patch.PatchSQLOption{patch.WithAutoTimestamp("updated_at"), patch.WithTimeFunc(func() time.Time { return at }), patch.WithIncrement("version")}
	parsed := composeParse(t, `[{"op":"replace","path":"/rank","value":2},{"op":"add","path":"/doc/x","value":1},{"op":"replace","path":"/name","value":"a"},{"op":"replace","path":"/version","value":9}]`)
	composed, err := patch.ComposePatch(parsed, opts...)
	require.NoError(t, err)
	assert.Equal(t, []string{"rank", "doc", "name", "updated_at", "version"}, composed.Columns())
	built, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{}, opts...)
	require.NoError(t, err)
	var assigned []string
	for _, a := range built.Assignments {
		assigned = append(assigned, a.Column)
	}
	assert.Equal(t, assigned, composed.Columns())
	rendered, err := composed.SQL(filter.SQLiteDialect{})
	require.NoError(t, err)
	assert.Equal(t, built, rendered, "BuildPatchSQL renders the composed update")

	serverOnly, err := patch.ComposePatch(composeParse(t, `[{"op":"replace","path":"/version","value":9}]`), opts...)
	require.NoError(t, err)
	assert.Empty(t, serverOnly.Columns())
	assert.False(t, serverOnly.Assigns())
	assert.True(t, serverOnly.Empty())
	write, err := serverOnly.Apply(storedRow(nil))
	require.NoError(t, err)
	assert.Equal(t, patch.RowWrite{}, write, "an update assigning nothing writes nothing")
}

// Each step takes its own options: composing decides what the update
// writes, rendering how its SQL reads.
func TestComposePatch_TakesTheCompositionOptionsAlone(t *testing.T) {
	parsed := composeParse(t, `[{"op":"replace","path":"/rank","value":2}]`)
	_, err := patch.ComposePatch(parsed, patch.WithStartParam(3))
	require.ErrorContains(t, err, "ComposePatch takes no rendering option")
	_, err = patch.ComposePatch(parsed, patch.WithChangePredicate())
	require.ErrorContains(t, err, "ComposePatch takes no rendering option")

	composed, err := patch.ComposePatch(parsed)
	require.NoError(t, err)
	for _, opt := range []patch.PatchSQLOption{patch.WithIncrement("version"), patch.WithAutoTimestamp("updated_at"), patch.WithTimeFunc(time.Now)} {
		_, err := composed.SQL(filter.SQLiteDialect{}, opt)
		require.ErrorContains(t, err, "ComposedPatch.SQL takes no composition option")
	}
}

// What Apply cannot write as the database does it refuses: a stamp only
// the database evaluates, and a stored value that is not of its column's
// type.
func TestComposedPatch_ApplyRefusesWhatItCannotWrite(t *testing.T) {
	stored := func() map[string]any {
		row := map[string]any{"name": "n", "rank": int64(1), "tags": `["a"]`, "doc": `{"a":1}`, "version": int64(1), "updated_at": nil}
		return row
	}
	stampSQL, err := patch.ComposePatch(composeParse(t, `[{"op":"replace","path":"/name","value":"x"}]`), patch.WithAutoTimestampSQL("updated_at", "NOW()"))
	require.NoError(t, err)
	_, err = stampSQL.Apply(storedRow(stored()))
	require.ErrorContains(t, err, "patch: column updated_at is stamped by the SQL expression NOW(), which only a database evaluates")

	for _, tc := range []struct {
		name, body, column string
		value              any
		want               string
	}{
		{"a column the row lacks", `[{"op":"replace","path":"/name","value":"x"}]`, "", nil, "patch: the row holds no column version"},
		{"a document that is no JSON text", `[{"op":"add","path":"/doc/b","value":2}]`, "doc", int64(1), "patch: column doc: a stored document is JSON text, not int64"},
		{"a document that is no JSON", `[{"op":"add","path":"/doc/b","value":2}]`, "doc", `{"a":`, "patch: column doc: decoding the stored document"},
		{"a document with text after it", `[{"op":"add","path":"/doc/b","value":2}]`, "doc", `{"a":1} {}`, "patch: column doc: decoding the stored document: text follows its value"},
		{"an array to append to that is an object", `[{"op":"array_append","path":"/tags","value":"b"}]`, "tags", `{"a":1}`, "patch: column tags holds an object, not an array, to append to"},
		{"a counter that is no integer", `[{"op":"replace","path":"/name","value":"x"}]`, "version", "1", "patch: counter column version holds string, not an integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			composed, err := patch.ComposePatch(composeParse(t, tc.body), patch.WithIncrement("version"))
			require.NoError(t, err)
			row := stored()
			if tc.column == "" {
				delete(row, "version")
			} else {
				row[tc.column] = tc.value
			}
			_, err = composed.Apply(storedRow(row))
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// A NULL counter stays NULL, as SQL's NULL + 1 is NULL, and the stamp is
// its instant's canonical text.
func TestComposedPatch_ApplyCountsAndStamps(t *testing.T) {
	at := time.Date(2026, 11, 27, 12, 0, 0, 5, time.FixedZone("", 3600))
	composed, err := patch.ComposePatch(composeParse(t, `[{"op":"replace","path":"/name","value":"x"}]`),
		patch.WithAutoTimestamp("updated_at"), patch.WithTimeFunc(func() time.Time { return at }), patch.WithIncrement("version"), patch.WithIncrement("rank"))
	require.NoError(t, err)
	write, err := composed.Apply(storedRow(map[string]any{"name": "x", "version": nil, "rank": int64(4), "updated_at": nil}))
	require.NoError(t, err)
	want := patch.RowWrite{Values: []any{"x", "2026-11-27T11:00:00.000000005Z", nil, int64(5)}}
	assert.Equal(t, want, write, "the name it holds is unchanged; the stamp and counters move regardless")
}

// Applying an update twice, or to two rows, writes each the same: a member
// write into a value an earlier one set does not write into the composed
// update's own.
func TestComposedPatch_ApplyLeavesTheUpdateAsItWas(t *testing.T) {
	composed, err := patch.ComposePatch(composeParse(t, `[{"op":"add","path":"/doc/x","value":{}},{"op":"add","path":"/doc/x/y","value":1},{"op":"remove","path":"/doc/a"}]`))
	require.NoError(t, err)
	for range 2 {
		write, err := composed.Apply(storedRow(map[string]any{"doc": `{"a":1,"b":2}`}))
		require.NoError(t, err)
		want := patch.RowWrite{Values: []any{`{"b":2,"x":{"y":1}}`}, Changed: true}
		assert.Equal(t, want, write)
	}
}

// A stored row a member operation does not apply to fails with that
// operation's error, the first in patch order, as the SQL update reports
// it; the same write over a row it applies to is written.
func TestComposedPatch_ApplyChecksThePreconditionsInPatchOrder(t *testing.T) {
	composed, err := patch.ComposePatch(composeParse(t, `[{"op":"replace","path":"/name","value":"x"},{"op":"remove","path":"/doc/b"},{"op":"replace","path":"/doc/a/c","value":1}]`))
	require.NoError(t, err)
	pre := composed.Preconditions()
	require.Len(t, pre, 2)
	_, err = composed.Apply(storedRow(map[string]any{"name": "n", "doc": `{"a":1}`}))
	require.EqualError(t, err, pre[0].Err().Error())
	_, err = composed.Apply(storedRow(map[string]any{"name": "n", "doc": `{"a":1,"b":2}`}))
	require.EqualError(t, err, pre[1].Err().Error(), "a member below a scalar")
	_, err = composed.Apply(storedRow(map[string]any{"name": "n", "doc": nil}))
	require.EqualError(t, err, pre[0].Err().Error(), "a NULL document holds no member")
	write, err := composed.Apply(storedRow(map[string]any{"name": "n", "doc": `{"a":{"c":0},"b":2}`}))
	require.NoError(t, err)
	want := patch.RowWrite{Values: []any{"x", `{"a":{"c":1}}`}, Changed: true}
	assert.Equal(t, want, write)
}

//go:build unit || integration

package patch_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// memberSchema is mergeSchema with JSON Patch add, replace and remove on
// /doc and on its members at any depth.
func memberSchema(storage core.SQLType) *patch.PatchSchema {
	ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpArrayAppend, patch.PatchOpMove, patch.PatchOpTest}
	return newSchema("docs", 20, map[string]*patch.PathSchema{
		"/doc":    {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLType: storage, SQLColumn: "doc"},
		"/doc/**": {AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: "doc"},
	})
}

type memberCase struct {
	name  string
	start string // stored document; "" means NULL
	ops   string // JSON Patch
	merge string // the merge patch writing the same document; "" when none does
	want  string // resulting document; "" when the patch does not apply
	unmet int    // when it does not, the index of the first operation that fails
}

const memberBase = `{"a":1,"b":{"c":2,"d":3},"e":[1,2]}`

func memberCases() []memberCase {
	cases := []memberCase{
		{name: "replace a member", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/a","value":9}]`, merge: `{"doc":{"a":9}}`,
			want: `{"a":9,"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "add a member", start: memberBase,
			ops: `[{"op":"add","path":"/doc/x","value":"new"}]`, merge: `{"doc":{"x":"new"}}`,
			want: `{"a":1,"b":{"c":2,"d":3},"e":[1,2],"x":"new"}`},
		{name: "add over a member replaces it", start: memberBase,
			ops: `[{"op":"add","path":"/doc/a","value":5}]`, merge: `{"doc":{"a":5}}`,
			want: `{"a":5,"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "remove a member", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/a"}]`, merge: `{"doc":{"a":null}}`,
			want: `{"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "replace a nested member, its siblings kept", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/b/c","value":7}]`, merge: `{"doc":{"b":{"c":7}}}`,
			want: `{"a":1,"b":{"c":7,"d":3},"e":[1,2]}`},
		{name: "remove a nested member", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/b/d"}]`, merge: `{"doc":{"b":{"d":null}}}`,
			want: `{"a":1,"b":{"c":2},"e":[1,2]}`},
		{name: "an array member is replaced whole", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/e","value":[3]}]`, merge: `{"doc":{"e":[3]}}`,
			want: `{"a":1,"b":{"c":2,"d":3},"e":[3]}`},
		{name: "an object value replaces the member rather than merging", start: memberBase,
			ops:  `[{"op":"replace","path":"/doc/b","value":{"z":1}}]`,
			want: `{"a":1,"b":{"z":1},"e":[1,2]}`},
		{name: "a null value is stored", start: memberBase,
			ops:  `[{"op":"add","path":"/doc/n","value":null}]`,
			want: `{"a":1,"b":{"c":2,"d":3},"e":[1,2],"n":null}`},
		{name: "keys holding quotes, backslashes, dots and newlines", start: `{"q\"u\\o":{"k":1},"a.b":1,"é\n":{"z":2}}`,
			ops:   `[{"op":"replace","path":"/doc/q\"u\\o/k","value":2},{"op":"remove","path":"/doc/a.b"},{"op":"add","path":"/doc/é\n/y","value":3}]`,
			merge: `{"doc":{"q\"u\\o":{"k":2},"a.b":null,"é\n":{"y":3}}}`,
			want:  `{"q\"u\\o":{"k":2},"é\n":{"z":2,"y":3}}`},
		{name: "a later operation reads the member an earlier one wrote", start: memberBase,
			ops:   `[{"op":"add","path":"/doc/x","value":{}},{"op":"add","path":"/doc/x/y","value":1},{"op":"replace","path":"/doc/x/y","value":2},{"op":"remove","path":"/doc/a"}]`,
			merge: `{"doc":{"x":{"y":2},"a":null}}`,
			want:  `{"b":{"c":2,"d":3},"e":[1,2],"x":{"y":2}}`},
		{name: "remove a member, then add it again", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/a"},{"op":"add","path":"/doc/a","value":3}]`, merge: `{"doc":{"a":3}}`,
			want: `{"a":3,"b":{"c":2,"d":3},"e":[1,2]}`},
		{name: "members of a document the patch wrote whole", start: memberBase,
			ops:  `[{"op":"replace","path":"/doc","value":{"k":1}},{"op":"add","path":"/doc/m","value":2},{"op":"remove","path":"/doc/k"}]`,
			want: `{"m":2}`},
		{name: "a member operation before the document is written whole", start: memberBase,
			ops:  `[{"op":"remove","path":"/doc/a"},{"op":"replace","path":"/doc","value":{"k":1}}]`,
			want: `{"k":1}`},

		{name: "replace a missing member", start: memberBase, ops: `[{"op":"replace","path":"/doc/zz","value":1}]`, unmet: 0},
		{name: "remove a missing member", start: memberBase, ops: `[{"op":"remove","path":"/doc/zz"}]`, unmet: 0},
		{name: "add below a missing member", start: memberBase, ops: `[{"op":"add","path":"/doc/zz/y","value":1}]`, unmet: 0},
		{name: "add below a scalar", start: memberBase, ops: `[{"op":"add","path":"/doc/a/y","value":1}]`, unmet: 0},
		{name: "add below an array", start: memberBase, ops: `[{"op":"add","path":"/doc/e/y","value":1}]`, unmet: 0},
		{name: "add to a NULL document", start: "", ops: `[{"op":"add","path":"/doc/x","value":1}]`, unmet: 0},
		{name: "add to a document that is not an object", start: `[1]`, ops: `[{"op":"add","path":"/doc/x","value":1}]`, unmet: 0},
		{name: "the second operation fails on the stored row", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/a","value":2},{"op":"remove","path":"/doc/zz"}]`, unmet: 1},
		{name: "remove a member twice", start: memberBase,
			ops: `[{"op":"remove","path":"/doc/a"},{"op":"remove","path":"/doc/a"}]`, unmet: 1},
		{name: "add below a member the patch set to a scalar", start: memberBase,
			ops: `[{"op":"replace","path":"/doc/b","value":1},{"op":"add","path":"/doc/b/y","value":1}]`, unmet: 1},
		{name: "a member of a removed document", start: memberBase,
			ops: `[{"op":"remove","path":"/doc"},{"op":"add","path":"/doc/x","value":1}]`, unmet: 1},
		{name: "replace a member the document written whole lacks", start: memberBase,
			ops: `[{"op":"replace","path":"/doc","value":{"k":1}},{"op":"replace","path":"/doc/a","value":1}]`, unmet: 1},
	}
	return cases
}

// RFC 6902 member writes compile to SQL on every dialect: each case is
// executed with its precondition, checked against json-patch applying the
// same operations to the document path's form of the row, and against the
// equivalent merge patch where there is one. A dialect that refuses a
// member name holding a double quote or a backslash refuses the case
// naming one, and its merge patch writes what json-patch does.
func runMemberCases(t *testing.T, mdb mergeDB) {
	t.Helper()
	parser := patch.NewParser(memberSchema(mdb.storage), nil)
	for _, tc := range memberCases() {
		t.Run(tc.name, func(t *testing.T) {
			reset := func() {
				t.Helper()
				_, err := mdb.db.ExecContext(t.Context(), "DROP TABLE IF EXISTS docs")
				require.NoError(t, err)
				_, err = mdb.db.ExecContext(t.Context(), mdb.schema)
				require.NoError(t, err)
				var start any
				if tc.start != "" {
					start = tc.start
				}
				_, err = mdb.db.ExecContext(t.Context(), mdb.insert, start)
				require.NoError(t, err)
			}
			read := func() string {
				t.Helper()
				var got sql.NullString
				require.NoError(t, mdb.db.QueryRowContext(t.Context(), mdb.read).Scan(&got))
				return got.String
			}
			reset()
			reference, referenceErr := applyRFC6902(t, tc.start, tc.ops)

			merged := func() string {
				t.Helper()
				reset()
				merge, err := patch.ParseMergePatch([]byte(tc.merge), mergeSchema(mdb.storage), nil)
				require.NoError(t, err)
				mergeRes, err := patch.BuildPatchSQL(merge, mdb.dialect)
				require.NoError(t, err)
				_, err = mdb.db.ExecContext(t.Context(), "UPDATE docs SET "+mergeRes.SetClause+" WHERE id = "+mdb.dialect.Placeholder(mergeRes.ParamCount+1), append(mergeRes.Args, 1)...)
				require.NoError(t, err)
				return read()
			}

			parsed, err := parser.Parse([]byte(tc.ops))
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, mdb.dialect)
			if tc.want == "" && err != nil {
				pe, ok := errors.AsType[*core.ParseError](err)
				require.True(t, ok, "%v is no ParseError", err)
				assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code, "the patch's own operations settle it fails")
				require.NotNil(t, pe.Index)
				assert.Equal(t, tc.unmet, *pe.Index)
				require.Error(t, referenceErr, "json-patch fails it too")
				return
			}
			require.NoError(t, err)

			where := "id = " + mdb.dialect.Placeholder(res.ParamCount+1)
			if res.Precondition != "" {
				where += " AND " + res.Precondition
			}
			stmt := "UPDATE docs SET " + res.SetClause + " WHERE " + where
			result, err := mdb.db.ExecContext(t.Context(), stmt, append(res.Args, 1)...)
			require.NoError(t, err, stmt)
			written, err := result.RowsAffected()
			require.NoError(t, err)

			if tc.want == "" {
				assert.Zero(t, written, "a row the patch does not apply to is not written")
				assert.Equal(t, decodeDoc(t, tc.start), decodeDoc(t, read()))
				require.Error(t, referenceErr, "json-patch fails it too")
				var args []any
				bind := func(v any) string { args = append(args, v); return mdb.dialect.Placeholder(len(args)) }
				_, firstUnmet, err := patch.RenderPreconditions(mdb.dialect, res.Preconditions, bind)
				require.NoError(t, err)
				var unmet int
				require.NoError(t, mdb.db.QueryRowContext(t.Context(), "SELECT "+firstUnmet+" FROM docs WHERE id = 1", args...).Scan(&unmet))
				assert.Equal(t, tc.unmet, unmet, "the first operation the stored row fails")
				return
			}
			assert.Equal(t, int64(1), written)
			got := read()
			assert.Equal(t, decodeDoc(t, tc.want), decodeDoc(t, got), "stored document")
			require.NoError(t, referenceErr)
			assert.Equal(t, reference, decodeDoc(t, got), "matches json-patch")

			if tc.merge == "" {
				return
			}
			assert.Equal(t, decodeDoc(t, got), decodeDoc(t, merged()), "the same row as the equivalent merge patch")
		})
	}
}

// applyRFC6902 applies ops to the document path's form of a row whose doc
// is start ("" is null) with json-patch, and returns the resulting doc.
func applyRFC6902(t *testing.T, start, ops string) (any, error) {
	t.Helper()
	doc := "null"
	if start != "" {
		doc = start
	}
	decoded, err := jsonpatch.DecodePatch([]byte(ops))
	require.NoError(t, err)
	out, err := decoded.Apply(fmt.Appendf(nil, `{"doc":%s}`, doc))
	if err != nil {
		return nil, err
	}
	var row map[string]any
	require.NoError(t, json.Unmarshal(out, &row))
	return row["doc"], nil
}

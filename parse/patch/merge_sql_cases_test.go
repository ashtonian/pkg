//go:build unit || integration

package patch_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
	"github.com/ashtonian/pkg/parse/patch"
)

// mergeSchema is a schema whose /doc is a document column stored as
// storage.
func mergeSchema(storage core.SQLType) *patch.PatchSchema {
	return newSchema("docs", 10, map[string]*patch.PathSchema{
		"/doc": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove},
			DataType:   core.FieldTypeJSON, SQLType: storage, SQLColumn: "doc",
		},
	})
}

type mergeCase struct {
	name  string
	start string // stored document; "" means NULL
	body  string // RFC 7396 merge patch
	want  string // resulting document; "" means NULL
}

const mergeBase = `{"a":1,"b":{"c":2,"d":3}}`

var mergeCases = []mergeCase{
	{"siblings survive a nested merge", mergeBase, `{"doc":{"b":{"c":9}}}`, `{"a":1,"b":{"c":9,"d":3}}`},
	{"null deletes a member", mergeBase, `{"doc":{"a":null}}`, `{"b":{"c":2,"d":3}}`},
	{"nested null deletes a nested member", mergeBase, `{"doc":{"b":{"d":null}}}`, `{"a":1,"b":{"c":2}}`},
	{"new members, arrays replace", mergeBase, `{"doc":{"e":[1,2],"f":{"g":true}}}`, `{"a":1,"b":{"c":2,"d":3},"e":[1,2],"f":{"g":true}}`},
	{"deleting a missing member is a no-op", mergeBase, `{"doc":{"zz":null}}`, mergeBase},
	{"object over a scalar member", `{"a":1}`, `{"doc":{"a":{"z":1}}}`, `{"a":{"z":1}}`},
	{"merge into NULL", "", `{"doc":{"x":1}}`, `{"x":1}`},
	{"merge into a non-object document", `[1,2]`, `{"doc":{"x":1}}`, `{"x":1}`},
	{"keys are data, not paths", mergeBase, `{"doc":{"it's":"q","a.b":{"c":1},"{x}":2}}`, `{"a":1,"b":{"c":2,"d":3},"it's":"q","a.b":{"c":1},"{x}":2}`},
	{"null document removes", mergeBase, `{"doc":null}`, ""},
	{"non-object value replaces", mergeBase, `{"doc":[3]}`, `[3]`},
	{"empty patch object keeps the document", mergeBase, `{"doc":{}}`, mergeBase},
	{"an array member is not an object to merge into", `{"a":[{"x":1}]}`, `{"doc":{"a":{"0":{"y":2}}}}`, `{"a":{"0":{"y":2}}}`},
	{"nested keys needing quotes", `{"q\"u\\o":{"k":1},"é\n":{"z":{"w":1,"v":2}}}`, `{"doc":{"q\"u\\o":{"k":null,"j":2},"é\n":{"z":{"w":null}}}}`, `{"q\"u\\o":{"j":2},"é\n":{"z":{"v":2}}}`},
	{"five levels", `{"a":{"b":{"c":{"x":1}}}}`, `{"doc":{"a":{"b":{"c":{"d":1}}}}}`, `{"a":{"b":{"c":{"x":1,"d":1}}}}`},
	wideMergeCase(),
}

// wideMergeCase merges far more members than one PostgreSQL function call
// takes arguments (100), at the top of the document and one level down.
func wideMergeCase() mergeCase {
	patchDoc := map[string]any{}
	want := map[string]any{"keep": 1}
	nestedPatch := map[string]any{"gone": nil}
	nestedWant := map[string]any{"old": true}
	for i := range 120 {
		patchDoc[fmt.Sprintf("k%03d", i)] = i
		want[fmt.Sprintf("k%03d", i)] = i
		nestedPatch[fmt.Sprintf("m%03d", i)] = i
		nestedWant[fmt.Sprintf("m%03d", i)] = i
	}
	patchDoc["n"] = nestedPatch
	want["n"] = nestedWant
	body, _ := json.Marshal(map[string]any{"doc": patchDoc})
	wantDoc, _ := json.Marshal(want)
	return mergeCase{"wider than a function call", `{"keep":1,"n":{"old":true,"gone":0}}`, string(body), string(wantDoc)}
}

// mergeDB describes one database under test.
type mergeDB struct {
	db      *sql.DB
	dialect patch.SQLDialect
	schema  string
	// storage is the doc column's declared type: core.SQLJSON for a json
	// column, "" otherwise.
	storage core.SQLType
	insert  string // INSERT with one placeholder for the document text
	read    string // SELECT yielding the document as JSON text
}

// RFC 7396 merges compile to SQL on every dialect:
// executed, asserting the stored document, cross-checked against the
// document path's jsonmerge.Apply.
func runMergeCases(t *testing.T, mdb mergeDB) {
	t.Helper()
	for _, tc := range mergeCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mdb.db.Exec("DROP TABLE IF EXISTS docs")
			require.NoError(t, err)
			_, err = mdb.db.Exec(mdb.schema)
			require.NoError(t, err)
			var start any
			if tc.start != "" {
				start = tc.start
			}
			_, err = mdb.db.Exec(mdb.insert, start)
			require.NoError(t, err)

			parsed, err := patch.ParseMergePatch([]byte(tc.body), mergeSchema(mdb.storage), nil)
			require.NoError(t, err)
			res, err := patch.BuildPatchSQL(parsed, mdb.dialect)
			require.NoError(t, err)
			stmt := fmt.Sprintf("UPDATE docs SET %s WHERE id = %s", res.SetClause, mdb.dialect.Placeholder(res.ParamCount+1))
			_, err = mdb.db.Exec(stmt, append(res.Args, 1)...)
			require.NoError(t, err, stmt)

			var got sql.NullString
			require.NoError(t, mdb.db.QueryRow(mdb.read).Scan(&got))
			assert.Equal(t, decodeDoc(t, tc.want), decodeDoc(t, got.String), "stored document")

			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(tc.body), &body))
			oracle := jsonmerge.Apply(decodeDoc(t, tc.start), body["doc"])
			assert.Equal(t, oracle, decodeDoc(t, got.String), "matches the document path")
		})
	}
}

func decodeDoc(t *testing.T, s string) any {
	t.Helper()
	if s == "" {
		return nil
	}
	var v any
	require.NoError(t, json.Unmarshal([]byte(s), &v))
	return v
}

//go:build unit

package patch_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func p3Schema() *patch.PatchSchema {
	return &patch.PatchSchema{
		Resource: "items",
		AllowedPaths: map[string]*patch.PathSchema{
			"/name":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpTest}, DataType: core.FieldTypeString, SQLColumn: "name"},
			"/config": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove}, DataType: core.FieldTypeJSON, SQLColumn: "config"},
			"/badcol": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString}, // SQLColumn deliberately empty
		},
		MaxOperations: 20,
	}
}

// TestMergePatchMarksObjectValues: RFC 7396 object values need recursive
// merge; ParseMergePatch marks them so apply/SQL paths can honor (or refuse)
// merge semantics instead of silently replacing whole sub-objects.
func TestMergePatchMarksObjectValues(t *testing.T) {
	parsed, err := patch.ParseMergePatch([]byte(`{"config": {"theme": "dark"}, "name": "x"}`), p3Schema(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*patch.PatchOperation{}
	for _, op := range parsed.Operations {
		byPath[op.Path] = op
	}
	if op := byPath["/config"]; op == nil || !op.Merge {
		t.Errorf("/config object value not marked Merge: %+v", op)
	}
	if op := byPath["/name"]; op == nil || op.Merge {
		t.Errorf("/name scalar value wrongly marked Merge: %+v", op)
	}
}

// TestMergeObjectBuildsSQL: a merge-derived object op compiles to a
// merge of the stored document — never a whole-column
// SET that would drop sibling keys. Executed in merge_sql_cases_test.go.
func TestMergeObjectBuildsSQL(t *testing.T) {
	parsed, err := patch.ParseMergePatch([]byte(`{"config": {"theme": "dark"}}`), p3Schema(), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatalf("merge-derived object op rejected: %v", err)
	}
	if !strings.Contains(res.SetClause, "config = (COALESCE(jsonb_path_query_first(config,") || strings.Contains(res.SetClause, "config = $1") {
		t.Errorf("SetClause = %q; want a merge into config, not a whole-column set", res.SetClause)
	}
}

// TestScalarAndNullMergeStillBuildSQL: scalar sets and null-removes carry
// exact RFC 7396 semantics as plain column writes — they must keep working.
func TestScalarAndNullMergeStillBuildSQL(t *testing.T) {
	parsed, err := patch.ParseMergePatch([]byte(`{"name": "renamed", "config": null}`), p3Schema(), nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatalf("scalar+null merge rejected: %v", err)
	}
	if !strings.Contains(res.SetClause, "name =") || !strings.Contains(res.SetClause, "config = NULL") {
		t.Errorf("SetClause = %q", res.SetClause)
	}
}

// TestTestOpRejectedInSQL (Q2 = a): test ops were silently dropped from
// SQL — optimistic-concurrency intent unenforced. Reject with a clear error.
func TestTestOpRejectedInSQL(t *testing.T) {
	parsed, err := patch.NewParser(p3Schema(), nil).Parse([]byte(`[{"op":"test","path":"/name","value":"expected"},{"op":"replace","path":"/name","value":"new"}]`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err == nil {
		t.Fatal("test op silently dropped from SQL; want rejection")
	}
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("error should name the test op: %v", err)
	}
}

// TestEmptySQLColumnErrors: columnName fell back to the raw request path
// as a SQL identifier when PathSchema.SQLColumn was empty — user text as
// identifier.
func TestEmptySQLColumnErrors(t *testing.T) {
	parsed, err := patch.NewParser(p3Schema(), nil).Parse([]byte(`[{"op":"replace","path":"/badcol","value":"x"}]`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err == nil {
		t.Fatal("empty SQLColumn fell back to raw path identifier; want error")
	}
}

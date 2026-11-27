//go:build unit

package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

// A patch of document members is planned to fail on a row it does not apply
// to: Current locks the targets, Conflict names the failing operation, and
// Owner's WHERE holds the same precondition.
func TestBuildUpdate_MembersPlanAConflictRead(t *testing.T) {
	r := gadgetAttrsResource(t, onPostgres)
	parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"red"}]`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(parsed, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	require.NotNil(t, plan.Current)
	require.NotNil(t, plan.Conflict)
	assert.Contains(t, plan.Owner.SQL, `#> $3::text[]) IS NOT NULL`)
	assert.Contains(t, plan.Conflict.SQL, `SELECT CASE WHEN NOT (("attrs"::jsonb #> $2::text[]) IS NOT NULL) THEN 0 END`)

	whole, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs","value":{"color":"red"}}]`))
	require.NoError(t, err)
	plan, err = r.BuildUpdate(whole, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	assert.Nil(t, plan.Conflict, "a whole-document write needs nothing of the stored row")
}

// The row Conflict reads fails the update with the operation it fails,
// before anything is written.
func TestUpdate_MembersConflictNamesTheFailingOperation(t *testing.T) {
	provider, db := providertest.SQLite(t)
	execStatements(t, db, `CREATE TABLE gadget_attrs (id INTEGER PRIMARY KEY, attrs TEXT NOT NULL)`,
		`INSERT INTO gadget_attrs VALUES (1, '{}')`)
	r := gadgetAttrsResource(t, provider)
	parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"red"}]`))
	require.NoError(t, err)
	_, err = r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{})
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%v is no ParseError", err)
	assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
	assert.Equal(t, "/attrs/color", pe.Path)
	assert.Equal(t, []string{"{}"}, queryRows[string](t, db, `SELECT attrs FROM gadget_attrs`), "no row is written")
}

// A batch compiles its rows' documents whole: a member operation the stored
// row decides is refused, one the row's own patch settles is not.
func TestBuildUpdateBatch_MembersTheStoredRowDecides(t *testing.T) {
	r := gadgetAttrsResource(t, onPostgres)
	member, err := r.ParsePatch([]byte(`[{"op":"remove","path":"/attrs/old"}]`))
	require.NoError(t, err)
	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: member}}, parse.Scope{}, parse.UpdateBatchLimits{})
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%v is no ParseError", err)
	assert.Equal(t, patch.ECodeOpNotSQLCompilable.String(), pe.Code)

	settled, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs","value":{}},{"op":"add","path":"/attrs/new","value":1}]`))
	require.NoError(t, err)
	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: settled}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
}

type gadgetSpec struct {
	W     int               `json:"w"`
	Label string            `json:"label,omitempty"`
	Count int               `json:"count,string"`
	Tags  map[string]string `json:"tags"`
}

type typedGadget struct {
	ID    int64       `json:"id"    db:"id"`
	Spec  gadgetSpec  `json:"spec"  db:"spec"  parse:"patch"`
	Sizes map[int]int `json:"sizes" db:"sizes" parse:"patch"`
}

// A member of a typed document is held to the field's Go type when the
// patch parses, for SQL and ApplyValidatedPatch alike: an exact member name,
// a map key as encoding/json writes it, and a value that decodes as the
// member, tag options included.
func TestParsePatch_TypedDocumentMembers(t *testing.T) {
	r, err := parse.NewResource[typedGadget](onPostgres, "typed_gadgets", parse.CustomizeSchemas(func(s parse.Schemas) error {
		ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove}
		for path, column := range map[string]string{"/spec/**": "spec", "/sizes/*": "sizes"} {
			err := s.Patch.AddPath(path, &patch.PathSchema{AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: column})
			if err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		ops  string
		ok   bool
	}{
		{"a struct member", `[{"op":"replace","path":"/spec/w","value":3}]`, true},
		{"a member of a nested map", `[{"op":"add","path":"/spec/tags/k","value":"v"}]`, true},
		{"a member with the string option", `[{"op":"replace","path":"/spec/count","value":"4"}]`, true},
		{"an integer key", `[{"op":"add","path":"/sizes/7","value":1}]`, true},
		{"a remove of a struct member", `[{"op":"remove","path":"/spec/label"}]`, true},
		{"a struct member in another case", `[{"op":"replace","path":"/spec/W","value":3}]`, false},
		{"a struct member it does not declare", `[{"op":"add","path":"/spec/extra","value":3}]`, false},
		{"a value of another type", `[{"op":"replace","path":"/spec/w","value":"three"}]`, false},
		{"a number for a member with the string option", `[{"op":"replace","path":"/spec/count","value":4}]`, false},
		{"a map value of another type", `[{"op":"add","path":"/spec/tags/k","value":1}]`, false},
		{"a member of a scalar", `[{"op":"add","path":"/spec/w/x","value":1}]`, false},
		{"a key encoding/json does not decode", `[{"op":"add","path":"/sizes/x","value":1}]`, false},
		{"a key encoding/json writes otherwise", `[{"op":"remove","path":"/sizes/07"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ParsePatch([]byte(tc.ops))
			if tc.ok {
				require.NoError(t, err)
				return
			}
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
			require.NotNil(t, pe.Index)
			assert.Zero(t, *pe.Index)
		})
	}
}

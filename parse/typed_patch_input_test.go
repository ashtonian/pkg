//go:build unit

package parse_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// A patch writes the row it returns, never the row it is given, nor a parent
// that row embeds by pointer and the caller still holds.

type PatchParent struct {
	Name  string `json:"name"  db:"name"  parse:"patch" patch_ops:"replace,test"`
	Label string `json:"label" db:"label" parse:"patch" patch_ops:"replace"`
	Note  string `json:"note"  db:"note"`
}

type PatchMiddle struct {
	*PatchParent
	Rank int `json:"rank" db:"rank" parse:"patch" patch_ops:"replace"`
}

// patchPrivate is embedded by an unexported pointer: a patch can write none
// of its fields and a row scan fill none, so none is tagged patch and a
// resource over promotedPatchRow reads none (promotedReadColumns).
type patchPrivate struct {
	Extra string            `json:"extra" db:"extra"`
	Attrs map[string]string `json:"attrs" db:"attrs"`
}

type promotedPatchRow struct {
	ID      string `json:"id"      db:"id"`
	Version int    `json:"version" db:"version" parse:"patch" patch_ops:"test"`
	*PatchMiddle
	*patchPrivate
}

// promotedReadColumns reads every column of promotedPatchRow but
// patchPrivate's.
func promotedReadColumns() parse.ResourceOption {
	return parse.ReadColumns("id", "version", "name", "label", "note", "rank")
}

// The rows a patch starts from: parents allocated, the middle or the parent
// nil, and the unexported parent allocated too.
func allocatedPatchRow() promotedPatchRow {
	row := promotedPatchRow{ID: "1", Version: 1, PatchMiddle: &PatchMiddle{Rank: 1, PatchParent: &PatchParent{Name: "before", Label: "before", Note: "kept"}}}
	return row
}

func nilMiddlePatchRow() promotedPatchRow {
	row := promotedPatchRow{ID: "1", Version: 1}
	return row
}

func nilParentPatchRow() promotedPatchRow {
	row := promotedPatchRow{ID: "1", Version: 1, PatchMiddle: &PatchMiddle{Rank: 1}}
	return row
}

func privatePatchRow() promotedPatchRow {
	row := allocatedPatchRow()
	row.patchPrivate = &patchPrivate{Extra: "private", Attrs: map[string]string{"k": "before"}}
	return row
}

func TestApplyPatch_LeavesTheRowAndItsEmbeddedParents(t *testing.T) {
	const (
		direct   = `[{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/rank","value":2},{"op":"replace","path":"/label","value":"labelled"}]`
		document = `[{"op":"test","path":"/version","value":1},{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/rank","value":2},{"op":"replace","path":"/label","value":"labelled"}]`
	)
	for _, tc := range []struct {
		name, body string
		row        func() promotedPatchRow
		// wantNote is the untouched parent field the result carries over.
		wantNote string
	}{
		{name: "direct, allocated parents", body: direct, row: allocatedPatchRow, wantNote: "kept"},
		{name: "document, allocated parents", body: document, row: allocatedPatchRow, wantNote: "kept"},
		{name: "direct, nil middle", body: direct, row: nilMiddlePatchRow},
		{name: "document, nil middle", body: document, row: nilMiddlePatchRow},
		{name: "direct, nil parent", body: direct, row: nilParentPatchRow},
		{name: "document, nil parent", body: document, row: nilParentPatchRow},
		{name: "direct, allocated unexported parent", body: direct, row: privatePatchRow, wantNote: "kept"},
		{name: "document, allocated unexported parent", body: document, row: privatePatchRow, wantNote: "kept"},
	} {
		check := func(t *testing.T, got promotedPatchRow, err error) {
			t.Helper()
			require.NoError(t, err)
			require.NotNil(t, got.PatchMiddle)
			require.NotNil(t, got.PatchParent)
			assert.Equal(t, PatchParent{Name: "after", Label: "labelled", Note: tc.wantNote}, *got.PatchParent)
			assert.Equal(t, 2, got.Rank)
			assert.Equal(t, "1", got.ID)
			assert.Equal(t, tc.row().patchPrivate, got.patchPrivate)
		}
		t.Run(tc.name+", value row", func(t *testing.T) {
			r, err := parse.NewResource[promotedPatchRow](onSQLite, "promoted_rows", parse.ColumnType("attrs", core.SQLJSONB), promotedReadColumns())
			require.NoError(t, err)
			in := tc.row()
			kept := retainParents(in)
			got, err := r.ApplyPatch([]byte(tc.body), in)
			check(t, got, err)
			kept.assertUnchanged(t, tc.row(), in)
		})
		t.Run(tc.name+", pointer row", func(t *testing.T) {
			r, err := parse.NewResource[*promotedPatchRow](onSQLite, "promoted_rows", parse.ColumnType("attrs", core.SQLJSONB), promotedReadColumns())
			require.NoError(t, err)
			in := new(promotedPatchRow)
			*in = tc.row()
			kept := retainParents(*in)
			got, err := r.ApplyPatch([]byte(tc.body), in)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.NotSame(t, in, got)
			check(t, *got, err)
			kept.assertUnchanged(t, tc.row(), *in)
		})
	}
}

// A field promoted through an unexported embedded pointer, nil or not, is
// refused where the patch schema is built.

type privatePatched struct {
	Extra string `json:"extra" db:"extra" parse:"patch"`
}

type privatePatchedRow struct {
	ID string `json:"id" db:"id"`
	*privatePatched
}

type privateServer struct {
	Fingerprint string `json:"-" db:"fingerprint" parse:"server"`
}

type privateServerRow struct {
	ID string `json:"id" db:"id"`
	*privateServer
}

type privateDocument struct {
	Attrs map[string]string `json:"attrs" db:"attrs" parse:"patch"`
}

type privateDocumentRow struct {
	ID string `json:"id" db:"id"`
	*privateDocument
}

// PrivateHolder is exported, and so settable, but holds the unexported
// pointer the field is promoted through.
type PrivateHolder struct {
	*privatePatched
}

type deepPrivateRow struct {
	ID string `json:"id" db:"id"`
	*PrivateHolder
}

type privateByValueRow struct {
	ID string `json:"id" db:"id"`
	privatePatched
}

func TestPatchSchema_RefusesAFieldBehindAnUnexportedEmbeddedPointer(t *testing.T) {
	const fix = "export the type or embed it by value"
	for _, tt := range []struct {
		name  string
		build func() error
		// field and embedded name the refused field and the pointer type.
		field, embedded string
	}{
		{
			name: "resource, value row",
			build: func() error {
				_, err := parse.NewResource[privatePatchedRow](onSQLite, "rows")
				return err
			},
			field: "Extra", embedded: "*parse_test.privatePatched",
		},
		{
			name: "resource, pointer row",
			build: func() error {
				_, err := parse.NewResource[*privatePatchedRow](onSQLite, "rows")
				return err
			},
			field: "Extra", embedded: "*parse_test.privatePatched",
		},
		{
			name: "standalone patch schema",
			build: func() error {
				_, err := patch.GeneratePatchSchema[privatePatchedRow](core.NewSchemaGenerator(core.WithSQLTableName("rows")))
				return err
			},
			field: "Extra", embedded: "*parse_test.privatePatched",
		},
		{
			name: "schema registry",
			build: func() error {
				return parse.Register[privatePatchedRow](parse.NewSchemaRegistry(core.WithSQLTableName("rows")))
			},
			field: "Extra", embedded: "*parse_test.privatePatched",
		},
		{
			name: "server field, resource",
			build: func() error {
				_, err := parse.NewResource[privateServerRow](onSQLite, "rows")
				return err
			},
			field: "Fingerprint", embedded: "*parse_test.privateServer",
		},
		{
			name: "server field, standalone server patch schema",
			build: func() error {
				_, err := patch.GenerateServerPatchSchema[privateServerRow](core.NewSchemaGenerator(core.WithSQLTableName("rows")))
				return err
			},
			field: "Fingerprint", embedded: "*parse_test.privateServer",
		},
		{
			name: "document column",
			build: func() error {
				_, err := parse.NewResource[privateDocumentRow](onSQLite, "rows", parse.ColumnType("attrs", core.SQLJSONB))
				return err
			},
			field: "Attrs", embedded: "*parse_test.privateDocument",
		},
		{
			name: "behind an exported embedded pointer",
			build: func() error {
				_, err := parse.NewResource[deepPrivateRow](onSQLite, "rows")
				return err
			},
			field: "Extra", embedded: "*parse_test.privatePatched",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build()
			require.Error(t, err)
			require.ErrorContains(t, err, "field "+tt.field+":")
			require.ErrorContains(t, err, "unexported embedded "+tt.embedded)
			require.ErrorContains(t, err, fix)
		})
	}
}

func TestPatchSchema_AdmitsAFieldOfAnUnexportedStructEmbeddedByValue(t *testing.T) {
	r, err := parse.NewResource[privateByValueRow](onSQLite, "rows")
	require.NoError(t, err)
	in := privateByValueRow{ID: "1", privatePatched: privatePatched{Extra: "before"}}
	got, err := r.ApplyPatch([]byte(`[{"op":"replace","path":"/extra","value":"after"}]`), in)
	require.NoError(t, err)
	assert.Equal(t, "after", got.Extra)
	assert.Equal(t, "before", in.Extra)
}

func TestNewResource_RefusesACustomizedPathBehindAnUnexportedEmbeddedPointer(t *testing.T) {
	replace := func(column string) *patch.PathSchema {
		ps := &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeAny, SQLColumn: column}
		return ps
	}
	for _, tt := range []struct {
		name      string
		customize func(parse.Schemas) error
		// wantPath is the refused key, "" when the resource builds.
		wantPath, wantField string
	}{
		{
			name:      "exact path",
			customize: func(s parse.Schemas) error { return s.Patch.AddPath("/extra", replace("extra")) },
			wantPath:  "/extra", wantField: "Extra",
		},
		{
			name:      "document member pattern",
			customize: func(s parse.Schemas) error { return s.Patch.AddPath("/attrs/*", replace("attrs")) },
			wantPath:  "/attrs/*", wantField: "Attrs",
		},
		{
			name:      "top-level wildcard",
			customize: func(s parse.Schemas) error { return s.Patch.AddPath("/*", replace("")) },
			wantPath:  "/*", wantField: "Attrs",
		},
		{
			name:      "any-depth wildcard",
			customize: func(s parse.Schemas) error { return s.Patch.AddPath("/**", replace("")) },
			wantPath:  "/**", wantField: "Attrs",
		},
		{
			name:      "server patch schema",
			customize: func(s parse.Schemas) error { return s.ServerPatch.AddPath("/extra", replace("extra")) },
			wantPath:  "/extra", wantField: "Extra",
		},
		{
			name:      "a pattern reaching only writable fields",
			customize: func(s parse.Schemas) error { return s.Patch.AddPath("/label/*", replace("label")) },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse.NewResource[promotedPatchRow](onSQLite, "promoted_rows", parse.ColumnType("attrs", core.SQLJSONB), parse.CustomizeSchemas(tt.customize), promotedReadColumns())
			if tt.wantPath == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, "path "+tt.wantPath+": field "+tt.wantField+":")
			require.ErrorContains(t, err, "unexported embedded *parse_test.patchPrivate")
		})
	}
}

// A schema completed by hand and never checked is the one way left to a
// write through an unexported embedded pointer. Apply refuses it as the
// server's fault, after an earlier operation's write, and leaves the row.
func TestApplier_RefusesAnUncheckedPathBehindAnUnexportedEmbeddedPointer(t *testing.T) {
	const (
		direct   = `[{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/extra","value":"x"}]`
		document = `[{"op":"test","path":"/version","value":1},{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/attrs","value":{"k":"v"}}]`
	)
	gen := core.NewSchemaGenerator(core.WithSQLTableName("promoted_rows"), core.WithColumnTypes(map[string]core.SQLType{"attrs": core.SQLJSONB}))
	schema, err := patch.GeneratePatchSchema[promotedPatchRow](gen)
	require.NoError(t, err)
	err = schema.AddPath("/extra", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, GoType: reflect.TypeFor[string](), SQLColumn: "extra"})
	require.NoError(t, err)
	err = schema.AddPath("/attrs", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeAny, SQLType: core.SQLJSONB, SQLColumn: "attrs"})
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)

	values, err := patch.NewApplier[promotedPatchRow](gen, nil)
	require.NoError(t, err)
	pointers, err := patch.NewApplier[*promotedPatchRow](gen, nil)
	require.NoError(t, err)
	for _, check := range []func(*patch.PatchSchema) error{values.CheckSchema, pointers.CheckSchema} {
		err = check(schema)
		require.Error(t, err)
		require.ErrorContains(t, err, "path /attrs: field Attrs:")
	}

	refused := func(t *testing.T, err error, field string) {
		t.Helper()
		require.Error(t, err)
		require.ErrorContains(t, err, "field "+field+": a write reaches it through the unexported embedded *parse_test.patchPrivate")
		_, isParseError := errors.AsType[*core.ParseError](err)
		assert.False(t, isParseError, "the server's fault answers as no request error: %v", err)
	}
	for _, tc := range []struct {
		name, body, field string
		row               func() promotedPatchRow
	}{
		{name: "direct, nil unexported parent", body: direct, field: "Extra", row: allocatedPatchRow},
		{name: "direct, allocated unexported parent", body: direct, field: "Extra", row: privatePatchRow},
		{name: "document, nil unexported parent", body: document, field: "Attrs", row: allocatedPatchRow},
		{name: "document, allocated unexported parent", body: document, field: "Attrs", row: privatePatchRow},
	} {
		parsed, err := parser.Parse([]byte(tc.body))
		require.NoError(t, err)
		t.Run(tc.name+", value row", func(t *testing.T) {
			in := tc.row()
			kept := retainParents(in)
			_, err := values.Apply(parsed, in)
			refused(t, err, tc.field)
			kept.assertUnchanged(t, tc.row(), in)
		})
		t.Run(tc.name+", pointer row", func(t *testing.T) {
			in := new(promotedPatchRow)
			*in = tc.row()
			kept := retainParents(*in)
			_, err := pointers.Apply(parsed, in)
			refused(t, err, tc.field)
			kept.assertUnchanged(t, tc.row(), *in)
		})
	}
}

// retainedParents are the embedded parents a caller kept pointers to before
// a patch.
type retainedParents struct {
	middle  *PatchMiddle
	parent  *PatchParent
	private *patchPrivate
}

func retainParents(row promotedPatchRow) retainedParents {
	kept := retainedParents{middle: row.PatchMiddle, private: row.patchPrivate}
	if row.PatchMiddle != nil {
		kept.parent = row.PatchParent
	}
	return kept
}

// assertUnchanged checks that row, the caller's row after a patch, still
// holds the parents it held and equals want, a fresh build of it, parents
// included.
func (k retainedParents) assertUnchanged(t *testing.T, want, row promotedPatchRow) {
	t.Helper()
	assert.Equal(t, want, row)
	assert.Same(t, k.middle, row.PatchMiddle)
	assert.Same(t, k.private, row.patchPrivate)
	if k.middle != nil {
		assert.Same(t, k.parent, row.PatchParent)
	}
}

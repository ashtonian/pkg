//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
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

// bsonDevice is tagged with bson only — the schema generator reads bson names
// via WithFieldNameTag; the direct-apply path must use the same tag.
type bsonDevice struct {
	ID     string `json:"id,omitempty" db:"id"`
	Name   string `bson:"name"   parse:"filter,patch" patch_ops:"replace"`
	Status string `bson:"status" parse:"filter,patch" patch_ops:"replace"`
}

// TestFieldNameTagThreadsToApply: with a non-json FieldNameTag, applying a
// validated patch finds each field by that tag, not by its json name.
func TestFieldNameTagThreadsToApply(t *testing.T) {
	tp, err := parse.NewResource[bsonDevice](onSQLite, "bson_devices", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"renamed"}]`),
		bsonDevice{Name: "orig", Status: "active"},
	)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if out.Name != "renamed" {
		t.Fatalf("Name = %q; want renamed (patch silently no-op'd)", out.Name)
	}
	if out.Status != "active" {
		t.Errorf("Status = %q; want active untouched", out.Status)
	}
}

// TestSchemaValidatedPathMissMustError: a path the schema validated but the
// struct map can't resolve is an internal invariant violation, not a skip.
type mixedTagDevice struct {
	ID   string `json:"id,omitempty" db:"id"`
	Name string `bson:"name" json:"-" parse:"filter,patch" patch_ops:"replace"`
}

func TestSchemaValidatedPathMissMustError(t *testing.T) {
	// Schema reads bson ("name" patchable); the json tag hides the field from
	// a json-keyed map. With bson threading the apply works; this pins that
	// the two layers agree on the SAME tag rather than silently dropping ops.
	tp, err := parse.NewResource[mixedTagDevice](onSQLite, "mixed_tag_devices", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"x"}]`),
		mixedTagDevice{Name: "orig"},
	)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if out.Name != "x" {
		t.Fatalf("Name = %q; want x", out.Name)
	}
}

// uintDevice exercises the numeric coercion guards on the direct-apply path:
// a value outside an unsigned or narrow field's range is refused.
type uintDevice struct {
	ID     string  `json:"id,omitempty" db:"id"`
	Count  uint    `json:"count"  parse:"filter,patch" patch_ops:"replace"`
	Level  int     `json:"level"  parse:"filter,patch" patch_ops:"replace"`
	Weight float64 `json:"weight" parse:"filter,patch" patch_ops:"replace"`
}

// TestNumericCoercionGuards: -1 into a uint and 3.7 into an int are refused,
// not wrapped or truncated; an integral float into an int and any finite
// float into a float apply.
func TestNumericCoercionGuards(t *testing.T) {
	tp, err := parse.NewResource[uintDevice](onSQLite, "uint_devices")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("negative into uint errors", func(t *testing.T) {
		_, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/count","value":-1}]`), uintDevice{})
		if err == nil {
			t.Fatal("-1 accepted into uint; want an error")
		}
		if !strings.Contains(err.Error(), "E2077") && !strings.Contains(strings.ToLower(err.Error()), "negative") {
			t.Logf("error text: %v", err)
		}
	})

	t.Run("fractional into int errors", func(t *testing.T) {
		_, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/level","value":3.7}]`), uintDevice{})
		if err == nil {
			t.Fatal("3.7 accepted into int; want error (silent truncation before)")
		}
	})

	t.Run("fractional into uint errors", func(t *testing.T) {
		_, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/count","value":2.5}]`), uintDevice{})
		if err == nil {
			t.Fatal("2.5 accepted into uint; want error")
		}
	})

	t.Run("integral float into int ok", func(t *testing.T) {
		out, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/level","value":3}]`), uintDevice{})
		if err != nil {
			t.Fatalf("3 rejected: %v", err)
		}
		if out.Level != 3 {
			t.Errorf("Level = %d; want 3", out.Level)
		}
	})

	t.Run("integral float into uint ok", func(t *testing.T) {
		out, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/count","value":7}]`), uintDevice{})
		if err != nil {
			t.Fatalf("7 rejected: %v", err)
		}
		if out.Count != 7 {
			t.Errorf("Count = %d; want 7", out.Count)
		}
	})

	t.Run("float field unaffected", func(t *testing.T) {
		out, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/weight","value":2.5}]`), uintDevice{})
		if err != nil {
			t.Fatalf("2.5 into float rejected: %v", err)
		}
		if out.Weight != 2.5 {
			t.Errorf("Weight = %v; want 2.5", out.Weight)
		}
	})
}

type cfgEntity struct {
	ID     string         `json:"id,omitempty" db:"id"`
	Name   string         `json:"name"   parse:"filter,patch" patch_ops:"replace"`
	Config map[string]any `json:"config" parse:"patch"        patch_ops:"replace,remove"`
}

// TestMergePatchPreservesSiblings: RFC 7396 §2 merges object values
// recursively, so applying {"config":{"theme":"dark"}} keeps config.lang.
func TestMergePatchPreservesSiblings(t *testing.T) {
	tp, err := parse.NewResource[cfgEntity](onSQLite, "cfg_entities")
	if err != nil {
		t.Fatal(err)
	}

	target := cfgEntity{
		Name:   "dev",
		Config: map[string]any{"theme": "light", "lang": "en"},
	}

	parsed, err := tp.ParseMergePatch([]byte(`{"config": {"theme": "dark"}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyValidatedPatch(parsed, target)
	if err != nil {
		t.Fatal(err)
	}
	if out.Config["theme"] != "dark" {
		t.Errorf("theme = %v; want dark", out.Config["theme"])
	}
	if out.Config["lang"] != "en" {
		t.Errorf("lang = %v; want en preserved (RFC 7396 recursive merge)", out.Config["lang"])
	}
	if out.Name != "dev" {
		t.Errorf("Name = %q; want untouched", out.Name)
	}
}

// TestMergePatchNestedNullDeletes: within an object merge, null at a key
// removes that key (RFC 7396); it is not stored as a JSON null.
func TestMergePatchNestedNullDeletes(t *testing.T) {
	tp, err := parse.NewResource[cfgEntity](onSQLite, "cfg_entities")
	if err != nil {
		t.Fatal(err)
	}
	target := cfgEntity{Config: map[string]any{"theme": "light", "lang": "en"}}

	parsed, err := tp.ParseMergePatch([]byte(`{"config": {"lang": null}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyValidatedPatch(parsed, target)
	if err != nil {
		t.Fatal(err)
	}
	_, exists := out.Config["lang"]
	if exists {
		t.Errorf("lang still present (%v); want removed per RFC 7396 nested null", out.Config["lang"])
	}
	if out.Config["theme"] != "light" {
		t.Errorf("theme = %v; want light preserved", out.Config["theme"])
	}
}

// TestTopLevelNullStillRemoves: a top-level null clears the whole field.
// A map field clears to its empty value, never NULL.
func TestTopLevelNullStillRemoves(t *testing.T) {
	tp, err := parse.NewResource[cfgEntity](onSQLite, "cfg_entities")
	if err != nil {
		t.Fatal(err)
	}
	target := cfgEntity{Config: map[string]any{"theme": "light"}}
	parsed, err := tp.ParseMergePatch([]byte(`{"config": null}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyValidatedPatch(parsed, target)
	if err != nil {
		t.Fatal(err)
	}
	if out.Config == nil || len(out.Config) != 0 {
		t.Errorf("Config = %#v; want an empty map", out.Config)
	}
}

type documentDimensions struct {
	W int `json:"w"`
	H int `json:"h"`
}

type documentRow struct {
	ID   string             `json:"id,omitempty" db:"id"`
	Dims documentDimensions `json:"dims" db:"dims" parse:"patch"`
}

func TestTypedDocumentReplacementCanonicalizes(t *testing.T) {
	p, err := parse.NewResource[documentRow](onSQLite, "document_rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParsePatch([]byte(`[{"op":"replace","path":"/dims","value":{"w":9}}]`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, documentRow{Dims: documentDimensions{W: 2, H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"w": json.Number("9"), "h": json.Number("0")}
	if !reflect.DeepEqual(parsed.Operations[0].Value, want) || got.Dims != (documentDimensions{W: 9}) {
		t.Fatalf("replacement = %#v, typed apply = %#v", parsed.Operations[0].Value, got)
	}
}

func TestTypedDocumentMergePreservesSiblings(t *testing.T) {
	p, err := parse.NewResource[documentRow](onSQLite, "document_rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParseMergePatch([]byte(`{"dims":{"w":9}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, documentRow{Dims: documentDimensions{W: 2, H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := parsed.Operations[0].Value.(map[string]any)
	if !ok {
		t.Fatalf("merge value = %T, want map[string]any", parsed.Operations[0].Value)
	}
	if got.Dims != (documentDimensions{W: 9, H: 7}) || len(value) != 1 {
		t.Fatalf("merge = %#v, typed apply = %#v", parsed.Operations[0].Value, got)
	}
}

func TestTypedDocumentRejectsUnknownMembersAndRequiredDeletion(t *testing.T) {
	p, err := parse.NewResource[documentRow](onSQLite, "document_rows")
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"dims":{"unknown":9}}`,
		`{"dims":{"w":null}}`,
		`{"dims":{"W":9}}`,
	} {
		_, err := p.ParseMergePatch([]byte(body))
		if err == nil {
			t.Errorf("accepted lossy merge %s", body)
		}
	}
	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":"/dims","value":{"unknown":9}}]`))
	if err == nil {
		t.Fatal("accepted unknown replacement member")
	}
}

func TestTypedDocumentMergeNestedAndNullable(t *testing.T) {
	type nested struct {
		Dims     documentDimensions  `json:"dims"`
		Optional *documentDimensions `json:"optional,omitempty"`
		Label    string              `json:"label,omitempty"`
	}
	type row struct {
		ID  string `json:"id,omitempty" db:"id"`
		Doc nested `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"nested partial", `{"doc":{"dims":{"w":9}}}`, true},
		{"nullable partial", `{"doc":{"optional":{"w":9}}}`, false},
		{"nullable complete", `{"doc":{"optional":{"w":9,"h":0}}}`, true},
		{"omitted deletion", `{"doc":{"optional":null,"label":null}}`, true},
		{"omitted zero", `{"doc":{"label":""}}`, false},
		{"unknown nested", `{"doc":{"dims":{"other":9}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.ParseMergePatch([]byte(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

type customDocument struct {
	W int `json:"w"`
}

func (c *customDocument) UnmarshalJSON(data []byte) error {
	type plain customDocument
	return json.Unmarshal(data, (*plain)(c))
}

func TestTypedDocumentMergeFailsClosedForCustom(t *testing.T) {
	schema := newSchema("", 0, map[string]*patch.PathSchema{
		"/doc": {GoType: reflect.TypeFor[customDocument](), AllowedOps: []patch.PatchOp{patch.PatchOpReplace}},
	})
	_, err := patch.ParseMergePatch([]byte(`{"doc":{"w":9}}`), schema, nil)
	if err == nil {
		t.Error("accepted a merge into a custom unmarshaler")
	}
	p := patch.NewParser(schema, nil)
	_, err = p.Parse([]byte(`[{"op":"replace","path":"/doc","value":{"w":9}}]`))
	if err != nil {
		t.Errorf("replacement: %v", err)
	}
}

// An object merge names an embedded struct's members as encoding/json
// writes them: promoted, beside the embedding struct's own.
func TestTypedDocumentMergeNamesEmbeddedMembers(t *testing.T) {
	type Embedded struct {
		W int `json:"w"`
	}
	type embedded struct {
		Embedded
		H int `json:"h"`
	}
	schema := newSchema("", 0, map[string]*patch.PathSchema{
		"/doc": {GoType: reflect.TypeFor[embedded](), AllowedOps: []patch.PatchOp{patch.PatchOpReplace}},
	})
	for body, valid := range map[string]bool{
		`{"doc":{"w":9}}`:         true,
		`{"doc":{"w":9,"h":1}}`:   true,
		`{"doc":{"Embedded":{}}}`: false,
	} {
		_, err := patch.ParseMergePatch([]byte(body), schema, nil)
		if (err == nil) != valid {
			t.Errorf("%s: valid=%v, error=%v", body, valid, err)
		}
	}
}

func TestTypedDocumentTestKeepsSuppliedShape(t *testing.T) {
	type row struct {
		ID   string             `json:"id,omitempty" db:"id"`
		Dims documentDimensions `json:"dims" db:"dims" parse:"patch" patch_ops:"replace,test"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParsePatch([]byte(`[{"op":"test","path":"/dims","value":{"w":9}}]`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.ApplyValidatedPatch(parsed, row{Dims: documentDimensions{W: 9}})
	if err == nil {
		t.Fatal("test accepted a document with a missing member")
	}
}

func TestTypedDocumentMergeHonorsJSONStringTag(t *testing.T) {
	type document struct {
		N int `json:"n,string"`
		H int `json:"h"`
	}
	type row struct {
		ID  string   `json:"id,omitempty" db:"id"`
		Doc document `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParseMergePatch([]byte(`{"doc":{"n":"9"}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, row{Doc: document{H: 7}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Doc != (document{N: 9, H: 7}) {
		t.Fatalf("got %#v", got)
	}
	value, ok := parsed.Operations[0].Value.(map[string]any)
	if !ok {
		t.Fatalf("merge value = %T, want map[string]any", parsed.Operations[0].Value)
	}
	v := value["n"]
	if v != "9" {
		t.Fatalf("wire value = %#v", v)
	}
}

func TestTypedDocumentMergeRejectsPotentiallyOmittedMap(t *testing.T) {
	type document struct {
		Labels map[string]string `json:"labels,omitempty"`
	}
	type row struct {
		ID  string   `json:"id,omitempty" db:"id"`
		Doc document `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	if err != nil {
		t.Fatal(err)
	}
	_, parseErr := p.ParseMergePatch([]byte(`{"doc":{"labels":{"a":null}}}`))
	if parseErr == nil {
		t.Fatal("accepted merge that can empty and omit the labels map")
	}
	parsed, err := p.ParseMergePatch([]byte(`{"doc":{"labels":{"a":null,"b":"new"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, row{Doc: document{Labels: map[string]string{"a": "old"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Doc.Labels, map[string]string{"b": "new"}) {
		t.Fatalf("got %#v", got)
	}
}

func TestTypedDocumentRawMessageMerge(t *testing.T) {
	type row struct {
		ID  string          `json:"id,omitempty" db:"id"`
		Doc json.RawMessage `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row](onSQLite, "rows")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := p.ParseMergePatch([]byte(`{"doc":{"w":9}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.ApplyValidatedPatch(parsed, row{Doc: json.RawMessage(`{"w":2,"h":7}`)})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]int
	err = json.Unmarshal(got.Doc, &doc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc, map[string]int{"w": 9, "h": 7}) {
		t.Fatalf("got %s", got.Doc)
	}
}

// A merge patch into a map keyed by an integer or by a type with text
// methods applies as encoding/json decodes the members over the map.
func TestMergePatch_MapKeysDecodeAsEncodingJSON(t *testing.T) {
	r := parse.MustResource[parsetest.KeyedMaps](onSQLite, "keyed_maps")
	for _, tc := range parsetest.KeyedMapMerges() {
		t.Run(tc.Name, func(t *testing.T) {
			parsed, err := r.ParseMergePatch([]byte(tc.Body))
			require.NoError(t, err)
			got, err := r.ApplyValidatedPatch(parsed, parsetest.KeyedMapsRow())
			require.NoError(t, err)
			assert.Equal(t, parsetest.MergedByEncodingJSON(t, parsetest.KeyedMapsRow(), tc.Body), got)
		})
	}
}

func TestMergePatch_MapKeysRefused(t *testing.T) {
	r := parse.MustResource[parsetest.KeyedMaps](onSQLite, "keyed_maps")
	for _, tc := range keyedMapRefusals() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ParseMergePatch([]byte(tc.body))
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
		})
	}
}

// keyedMapRefusals name a member no key of the map's type decodes from, or
// two members that decode to one key.
func keyedMapRefusals() []struct{ name, body string } {
	cases := []struct{ name, body string }{
		{"not an integer", `{"by_int":{"x":1}}`},
		{"an integer in exponent form", `{"by_int":{"1e2":1}}`},
		{"past the unsigned range", `{"by_uint":{"256":"x"}}`},
		{"a negative unsigned key", `{"by_uint":{"-1":"x"}}`},
		{"not an address", `{"by_addr":{"host.example":1}}`},
		{"two spellings of one integer key", `{"by_int":{"01":1,"1":2}}`},
		{"two spellings of one address", `{"by_addr":{"::1":1,"0:0:0:0:0:0:0:1":2}}`},
	}
	return cases
}

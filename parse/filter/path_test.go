//go:build unit

package filter_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

type pathDims struct {
	W float64 `json:"w" parse:"filter" indexed:"true"`
}

type pathDoc struct {
	Mode string    `json:"mode" parse:"filter"`
	Dims *pathDims `json:"dims" parse:"filter"`
	Skip string    `json:"skip"`
}

type pathItem struct {
	ID    string          `json:"id"    db:"id"    parse:"filter"`
	Attrs json.RawMessage `json:"attrs" db:"attrs" parse:"filter" paths:"color:string;size:int:filter+indexed;a-b.c_d:uuid" storage:"json"`
	Doc   *pathDoc        `json:"doc"   db:"doc"   parse:"filter"`
}

func TestPaths_Declared(t *testing.T) {
	s, err := filter.GenerateFilterSchema[pathItem](core.NewSchemaGenerator())
	require.NoError(t, err)

	for name, want := range map[string]struct {
		typ     core.FieldType
		column  string
		segs    []string
		storage filter.JSONStorage
		indexed bool
	}{
		"attrs.color":   {core.FieldTypeString, "path_items.attrs", []string{"color"}, filter.StorageJSON, false},
		"attrs.size":    {core.FieldTypeInt, "path_items.attrs", []string{"size"}, filter.StorageJSON, true},
		"attrs.a-b.c_d": {core.FieldTypeUUID, "path_items.attrs", []string{"a-b", "c_d"}, filter.StorageJSON, false},
		"doc.mode":      {core.FieldTypeString, "path_items.doc", []string{"mode"}, filter.StorageJSONB, false},
		"doc.dims.w":    {core.FieldTypeFloat, "path_items.doc", []string{"dims", "w"}, filter.StorageJSONB, true},
	} {
		f := s.Fields[name]
		require.NotNil(t, f, name)
		require.NotNil(t, f.Path, name)
		assert.Empty(t, f.SQLExpr, "%s: a path's value comes from the dialect", name)
		assert.Equal(t, want.typ, f.DataType, name)
		assert.Equal(t, want.column, f.Path.Column, name)
		assert.Equal(t, want.segs, f.Path.Segments, name)
		assert.Equal(t, want.storage, f.Path.Storage, name)
		assert.Equal(t, want.indexed, f.Indexed, name)
		assert.True(t, f.AllowsOperator(filter.OpNull), "%s: a path can be absent", name)
	}
	assert.NotContains(t, s.Fields, "doc.skip", "an untagged member is not a path")
	assert.NotContains(t, s.Fields, "doc.dims", "a nested struct is a step, not a path")
	assert.Equal(t, core.FieldTypeJSON, s.Fields["attrs"].DataType, "the document stays filterable as a whole")
}

func TestPaths_Refused(t *testing.T) {
	type badSegment struct {
		A json.RawMessage `json:"a" db:"a" parse:"filter" paths:"x.y z:string"`
	}
	type badType struct {
		A json.RawMessage `json:"a" db:"a" parse:"filter" paths:"x:object"`
	}
	type badCap struct {
		A json.RawMessage `json:"a" db:"a" parse:"filter" paths:"x:string:sortable"`
	}
	type notDocument struct {
		A string `json:"a" db:"a" parse:"filter" paths:"x:string"`
	}
	type badStorage struct {
		A json.RawMessage `json:"a" db:"a" parse:"filter" storage:"xml"`
	}
	type scalarWithPaths struct {
		A struct{ V string } `json:"a" db:"a" parse:"filter" storage:"scalar:float" paths:"v:string"`
	}
	type tooDeep struct {
		A json.RawMessage `json:"a" db:"a" parse:"filter" paths:"a.b.c.d.e.f.g.h.i:string"`
	}
	type collides struct {
		A  json.RawMessage `json:"a"   db:"a"  parse:"filter" paths:"x:string"`
		AX string          `json:"a.x" db:"ax" parse:"filter"`
	}
	type arrayMember struct {
		A *struct {
			L []string `json:"l" parse:"filter"`
		} `json:"a" db:"a" parse:"filter"`
	}
	g := core.NewSchemaGenerator()
	for name, gen := range map[string]func() error{
		"segment":      func() error { _, err := filter.GenerateFilterSchema[badSegment](g); return err },
		"type":         func() error { _, err := filter.GenerateFilterSchema[badType](g); return err },
		"capability":   func() error { _, err := filter.GenerateFilterSchema[badCap](g); return err },
		"not document": func() error { _, err := filter.GenerateFilterSchema[notDocument](g); return err },
		"storage":      func() error { _, err := filter.GenerateFilterSchema[badStorage](g); return err },
		"scalar paths": func() error { _, err := filter.GenerateFilterSchema[scalarWithPaths](g); return err },
		"depth":        func() error { _, err := filter.GenerateFilterSchema[tooDeep](g); return err },
		"collision":    func() error { _, err := filter.GenerateFilterSchema[collides](g); return err },
		"array member": func() error { _, err := filter.GenerateFilterSchema[arrayMember](g); return err },
	} {
		assert.Error(t, gen(), name)
	}
}

// A struct a database holds as a scalar (a decimal) declares its type and
// filters as that scalar.
func TestPaths_ScalarStorage(t *testing.T) {
	type decimal struct{ Units, Scale int64 }
	type item struct {
		Price decimal `json:"price" db:"price" parse:"filter" storage:"scalar:float"`
	}
	s, err := filter.GenerateFilterSchema[item](core.NewSchemaGenerator())
	require.NoError(t, err)
	f := s.Fields["price"]
	assert.Equal(t, core.FieldTypeFloat, f.DataType)
	assert.Nil(t, f.Path)
	assert.True(t, f.AllowsOperator(filter.OpGt))
}

func TestPaths_WithPathsIsARequestView(t *testing.T) {
	base, err := filter.GenerateFilterSchema[pathItem](core.NewSchemaGenerator())
	require.NoError(t, err)

	view, err := base.WithPaths(filter.PathDecl{Path: "attrs.tier", Type: core.FieldTypeString}, filter.PathDecl{Path: "doc.extra.n", Type: core.FieldTypeInt})
	require.NoError(t, err)
	assert.Contains(t, view.Fields, "attrs.tier")
	assert.Contains(t, view.Fields, "doc.extra.n")
	assert.NotContains(t, base.Fields, "attrs.tier", "the shared schema is unchanged")
	assert.Equal(t, filter.StorageJSON, view.Fields["attrs.tier"].Path.Storage, "a request path takes its document's storage")

	ast, err := filter.NewFilterParser(view, nil).Parse(`{"attrs.tier":"gold"}`)
	require.NoError(t, err)
	res, err := filter.NewSQLBuilder(view, filter.PostgresDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Equal(t, `(CASE WHEN jsonb_typeof((path_items.attrs)::jsonb #> '{tier}') = 'string' THEN ((path_items.attrs)::jsonb #>> '{tier}') END) = $1`, res.Where)
	assert.Equal(t, []any{"gold"}, res.Args)

	for name, d := range map[string]filter.PathDecl{
		"not a document": {Path: "id.x", Type: core.FieldTypeString},
		"unknown field":  {Path: "nope.x", Type: core.FieldTypeString},
		"no member":      {Path: "attrs", Type: core.FieldTypeString},
		"duplicate":      {Path: "attrs.color", Type: core.FieldTypeString},
		"bad type":       {Path: "attrs.x", Type: core.FieldTypeArray},
	} {
		_, err := base.WithPaths(d)
		assert.Error(t, err, name)
	}
	if _, err := base.WithPaths(filter.PathDecl{Path: "attrs.ok", Type: core.FieldTypeString}, filter.PathDecl{Path: "attrs.ok", Type: core.FieldTypeString}); err == nil {
		t.Error("a request declaring one path twice must fail")
	}
}

func TestPaths_SQLiteRendering(t *testing.T) {
	s, err := filter.GenerateFilterSchema[pathItem](core.NewSchemaGenerator())
	require.NoError(t, err)
	got := filter.FieldExpr(filter.SQLiteDialect{}, s.Fields["attrs.a-b.c_d"])
	assert.True(t, strings.HasPrefix(got, `(CASE WHEN json_type(path_items.attrs, '$."a-b"."c_d"') = 'text' AND lower(json_extract(path_items.attrs, '$."a-b"."c_d"')) GLOB '`), got)
}

func TestPaths_SortKeys(t *testing.T) {
	s, err := filter.GenerateSortSchema[filtertest.PathRecord](core.NewSchemaGenerator())
	require.NoError(t, err)
	for name, column := range map[string]string{"attrs.size": "attrs", "attrs.seen": "attrs", "attrs.color": "attrs", "settings.level": "settings"} {
		k := s.Fields[name]
		require.NotNil(t, k, name)
		require.NotNil(t, k.Path, name)
		assert.Equal(t, column, k.Column, "%s: a cursor reads the document's row column", name)
		assert.True(t, k.Nullable, "%s: an absent member sorts as NULL", name)
	}
	assert.NotContains(t, s.Fields, "attrs.ratio", "a path without the sort capability is not a sort key")
	assert.NotContains(t, s.Fields, "settings.mode", "a member not tagged sort is not a sort key")

	sr, err := filter.ParseSort(s, "-attrs.size")
	require.NoError(t, err)
	assert.Equal(t, `ORDER BY (CASE WHEN jsonb_typeof(path_records.attrs #> '{size}') = 'number' THEN (path_records.attrs #>> '{size}')::numeric END) DESC NULLS FIRST`, sr.ToSQL(filter.PostgresDialect{}))
}

func TestPaths_SortWithPaths(t *testing.T) {
	g := core.NewSchemaGenerator()
	fields, err := filter.GenerateFilterSchema[pathItem](g)
	require.NoError(t, err)
	sorts, err := filter.GenerateSortSchema[pathItem](g)
	require.NoError(t, err)

	view, err := sorts.WithPaths(fields,
		filter.PathDecl{Path: "attrs.tier", Type: core.FieldTypeString, Sort: true},
		filter.PathDecl{Path: "attrs.unsorted", Type: core.FieldTypeString})
	require.NoError(t, err)
	require.Contains(t, view.Fields, "attrs.tier")
	assert.Equal(t, filter.StorageJSON, view.Fields["attrs.tier"].Path.Storage)
	assert.Equal(t, "attrs", view.Fields["attrs.tier"].Column)
	assert.NotContains(t, view.Fields, "attrs.unsorted")
	assert.NotContains(t, sorts.Fields, "attrs.tier", "the shared schema is unchanged")

	_, err = sorts.WithPaths(fields, filter.PathDecl{Path: "id.x", Type: core.FieldTypeString, Sort: true})
	assert.Error(t, err)
}

// An int path may hold a decimal member, so its cursor carries either kind.
func TestPaths_NumericCursorValues(t *testing.T) {
	s, err := filter.GenerateSortSchema[filtertest.PathRecord](core.NewSchemaGenerator())
	require.NoError(t, err)
	sr, err := filter.ParseSort(s, "attrs.size")
	require.NoError(t, err)
	keys := filter.Keyset(sr, filter.CursorKey{Name: "id", SQLExpr: "path_records.id", Column: "id", DataType: core.FieldTypeString})
	for _, v := range []any{int64(3), 3.5, nil} {
		tok := &filter.PageToken{Fields: []filter.CursorField{{Name: "attrs.size", Value: v, Direction: filter.SortAsc}, {Name: "id", Value: "a", Direction: filter.SortAsc}}}
		assert.NoError(t, filter.ValidateCursor(tok, keys), "%v", v)
	}
	tok := &filter.PageToken{Fields: []filter.CursorField{{Name: "attrs.size", Value: "3", Direction: filter.SortAsc}, {Name: "id", Value: "a", Direction: filter.SortAsc}}}
	assert.Error(t, filter.ValidateCursor(tok, keys))

	got, err := filter.CursorPlan{Keys: keys}.Token(false, func(col string) (any, error) {
		return map[string]any{"attrs": json.RawMessage(`{"size":3.5}`), "id": "g"}[col], nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3.5, got.Fields[0].Value, "a cursor reads the member with the path rule")
}

func TestPaths_Facets(t *testing.T) {
	s, err := filtertest.PathSchema()
	require.NoError(t, err)
	color := s.Fields["attrs.color"]
	require.True(t, color.Facetable)
	assert.Equal(t, filter.FacetTerms, color.FacetSchema.Type)
	assert.Empty(t, color.FacetSchema.SQLExpr, "a path facet renders per dialect")
	level := s.Fields["settings.level"]
	require.True(t, level.Facetable)
	assert.Equal(t, filter.FacetTypeRange, level.FacetSchema.Type)
	assert.False(t, s.Fields["attrs.size"].Facetable)

	view, err := s.WithPaths(filter.PathDecl{Path: "attrs.tier", Type: core.FieldTypeString, Facet: &filter.FacetSchema{Type: filter.FacetTerms, Limit: 5}})
	require.NoError(t, err)
	tier := view.Fields["attrs.tier"]
	require.True(t, tier.Facetable)
	assert.Equal(t, "attrs.tier", tier.FacetSchema.Field)
	assert.Equal(t, 5, tier.FacetSchema.Limit)

	q, err := filter.BuildFacetQuery(view, filter.PostgresDialect{}, "1=1", nil, []filter.FacetRequest{{Field: "attrs.tier"}})
	require.NoError(t, err)
	assert.Contains(t, q.SQL, `(CASE WHEN jsonb_typeof(path_records.attrs #> '{tier}') = 'string' THEN (path_records.attrs #>> '{tier}') END) AS`)
}

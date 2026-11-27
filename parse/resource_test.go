//go:build unit

package parse_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

type widget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort,facet,search" facet:"terms:10"`
	Rank      int64      `json:"rank"       db:"rank"       parse:"filter,sort"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,include" relation:"widget_tags,widget_id,tag_id,id,tenant_id" indexed:"true"`
}

// atOneInstant pins a resource's clock: a list request holds the instant
// it was parsed at, so two parses of one request are equal only at one.
func atOneInstant() parse.ResourceOption {
	return parse.ParserOptions(core.WithClock(func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }))
}

// widgets is the widget resource, compiling for SQLite without a database.
func widgets(t *testing.T) *parse.Resource[widget] {
	t.Helper()
	return widgetsOn(t, onSQLite)
}

func widgetsOn(t *testing.T, p *parse.Provider) *parse.Resource[widget] {
	t.Helper()
	r, err := parse.NewResource[widget](p, "widgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.PageSize(2, 3), atOneInstant())
	require.NoError(t, err)
	return r
}

func TestNewResource_RefusesBadDeclarations(t *testing.T) {
	for name, build := range map[string]func() error{
		"table": func() error { _, err := parse.NewResource[widget](onSQLite, "widgets; DROP"); return err },
		"key column": func() error {
			_, err := parse.NewResource[widget](onSQLite, "widgets", parse.PrimaryKey("uuid"))
			return err
		},
		"tenant column": func() error {
			_, err := parse.NewResource[widget](onSQLite, "widgets", parse.TenantColumn("org_id"))
			return err
		},
		"deleted column": func() error {
			_, err := parse.NewResource[widget](onSQLite, "widgets", parse.DeletedColumn("removed_at"))
			return err
		},
		"zero page": func() error {
			_, err := parse.NewResource[widget](onSQLite, "widgets", parse.PageSize(0, 10))
			return err
		},
		"max below def": func() error {
			_, err := parse.NewResource[widget](onSQLite, "widgets", parse.PageSize(20, 10))
			return err
		},
		"relation column": func() error {
			_, err := parse.NewResource[widget](onSQLite, "widgets", parse.PrimaryKey("tags"))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { assert.Error(t, build()) })
	}
	assert.Panics(t, func() { parse.MustResource[widget](onSQLite, "widgets", parse.PrimaryKey("nope")) })
}

type hiddenStampA struct {
	Stamp string `json:"-"`
}

type hiddenStampB struct {
	Stamp string `json:"-"`
}

// Fields the field-name tag hides keep their columns, so two of them on one
// column are refused as any two fields are, at one depth or two.
func TestNewResource_RefusesHiddenFieldsSharingAColumn(t *testing.T) {
	type oneDepth struct {
		hiddenStampA
		hiddenStampB
		ID string `json:"id" db:"id"`
	}
	type twoDepths struct {
		hiddenStampA
		ID    string `json:"id" db:"id"`
		Stamp string `json:"-"`
	}
	_, err := parse.NewResource[oneDepth](onSQLite, "stamps")
	require.ErrorContains(t, err, `fields Stamp and Stamp are both column "stamp"`)
	_, err = parse.NewResource[twoDepths](onSQLite, "stamps")
	require.ErrorContains(t, err, `fields Stamp and Stamp are both column "stamp"`)
}

func TestNewResource_RefusesMisspeltTagKeys(t *testing.T) {
	type row struct {
		ID   string `json:"id" db:"id"`
		Name string `json:"name" db:"name" parse:"filter" filter_op:"eq"`
	}
	_, err := parse.NewResource[row](onSQLite, "rows")
	require.ErrorContains(t, err, `field Name of parse_test.row has tag key "filter_op"; did you mean "filter_ops"?`)
	_, err = parse.NewResource[row](onSQLite, "rows", parse.ParserOptions(core.WithGeneratorOptions(core.WithForeignTagKeys("filter_op"))))
	require.NoError(t, err)
}

func TestResource_DeclaresItsIdentity(t *testing.T) {
	r := widgets(t)
	assert.Equal(t, "widgets", r.Table())
	assert.Equal(t, []string{"id"}, r.PrimaryKeyColumns())
	assert.Equal(t, "tenant_id", r.TenantColumnName())
	assert.Equal(t, "deleted_at", r.DeletedColumnName())
	assert.Equal(t, []string{`"widgets"."id"`, `"widgets"."tenant_id"`, `"widgets"."name"`, `"widgets"."rank"`, `"widgets"."deleted_at"`}, r.Columns())
	for _, item := range []any{widget{ID: "w9"}, &widget{ID: "w9"}} {
		key, err := r.Key(item)
		require.NoError(t, err)
		assert.Equal(t, parse.Key{"w9"}, key)
	}
	sys := r.FilterSchema().SystemFields
	assert.Equal(t, core.FieldTypeTimestamp, sys["deleted_at"].DataType, "system fields are typed from T")
	assert.Equal(t, `"widgets"."tenant_id"`, filter.FieldExpr(filter.PostgresDialect{}, sys["tenant_id"]))
}

// A plan carries the table it was built for: another resource's keyset and
// includes name columns this one does not have.
func TestResource_RefusesAnotherResourcesPlan(t *testing.T) {
	r := widgets(t)
	other, err := parse.NewResource[widget](onSQLite, "other_widgets", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	plan, err := other.BuildList(nil, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)

	_, err = r.Page(nil, plan)
	require.ErrorContains(t, err, `list plan built for "other_widgets"`)
	_, err = r.PagePointers(nil, plan)
	require.Error(t, err)
	_, err = r.IncludeQueries(plan, []parse.Key{{"w1"}})
	require.Error(t, err)
	_, err = r.Page(nil, nil)
	require.ErrorContains(t, err, "nil list plan")
}

// parenthesizedColumns maps every field to a computed expression, so a
// field's SQL names no column the way a plain identifier does.
type parenthesizedColumns struct{}

func (parenthesizedColumns) MapColumn(table, column string, _ core.FieldType) string {
	return "(" + table + "." + column + ")"
}

// mappedColumns maps every field through parenthesizedColumns.
func mappedColumns() parse.ResourceOption {
	opt := parse.ParserOptions(core.WithGeneratorOptions(core.WithColumnMapper(parenthesizedColumns{})))
	return opt
}

type team struct {
	ID string `json:"id" db:"id" parse:"filter,sort"`
}

type TestDevice struct {
	ID     string `json:"id"     parse:"filter"       filter_ops:"eq,in" indexed:"true"`
	Name   string `json:"name"   parse:"filter,patch" filter_ops:"eq,like" patch_ops:"replace"`
	Status string `json:"status" parse:"filter,patch" filter_ops:"eq,ne" patch_ops:"replace" enum:"active,offline"`
}

func TestResource_ParsingSchemas(t *testing.T) {
	tp, err := parse.NewResource[TestDevice](onPostgres, "test_devices")
	if err != nil {
		t.Fatal(err)
	}
	if tp.FilterSchema() == nil {
		t.Error("expected filter schema")
	}
	if tp.PatchSchema() == nil {
		t.Error("expected patch schema")
	}
	if tp.SortSchema() == nil {
		t.Error("expected sort schema")
	}
}

func TestResource_Conventions(t *testing.T) {
	type Item struct {
		ID   string `bson:"id"   parse:"filter" filter_ops:"eq" sql:"item_id"`
		Name string `bson:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace" sql:"item_name"`
	}

	tp, err := parse.NewResource[Item](onPostgres, "item", parse.PrimaryKey("item_id"), parse.ParserOptions(core.WithGeneratorOptions(
		core.WithFieldNameTag("bson"),
		core.WithColumnTag("sql"),
	)))
	if err != nil {
		t.Fatal(err)
	}

	if tp.FilterSchema().Name != "item" {
		t.Errorf("expected table 'item', got %q", tp.FilterSchema().Name)
	}
	id := tp.FilterSchema().Fields["id"]
	if id.Table != "item" || id.Column != "item_id" || id.Expr != "" {
		t.Errorf("expected item.item_id, got %q.%q (expr %q)", id.Table, id.Column, id.Expr)
	}
	if pathOf(tp.PatchSchema(), "/name").SQLColumn != "item_name" {
		t.Errorf("expected item_name, got %q", pathOf(tp.PatchSchema(), "/name").SQLColumn)
	}
}

// A server type that embeds its wire type is the same resource as the type
// written flat.

type deviceFlat struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch,search"`
	Rank     int64  `json:"rank"      db:"rank"      parse:"filter,sort,patch"`
	Hash     string `json:"-"         db:"hash"      parse:"server"`
}

type DeviceWire struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch,search"`
	Rank     int64  `json:"rank"      db:"rank"      parse:"filter,sort,patch"`
}

type deviceEmbedded struct {
	DeviceWire
	Hash string `json:"-" db:"hash" parse:"server"`
}

type DeviceMiddle struct{ DeviceWire }

type deviceTwoDeep struct {
	DeviceMiddle
	Hash string `json:"-" db:"hash" parse:"server"`
}

type devicePointer struct {
	*DeviceWire
	Hash string `json:"-" db:"hash" parse:"server"`
}

type deviceWire struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch,search"`
	Rank     int64  `json:"rank"      db:"rank"      parse:"filter,sort,patch"`
}

type deviceUnexported struct {
	deviceWire
	Hash string `json:"-" db:"hash" parse:"server"`
}

// resourceView is everything a resource derives from its type.
type resourceView struct {
	Columns    []string
	Filter     map[string]string // name -> SQL expression and operators
	Sort       map[string]string
	Search     []string
	Patch      map[string]string
	Server     map[string]string
	EndCursor  string
	Key        any
	Patched    map[string]any // the patched row's fields, by column
	FilterSQL  string
	FilterArgs []any
}

func viewOf[T any](t *testing.T, row func(id, name string, rank int64, hash string) T) resourceView {
	t.Helper()
	r, err := parse.NewResource[T](onPostgres, "devices", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	columns := r.Columns()
	v := resourceView{Columns: columns, Filter: map[string]string{}, Sort: map[string]string{}, Patch: map[string]string{}, Server: map[string]string{}}
	for name, f := range r.FilterSchema().Fields {
		v.Filter[name] = filter.FieldExpr(filter.PostgresDialect{}, f) + " " + string(f.DataType)
	}
	for name, f := range r.SortSchema().Fields {
		v.Sort[name] = f.Table + " " + f.Column + " " + f.Expr
	}
	for _, f := range r.SearchSchema().Fields {
		v.Search = append(v.Search, f.Name+" "+f.Table+" "+f.Column+" "+f.Expr)
	}
	for path, p := range r.PatchSchema().Paths() {
		v.Patch[path] = p.SQLColumn
	}
	for path, p := range r.ServerPatchSchema().Paths() {
		v.Server[path] = p.SQLColumn
	}

	req, err := r.ParseList(url.Values{"name": {"pump"}, "sort": {"-rank,name"}, "page_size": {"2"}})
	require.NoError(t, err, "a promoted field is filterable and sortable")
	plan, err := r.BuildList(req, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	v.FilterSQL, v.FilterArgs = plan.Page.SQL, plan.Page.Args
	rows := []T{row("a", "pump", 3, "x"), row("b", "pump", 2, "y"), row("c", "pump", 1, "z")}
	page, err := r.Page(rows, plan)
	require.NoError(t, err)
	v.EndCursor = string(page.PageInfo.EndCursor)
	v.Key, err = r.Key(rows[0])
	require.NoError(t, err)

	patch, err := r.ParseMergePatch([]byte(`{"name":"valve","rank":9}`))
	require.NoError(t, err)
	patched, err := r.ApplyValidatedPatch(patch, rows[0])
	require.NoError(t, err)
	server, err := r.ParseServerMergePatch([]byte(`{"hash":"rotated"}`))
	require.NoError(t, err)
	patched, err = r.ApplyValidatedPatch(server, patched)
	require.NoError(t, err)
	v.Patched = map[string]any{}
	for _, col := range []string{"id", "name", "rank", "hash"} {
		got, err := filter.CursorValueByTag(patched, "db", col)
		require.NoError(t, err)
		v.Patched[col] = got
	}
	slices.Sort(v.Search)
	return v
}

func TestEmbeddedWireType_IsTheFlatResource(t *testing.T) {
	want := viewOf(t, func(id, name string, rank int64, hash string) deviceFlat {
		row := deviceFlat{ID: id, TenantID: "t1", Name: name, Rank: rank, Hash: hash}
		return row
	})
	assert.NotEmpty(t, want.Filter["name"])
	for name, got := range map[string]resourceView{
		"depth 1": viewOf(t, func(id, name string, rank int64, hash string) deviceEmbedded {
			row := deviceEmbedded{DeviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}, hash}
			return row
		}),
		"depth 2": viewOf(t, func(id, name string, rank int64, hash string) deviceTwoDeep {
			row := deviceTwoDeep{DeviceMiddle{DeviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}}, hash}
			return row
		}),
		"pointer embed": viewOf(t, func(id, name string, rank int64, hash string) devicePointer {
			row := devicePointer{&DeviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}, hash}
			return row
		}),
		"unexported embedded type": viewOf(t, func(id, name string, rank int64, hash string) deviceUnexported {
			row := deviceUnexported{deviceWire{ID: id, TenantID: "t1", Name: name, Rank: rank}, hash}
			return row
		}),
	} {
		t.Run(name, func(t *testing.T) { assert.Equal(t, want, got) })
	}
}

// A patch through a nil embedded pointer allocates it, as encoding/json does
// when decoding; the caller's value is untouched.
func TestEmbeddedWireType_PatchAllocatesANilEmbed(t *testing.T) {
	r, err := parse.NewResource[devicePointer](onPostgres, "devices")
	require.NoError(t, err)
	patch, err := r.ParseMergePatch([]byte(`{"name":"valve"}`))
	require.NoError(t, err)
	original := devicePointer{Hash: "h"}
	got, err := r.ApplyValidatedPatch(patch, original)
	require.NoError(t, err)
	require.NotNil(t, got.DeviceWire)
	assert.Equal(t, "valve", got.Name)
	assert.Nil(t, original.DeviceWire)
	_, err = r.Key(original)
	require.ErrorContains(t, err, "nil pointer", "a key under a nil embed is not there")
	assert.Equal(t, reflect.TypeFor[devicePointer](), reflect.TypeOf(got))
}

type enumSettings struct {
	Mode  string   `json:"mode"  parse:"filter" enum:"a,b"`
	Kinds []string `json:"kinds" parse:"filter" enum:"x,y"`
}

type enumAccount struct {
	ID       string       `json:"id"       db:"id"       parse:"filter,sort"`
	Terms    string       `json:"terms"    db:"terms"    parse:"filter" enum:"'net 30, eom',' padded',due"`
	Settings enumSettings `json:"settings" db:"settings" parse:"filter"`
}

// A value holding a comma or edge spaces is declared in quotes and
// round-trips through the tag, request validation and the schema document;
// a struct member's enum holds on its path as a column's does.
func TestEnum_ValuesRoundTrip(t *testing.T) {
	accounts := parse.MustResource[enumAccount](onSQLite, "enum_accounts")
	for _, ok := range []string{`{"terms":"net 30, eom"}`, `{"terms":" padded"}`, `{"terms":"due"}`, `{"settings.mode":"a"}`, `{"settings.kinds":{"$contains":["y"]}}`} {
		_, err := accounts.ParseFilter(ok)
		require.NoError(t, err, ok)
	}
	for _, bad := range []string{`{"terms":"net 30"}`, `{"terms":"padded"}`, `{"settings.mode":"zzz"}`, `{"settings.kinds":{"$contains":["z"]}}`} {
		_, err := accounts.ParseFilter(bad)
		pe, ok := errors.AsType[*parse.ParseError](err)
		require.True(t, ok, "%s: %v", bad, err)
		assert.Equal(t, filter.ECodeInvalidEnumValue.String(), pe.Code, bad)
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, accounts))
	doc := reg.SchemaFor("enum_accounts")
	require.NotNil(t, doc)
	assert.Equal(t, []string{"net 30, eom", " padded", "due"}, doc.Filter.Fields["terms"].EnumValues)
	assert.Equal(t, []string{"a", "b"}, doc.Filter.Fields["settings.mode"].EnumValues)
}

// An enum is text: on a field whose values are not strings it would never
// be checked, so generation fails, naming the field.
func TestEnum_OnlyStringsAndArraysOfStrings(t *testing.T) {
	type intEnum struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Rank int    `json:"rank" db:"rank" parse:"filter" enum:"1,2"`
	}
	type unfilteredIntEnum struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Rank int    `json:"rank" db:"rank" enum:"1,2"`
	}
	type intMember struct {
		Level int `json:"level" parse:"filter" enum:"1,2"`
	}
	type memberEnum struct {
		ID       string    `json:"id"       db:"id"       parse:"filter"`
		Settings intMember `json:"settings" db:"settings" parse:"filter"`
	}
	type badQuote struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Mode string `json:"mode" db:"mode" parse:"filter" enum:"'a"`
	}
	for name, tc := range map[string]struct {
		build func() error
		want  string
	}{
		"an int field":         {func() error { _, err := parse.NewResource[intEnum](onSQLite, "t"); return err }, "field Rank: enum on a int field"},
		"an unfiltered int":    {func() error { _, err := parse.NewResource[unfilteredIntEnum](onSQLite, "t"); return err }, "field Rank: enum on a int field"},
		"an int member":        {func() error { _, err := parse.NewResource[memberEnum](onSQLite, "t"); return err }, "field Settings: path level: enum on a int field"},
		"an unterminated tick": {func() error { _, err := parse.NewResource[badQuote](onSQLite, "t"); return err }, `field Mode: enum:"'a": value 1: unterminated quote`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorContains(t, tc.build(), tc.want)
		})
	}
}

type ownedDoc struct {
	Kind string `json:"kind"`
}

type ownedRow struct {
	ID   string   `json:"id"   db:"id"`
	Name string   `json:"name" db:"name" parse:"filter,patch" enum:"a,b"`
	Code string   `json:"code" db:"code" parse:"patch" maxlen:"2"`
	Doc  ownedDoc `json:"doc"  db:"doc"`
}

// describedWalk is what a walk tells its readers, read through the
// accessors.
type describedWalk struct {
	Names  []string
	Routes [][]int
	Enums  [][]string
	MaxLen []int
}

func describeWalk(walk *core.StructFields) describedWalk {
	var d describedWalk
	for f := range walk.Fields() {
		bounds := f.Bounds()
		maxLen := -1
		if bounds.MaxLen != nil {
			maxLen = *bounds.MaxLen
		}
		d.Names = append(d.Names, f.External())
		d.Routes = append(d.Routes, f.Route())
		d.Enums = append(d.Enums, f.Enum())
		d.MaxLen = append(d.MaxLen, maxLen)
	}
	return d
}

// scribble changes everything a walk hands its caller.
func scribble(walk *core.StructFields) {
	fields := slices.Collect(walk.Fields())
	for _, f := range fields {
		route := f.Route()
		route[0] = 99
		enum := f.Enum()
		for i := range enum {
			enum[i] = "z"
		}
		bounds := f.Bounds()
		if bounds.MaxLen != nil {
			*bounds.MaxLen = 99
		}
	}
	slices.Reverse(fields)
	clear(fields)
}

// The walks are shared by every generator, resource and stored document of
// a type; what one caller does with what a walk returns reaches none of them.
func TestFieldMetadata_CallerChangesReachNoOtherReader(t *testing.T) {
	rowType := reflect.TypeFor[ownedRow]()
	walk, err := core.StructFieldsOf(rowType, core.JSONTagKey, core.DefaultColumnTagKey)
	require.NoError(t, err)
	want := describeWalk(walk)
	require.Equal(t, describedWalk{
		Names:  []string{"id", "name", "code", "doc"},
		Routes: [][]int{{0}, {1}, {2}, {3}},
		Enums:  [][]string{nil, {"a", "b"}, nil, nil},
		MaxLen: []int{-1, -1, 2, -1},
	}, want)

	scribble(walk)
	generated, err := core.NewSchemaGenerator().StructFields(rowType)
	require.NoError(t, err)
	scribble(generated)
	docWalk, err := core.JSONFieldsOf(reflect.TypeFor[ownedDoc]())
	require.NoError(t, err)
	scribble(docWalk)

	t.Run("another generator", func(t *testing.T) {
		other, err := core.NewSchemaGenerator(core.WithSortMaxFields(1)).StructFields(rowType)
		require.NoError(t, err)
		assert.Equal(t, want, describeWalk(other))
		doc, err := core.JSONFieldsOf(reflect.TypeFor[*ownedDoc]())
		require.NoError(t, err)
		kind, ok := doc.External("kind")
		require.True(t, ok)
		assert.Equal(t, []int{0}, kind.Route())
	})

	r := parse.MustResource[ownedRow](onPostgres, "owned", parse.ColumnType("doc", core.SQLJSONB))
	t.Run("a later resource", func(t *testing.T) {
		_, err := r.ParseFilter(`{"name":"a"}`)
		require.NoError(t, err)
		_, err = r.ParseFilter(`{"name":"z"}`)
		require.Error(t, err, "the enum still refuses z")
		assert.Equal(t, []string{"a", "b"}, r.FilterSchema().Fields["name"].EnumValues)

		_, err = r.ParsePatch([]byte(`[{"op":"replace","path":"/code","value":"abc"}]`))
		require.Error(t, err, "maxlen 2 still refuses three characters")
		got, err := r.ApplyPatch([]byte(`[{"op":"replace","path":"/name","value":"b"},{"op":"replace","path":"/code","value":"xy"}]`), ownedRow{ID: "1", Name: "a"})
		require.NoError(t, err)
		assert.Equal(t, ownedRow{ID: "1", Name: "b", Code: "xy"}, got)
	})

	t.Run("stored-document validation", func(t *testing.T) {
		scanner, err := r.RowScanner()
		require.NoError(t, err)
		_, err = scanner.Scan(scanStrings(`{"Kind":"x"}`))
		require.ErrorContains(t, err, `JSON member "/Kind" is noncanonical`)
		row, err := scanner.Scan(scanStrings(`{"kind":"x"}`))
		require.NoError(t, err)
		assert.Equal(t, ownedRow{ID: "v", Name: "v", Code: "v", Doc: ownedDoc{Kind: "x"}}, row)
	})
}

// scanStrings is a driver scan that fills each string column with "v" and
// hands each scanner doc.
func scanStrings(doc string) func(dest ...any) error {
	return func(dest ...any) error {
		for _, d := range dest {
			scanner, ok := d.(sql.Scanner)
			if !ok {
				*d.(*string) = "v"
				continue
			}
			err := scanner.Scan([]byte(doc))
			if err != nil {
				return err
			}
		}
		return nil
	}
}

// scanOnly scans a NULL but has no Value: whatever a write stores for it,
// it is not the NULL a live row needs.
type scanOnly struct{ at time.Time }

func (s *scanOnly) Scan(src any) error {
	s.at, _ = src.(time.Time)
	return nil
}

// markedNull's zero value writes the zero time, and only a NULL scan marks
// it NULL: a row written from its zero value is deleted.
type markedNull struct {
	at   time.Time
	null bool
}

func (m *markedNull) Scan(src any) error {
	m.null = src == nil
	m.at, _ = src.(time.Time)
	return nil
}

func (m markedNull) Value() (driver.Value, error) {
	if m.null {
		return nil, nil
	}
	return m.at, nil
}

// Each declaration below compiles statements no row can be served by, or
// that move a row out of its tenant; NewResource refuses it, naming the
// column or field.
func TestNewResource_RefusesIdentityDeclarations(t *testing.T) {
	type patchedKey struct {
		ID   string `json:"id"   db:"id"   parse:"filter,patch"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	type patchedTenant struct {
		ID       string `json:"id"        db:"id"`
		TenantID string `json:"tenant_id" db:"tenant_id" parse:"filter,patch"`
	}
	type serverKey struct {
		ID   string `json:"id"   db:"id"   parse:"server"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	type softDeleted struct {
		ID        string          `json:"id"         db:"id"`
		TenantID  string          `json:"tenant_id"  db:"tenant_id"`
		Deleted   bool            `json:"deleted"    db:"deleted"`
		N         int             `json:"n"          db:"n"`
		DeletedAt *time.Time      `json:"deleted_at" db:"deleted_at"`
		Ref       googleuuid.UUID `json:"ref"        db:"ref"`
		Amount    decimal.Decimal `json:"amount"     db:"amount"`
		Scanned   scanOnly        `json:"scanned"    db:"scanned"`
		Marked    markedNull      `json:"marked"     db:"marked"`
	}
	type immutablePatch struct {
		ID   string `json:"id"   db:"id"`
		Slug string `json:"slug" db:"slug" parse:"filter,patch" immutable:"true"`
	}
	for _, tc := range []struct {
		name  string
		build func() error
		want  string
	}{
		{"a patched key", func() error { _, err := parse.NewResource[patchedKey](onPostgres, "docs"); return err },
			`parse: docs: patch path /id: writes the key column "id"`},
		{"a patched tenant column", func() error {
			_, err := parse.NewResource[patchedTenant](onPostgres, "docs", parse.TenantColumn("tenant_id"))
			return err
		}, `parse: docs: patch path /tenant_id: writes the tenant column "tenant_id"`},
		{"a server-patched key", func() error { _, err := parse.NewResource[serverKey](onPostgres, "docs"); return err },
			`parse: docs: patch path /id: writes the key column "id"`},
		{"the key as the deleted column", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("id"))
			return err
		}, `parse: odds: deleted column "id" is a key column`},
		{"the tenant column as the deleted column", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.TenantColumn("tenant_id"), parse.DeletedColumn("tenant_id"))
			return err
		}, `parse: odds: deleted column "tenant_id" is the tenant column`},
		{"a bool deleted column", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("deleted"))
			return err
		}, `parse: odds: deleted column "deleted" is bool, which cannot hold NULL`},
		{"an int deleted column", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("n"))
			return err
		}, `parse: odds: deleted column "n" is int, which cannot hold NULL`},
		// Each is an sql.Scanner that never holds NULL: the UUID scans NULL
		// as the nil UUID it writes back, and the decimal refuses it.
		{"a UUID deleted column", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("ref"))
			return err
		}, `parse: odds: deleted column "ref" is uuid.UUID, which cannot hold NULL`},
		{"a decimal deleted column", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("amount"))
			return err
		}, `parse: odds: deleted column "amount" is decimal.Decimal, which cannot hold NULL`},
		{"a Scanner that writes nothing", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("scanned"))
			return err
		}, `parse: odds: deleted column "scanned" is parse_test.scanOnly, which cannot hold NULL`},
		{"a Scanner whose zero value writes a value", func() error {
			_, err := parse.NewResource[softDeleted](onPostgres, "odds", parse.DeletedColumn("marked"))
			return err
		}, `parse: odds: deleted column "marked" is parse_test.markedNull, which cannot hold NULL`},
		{"an immutable patched field", func() error { _, err := parse.NewResource[immutablePatch](onPostgres, "imms"); return err },
			`field Slug: an immutable field cannot have the patch capability`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorContains(t, tc.build(), tc.want)
		})
	}
}

func TestNewResource_AcceptsWhatARowCanBeServedBy(t *testing.T) {
	type nullable struct {
		ID        string              `json:"id"         db:"id"`
		TenantID  string              `json:"tenant_id"  db:"tenant_id" parse:"filter"`
		At        *time.Time          `json:"at"         db:"at"`
		NullTime  sql.NullTime        `json:"null_time"  db:"null_time"`
		Generic   sql.Null[time.Time] `json:"generic"    db:"generic"`
		Slug      string              `json:"slug"       db:"slug" parse:"filter,server" immutable:"true"`
		DeletedAt *time.Time          `json:"deleted_at" db:"deleted_at" parse:"patch"`
	}
	for _, deleted := range []string{"at", "null_time", "generic"} {
		_, err := parse.NewResource[nullable](onPostgres, "rows", parse.TenantColumn("tenant_id"), parse.DeletedColumn(deleted))
		require.NoError(t, err, deleted)
	}

	type tenantKeyed struct {
		TenantID string `json:"tenant_id" db:"tenant_id" parse:"filter"`
		Name     string `json:"name"      db:"name"      parse:"patch"`
	}
	_, err := parse.NewResource[tenantKeyed](onPostgres, "tenants", parse.PrimaryKey("tenant_id"), parse.TenantColumn("tenant_id"))
	require.NoError(t, err, "a resource whose tenant column is its key")

	// A pointer or a nullable wrapper is the type it holds, for a key or a
	// tenant column too.
	type pointerKeys struct {
		ID       *int64  `json:"id"        db:"id"`
		TenantID *string `json:"tenant_id" db:"tenant_id" parse:"filter"`
	}
	_, err = parse.NewResource[pointerKeys](onPostgres, "pointed", parse.TenantColumn("tenant_id"))
	require.NoError(t, err, "a pointer key and tenant column")
	type wrappedKeys struct {
		ID       sql.NullInt64  `json:"id"        db:"id"`
		TenantID sql.NullString `json:"tenant_id" db:"tenant_id" parse:"filter"`
	}
	_, err = parse.NewResource[wrappedKeys](onPostgres, "wrapped", parse.TenantColumn("tenant_id"))
	require.NoError(t, err, "a wrapper key and tenant column")

	r, err := parse.NewResource[nullable](onPostgres, "rows", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, r))
	doc := reg.SchemaFor("rows")
	require.NotNil(t, doc.Patch)
	assert.NotContains(t, doc.Patch.Paths, "/slug", "an immutable field is never a request patch path")
	assert.Contains(t, doc.Patch.Paths, "/deleted_at", "a request may soft-delete a row the patch schema lists")
	_, err = r.ParseServerMergePatch([]byte(`{"slug":"rotated"}`))
	assert.NoError(t, err, "the server may write an immutable field")
}

// A soft-delete column of a nullable wrapper holds the wrapper's value: its
// system field is a timestamp, and Evaluate admits a row under the floor
// while its sql.NullTime is NULL, as SQL's IS NULL does.
func TestDeletedColumn_AWrapperRowIsLiveWhileNull(t *testing.T) {
	type note struct {
		ID        string       `json:"id"         db:"id"`
		DeletedAt sql.NullTime `json:"deleted_at" db:"deleted_at"`
	}
	r, err := parse.NewResource[note](onPostgres, "notes", parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	schema := r.FilterSchema()
	assert.Equal(t, core.FieldTypeTimestamp, schema.SystemFields["deleted_at"].DataType)
	floor, err := filter.ApplyPolicyConstraints(schema, nil, filter.ComposeConstraints(filter.RepositoryScope{DeleteColumn: r.DeletedColumnName()}, nil, nil))
	require.NoError(t, err)
	for _, tt := range []struct {
		name      string
		deletedAt sql.NullTime
		want      filter.Truth
	}{
		{"live", sql.NullTime{}, filter.True},
		{"deleted", sql.NullTime{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true}, filter.False},
	} {
		t.Run(tt.name, func(t *testing.T) {
			truth, err := filter.Evaluate(floor.Root, filter.ColumnRow(func(column string) (any, bool) {
				return tt.deletedAt, column == "notes.deleted_at"
			}))
			require.NoError(t, err)
			assert.Equal(t, tt.want, truth)
		})
	}
}

// Both update builders hold a write to the key or the tenant column to one
// rule: a patch never names one (NewResource refuses the path), and a server
// column that does is refused by both alike.
func TestUpdateBuilders_RefuseIdentityWritesAlike(t *testing.T) {
	type versioned struct {
		ID       int64  `json:"id"        db:"id"        parse:"filter"`
		TenantID string `json:"tenant_id" db:"tenant_id"`
		Name     string `json:"name"      db:"name"      parse:"patch"`
		Version  int64  `json:"version"   db:"version"   parse:"server"`
	}
	r, err := parse.NewResource[versioned](onPostgres, "versioned", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}
	for _, tc := range []struct {
		name   string
		body   string
		server bool
		opts   []parse.UpdateOption
		refuse string
	}{
		{"a column", `{"name":"n"}`, false, nil, ""},
		{"a counter", `{"name":"n"}`, false, []parse.UpdateOption{parse.Increment("version")}, ""},
		{"a server column", `{"version":7}`, true, nil, ""},
		{"a counter on the key", `{"name":"n"}`, false, []parse.UpdateOption{parse.Increment("id")}, "cannot change identity column id"},
		{"a stamp on the tenant column", `{"name":"n"}`, false, []parse.UpdateOption{parse.StampSQL("tenant_id", "'t2'")}, "cannot change identity column tenant_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := r.ParseMergePatch([]byte(tc.body))
			if tc.server {
				parsed, err = r.ParseServerMergePatch([]byte(tc.body))
			}
			require.NoError(t, err)
			_, single := r.BuildUpdate(parsed, parse.ByID(int64(1)), scope, tc.opts...)
			_, batch := r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: parsed}}, scope, parse.UpdateBatchLimits{}, tc.opts...)
			if tc.refuse == "" {
				assert.NoError(t, single)
				assert.NoError(t, batch)
				return
			}
			require.ErrorContains(t, single, "versioned: update "+tc.refuse)
			assert.ErrorContains(t, batch, "versioned: update batch "+tc.refuse)
		})
	}
}

// Two resources over one table: the wide one serves secret, the narrow one
// withholds its column (ReadColumns).
type wideUser struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Secret   string `json:"secret"    db:"secret"    parse:"filter,sort,patch"`
}

type narrowUser struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Secret   string `json:"-"         db:"secret"`
}

func ownershipResources(t *testing.T, opts ...parse.ResourceOption) (*parse.Resource[wideUser], *parse.Resource[narrowUser]) {
	t.Helper()
	wide, err := parse.NewResource[wideUser](onPostgres, "users", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	narrow, err := parse.NewResource[narrowUser](onPostgres, "users", append([]parse.ResourceOption{
		parse.TenantColumn("tenant_id"), parse.ReadColumns("id", "tenant_id", "name"),
	}, opts...)...)
	require.NoError(t, err)
	return wide, narrow
}

func secretQuery() url.Values {
	query := url.Values{filter.ParamFilter: {`{"secret":"x"}`}, filter.ParamSort: {"secret"}}
	return query
}

// A resource compiles only the list requests it parsed: another resource's
// over the same table would reach a column it withholds.
func TestBuildList_RefusesARequestItDidNotParse(t *testing.T) {
	wide, narrow := ownershipResources(t)
	scope := parse.Scope{Tenant: "t1"}

	_, err := narrow.ParseList(secretQuery())
	require.ErrorContains(t, err, "E2071", "the narrow resource has no secret field")

	wideReq, err := wide.ParseList(secretQuery())
	require.NoError(t, err)
	_, err = narrow.BuildList(wideReq, scope)
	require.ErrorIs(t, err, parse.ErrForeign)
	require.ErrorContains(t, err, "parse: users: list request")

	_, err = narrow.BuildList(&parse.ListRequest{}, scope)
	require.ErrorIs(t, err, parse.ErrForeign, "a hand-built request")

	own, err := narrow.ParseList(url.Values{filter.ParamFilter: {`{"name":"a"}`}})
	require.NoError(t, err)
	plan, err := narrow.BuildList(own, scope)
	require.NoError(t, err)
	assert.NotContains(t, plan.Page.SQL, "secret")
	_, err = narrow.BuildList(nil, scope)
	require.NoError(t, err, "a nil request is a first page")
}

// A WithPaths view and its base each compile only their own list requests
// and filters; they share patch schemas, so each accepts the other's patches.
func TestWithPaths_ViewAndBaseKeepTheirOwnRequests(t *testing.T) {
	_, base := ownershipResources(t)
	view, err := base.WithPaths()
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}
	q := url.Values{filter.ParamFilter: {`{"name":"a"}`}}

	baseReq, err := base.ParseList(q)
	require.NoError(t, err)
	viewReq, err := view.ParseList(q)
	require.NoError(t, err)
	_, err = view.BuildList(viewReq, scope)
	require.NoError(t, err)
	_, err = view.BuildList(baseReq, scope)
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = base.BuildList(viewReq, scope)
	require.ErrorIs(t, err, parse.ErrForeign)

	baseFilter, err := base.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	p, err := base.ParseMergePatch([]byte(`{"name":"b"}`))
	require.NoError(t, err)
	_, err = view.BuildUpdate(p, parse.ByFilter(baseFilter), scope)
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = view.BuildUpdate(p, parse.ByID("u1"), scope)
	require.NoError(t, err, "the view shares its base's patch schema")
}

// Updates and patch application take only the filters and patches the
// resource parsed.
func TestBuildUpdate_RefusesAFilterOrPatchItDidNotParse(t *testing.T) {
	wide, narrow := ownershipResources(t)
	scope := parse.Scope{Tenant: "t1"}

	ownPatch, err := narrow.ParseMergePatch([]byte(`{"name":"b"}`))
	require.NoError(t, err)
	widePatch, err := wide.ParseMergePatch([]byte(`{"secret":"s"}`))
	require.NoError(t, err)
	wideFilter, err := wide.ParseFilterMap(map[string]any{"secret": "x"})
	require.NoError(t, err)
	ast, err := filter.NewFilterParser(narrow.FilterSchema(), nil).ParseMap(map[string]any{"name": "a"})
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		patch  *patch.ParsedPatch
		target parse.Target
	}{
		"another resource's filter": {ownPatch, parse.ByFilter(wideFilter)},
		"an unmarked filter":        {ownPatch, parse.ByFilter(filter.NewParsedFilter(ast))},
		"another resource's patch":  {widePatch, parse.ByID("u1")},
		"a nil patch":               {nil, parse.ByID("u1")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := narrow.BuildUpdate(tc.patch, tc.target, scope)
			require.ErrorIs(t, err, parse.ErrForeign)
		})
	}

	_, err = narrow.ApplyValidatedPatch(widePatch, narrowUser{})
	require.ErrorIs(t, err, parse.ErrForeign)
	_, err = narrow.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{"u1"}, Patch: ownPatch}, {Key: parse.Key{"u2"}, Patch: widePatch}}, scope, parse.UpdateBatchLimits{})
	require.ErrorIs(t, err, parse.ErrForeign)
	require.ErrorContains(t, err, "update batch row 1 patch")

	serverPatch, err := narrow.ServerMergePatchMap(map[string]any{"name": "c"})
	require.NoError(t, err)
	ownFilter, err := narrow.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	_, err = narrow.BuildUpdate(serverPatch, parse.ByFilter(ownFilter), scope)
	require.NoError(t, err, "the server patch schema is the resource's too")
}

// WithFilter swaps in a filter the request's resource parsed, and nothing
// else.
func TestListRequest_WithFilterKeepsTheRequestSealed(t *testing.T) {
	wide, narrow := ownershipResources(t)
	req, err := narrow.ParseList(url.Values{filter.ParamPageSize: {"7"}})
	require.NoError(t, err)

	wideFilter, err := wide.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	_, err = req.WithFilter(wideFilter)
	require.ErrorIs(t, err, parse.ErrForeign)

	own, err := narrow.ParseFilterMap(map[string]any{"name": "a"})
	require.NoError(t, err)
	filtered, err := req.WithFilter(own)
	require.NoError(t, err)
	assert.Same(t, own, filtered.Filter())
	assert.Equal(t, 7, filtered.PageSize())
	assert.Nil(t, req.Filter(), "the original request is unchanged")
	_, err = narrow.BuildList(filtered, parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
}

// What the accessors and the registry hand out are copies: changing them
// changes nothing the resource parses or the registry describes.
func TestSchemaAccessors_HandOutCopies(t *testing.T) {
	_, narrow := ownershipResources(t)
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, narrow))
	secret := func() *filter.FieldSchema {
		field := &filter.FieldSchema{ExternalName: "secret", Table: "users", Column: "secret", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}}
		return field
	}

	narrow.FilterSchema().Fields["secret"] = secret()
	registered, ok := reg.FilterSchema("users")
	require.True(t, ok)
	registered.Fields["secret"] = secret()
	narrow.SortSchema().Fields["secret"] = &filter.SortFieldSchema{Table: "users", Column: "secret", DataType: core.FieldTypeString}
	require.NoError(t, narrow.PatchSchema().AddPath("/secret", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "secret"}))
	pathOf(narrow.ServerPatchSchema(), "/name").AllowedOps = nil

	_, err := narrow.ParseList(secretQuery())
	require.ErrorContains(t, err, "E2071")
	_, err = narrow.ParseList(url.Values{filter.ParamSort: {"secret"}})
	require.Error(t, err)
	_, err = narrow.ParseMergePatch([]byte(`{"secret":"s"}`))
	require.Error(t, err)
	_, err = narrow.ServerMergePatchMap(map[string]any{"name": "c"})
	require.NoError(t, err)
	assert.NotContains(t, reg.SchemaFor("users").Filter.Fields, "secret")
}

// Customization runs before the read-set checks, and a schema the function
// keeps changes nothing once the resource is built.
func TestCustomizeSchemas_IsHeldToTheReadSet(t *testing.T) {
	_, err := parse.NewResource[narrowUser](onPostgres, "users", parse.ReadColumns("id", "tenant_id", "name"), parse.CustomizeSchemas(func(s parse.Schemas) error {
		s.Filter.Fields["secret"] = &filter.FieldSchema{ExternalName: "secret", Table: "users", Column: "secret", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}}
		return nil
	}))
	require.ErrorContains(t, err, `"secret"`, "a customized field over a withheld column is refused")

	_, err = parse.NewResource[narrowUser](onPostgres, "users", parse.CustomizeSchemas(func(parse.Schemas) error { return assert.AnError }))
	require.ErrorIs(t, err, assert.AnError)

	var kept parse.Schemas
	_, narrow := ownershipResources(t, parse.CustomizeSchemas(func(s parse.Schemas) error {
		kept = s
		return nil
	}))
	kept.Filter.Fields["secret"] = &filter.FieldSchema{ExternalName: "secret", Table: "users", Column: "secret", DataType: core.FieldTypeString, AllowedOperators: []filter.FilterOperator{filter.OpEq}}
	_, err = narrow.ParseList(secretQuery())
	require.ErrorContains(t, err, "E2071")
}

type customItem struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Custom json.RawMessage `json:"custom" db:"custom" parse:"filter"`
}

// A path sort key's cursor reads its document's row column, which a column
// mapper's computed SQL does not name; the document field's Column does.
func TestResource_WithPathsSortsAMappedDocument(t *testing.T) {
	r, err := parse.NewResource[customItem](onSQLite, "custom_items", mappedColumns())
	require.NoError(t, err)
	view, err := r.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true})
	require.NoError(t, err)
	req, err := view.ParseList(url.Values{filter.ParamSort: {"custom.tier"}})
	require.NoError(t, err)
	plan, err := view.BuildList(req, parse.Scope{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Keyset.Keys)
	assert.Equal(t, "custom.tier", plan.Keyset.Keys[0].Name)
	assert.Equal(t, "custom", plan.Keyset.Keys[0].Column)
}

// A view is a value for one request: it declares no relation and is no
// relation's target, and the refusal leaves the resource it came from as it
// was.
func TestResource_WithPathsViewRelatesNothing(t *testing.T) {
	type tag struct {
		ID string `json:"id" db:"id" parse:"filter"`
	}
	tags := parse.MustResource[tag](onSQLite, "tags")
	items := parse.MustResource[customItem](onSQLite, "custom_items")
	itemView, err := items.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true})
	require.NoError(t, err)
	tagView, err := tags.WithPaths()
	require.NoError(t, err)
	through := parse.Through("item_tags", "item_id", "tag_id")
	shape := func() (filters, sorts, patches []string) {
		return slices.Sorted(maps.Keys(items.FilterSchema().Fields)),
			slices.Sorted(maps.Keys(items.SortSchema().Fields)),
			pathNames(items.PatchSchema())
	}
	filters, sorts, patches := shape()

	err = itemView.Relate("tags", parse.ManyToMany(tags, through, parse.Writable()))
	require.ErrorContains(t, err, `parse: custom_items: relation "tags": a view (WithPaths, In) declares no relations`)
	err = items.Relate("tags", parse.ManyToMany(tagView, through, parse.Writable()))
	require.ErrorContains(t, err, `parse: custom_items: relation "tags": the target is a view (WithPaths, In)`)

	gotFilters, gotSorts, gotPatches := shape()
	assert.Equal(t, filters, gotFilters)
	assert.Equal(t, sorts, gotSorts)
	assert.Equal(t, patches, gotPatches)
	_, err = items.ParseMergePatch([]byte(`{"tags":["t1"]}`))
	require.Error(t, err)

	require.NoError(t, items.Relate("tags", parse.ManyToMany(tags, through, parse.Writable())))
	_, err = items.ParseMergePatch([]byte(`{"tags":["t1"]}`))
	require.NoError(t, err)
}

// A request declares its tenant's custom fields as paths on a view of the
// resource; the shared resource never learns them.
func TestResource_WithPathsIsARequestView(t *testing.T) {
	p, db := providertest.SQLite(t)
	r, err := parse.NewResource[customItem](p, "custom_items", parse.PageSize(10, 10))
	require.NoError(t, err)
	view, err := r.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})
	require.NoError(t, err)

	q := url.Values{"filter": {`{"custom.tier":{"$ne":"bronze"}}`}, "sort": {"-custom.tier"}, "facets": {"custom.tier"}}
	_, err = r.ParseList(q)
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe, "the shared resource does not know the path: %v", err)
	assert.Equal(t, "E2071", pe.Code)

	req, err := view.ParseList(q)
	require.NoError(t, err)
	plan, err := view.BuildList(req, parse.Scope{})
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), `CREATE TABLE custom_items (id TEXT PRIMARY KEY, custom TEXT)`)
	require.NoError(t, err)
	for id, doc := range map[string]any{"a": `{"tier":"gold"}`, "b": `{"tier":"silver"}`, "c": `{"tier":"bronze"}`, "d": `{}`, "e": nil} {
		_, insertErr := db.ExecContext(t.Context(), `INSERT INTO custom_items VALUES (?1, ?2)`, id, doc)
		require.NoError(t, insertErr)
	}
	page, err := view.List(t.Context(), req, parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "a"}, parsetest.ItemIDs(page.Items, func(it customItem) string { return it.ID }), "silver then gold; bronze filtered out, absent tiers never match $ne")

	require.NotNil(t, plan.Facets)
	require.Len(t, page.Facets, 1)
	assert.ElementsMatch(t, []filter.FacetBucket{{Key: "gold", Count: 1}, {Key: "silver", Count: 1}}, page.Facets["custom.tier"])

	_, err = r.WithPaths(filter.PathDecl{Path: "id.x", Type: core.FieldTypeString})
	assert.Error(t, err, "a path needs a document field")
}

func pathNames(s *patch.PatchSchema) []string {
	var out []string
	for path := range s.Paths() {
		out = append(out, path)
	}
	return out
}

type pointerResourceRow struct {
	ID string `json:"id" db:"id"`
}

func TestResource_PointerRowsKeepTheirKeys(t *testing.T) {
	first, second := pointerResourceRow{ID: "r1"}, pointerResourceRow{ID: "r2"}
	t.Run("value model", func(t *testing.T) {
		checkResourceRowPages(t, []pointerResourceRow{first, second})
	})
	t.Run("pointer model", func(t *testing.T) {
		checkResourceRowPages(t, []*pointerResourceRow{&first, &second})
	})
	for _, resource := range []interface{ Key(any) (parse.Key, error) }{
		parse.MustResource[pointerResourceRow](onSQLite, "rows"),
		parse.MustResource[*pointerResourceRow](onSQLite, "rows"),
	} {
		for _, item := range []any{first, &first, new(&first)} {
			key, err := resource.Key(item)
			require.NoError(t, err)
			assert.Equal(t, parse.Key{"r1"}, key)
		}
		type otherRow pointerResourceRow
		for _, item := range []any{nil, (*pointerResourceRow)(nil), new((*pointerResourceRow)(nil)), otherRow(first), 1} {
			_, err := resource.Key(item)
			require.ErrorContains(t, err, "rows")
		}
	}
	r := parse.MustResource[*pointerResourceRow](onSQLite, "rows", parse.PageSize(1, 10))
	plan, err := r.BuildList(nil, parse.Scope{})
	require.NoError(t, err)
	_, err = r.Page([]*pointerResourceRow{nil, &second}, plan)
	require.ErrorContains(t, err, "nil row")
}

func checkResourceRowPages[T any](t *testing.T, items []T) {
	t.Helper()
	r := parse.MustResource[T](onSQLite, "rows", parse.PageSize(1, 10))
	plan, err := r.BuildList(nil, parse.Scope{})
	require.NoError(t, err)
	page, err := r.Page(items, plan)
	require.NoError(t, err)
	require.True(t, page.PageInfo.HasNextPage)
	cursor, err := filter.DecodeCursor(string(page.PageInfo.EndCursor))
	require.NoError(t, err)
	require.Len(t, cursor.Fields, 1)
	assert.Equal(t, "r1", cursor.Fields[0].Value)

	pointers := []*T{&items[0], &items[1]}
	pointerPage, err := r.PagePointers(pointers, plan)
	require.NoError(t, err)
	assert.Equal(t, page.PageInfo, pointerPage.PageInfo)
	_, err = r.PagePointers([]*T{nil, &items[1]}, plan)
	require.ErrorContains(t, err, "nil row")
}

type keyedDevice struct {
	ID   googleuuid.UUID `json:"id"   db:"id"`
	Name string          `json:"name" db:"name" parse:"filter"`
}

// A primary key is an index by construction, so a request filtering on
// the key's system field costs as an indexed condition: three key
// equalities under $or fit the default MaxCost.
func TestKeyFilter_CostsAsIndexed(t *testing.T) {
	r := parse.MustResource[keyedDevice](onSQLite, "keyed_devices")
	a, b, c := googleuuid.NewString(), googleuuid.NewString(), googleuuid.NewString()
	_, err := r.ParseListInput(parse.ListInput{Filter: `{"$or":[{"id":"` + a + `"},{"id":"` + b + `"},{"id":"` + c + `"}]}`})
	require.NoError(t, err)
}

type stdlibKeyed struct {
	ID   uuid.UUID `json:"id"   db:"id"   parse:"filter,sort"`
	Name string    `json:"name" db:"name" parse:"filter,sort"`
}

type googleKeyed struct {
	ID   googleuuid.UUID `json:"id"   db:"id"   parse:"filter,sort"`
	Name string          `json:"name" db:"name" parse:"filter,sort"`
}

// A resource keyed by the standard library's uuid.UUID lists like one keyed
// by github.com/google/uuid: the same filter SQL, the same cursors.
func TestUUIDKeyedResources_ListAlike(t *testing.T) {
	const id = "0198a3c2-7a1e-7c00-8000-000000000001"
	q := url.Values{"id": {"0198A3C2-7A1E-7C00-8000-000000000001"}, "sort": {"name"}, "page_size": {"1"}}

	std, err := parse.NewResource[stdlibKeyed](onPostgres, "rows")
	require.NoError(t, err)
	stdReq, err := std.ParseList(q)
	require.NoError(t, err)
	stdPlan, err := std.BuildList(stdReq, parse.Scope{})
	require.NoError(t, err)
	stdPage, err := std.Page([]stdlibKeyed{{uuid.MustParse(id), "a"}, {uuid.MustParse(id), "b"}}, stdPlan)
	require.NoError(t, err)

	g, err := parse.NewResource[googleKeyed](onPostgres, "rows")
	require.NoError(t, err)
	gReq, err := g.ParseList(q)
	require.NoError(t, err)
	gPlan, err := g.BuildList(gReq, parse.Scope{})
	require.NoError(t, err)
	gPage, err := g.Page([]googleKeyed{{googleuuid.MustParse(id), "a"}, {googleuuid.MustParse(id), "b"}}, gPlan)
	require.NoError(t, err)

	assert.Equal(t, gPlan.Page.SQL, stdPlan.Page.SQL)
	assert.Equal(t, gPlan.Page.Args, stdPlan.Page.Args)
	assert.Contains(t, stdPlan.Page.Args, id, "the filter value binds as canonical text")
	assert.Equal(t, gPage.PageInfo.EndCursor, stdPage.PageInfo.EndCursor)
}

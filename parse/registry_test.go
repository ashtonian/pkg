//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestSchemaRegistry_Register(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[TestDevice](reg)

	fs, ok := reg.FilterSchema("test_devices")
	if !ok {
		t.Fatal("expected filter schema")
	}
	_, found := fs.Fields["status"]
	if !found {
		t.Error("expected status field")
	}

	ps, ok := reg.PatchSchema("test_devices")
	if !ok {
		t.Fatal("expected patch schema")
	}
	_, ok = ps.Path("/name")
	if !ok {
		t.Error("expected /name path")
	}
}

// TestResourceNamesSorted verifies the registry returns resource names in
// a deterministic (sorted) order rather than Go's random map-iteration order,
// so a schema-index API listing is stable.
func TestResourceNamesSorted(t *testing.T) {
	type Zebra struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	type Alpha struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	type Mango struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	type Delta struct {
		Name string `json:"name" parse:"filter" filter_ops:"eq"`
	}
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Zebra](reg)
	parse.MustRegister[Alpha](reg)
	parse.MustRegister[Mango](reg)
	parse.MustRegister[Delta](reg)
	names := reg.ResourceNames()
	if !slices.IsSorted(names) {
		t.Errorf("ResourceNames not sorted: %v", names)
	}
}

func TestSchemaRegistry_SchemaFor(t *testing.T) {
	type Product struct {
		Name     string `json:"name"     parse:"filter,patch,search" filter_ops:"eq" patch_ops:"replace" label:"Name"`
		Category string `json:"category" parse:"filter,facet" filter_ops:"eq" facet:"terms" enum:"a,b"`
	}

	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Product](reg)

	resp := reg.SchemaFor("products")
	if resp == nil {
		t.Fatal("expected schema")
	}
	if resp.Filter == nil {
		t.Error("expected filter")
	}
	if resp.Patch == nil {
		t.Error("expected patch")
	}
	if resp.Search == nil {
		t.Error("expected search")
	}
	if resp.Filter.Fields["name"].Label != "Name" {
		t.Error("expected label")
	}
	if !resp.Filter.Fields["category"].Facetable {
		t.Error("expected facetable")
	}
}

func TestSchemaRegistry_NotFound(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	if reg.SchemaFor("nope") != nil {
		t.Error("expected nil for unregistered resource")
	}
}

// --- Immutable in SchemaResponse ---

func TestSchemaRegistry_ImmutableField(t *testing.T) {
	type Entity struct {
		ID   string `json:"id"   parse:"filter" filter_ops:"eq" immutable:"true"`
		Name string `json:"name" parse:"filter,patch" filter_ops:"eq" patch_ops:"replace"`
	}
	reg := parse.NewSchemaRegistry()
	parse.MustRegister[Entity](reg)
	resp := reg.SchemaFor("entitys")
	if resp == nil {
		// Try with proper plural.
		resp = reg.SchemaFor("entities")
	}
	if resp == nil {
		t.Fatal("expected schema response")
	}
	idField := resp.Filter.Fields["id"]
	if idField == nil {
		t.Fatal("expected id field in filter schema")
	}
	if !idField.Immutable {
		t.Error("expected id to be immutable in schema response")
	}
	nameField := resp.Filter.Fields["name"]
	if nameField == nil {
		t.Fatal("expected name field in filter schema")
	}
	if nameField.Immutable {
		t.Error("expected name to NOT be immutable")
	}
}

func TestSchemaRegistrySnapshotOwnership(t *testing.T) {
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	response := r.SchemaFor("items")
	response.Filter.Fields["name"].Operators[0] = filter.OpNe
	response.Filter.Fields["name"].EnumValues[0] = "changed"
	response.Patch.Paths["/name"].AllowedOps[0] = patch.PatchOpRemove
	got := r.SchemaFor("items")
	if got.Filter.Fields["name"].Operators[0] != filter.OpEq || got.Filter.Fields["name"].EnumValues[0] != "a" || got.Patch.Paths["/name"].AllowedOps[0] != patch.PatchOpReplace {
		t.Fatal("editing a discovery response changed registered validation metadata")
	}
}

// A name is registered once: a second registration under it, by type or by
// resource, is refused and leaves the first as it was.
func TestSchemaRegistry_RefusesATakenName(t *testing.T) {
	type plain struct {
		ID   string `json:"id"   db:"id"`
		Name string `json:"name" db:"name" parse:"filter,patch"`
	}
	r := parse.NewSchemaRegistry(core.WithSQLTableName("items"))
	parse.MustRegister[registrySnapshotEntity](r)
	require.ErrorIs(t, parse.Register[plain](r), parse.ErrRegistered)
	items, err := parse.NewResource[plain](onSQLite, "items")
	require.NoError(t, err)
	err = parse.RegisterResource(r, items)
	require.ErrorIs(t, err, parse.ErrRegistered)
	require.EqualError(t, err, "parse: items: already registered")
	require.Error(t, parse.Register[int](r))

	got := r.SchemaFor("items")
	require.NotNil(t, got)
	assert.NotNil(t, got.Sort, "the first registration's capabilities stay")
	assert.NotNil(t, got.Search)
	assert.Equal(t, []string{"items"}, r.ResourceNames())

	assert.PanicsWithError(t, "parse: items: already registered", func() { parse.MustRegister[plain](r) })
	assert.PanicsWithError(t, "parse: items: already registered", func() { parse.MustRegisterResource(r, items) })
}

// Two resources over one table register under one name: the registry holds
// the first, never the wider of the two.
func TestRegisterResource_TwoResourcesOverOneTable(t *testing.T) {
	type narrow struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Name string `json:"name" db:"name" parse:"filter"`
	}
	type wide struct {
		ID     string `json:"id"     db:"id"     parse:"filter"`
		Name   string `json:"name"   db:"name"   parse:"filter"`
		Secret string `json:"secret" db:"secret" parse:"filter"`
	}
	r := parse.NewSchemaRegistry()
	parse.MustRegisterResource(r, parse.MustResource[narrow](onSQLite, "users"))
	require.ErrorIs(t, parse.RegisterResource(r, parse.MustResource[wide](onSQLite, "users")), parse.ErrRegistered)
	assert.NotContains(t, r.SchemaFor("users").Filter.Fields, "secret")
}

// The registry serves a resource's search schema as a copy, like its other
// three; a view is not registered.
func TestSchemaRegistry_SearchSchemaAndViews(t *testing.T) {
	r := parse.NewSchemaRegistry()
	items := parse.MustResource[registrySnapshotEntity](onSQLite, "items")
	parse.MustRegisterResource(r, items)
	search, ok := r.SearchSchema("items")
	require.True(t, ok)
	require.Len(t, search.Fields, 1)
	search.Fields[0].Name = "changed"
	again, _ := r.SearchSchema("items")
	assert.Equal(t, "name", again.Fields[0].Name)
	_, ok = r.SearchSchema("unknown")
	assert.False(t, ok)

	view, err := items.WithPaths()
	require.NoError(t, err)
	assert.ErrorContains(t, parse.RegisterResource(parse.NewSchemaRegistry(), view), "parse: items: a view (WithPaths, In) is a value for one request and is not registered")
}

const schemaDocumentPath = "testdata/schema_document.json"

// schemaDocEntity exercises every member of the schema document: field metadata,
// enums, bounds, facets, search, geo, JSON paths, array elements, a junction
// relation, a declared relation (schemaDocPart), and patch paths of every kind.
type schemaDocEntity struct {
	ID       string          `json:"id"        db:"id"        parse:"filter,sort" indexed:"true" immutable:"true"`
	TenantID string          `json:"tenant_id" db:"tenant_id"`
	Name     string          `json:"name"      db:"name"      parse:"filter,sort,facet,search,patch" facet:"terms:10" label:"Name" description:"Display name" group:"general" order:"1" minlen:"1" maxlen:"64"`
	Status   string          `json:"status"    db:"status"    parse:"filter,patch" enum:"active,offline" patch_ops:"replace"`
	Rank     int32           `json:"rank"      db:"rank"      parse:"filter,sort,patch" min:"0" max:"100"`
	Note     *string         `json:"note"      db:"note"      parse:"patch"`
	Scores   []int16         `json:"scores"    db:"scores"    parse:"patch"`
	Location map[string]any  `json:"location"  db:"location"  parse:"filter" filter_ops:"near,within" geo:"jsonb" geo_srid:"4326"`
	Meta     map[string]any  `json:"meta"      db:"meta"      parse:"filter" paths:"region:string"`
	Lines    []schemaDocLine `json:"lines"     db:"lines"     parse:"filter"`
	Tags     []string        `json:"tags"      db:"-"         parse:"filter,include,patch" relation:"doc_tags,entity_id,tag_id,id,tenant_id"`
	Serial   int64           `json:"serial,string" db:"serial" parse:"filter,sort,patch"`
}

type schemaDocLine struct {
	SKU string `json:"sku" parse:"filter" filter_ops:"eq,in"`
	Qty int    `json:"qty" parse:"filter" filter_ops:"eq,gt"`
}

// schemaDocLabel is the target of a writable relation whose key travels as
// a JSON string, so its membership path's keys do too.
type schemaDocLabel struct {
	ID       int64  `json:"id,string" db:"id"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
}

type schemaDocPart struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	EntityID string `json:"entity_id" db:"entity_id"`
	State    string `json:"state"     db:"state"     parse:"filter" enum:"ok,worn"`
}

func schemaDocument(t *testing.T) *parse.SchemaResponse {
	t.Helper()
	entities, err := parse.NewResource[schemaDocEntity](onSQLite, "doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	parts, err := parse.NewResource[schemaDocPart](onSQLite, "doc_parts", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	require.NoError(t, entities.Relate("parts", parse.HasMany(parts, "entity_id", parse.Includable(), parse.Facetable(),
		parse.Describe("Parts", "The entity's parts"))))
	labels, err := parse.NewResource[schemaDocLabel](onSQLite, "doc_labels", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	require.NoError(t, entities.Relate("labels", parse.ManyToMany(labels, parse.Through("doc_entity_labels", "entity_id", "label_id"), parse.Writable())))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, entities))
	require.NoError(t, parse.RegisterResource(reg, parts))
	doc := reg.SchemaFor("doc_entities")
	require.NotNil(t, doc)
	return doc
}

// The schema document is a contract: its golden snapshot changes only with a
// deliberate format change, and a change that removes, renames or redefines a
// member increments SchemaVersion. Regenerate with -update.
func TestSchemaDocument_MatchesItsGoldenSnapshot(t *testing.T) {
	got, err := json.MarshalIndent(schemaDocument(t), "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	if *updateRendered {
		require.NoError(t, os.WriteFile(schemaDocumentPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(schemaDocumentPath)
	require.NoError(t, err, "missing snapshot; regenerate with -update")
	assert.Equal(t, string(want), string(got), "the schema document changed; regenerate with -update and, for a breaking change, increment SchemaVersion")
}

func TestSchemaDocument_CarriesItsVersion(t *testing.T) {
	doc := schemaDocument(t)
	assert.Equal(t, parse.SchemaVersion, doc.Version)

	data, err := json.Marshal(doc)
	require.NoError(t, err)
	var decoded parse.SchemaResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, doc, &decoded, "the document decodes to what was served")
}

// docThing has a key with no parse tag, a tenant and a soft-delete column,
// an untagged and a hidden field, and relations of both directions.
type docThing struct {
	ID        string     `json:"id"         db:"id"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Name      string     `json:"name"       db:"name"     parse:"filter,sort"`
	Labels    []string   `json:"labels"     db:"labels"   parse:"filter"`
	Rank      int        `json:"rank"       db:"rank"`
	Secret    string     `json:"-"          db:"secret"`
	OwnerID   int64      `json:"owner_id"   db:"owner_id"`
}

type docOwner struct {
	Key  int64  `json:"key"  db:"key"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type docPart struct {
	ID      string `json:"id"       db:"id"       parse:"filter"`
	ThingID string `json:"thing_id" db:"thing_id"`
	State   string `json:"state"    db:"state"    parse:"filter"`
}

// The schema document's filter section lists exactly the names a request
// filter may name, and for each exactly the operators the parser takes on
// it, on every resource the registry serves: the key included, untagged or
// not, and the tenant and soft-delete columns, untagged fields and hidden
// fields left out.
func TestSchemaDocument_ListsWhatTheParserAccepts(t *testing.T) {
	owners := parse.MustResource[docOwner](onSQLite, "doc_owners", parse.PrimaryKey("key"))
	parts := parse.MustResource[docPart](onSQLite, "doc_parts")
	things := parse.MustResource[docThing](onSQLite, "doc_things", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	things.MustRelate("owner", parse.BelongsTo(owners, "owner_id", parse.Indexed()))
	things.MustRelate("parts", parse.HasMany(parts, "thing_id", parse.Indexed()))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, things))
	require.NoError(t, parse.RegisterResource(reg, owners))
	require.NoError(t, parse.RegisterResource(reg, parts))

	parsers := map[string]func(string) error{
		"doc_things": func(doc string) error { _, err := things.ParseFilter(doc); return err },
		"doc_owners": func(doc string) error { _, err := owners.ParseFilter(doc); return err },
		"doc_parts":  func(doc string) error { _, err := parts.ParseFilter(doc); return err },
	}
	// Every name any of the resources' fields has, as JSON or as a column.
	candidates := []string{
		"id", "tenant_id", "deleted_at", "name", "labels", "rank", "secret", "owner_id", "owner", "parts",
		"key", "thing_id", "state", "Secret", "missing",
	}
	operators := append(filter.Operators(), filter.OpIs, filter.OpIsNot)
	require.ElementsMatch(t, slices.Collect(maps.Keys(parsers)), reg.ResourceNames())

	for _, resource := range reg.ResourceNames() {
		parseFilter := parsers[resource]
		listed := reg.SchemaFor(resource).Filter.Fields
		for _, name := range candidates {
			code := filterCode(t, parseFilter, name, filter.OpExists)
			_, documented := listed[name]
			assert.Equal(t, documented, code != filter.ECodeUnknownField.String(),
				"%s: the document lists %q: %v; the parser answers %q", resource, name, documented, code)
		}
		for name, field := range listed {
			for _, op := range operators {
				code := filterCode(t, parseFilter, name, op)
				assert.Equal(t, slices.Contains(field.Operators, op), code != filter.ECodeInvalidOperator.String(),
					"%s: %q lists $%s: %v; the parser answers %q", resource, name, op, slices.Contains(field.Operators, op), code)
			}
		}
	}
	key := reg.SchemaFor("doc_things").Filter.Fields["id"]
	require.NotNil(t, key, "the untagged key is listed")
	assert.Equal(t, core.FieldTypeString, key.DataType)
	assert.Equal(t, core.FieldTypeInt, reg.SchemaFor("doc_owners").Filter.Fields["key"].DataType, "a key of its own name and type")
}

// filterCode is the code parse answers {name: {$op: operand}} with, "" when
// it accepts it. The operand need not suit the operator: whether the
// parser takes the operator on the field is decided first.
func filterCode(t *testing.T, parseFilter func(string) error, name string, op filter.FilterOperator) string {
	t.Helper()
	var operand any = "a"
	switch op {
	case filter.OpIn, filter.OpNin:
		operand = []any{"a"}
	case filter.OpNull, filter.OpNotNull, filter.OpExists:
		operand = true
	case filter.OpSome, filter.OpNone, filter.OpIs, filter.OpIsNot:
		operand = map[string]any{}
	}
	doc, err := json.Marshal(map[string]any{name: map[string]any{"$" + string(op): operand}})
	require.NoError(t, err)
	err = parseFilter(string(doc))
	if err == nil {
		return ""
	}
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%s $%s: %v is no ParseError", name, op, err)
	return pe.Code
}

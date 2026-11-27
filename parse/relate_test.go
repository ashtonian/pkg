//go:build unit

package parse_test

import (
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/patch"
)

// One `$some` is one correlated EXISTS over the target's live rows in the
// tenant, its predicates on one aliased row.
func TestDeclaredRelations_SQL(t *testing.T) {
	r := parsetest.RelationResources(t, onPostgres)
	plan := relationPlan(t, r.Devices, `{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}`, parsetest.RelationScopeT1())
	assert.Contains(t, plan.Page.SQL, `EXISTS (SELECT 1 FROM "commands" WHERE "commands"."device_id" = "devices"."id" AND "commands"."tenant_id" = $1 AND "commands"."deleted_at" IS NULL AND ("commands"."attempts" > $2 AND "commands"."status" = $3))`)

	plan = relationPlan(t, r.Devices, `{"tags":{"$some":{"label":"red"}}}`, parsetest.RelationScopeT1())
	assert.Contains(t, plan.Page.SQL, `EXISTS (SELECT 1 FROM "device_tags" "rel1__" JOIN "tags" ON "tags"."id" = "rel1__"."tag_id" WHERE "rel1__"."device_id" = "devices"."id" AND "rel1__"."tenant_id" = $1 AND "tags"."tenant_id" = $2 AND "tags"."deleted_at" IS NULL AND "tags"."label" = $3)`)

	assets := relationPlan(t, r.Assets, `{"parent":{"$is":{"name":"root"}}}`, parse.Scope{Tenant: "t1"})
	assert.Contains(t, assets.Page.SQL, `EXISTS (SELECT 1 FROM "assets" "assets_rel1__" WHERE "assets_rel1__"."id" = "assets"."parent_id" AND "assets_rel1__"."tenant_id" = $1 AND "assets_rel1__"."name" = $2)`, "the target is aliased away from its owner")
}

func relationPlan(t *testing.T, r interface {
	ParseList(url.Values) (*parse.ListRequest, error)
}, f string, s parse.Scope) *parse.ListPlan {
	t.Helper()
	req, err := r.ParseList(url.Values{filter.ParamFilter: {f}})
	require.NoError(t, err)
	switch res := r.(type) {
	case *parse.Resource[parsetest.RelDevice]:
		plan, err := res.BuildList(req, s)
		require.NoError(t, err)
		return plan
	case *parse.Resource[parsetest.RelAsset]:
		plan, err := res.BuildList(req, s)
		require.NoError(t, err)
		return plan
	}
	t.Fatalf("unexpected resource %T", r)
	return nil
}

func TestDeclaredRelations_Refuse(t *testing.T) {
	r := parsetest.RelationResources(t, onPostgres)
	for _, tc := range []struct {
		name, filter string
		code         string
	}{
		{"an unknown target field", `{"commands":{"$some":{"colour":"red"}}}`, filter.ECodeUnknownField.String()},
		{"$is on a collection", `{"commands":{"$is":{}}}`, filter.ECodeInvalidOperator.String()},
		{"$some on a belongs-to", `{"type":{"$some":{}}}`, filter.ECodeInvalidOperator.String()},
		{"a non-object target filter", `{"commands":{"$some":"c1"}}`, filter.ECodeInvalidJSON.String()},
		{"three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":{"$some":{}}}}}}}`, filter.ECodeDepthExceeded.String()},
		{"a key equality three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":"c1"}}}}}`, filter.ECodeDepthExceeded.String()},
		{"a key set three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":{"$in":["c1"]}}}}}}`, filter.ECodeDepthExceeded.String()},
		{"a key inequality three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":{"$ne":"c1"}}}}}}`, filter.ECodeDepthExceeded.String()},
		{"a key exclusion three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":{"$nin":["c1"]}}}}}}`, filter.ECodeDepthExceeded.String()},
		{"a key NULL test three levels deep", `{"commands":{"$some":{"device":{"$is":{"commands":{"$null":true}}}}}}`, filter.ECodeDepthExceeded.String()},
		{"nine relation predicates", `{"$and":[` + strings.Repeat(`{"commands":{"$some":{}}},`, 8) + `{"commands":{"$some":{}}}]}`, filter.ECodeMaxConditions.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Devices.ParseList(url.Values{filter.ParamFilter: {tc.filter}})
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.code, pe.Code, pe.Message)
		})
	}

	_, err := r.Devices.ParseList(url.Values{filter.ParamFilter: {`{"commands":{"$some":{"device":"d1"}}}`}})
	require.NoError(t, err, "a key comparison two levels deep")

	req, err := r.Devices.ParseList(url.Values{filter.ParamFilter: {`{"type":{"$is":{}}}`}})
	require.NoError(t, err)
	_, err = r.Devices.BuildList(req, parse.Scope{Tenant: "t1"})
	assert.ErrorIs(t, err, filter.ErrRelatedPolicyRequired, "a policy-scoped relation needs the target's policy")
}

func TestRelate_RefusesBadDeclarations(t *testing.T) {
	commands := parse.MustResource[parsetest.RelCommand](onPostgres, "commands", parse.TenantColumn("tenant_id"))
	for name, tc := range map[string]struct {
		relName string
		rel     parse.Relation
	}{
		"unknown foreign key":     {"commands", parse.HasMany(commands, "devise_id")},
		"unknown owner column":    {"commands", parse.HasMany(commands, "device_id", parse.References("nope"))},
		"unknown belongs-to key":  {"command", parse.BelongsTo(commands, "type_id", parse.References("nope"))},
		"existing field":          {"name", parse.HasMany(commands, "device_id")},
		"system field":            {"tenant_id", parse.HasMany(commands, "device_id")},
		"not an identifier":       {"my commands", parse.HasMany(commands, "device_id")},
		"pair table identifiers":  {"tags", parse.ManyToMany(commands, parse.Through("pairs", "device id", "tag_id"))},
		"references on a pairing": {"tags", parse.ManyToMany(commands, parse.Through("pairs", "device_id", "tag_id"), parse.References("id"))},
	} {
		t.Run(name, func(t *testing.T) {
			devices := parse.MustResource[parsetest.RelDevice](onPostgres, "devices", parse.TenantColumn("tenant_id"))
			assert.Error(t, devices.Relate(tc.relName, tc.rel))
		})
	}
}

type keyedLine struct {
	ID     string          `json:"id"      db:"id"      parse:"filter,sort"`
	TierID decimal.Decimal `json:"tier_id" db:"tier_id" parse:"filter"`
	Weight float64         `json:"weight"  db:"weight"  parse:"filter"`
}

type keyedTier struct {
	ID     decimal.Decimal `json:"id"      db:"id"      parse:"filter,sort"`
	LineID string          `json:"line_id" db:"line_id" parse:"filter"`
}

type keyedNote struct {
	ID     string         `json:"id"      db:"id"      parse:"filter,sort"`
	LineID string         `json:"line_id" db:"line_id" parse:"filter"`
	Weight float64        `json:"weight"  db:"weight"  parse:"filter"`
	Meta   map[string]any `json:"meta"    db:"meta"`
}

type taggedWeights struct {
	ID      string    `json:"id" db:"id" parse:"filter,sort"`
	Weights []float64 `json:"-"  db:"-"  parse:"filter" relation:"line_weights,line_id,weight_id"`
}

type taggedByWeight struct {
	ID     string   `json:"id"     db:"id"     parse:"filter,sort"`
	Weight float64  `json:"weight" db:"weight"`
	Tags   []string `json:"-"      db:"-"      parse:"filter" relation:"line_tags,line_id,tag_id,weight"`
}

type taggedDocs struct {
	ID   string           `json:"id" db:"id" parse:"filter,sort"`
	Docs []map[string]any `json:"-"  db:"-"  parse:"filter" relation:"line_docs,line_id,doc"`
}

// A relation correlates on scalar keys of any type, each column compared
// as a filter compares its type. A document has no equality to join by,
// and paired columns of two types no comparison.
func TestRelate_CorrelatesOnScalarKeys(t *testing.T) {
	tiers := parse.MustResource[keyedTier](onPostgres, "tiers")
	notes := parse.MustResource[keyedNote](onPostgres, "notes")
	for name, tc := range map[string]struct {
		relName string
		rel     parse.Relation
		want    string
	}{
		"decimal target key, belongs-to": {relName: "tier", rel: parse.BelongsTo(tiers, "tier_id")},
		"decimal target key, has-many":   {relName: "tiers", rel: parse.HasMany(tiers, "line_id")},
		"decimal target key, pair table": {relName: "tiers", rel: parse.ManyToMany(tiers, parse.Through("line_tiers", "line_id", "tier_id"))},
		"float columns paired":           {relName: "notes", rel: parse.HasMany(notes, "weight", parse.References("weight"))},
		"string and float paired": {
			relName: "notes", rel: parse.HasMany(notes, "weight"),
			want: "lines.id is string and notes.weight, its pair, is float; paired columns hold one kind of key",
		},
		"document column": {
			relName: "notes", rel: parse.HasMany(notes, "meta"),
			want: "notes.meta is json; a relation correlates on scalar keys",
		},
	} {
		t.Run(name, func(t *testing.T) {
			lines := parse.MustResource[keyedLine](onPostgres, "lines")
			err := lines.Relate(tc.relName, tc.rel)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tc.want)
		})
	}

	_, err := parse.NewResource[taggedWeights](onPostgres, "lines")
	require.NoError(t, err, "float target keys")
	_, err = parse.NewResource[taggedByWeight](onPostgres, "lines")
	require.NoError(t, err, "a float owner key")
	_, err = parse.NewResource[taggedDocs](onPostgres, "lines")
	assert.ErrorContains(t, err, "target column doc is json; a relation correlates on scalar keys")
}

// A key comparison that reaches the builder without the parser — a policy
// constraint naming the relation — still tests only visible related rows,
// and flat parameters are parsed as the JSON filter is.
func TestDeclaredRelations_EveryEntryReachesOnlyVisibleRows(t *testing.T) {
	r := parsetest.RelationResources(t, onPostgres)
	s := parsetest.RelationScopeT1()
	s.Policy = &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "commands", Operator: filter.OpEq, Value: "c4"}}}
	plan, err := r.Devices.BuildList(nil, s)
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, `"commands"."deleted_at" IS NULL`, "the policy's key comparison is a relation predicate")

	req, err := r.Devices.ParseList(url.Values{"commands": {"c1"}})
	require.NoError(t, err)
	plan, err = r.Devices.BuildList(req, s)
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(plan.Page.SQL, `"commands"."deleted_at" IS NULL`), plan.Page.SQL)
}

// A relation's label and description reach schema discovery as a column's
// tags do.
func TestRelate_DescribeReachesDiscovery(t *testing.T) {
	commands := parse.MustResource[parsetest.RelCommand](onPostgres, "commands", parse.TenantColumn("tenant_id"))
	devices := parse.MustResource[parsetest.RelDevice](onPostgres, "devices", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Describe("Commands", "Commands sent to the device")))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, devices))

	f := reg.SchemaFor("devices").Filter.Fields["commands"]
	require.NotNil(t, f)
	assert.Equal(t, "Commands", f.Label)
	assert.Equal(t, "Commands sent to the device", f.Description)
}

func crResources(t *testing.T) (*parse.Resource[parsetest.CrStock], *parse.Resource[parsetest.CrMove], *parse.Resource[parsetest.CrTag]) {
	t.Helper()
	stock, err := parse.NewResource[parsetest.CrStock](onSQLite, "cr_stock", parse.PrimaryKey("warehouse_id", "sku"))
	require.NoError(t, err)
	moves, err := parse.NewResource[parsetest.CrMove](onSQLite, "cr_moves")
	require.NoError(t, err)
	tags, err := parse.NewResource[parsetest.CrTag](onSQLite, "cr_tags")
	require.NoError(t, err)
	return stock, moves, tags
}

// A relation's columns pair by position with the key they hold: as many,
// each named once, each a column of its table holding the same kind of key
// as its pair. Errors name the relation and the columns.
func TestRelate_CompositeKeyDeclarations(t *testing.T) {
	key := []string{"warehouse_id", "sku"}
	for _, tt := range []struct {
		name    string
		owner   string
		rel     func(stock *parse.Resource[parsetest.CrStock], moves *parse.Resource[parsetest.CrMove], tags *parse.Resource[parsetest.CrTag]) parse.Relation
		wantErr string
	}{
		{
			name:  "has-many on the owner's key",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], moves *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.HasMany(moves, key, parse.Includable(), parse.Facetable())
			},
		},
		{
			name:  "belongs-to the target's key",
			owner: "moves",
			rel: func(stock *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.BelongsTo(stock, key)
			},
		},
		{
			name:  "many-to-many from the key",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], tags *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.ManyToMany(tags, parse.Through("cr_stock_tags", key, "tag_id"), parse.Includable(), parse.Facetable(), parse.Writable())
			},
		},
		{
			name:  "many-to-many to the key",
			owner: "tags",
			rel: func(stock *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.ManyToMany(stock, parse.Through("cr_stock_tags", "tag_id", key), parse.Includable(), parse.Facetable(), parse.Writable())
			},
		},
		{
			name:  "has-many foreign key shorter than the key",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], moves *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.HasMany(moves, "sku")
			},
			wantErr: `relation "rel": foreign key (sku) pairs by position with cr_stock's key (warehouse_id, sku), so it names as many columns`,
		},
		{
			name:  "belongs-to foreign key naming a column twice",
			owner: "moves",
			rel: func(stock *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.BelongsTo(stock, []string{"sku", "sku"})
			},
			wantErr: `foreign key names "sku" twice`,
		},
		{
			name:  "paired columns of different key kinds",
			owner: "moves",
			rel: func(stock *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.BelongsTo(stock, []string{"code", "sku"})
			},
			wantErr: "cr_moves.code is string and cr_stock.warehouse_id, its pair, is int",
		},
		{
			name:  "unknown foreign key column",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], moves *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.HasMany(moves, []string{"warehouse_id", "skew"})
			},
			wantErr: `foreign key "skew" is not a column of cr_moves`,
		},
		{
			name:  "pair table source shorter than the owner's key",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], tags *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.ManyToMany(tags, parse.Through("cr_stock_tags", "stock_id", "tag_id"))
			},
			wantErr: "pair table cr_stock_tags's source (stock_id) pairs by position with cr_stock's key (warehouse_id, sku), so it names as many columns",
		},
		{
			name:  "pair table target longer than the target's key",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], _ *parse.Resource[parsetest.CrMove], tags *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.ManyToMany(tags, parse.Through("cr_stock_tags", key, []string{"tag_id", "kind"}))
			},
			wantErr: "pair table cr_stock_tags's target (tag_id, kind) pairs by position with cr_tags's key (id), so it names as many columns",
		},
		{
			name:  "include on another owner column",
			owner: "stock",
			rel: func(_ *parse.Resource[parsetest.CrStock], moves *parse.Resource[parsetest.CrMove], _ *parse.Resource[parsetest.CrTag]) parse.Relation {
				return parse.HasMany(moves, "sku", parse.References("sku"), parse.Includable())
			},
			wantErr: "an include groups related keys by the owner's key (warehouse_id, sku), and the relation correlates on sku",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stock, moves, tags := crResources(t)
			owners := map[string]interface {
				Relate(string, parse.Relation) error
			}{"stock": stock, "moves": moves, "tags": tags}
			err := owners[tt.owner].Relate("rel", tt.rel(stock, moves, tags))
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// A relation to a key of several columns names no one value: its field
// takes the NULL tests and the relation predicates, not a key comparison,
// and the schema document lists its operators so.
func TestRelate_CompositeTargetField(t *testing.T) {
	stock, moves, tags := crResources(t)
	require.NoError(t, moves.Relate("stock", parse.BelongsTo(stock, []string{"warehouse_id", "sku"})))
	require.NoError(t, tags.Relate("stock", parse.ManyToMany(stock, parse.Through("cr_stock_tags", "tag_id", []string{"warehouse_id", "sku"}), parse.Indexed())))

	for _, doc := range []string{
		`{"stock":{"$is":{"name":"bolt"}}}`,
		`{"stock":{"$is_not":{"sku":"a"}}}`,
		`{"stock":{"$null":true}}`,
	} {
		_, err := moves.ParseFilter(doc)
		require.NoError(t, err, doc)
	}
	for _, doc := range []string{`{"stock":{"$some":{"warehouse_id":1}}}`, `{"stock":{"$none":{}}}`, `{"stock":{"$null":false}}`} {
		_, err := tags.ParseFilter(doc)
		require.NoError(t, err, doc)
	}
	for _, doc := range []string{`{"stock":"a"}`, `{"stock":{"$in":[[1,"a"]]}}`} {
		_, err := tags.ParseFilter(doc)
		require.Error(t, err, doc)
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, tags))
	field := reg.SchemaFor("cr_tags").Filter.Fields["stock"]
	assert.Equal(t, core.FieldTypeArray, field.DataType)
	assert.Subset(t, field.Operators, []filter.FilterOperator{filter.OpNull, filter.OpNotNull, filter.OpSome, filter.OpNone})
	for _, op := range []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn, filter.OpNin} {
		assert.NotContains(t, field.Operators, op)
	}
}

// A membership whose target key has several columns takes each key as an
// array of a value per column, each normalized as its column, and a repeat
// once; the schema document describes each column.
func TestRelate_CompositeMembership(t *testing.T) {
	stock, _, tags := crResources(t)
	require.NoError(t, tags.Relate("stock", parse.ManyToMany(stock, parse.Through("cr_stock_tags", "tag_id", []string{"warehouse_id", "sku"}), parse.Writable())))

	parsed, err := tags.ParseMergePatch([]byte(`{"stock":[[1,"a"],[1,"a"],[2,"b"]]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{core.Key{int64(1), "a"}, core.Key{int64(2), "b"}}, parsed.Operations[0].Value)

	for _, body := range []string{`{"stock":[[1]]}`, `{"stock":[[1,"a","x"]]}`, `{"stock":["1:a"]}`, `{"stock":[["one","a"]]}`} {
		_, err := tags.ParseMergePatch([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, tags))
	path := reg.SchemaFor("cr_tags").Patch.Paths["/stock"]
	require.NotNil(t, path)
	assert.True(t, path.Relation)
	assert.Equal(t, []*parse.RelationKeyResponse{{DataType: core.FieldTypeInt, NumberKind: "int64"}, {DataType: core.FieldTypeString}}, path.Key)
}

type crPairTag struct {
	ID    string       `json:"id"    db:"id"    parse:"filter,sort"`
	Stock []crStockRef `json:"stock" db:"-"     relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter,include,facet,patch" indexed:"true"`
}

type crStockRef struct {
	WarehouseID int64  `db:"warehouse_id" json:",string"`
	SKU         string `db:"sku"`
}

// A pair table tagged with a target key of several columns is typed by the
// struct its field holds: the filter takes only the NULL tests, and the
// membership each key as an array of a value per column, as a declared
// relation's; the schema document describes each column, its wire form
// included.
func TestRelationTag_CompositeTarget(t *testing.T) {
	tags, err := parse.NewResource[crPairTag](onSQLite, "cr_tags")
	require.NoError(t, err)

	for _, doc := range []string{`{"stock":{"$null":true}}`, `{"stock":{"$null":false}}`, `{"stock":{"$exists":true}}`} {
		_, err := tags.ParseFilter(doc)
		require.NoError(t, err, doc)
	}
	for _, doc := range []string{`{"stock":"a"}`, `{"stock":{"$in":[[1,"a"]]}}`, `{"stock":{"$some":{}}}`} {
		_, err := tags.ParseFilter(doc)
		require.Error(t, err, doc)
	}

	parsed, err := tags.ParseMergePatch([]byte(`{"stock":[["1","a"],["1","a"],["2","b"]]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{core.Key{int64(1), "a"}, core.Key{int64(2), "b"}}, parsed.Operations[0].Value)
	for _, body := range []string{`{"stock":[["1"]]}`, `{"stock":["1:a"]}`, `{"stock":[["one","a"]]}`} {
		_, err := tags.ParseMergePatch([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, tags))
	schema := reg.SchemaFor("cr_tags")
	field := schema.Filter.Fields["stock"]
	assert.Equal(t, core.FieldTypeArray, field.DataType)
	assert.ElementsMatch(t, []filter.FilterOperator{filter.OpNull, filter.OpNotNull, filter.OpExists}, field.Operators)
	path := schema.Patch.Paths["/stock"]
	require.NotNil(t, path)
	assert.True(t, path.Relation)
	assert.Equal(t, []*parse.RelationKeyResponse{{DataType: core.FieldTypeInt, NumberKind: "int64", Wire: core.WireString}, {DataType: core.FieldTypeString}}, path.Key)
}

// A tagged field whose target key has several columns refuses an element
// that is no struct, a column its struct does not hold and an operator that
// names one key value.
func TestRelationTag_CompositeTargetRefusals(t *testing.T) {
	type flat struct {
		ID    string   `json:"id"    db:"id"`
		Stock []string `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter"`
	}
	type missing struct {
		ID    string       `json:"id"    db:"id"`
		Stock []crStockRef `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+bin" parse:"filter"`
	}
	type compared struct {
		ID    string       `json:"id"    db:"id"`
		Stock []crStockRef `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter" filter_ops:"eq,null"`
	}
	_, err := parse.NewResource[flat](onSQLite, "cr_tags")
	require.ErrorContains(t, err, "target key warehouse_id+sku has 2 columns; the field holds string, not a struct holding a field per column")
	_, err = parse.NewResource[missing](onSQLite, "cr_tags")
	require.ErrorContains(t, err, "relation tag target column bin is no column of parse_test.crStockRef")
	_, err = parse.NewResource[compared](onSQLite, "cr_tags")
	require.ErrorContains(t, err, "relation target key warehouse_id+sku has 2 columns, which $eq cannot name")

	type nullTests struct {
		ID    string       `json:"id"    db:"id"`
		Stock []crStockRef `json:"stock" db:"-" relation:"cr_stock_tags,tag_id,warehouse_id+sku" parse:"filter" filter_ops:"null,exists" indexed:"true"`
	}
	_, err = parse.NewResource[nullTests](onSQLite, "cr_tags")
	assert.NoError(t, err, "every NULL test asks whether the owner has pairs")
}

// A relation joins tables of one database: its target is built on the
// owner's provider, not on another of any kind, and a refused relation
// leaves the owner as it was.
func TestRelate_RefusesATargetOnAnotherProvider(t *testing.T) {
	for name, target := range map[string]*parse.Provider{
		"another kind of provider":   onSQLite,
		"another database of a kind": providertest.Compile(filter.PostgresDialect{}),
	} {
		t.Run(name, func(t *testing.T) {
			devices := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "devices")
			commands := parse.MustResource[parsetest.UntenantedCommand](target, "commands")
			err := devices.Relate("commands", parse.HasMany(commands, "device_id"))
			require.ErrorIs(t, err, parse.ErrProviderMismatch)
			require.ErrorContains(t, err, `parse: devices: relation "commands": target commands`)
			assert.NotContains(t, devices.FilterSchema().Fields, "commands")

			own := parse.MustResource[parsetest.UntenantedCommand](onPostgres, "commands")
			require.NoError(t, devices.Relate("commands", parse.HasMany(own, "device_id")))
		})
	}
}

// A resource runs only in a transaction of its kind of provider, whether
// the provider began it or it is the application's own, and not in one
// another provider of its kind began.
func TestIn_RefusesATransactionOfAnotherProvider(t *testing.T) {
	devices := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "devices")
	_, err := devices.In(nil)
	require.ErrorContains(t, err, "parse: devices: nil transaction")

	for name, begin := range map[string]func(t *testing.T, p *parse.Provider, db *sql.DB) *parse.Tx{
		"begun by the provider": func(t *testing.T, p *parse.Provider, _ *sql.DB) *parse.Tx {
			t.Helper()
			tx, err := p.Begin(t.Context())
			require.NoError(t, err)
			return tx
		},
		"the application's own": func(t *testing.T, _ *parse.Provider, db *sql.DB) *parse.Tx {
			t.Helper()
			sqlTx, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			return parsesql.Tx(sqlTx)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, db := providertest.SQLite(t)
			tx := begin(t, p, db)
			defer func() { assert.NoError(t, tx.Rollback(t.Context())) }()

			_, err := devices.In(tx)
			require.ErrorIs(t, err, parse.ErrProviderMismatch)
			require.ErrorContains(t, err, "parse: devices: sqlite transaction for a resource on postgres")

			stored := parse.MustResource[parsetest.UntenantedDevice](p, "devices")
			_, err = stored.In(tx)
			require.NoError(t, err)
		})
	}

	t.Run("begun by another provider of its kind", func(t *testing.T) {
		p, _ := providertest.SQLite(t)
		other, _ := providertest.SQLite(t)
		tx, err := other.Begin(t.Context())
		require.NoError(t, err)
		defer func() { assert.NoError(t, tx.Rollback(t.Context())) }()

		_, err = parse.MustResource[parsetest.UntenantedDevice](p, "devices").In(tx)
		require.ErrorIs(t, err, parse.ErrProviderMismatch)
		assert.ErrorContains(t, err, "parse: devices: transaction another sqlite provider began")
	})
}

// A belongs-to resolves its foreign key as the owner's own filter on that
// column does, column mapper included.
func TestRelate_CorrelatesThroughTheOwnersMappedField(t *testing.T) {
	type mappedMember struct {
		ID     string `json:"id"      db:"id"      parse:"filter,sort"`
		TeamID string `json:"team_id" db:"team_id" parse:"filter"`
	}
	members := parse.MustResource[mappedMember](onSQLite, "members", mappedColumns)
	members.MustRelate("team", parse.BelongsTo(parse.MustResource[team](onSQLite, "teams"), "team_id"))
	assert.Equal(t, "(members.team_id)", members.FilterSchema().Fields["team"].RelatedBy[0].Expr)
}

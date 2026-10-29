//go:build unit

package parse_test

// parse/client against the corpora the TS SDK runs. Every corpus case is
// classified: the client builds it (and the server reads the same request),
// builds it and the server refuses it with the corpus's code, refuses it
// itself, or cannot express it at all because its types rule it out. A new
// corpus case fails these tests until it is classified.

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/client"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// confFields is the catalog a generated SDK would declare for confEntity.
var confFields = struct {
	ID      client.Field[string]
	Name    client.Text
	Status  client.Field[string]
	Count   client.Ordered[int]
	Price   client.Ordered[decimal.Decimal]
	Active  client.Field[bool]
	Created client.Time
	Meta    client.Document[map[string]any]
	Region  client.Text
	Note    client.Field[string]
	Lines   client.Elements[[]confLine]
	Parts   client.Relation[string]
}{
	ID:      client.NewField[string]("id"),
	Name:    client.NewText("name"),
	Status:  client.NewField[string]("status"),
	Count:   client.NewOrdered[int]("count"),
	Price:   client.NewOrdered[decimal.Decimal]("price"),
	Active:  client.NewField[bool]("active"),
	Created: client.NewTime("created"),
	Meta:    client.NewDocument[map[string]any]("meta"),
	Region:  client.NewText("meta.region"),
	Note:    client.NewField[string]("note"),
	Lines:   client.NewElements[[]confLine]("lines"),
	Parts:   client.NewRelation[string]("parts"),
}

var confLineFields = struct {
	SKU client.Field[string]
	Qty client.Ordered[int]
}{client.NewField[string]("sku"), client.NewOrdered[int]("qty")}

var confPartFields = struct {
	State client.Field[string]
}{client.NewField[string]("state")}

// filterBuilt are the corpus cases the client builds; the server reads each
// as the corpus records, valid or refused with its code.
func filterBuilt() map[string]client.Filter {
	f, line, part := confFields, confLineFields, confPartFields
	statusActive := f.Status.Eq("active")
	return map[string]client.Filter{
		"eq_string":                     statusActive,
		"eq_number":                     f.Count.Eq(5),
		"eq_bool":                       f.Active.Eq(true),
		"ne_number":                     f.Count.Ne(5),
		"gt_number":                     f.Count.Gt(5),
		"gte_number":                    f.Count.Gte(5),
		"lt_number":                     f.Count.Lt(5),
		"lte_number":                    f.Count.Lte(5),
		"eq_decimal_string":             f.Price.Eq(decimal.RequireFromString("10.50")),
		"eq_decimal_number":             f.Price.Eq(decimal.NewFromFloat(10.5)),
		"gt_decimal_exponent":           f.Price.Gt(decimal.RequireFromString("1.5E-3")),
		"eq_decimal_beyond_float64":     f.Price.Eq(decimal.RequireFromString("0.10000000000000001")),
		"in_decimal":                    f.Price.In(decimal.RequireFromString("9.5"), decimal.NewFromInt(2), decimal.RequireFromString("-0")),
		"err_decimal_beyond_digits":     f.Price.Eq(decimal.New(1, 1000)),
		"in_array":                      f.Status.In("active", "offline"),
		"nin_array":                     f.Status.Nin("error"),
		"like":                          f.Name.Like("dev%"),
		"ilike":                         f.Name.ILike("DEV%"),
		"contains":                      f.Name.Contains("sensor"),
		"null_true":                     f.Name.Null(),
		"null_false":                    f.Name.NotNull(),
		"exists_true":                   f.Name.NotNull(),
		"not_null_true":                 f.Name.NotNull(),
		"not_null_false":                f.Name.Null(),
		"null_test_capability_not_null": f.Note.NotNull(),
		"null_test_capability_null":     f.Note.NotNull(),
		"null_test_capability_exists":   f.Note.NotNull(),
		"and_explicit":                  client.And(statusActive, f.Count.Gt(5)),
		"or_explicit":                   client.Or(statusActive, f.Status.Eq("offline")),
		"not_explicit":                  client.Not(f.Status.Eq("error")),
		"nested_and_or":                 client.And(f.Active.Eq(true), client.Or(statusActive, f.Status.Eq("offline"))),
		"time_token_relative":           f.Created.Gt(client.Ago(7 * 24 * time.Hour)),
		"empty":                         {},
		"err_invalid_enum":              f.Status.Eq("bogus"),
		"err_nin_enum":                  f.Status.Nin("bogus"),
		"err_like_trailing_escape":      f.Name.Like(`abc\`),
		"json_eq_document":              f.Meta.Eq(map[string]any{"env": "prod"}),
		"json_contains":                 f.Meta.Contains(map[string]any{"env": "prod"}),
		"err_json_integer_beyond_int64": f.Meta.Eq(map[string]any{"n": uint64(math.MaxUint64)}),
		"json_decimal":                  f.Meta.Eq(map[string]any{"n": 0.1}),
		"json_has_key":                  f.Meta.HasKey("env"),
		"path_eq":                       f.Region.Eq("eu"),
		"path_like":                     f.Region.Like("eu-%"),
		"elements_some":                 f.Lines.Some(client.And(line.SKU.Eq("a"), line.Qty.Gt(2))),
		"elements_none":                 f.Lines.None(line.SKU.Eq("a")),
		"relation_some":                 f.Parts.Some(part.State.Eq("worn")),
		"relation_none_any":             f.Parts.None(client.Filter{}),
		"relation_key_shorthand":        f.Parts.Eq("p1"),
		"relation_key_list":             f.Parts.In("p1", "p2"),
		"err_relation_target_enum":      f.Parts.Some(part.State.Eq("new")),
	}
}

// filterRefused are the corpus cases the client refuses to encode.
var filterRefused = map[string]error{
	"err_or_empty":  client.ErrEmptyOr,
	"err_not_empty": client.ErrNegatedEmpty,
}

// filterInexpressible are the corpus cases the client's types rule out.
var filterInexpressible = map[string]string{
	"err_not_null_not_bool":               "null tests take no operand",
	"err_unknown_field":                   "the catalog names declared fields only",
	"err_operator_not_allowed":            "a boolean field has no Gt",
	"err_in_not_array":                    "In is variadic",
	"err_null_not_bool":                   "null tests take no operand",
	"err_eq_null":                         "Null is the null test; Eq takes a value",
	"err_int_type":                        "Count is an Ordered[int]",
	"err_int_fraction":                    "Count is an Ordered[int]",
	"err_bool_type":                       "Active is a Field[bool]",
	"err_decimal_word":                    "Price is an Ordered[decimal.Decimal]",
	"err_decimal_bool":                    "Price is an Ordered[decimal.Decimal]",
	"err_decimal_null":                    "Null is the null test; Eq takes a value",
	"err_decimal_hexadecimal":             "Price is an Ordered[decimal.Decimal]",
	"err_decimal_leading_plus":            "a decimal.Decimal marshals canonical text",
	"err_decimal_leading_point":           "a decimal.Decimal marshals canonical text",
	"err_decimal_leading_zero":            "a decimal.Decimal marshals canonical text",
	"err_decimal_exponent_beyond_int32":   "a decimal.Decimal's exponent is an int32",
	"err_decimal_in_member":               "Price is an Ordered[decimal.Decimal]",
	"err_unknown_dollar_key_is_literal":   "operators are methods",
	"err_operator_key_mixed_with_literal": "operators are methods",
	"err_or_not_array":                    "Or is variadic",
	"err_or_branch_not_object":            "Or takes filters",
	"err_not_not_object":                  "Not takes a filter",
	"err_json_eq_null":                    "Null is the null test",
	"err_json_has_key_not_string":         "HasKey takes a string",
	"err_path_type":                       "the path is Text",
	"err_undeclared_path":                 "the catalog names declared paths only",
	"err_elements_unknown_member":         "the element catalog names declared members only",
	"err_elements_member_type":            "Qty is an Ordered[int]",
	"err_elements_not_object":             "Some takes a filter",
	"err_elements_is":                     "Elements has no Is",
	"err_relation_unknown_target_field":   "the target catalog names declared fields only",
	"err_relation_is_on_collection":       "a to-many Relation has no Is",
	"err_relation_not_object":             "Some takes a filter",
}

func readFilterCorpus(t *testing.T) []conformanceCase {
	t.Helper()
	data, err := os.ReadFile(conformanceCorpusPath)
	require.NoError(t, err)
	var corpus []conformanceCase
	require.NoError(t, json.Unmarshal(data, &corpus))
	return corpus
}

func TestClient_EveryFilterCaseIsClassified(t *testing.T) {
	built := filterBuilt()
	for _, c := range readFilterCorpus(t) {
		_, b := built[c.Name]
		_, r := filterRefused[c.Name]
		_, x := filterInexpressible[c.Name]
		assert.Equal(t, 1, btoi(b)+btoi(r)+btoi(x), "corpus case %q: classify it in exactly one of filterBuilt, filterRefused, filterInexpressible", c.Name)
	}
}

// The server reads what the client builds as it reads the corpus input:
// the same verdict and code, the same canonical AST and the same SQL.
func TestClient_FiltersMeanWhatTheCorpusMeans(t *testing.T) {
	schema := confSchema(t)
	built := filterBuilt()
	for _, want := range readFilterCorpus(t) {
		f, ok := built[want.Name]
		if !ok {
			continue
		}
		t.Run(want.Name, func(t *testing.T) {
			body, err := json.Marshal(f)
			require.NoError(t, err)
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber()
			var input map[string]any
			require.NoError(t, dec.Decode(&input))

			got := runCase(t, schema, caseInput{name: want.Name, input: input})
			require.Equal(t, want.Valid, got.Valid, "client sent %s", body)
			assert.Equal(t, want.ErrorCode, got.ErrorCode)
			assert.Equal(t, normalize(t, want.CanonicalAST), normalize(t, got.CanonicalAST))
			assert.Equal(t, want.SQLPostgres, got.SQLPostgres)
			assert.Equal(t, want.SQLSqlite, got.SQLSqlite)
		})
	}
}

func TestClient_RefusesWhatTheGrammarCannotSay(t *testing.T) {
	built := map[string]client.Filter{
		"err_or_empty":  client.Or(),
		"err_not_empty": client.Not(client.Filter{}),
	}
	for _, c := range readFilterCorpus(t) {
		want, ok := filterRefused[c.Name]
		if !ok {
			continue
		}
		assert.False(t, c.Valid, "%s: the server refuses the document the client will not send", c.Name)
		_, err := json.Marshal(built[c.Name])
		assert.ErrorIs(t, err, want, c.Name)
	}
}

// confPatchInput is the merge-patch struct a generated SDK would declare for
// confPatch.
type confPatchInput struct {
	Name     client.Nullable[string]          `json:"name,omitzero"`
	Nick     client.Nullable[string]          `json:"nick,omitzero"`
	Status   client.Nullable[string]          `json:"status,omitzero"`
	Count    client.Nullable[int32]           `json:"count,omitzero"`
	Small    client.Nullable[int8]            `json:"small,omitzero"`
	Ratio    client.Nullable[float64]         `json:"ratio,omitzero"`
	Price    client.Nullable[decimal.Decimal] `json:"price,omitzero"`
	Active   client.Nullable[bool]            `json:"active,omitzero"`
	Tags     client.Nullable[[]string]        `json:"tags,omitzero"`
	Labels   client.Nullable[map[string]any]  `json:"labels,omitzero"`
	SeenAt   client.Nullable[time.Time]       `json:"seen_at,omitzero"`
	Currency client.Nullable[string]          `json:"currency,omitzero"`
	Level    client.Nullable[int32]           `json:"level,omitzero"`
	Rate     client.Nullable[float32]         `json:"rate,omitzero"`
	Amount   client.Nullable[decimal.Decimal] `json:"amount,omitzero"`
	Codes    client.Nullable[[]string]        `json:"codes,omitzero"`
	Modes    client.Nullable[[]string]        `json:"modes,omitzero"`
	Mode     client.Nullable[string]          `json:"mode,omitzero"`
}

var patchBuilt = map[string]confPatchInput{
	"string":                    {Name: client.Set("a")},
	"err_null_on_value_field":   {Name: client.Null[string]()},
	"null_on_pointer_field":     {Nick: client.Null[string]()},
	"enum":                      {Status: client.Set("active")},
	"err_enum":                  {Status: client.Set("busy")},
	"int":                       {Count: client.Set[int32](5)},
	"int8_min":                  {Small: client.Set[int8](-128)},
	"float":                     {Ratio: client.Set(0.5)},
	"decimal_string":            {Price: client.Set(decimal.RequireFromString("10.25"))},
	"err_decimal_null":          {Price: client.Null[decimal.Decimal]()},
	"bool":                      {Active: client.Set(false)},
	"array":                     {Tags: client.Set([]string{"a", "b"})},
	"clear_array":               {Tags: client.Null[[]string]()},
	"document_merge":            {Labels: client.Set(map[string]any{"a": 1, "old": nil})},
	"timestamp":                 {SeenAt: client.Set(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))},
	"several":                   {Name: client.Set("a"), Count: client.Set[int32](1), Active: client.Set(true)},
	"array_enum":                {Modes: client.Set([]string{"auto", "manual"})},
	"err_array_enum":            {Modes: client.Set([]string{"auto", "off"})},
	"null_clears_nullable_enum": {Mode: client.Null[string]()},
	// Bounds are the server's: the client builds a value outside them.
	"length_bounds":                  {Currency: client.Set("USD")},
	"length_counts_code_points":      {Currency: client.Set("€😀ß")},
	"err_maxlen":                     {Currency: client.Set("USDX")},
	"err_minlen":                     {Currency: client.Set("US")},
	"int_bounds":                     {Level: client.Set[int32](10)},
	"err_int_max":                    {Level: client.Set[int32](11)},
	"err_int_min":                    {Level: client.Set[int32](0)},
	"float32_bound_at_its_precision": {Rate: client.Set[float32](0.1)},
	"err_float_max":                  {Rate: client.Set[float32](0.11)},
	"decimal_bounds":                 {Amount: client.Set(decimal.RequireFromString("999.99"))},
	"err_decimal_max_exact":          {Amount: client.Set(decimal.RequireFromString("999.990000000000000001"))},
	"err_decimal_min":                {Amount: client.Set(decimal.RequireFromString("0.001"))},
	"array_element_length":           {Codes: client.Set([]string{"ab", "c"})},
	"err_array_element_length":       {Codes: client.Set([]string{"ab", "abc"})},
}

var patchInexpressible = map[string]string{
	"err_string_type":           "Name is a Nullable[string]",
	"err_int_fraction":          "Count is a Nullable[int32]",
	"err_int_type":              "Count is a Nullable[int32]",
	"err_int32_range":           "Count is a Nullable[int32]",
	"err_int8_range":            "Small is a Nullable[int8]",
	"err_float_type":            "Ratio is a Nullable[float64]",
	"decimal_number":            "a decimal.Decimal marshals as a JSON string",
	"decimal_exponent":          "a decimal.Decimal marshals plain notation",
	"err_decimal_word":          "Price is a Nullable[decimal.Decimal]",
	"err_decimal_bool":          "Price is a Nullable[decimal.Decimal]",
	"err_decimal_leading_plus":  "a decimal.Decimal marshals canonical text",
	"err_decimal_beyond_digits": "a decimal.Decimal marshals plain notation",
	"err_bool_type":             "Active is a Nullable[bool]",
	"err_array_type":            "Tags is a Nullable[[]string]",
	"err_array_element_type":    "Tags is a Nullable[[]string]",
	"err_array_element_range":   "the struct declares no int16 array",
	"err_timestamp_format":      "SeenAt is a Nullable[time.Time]",
	"err_timestamp_type":        "SeenAt is a Nullable[time.Time]",
	"err_not_patchable":         "the patch struct holds patchable members only",
	"err_unknown_field":         "the patch struct holds declared members only",
	"decimal_bound_exponent":    "a decimal.Decimal marshals plain notation",
	"decimal_bound_number":      "a decimal.Decimal marshals as a JSON string",
}

// A merge patch is its JSON, so the client's document equals the corpus
// input and the server's verdict is the corpus's.
func TestClient_MergePatchesAreTheCorpusDocuments(t *testing.T) {
	data, err := os.ReadFile(patchConformancePath)
	require.NoError(t, err)
	var corpus patchCorpus
	require.NoError(t, json.Unmarshal(data, &corpus))
	for _, c := range corpus.Cases {
		in, built := patchBuilt[c.Name]
		_, inexpressible := patchInexpressible[c.Name]
		require.True(t, built != inexpressible, "patch case %q: classify it in exactly one of patchBuilt, patchInexpressible", c.Name)
		if !built {
			continue
		}
		got, err := json.Marshal(in)
		require.NoError(t, err)
		want, err := json.Marshal(c.Input)
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(got), c.Name)
	}
}

func TestClient_JSONPatchOperationsParse(t *testing.T) {
	res, err := parse.NewResource[schemaDocEntity]("doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	tags := client.NewRelation[string]("tags")
	ops := []client.Operation{
		client.Replace(client.Pointer("name"), "pump"),
		client.Remove(client.Pointer("note")),
		client.Replace(client.Pointer("scores"), []int16{1, 2}),
		tags.Add("t1", "t2"),
		tags.Remove("t3"),
		tags.Replace("t4"),
	}
	body, err := json.Marshal(ops)
	require.NoError(t, err)
	parsed, err := res.ParsePatch(body)
	require.NoError(t, err, "%s", body)

	type opAt struct{ op, path string }
	var got []opAt
	for _, op := range parsed.Operations {
		got = append(got, opAt{string(op.Op), op.Path})
	}
	assert.Equal(t, []opAt{
		{string(patch.PatchOpReplace), "/name"}, {string(patch.PatchOpRemove), "/note"}, {string(patch.PatchOpReplace), "/scores"},
		{string(patch.PatchOpAdd), "/tags"}, {string(patch.PatchOpRemove), "/tags"}, {string(patch.PatchOpReplace), "/tags"},
	}, got)
	assert.Equal(t, []any{"t3"}, parsed.Operations[4].Value, "a relation's remove carries its keys")
}

// The client's list parameters are the server's: everything ListParams
// encodes, ParseList reads as ParseListInput reads the same request.
func TestClient_ListParamsAreTheServersParameters(t *testing.T) {
	res := widgets(t)
	name, rank := client.NewSortableText("name"), client.NewSortableOrdered[int64]("rank")
	params := client.ListParams{
		Filter:         client.And(name.Eq("beta"), rank.Gte(1)),
		Sort:           client.Sort{rank.Desc(), name.Asc()},
		Cursor:         "abc",
		PageSize:       3,
		IncludeTotal:   true,
		IncludeDeleted: true,
		Search:         "b",
		Facets:         []string{"name"},
		Include:        []string{"tags"},
		Fields:         []string{"id", "name"},
	}
	q, err := params.Values()
	require.NoError(t, err)
	for key := range q {
		assert.True(t, filter.IsListParam(key), "%q is no list parameter", key)
	}
	assert.Len(t, q, 10, "every member encodes to its own parameter")

	got, err := res.ParseList(q)
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{
		Filter: `{"$and":[{"name":{"$eq":"beta"}},{"rank":{"$gte":1}}]}`, Sort: "-rank,name", Cursor: "abc",
		PageSize: 3, IncludeTotal: true, IncludeDeleted: true, Search: "b",
		Facets: []string{"name"}, Include: []string{"tags"}, Fields: []string{"id", "name"},
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)

	empty, err := client.ListParams{}.Values()
	require.NoError(t, err)
	assert.Empty(t, empty, "zero members leave the server's defaults")
}

func TestClient_GeoFiltersParse(t *testing.T) {
	type place struct {
		ID       string         `json:"id"       db:"id"`
		Location map[string]any `json:"location" parse:"filter" filter_ops:"near,within,intersects" geo:"jsonb" geo_srid:"4326"`
	}
	res, err := parse.NewResource[place]("places")
	require.NoError(t, err)
	loc := client.NewGeo("location")
	nyc := client.Point{Lat: 40.7128, Lon: -74.006}
	box := client.Box(client.Point{Lat: 40.8, Lon: -74.1}, client.Point{Lat: 40.65, Lon: -73.7})
	triangle := client.Polygon(client.Point{}, client.Point{Lat: 1, Lon: 1}, client.Point{Lon: 1})
	for _, f := range []client.Filter{loc.Near(nyc, 1500.5), loc.Within(box), loc.Within(triangle), loc.Intersects(box)} {
		body, err := json.Marshal(f)
		require.NoError(t, err)
		_, err = res.ParseFilter(string(body))
		assert.NoError(t, err, "%s", body)
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestClient_CatalogsCoverTheCorpusFields(t *testing.T) {
	schema := confSchema(t)
	names := []string{
		confFields.ID.Name(), confFields.Name.Name(), confFields.Status.Name(), confFields.Count.Name(),
		confFields.Price.Name(), confFields.Active.Name(), confFields.Created.Name(), confFields.Meta.Name(), confFields.Region.Name(),
		confFields.Note.Name(), confFields.Lines.Name(), confFields.Parts.Name(),
	}
	for name := range schema.Fields {
		assert.True(t, slices.Contains(names, name), "the catalog misses %q", name)
	}
}

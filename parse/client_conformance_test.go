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
	"errors"
	"math"
	"os"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/client"
	"github.com/ashtonian/pkg/parse/core"
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
	Tags    client.TextArray
	Labels  client.TextArray
	Sizes   client.OrderedArray[int]
	Parts   client.Relation[string]
	Serial  client.Ordered[client.Quoted[int64]]
	Live    client.Field[client.Quoted[bool]]
	Big     client.Ordered[client.Quoted[uint64]]
	Ratio   client.Ordered[client.Quoted[float64]]
	Score   client.Ordered[float64]
	Day     client.Ordered[core.Date]
	At      client.Ordered[core.LocalTime]
	Span    client.Field[core.Interval]
	Addr    client.Field[core.Inet]
	Subnet  client.Field[core.CIDR]
	Mac     client.Field[core.MACAddr]
	Key     client.Field[uuid.UUID]
	Spot    client.Geo
	Dims    client.Document[confDims]
	Blob    client.Field[[]byte]
	Owner   client.Reference[string]
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
	Tags:    client.NewTextArray("tags"),
	Labels:  client.NewTextArray("meta.labels"),
	Sizes:   client.NewOrderedArray[int]("meta.sizes"),
	Serial:  client.NewOrdered[client.Quoted[int64]]("serial"),
	Live:    client.NewField[client.Quoted[bool]]("live"),
	Big:     client.NewOrdered[client.Quoted[uint64]]("big"),
	Ratio:   client.NewOrdered[client.Quoted[float64]]("ratio"),
	Parts:   client.NewRelation[string]("parts"),
	Score:   client.NewOrdered[float64]("score"),
	Day:     client.NewOrdered[core.Date]("day"),
	At:      client.NewOrdered[core.LocalTime]("at"),
	Span:    client.NewField[core.Interval]("span"),
	Addr:    client.NewField[core.Inet]("addr"),
	Subnet:  client.NewField[core.CIDR]("subnet"),
	Mac:     client.NewField[core.MACAddr]("mac"),
	Key:     client.NewField[uuid.UUID]("key"),
	Spot:    client.NewGeo("spot"),
	Dims:    client.NewDocument[confDims]("dims"),
	Blob:    client.NewField[[]byte]("blob"),
	Owner:   client.NewReference[string]("owner"),
}

var confLineFields = struct {
	SKU client.Field[string]
	Qty client.Ordered[int]
}{client.NewField[string]("sku"), client.NewOrdered[int]("qty")}

var confPartFields = struct {
	State client.Field[string]
}{client.NewField[string]("state")}

var confOwnerFields = struct {
	Name client.Text
}{client.NewText("name")}

// must is v, failing on err: the corpus's values are valid by construction.
func must[V any](v V, err error) V {
	if err != nil {
		panic(err)
	}
	return v
}

// filterBuilt are the corpus cases the client builds; the server reads each
// as the corpus records, valid or refused with its code.
func filterBuilt() map[string]client.Filter {
	f, line, part, owner := confFields, confLineFields, confPartFields, confOwnerFields
	tag, label, size := f.Tags.Element(), f.Labels.Element(), f.Sizes.Element()
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
		"json_eq_dollar_keyed_document": f.Meta.Eq(map[string]any{"$env": "prod"}),
		"json_eq_mixed_keyed_document":  f.Meta.Eq(map[string]any{"$env": "prod", "region": "eu"}),
		"json_contains":                 f.Meta.Contains(map[string]any{"env": "prod"}),
		"err_json_integer_beyond_int64": f.Meta.Eq(map[string]any{"n": uint64(math.MaxUint64)}),
		"json_decimal":                  f.Meta.Eq(map[string]any{"n": 0.1}),
		"json_has_key":                  f.Meta.HasKey("env"),
		"path_eq":                       f.Region.Eq("eu"),
		"path_like":                     f.Region.Like("eu-%"),
		"elements_some":                 f.Lines.Some(client.And(line.SKU.Eq("a"), line.Qty.Gt(2))),
		"elements_none":                 f.Lines.None(line.SKU.Eq("a")),
		"array_some_eq":                 f.Tags.Some(tag.Eq("a")),
		"array_some_and":                f.Sizes.Some(client.And(size.Gte(1), size.Lt(5))),
		"array_some_or":                 f.Labels.Some(client.Or(label.Like("a%"), label.Eq("b"))),
		"array_none_not":                f.Tags.None(client.Not(tag.In("a", "b"))),
		"array_some_any":                f.Labels.Some(client.Filter{}),
		"array_path_contains":           f.Labels.Contains("a", "b"),
		"array_contains_scalar":         f.Tags.Contains("a"),
		"array_path_null":               f.Sizes.Null(),
		"err_array_element_null_test":   f.Tags.Some(tag.Null()),
		"relation_some":                 f.Parts.Some(part.State.Eq("worn")),
		"relation_none_any":             f.Parts.None(client.Filter{}),
		"relation_key_shorthand":        f.Parts.Eq("p1"),
		"relation_key_list":             f.Parts.In("p1", "p2"),
		"err_relation_target_enum":      f.Parts.Some(part.State.Eq("new")),
		"quoted_eq_int64_max":           f.Serial.Eq(client.Quote(int64(math.MaxInt64))),
		"quoted_eq_int64_min":           f.Serial.Eq(client.Quote(int64(math.MinInt64))),
		"quoted_gt_past_2_53":           f.Serial.Gt(client.Quote(int64(9007199254740992))),
		"quoted_in":                     f.Serial.In(client.Quote(int64(1)), client.Quote(int64(9007199254740993))),
		"quoted_eq_bool":                f.Live.Eq(client.Quote(true)),
		"quoted_uint64_max":             f.Big.Eq(client.Quote(uint64(math.MaxUint64))),
		"quoted_float":                  f.Ratio.Lt(client.Quote(0.5)),
		"reference_is":                  f.Owner.Is(owner.Name.Eq("acme")),
		"reference_is_not":              f.Owner.IsNot(owner.Name.Like("ac%")),
		"reference_key_shorthand":       f.Owner.Eq("o1"),
		"float_gt":                      f.Score.Gt(0.5),
		"date_gt":                       f.Day.Gt("2026-01-01"),
		"err_date_operator":             f.Day.Gte("2026-01-01"),
		"time_gte":                      f.At.Gte("09:30:00"),
		"interval_in":                   f.Span.In(must(core.ParseInterval("P1D")), must(core.ParseInterval("PT2H30M"))),
		"inet_eq":                       f.Addr.Eq(must(core.ParseInet("192.0.2.1"))),
		"cidr_eq":                       f.Subnet.Eq(must(core.ParseCIDR("192.0.2.0/24"))),
		"macaddr_ne":                    f.Mac.Ne("08:00:2b:01:02:03"),
		"uuid_in":                       f.Key.In(uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")),
		"geo_near":                      f.Spot.Near(client.Point{Lat: 40.7128, Lon: -74.006}, 1500),
		"geo_within_box":                f.Spot.Within(client.Box(client.Point{Lat: 40.8, Lon: -74.1}, client.Point{Lat: 40.65, Lon: -73.7})),
		"geo_intersects_polygon":        f.Spot.Intersects(client.Polygon(client.Point{}, client.Point{Lat: 1, Lon: 1}, client.Point{Lon: 1})),
		"err_geo_intersects_two_points": f.Spot.Intersects(client.Polygon(client.Point{}, client.Point{Lat: 1, Lon: 1})),
		"object_has_key":                f.Dims.HasKey("w"),
		"object_eq_document":            f.Dims.Eq(confDims{W: 2, H: 3}),
		"err_object_operator":           f.Dims.Contains(confDims{W: 2}),
		"any_null":                      f.Blob.Null(),
		"err_any_operator":              f.Blob.Eq([]byte("x")),
	}
}

// filterRefused are the corpus cases the client refuses to encode.
var filterRefused = map[string]error{
	"err_or_empty":                client.ErrEmptyOr,
	"err_not_empty":               client.ErrNegatedEmpty,
	"err_array_element_member":    client.ErrElementCondition,
	"err_array_element_empty_or":  client.ErrEmptyOr,
	"err_array_element_empty_not": client.ErrNegatedEmpty,
}

// filterInexpressible are the corpus cases the client's types rule out.
var filterInexpressible = map[string]string{
	"err_not_null_not_bool":                  "null tests take no operand",
	"err_unknown_field":                      "the catalog names declared fields only",
	"err_operator_not_allowed":               "a boolean field has no Gt",
	"err_in_not_array":                       "In is variadic",
	"err_null_not_bool":                      "null tests take no operand",
	"err_eq_null":                            "Null is the null test; Eq takes a value",
	"err_int_type":                           "Count is an Ordered[int]",
	"err_quoted_bare_number":                 "Serial is an Ordered[Quoted[int64]]",
	"err_quoted_bare_number_in_set":          "Serial is an Ordered[Quoted[int64]]",
	"err_quoted_not_a_number":                "Serial is an Ordered[Quoted[int64]]",
	"err_quoted_spaces":                      "a Quoted marshals its value's JSON text",
	"err_quoted_past_int64":                  "Serial is an Ordered[Quoted[int64]]",
	"quoted_bool_bare_boolean":               "a Quoted[bool] marshals a JSON string",
	"quoted_integral_spelling":               "a Quoted marshals its value's JSON text",
	"err_quoted_leading_plus":                "a Quoted marshals its value's JSON text",
	"err_quoted_leading_zero":                "a Quoted marshals its value's JSON text",
	"err_quoted_null":                        "Null is the null test; Eq takes a value",
	"err_quoted_float_word":                  "Ratio is an Ordered[Quoted[float64]]",
	"err_quoted_uint_negative":               "Big is an Ordered[Quoted[uint64]]",
	"err_int_fraction":                       "Count is an Ordered[int]",
	"err_bool_type":                          "Active is a Field[bool]",
	"err_decimal_word":                       "Price is an Ordered[decimal.Decimal]",
	"err_decimal_bool":                       "Price is an Ordered[decimal.Decimal]",
	"err_decimal_null":                       "Null is the null test; Eq takes a value",
	"err_decimal_hexadecimal":                "Price is an Ordered[decimal.Decimal]",
	"err_decimal_leading_plus":               "a decimal.Decimal marshals canonical text",
	"err_decimal_leading_point":              "a decimal.Decimal marshals canonical text",
	"err_decimal_leading_zero":               "a decimal.Decimal marshals canonical text",
	"err_decimal_exponent_beyond_int32":      "a decimal.Decimal's exponent is an int32",
	"err_decimal_in_member":                  "Price is an Ordered[decimal.Decimal]",
	"err_unknown_dollar_key":                 "operators are methods",
	"err_misspelt_operator_on_document":      "operators are methods",
	"err_operator_key_mixed_with_literal":    "operators are methods",
	"err_or_not_array":                       "Or is variadic",
	"err_or_branch_not_object":               "Or takes filters",
	"err_not_not_object":                     "Not takes a filter",
	"err_json_eq_null":                       "Null is the null test",
	"err_json_has_key_not_string":            "HasKey takes a string",
	"err_path_type":                          "the path is Text",
	"err_undeclared_path":                    "the catalog names declared paths only",
	"err_elements_unknown_member":            "the element catalog names declared members only",
	"err_elements_member_type":               "Qty is an Ordered[int]",
	"err_elements_not_object":                "Some takes a filter",
	"err_elements_is":                        "Elements has no Is",
	"err_relation_unknown_target_field":      "the target catalog names declared fields only",
	"err_relation_is_on_collection":          "a to-many Relation has no Is",
	"err_relation_not_object":                "Some takes a filter",
	"err_array_element_operator":             "Sizes' element is an Ordered[int]",
	"err_array_element_type":                 "Sizes' element is an Ordered[int]",
	"err_array_element_not_object":           "Some takes a condition",
	"err_array_contains_member_type":         "Sizes' Contains takes ints",
	"err_relation_none_unknown_target_field": "the target catalog names declared fields only",
	"err_relation_is_not_on_collection":      "a to-many Relation has no IsNot",
	"err_reference_is_unknown_target_field":  "the target catalog names declared fields only",
	"err_reference_some":                     "a Reference has no Some",
	"err_ilike_not_string":                   "ILike takes a string",
	"err_exists_not_bool":                    "null tests take no operand",
	"err_and_not_array":                      "And is variadic",
	"err_float_lte_type":                     "Score is an Ordered[float64]",
	"err_timestamp_type":                     "Created is a Time",
	"err_timestamp_spelling":                 "a Moment marshals RFC 3339 or a relative token",
	"err_interval_operator":                  "Span is a Field[core.Interval]",
	"err_interval_spelling":                  "a core.Interval marshals its canonical text",
	"err_inet_spelling":                      "a core.Inet holds a parsed address",
	"err_cidr_host_bits":                     "a core.CIDR holds a parsed network",
	"err_macaddr_type":                       "Mac is a Field[core.MACAddr]",
	"err_uuid_type":                          "Key is a Field[uuid.UUID]",
	"err_uuid_spelling":                      "a uuid.UUID marshals its canonical text",
	"err_geo_operator":                       "Geo has no Eq",
	"err_geo_near_without_distance":          "Near takes a distance",
	"err_geo_within_not_a_shape":             "Within takes a Shape",
	"err_date_spelling":                      "a core.Date marshals only a civil date",
	"err_time_spelling":                      "a core.LocalTime marshals only a time of day",
	"err_macaddr_spelling":                   "a core.MACAddr marshals only a MAC address",
}

// Every classification names a corpus case: a case renamed or removed from
// the corpus is removed from its classification too.
func TestClient_EveryClassificationNamesACorpusCase(t *testing.T) {
	filterCases := map[string]bool{}
	for _, c := range readFilterCorpus(t).Cases {
		filterCases[c.Name] = true
	}
	for name := range filterBuilt() {
		assert.True(t, filterCases[name], "filterBuilt names %q, which is no corpus case", name)
	}
	for name := range filterRefused {
		assert.True(t, filterCases[name], "filterRefused names %q, which is no corpus case", name)
	}
	for name := range filterInexpressible {
		assert.True(t, filterCases[name], "filterInexpressible names %q, which is no corpus case", name)
	}

	data, err := os.ReadFile(patchConformancePath)
	require.NoError(t, err)
	var patches patchCorpus
	require.NoError(t, json.Unmarshal(data, &patches))
	patchCases := map[string]bool{}
	for _, c := range patches.Cases {
		patchCases[c.Name] = true
	}
	for name := range patchBuilt {
		assert.True(t, patchCases[name], "patchBuilt names %q, which is no corpus case", name)
	}
	for name := range patchInexpressible {
		assert.True(t, patchCases[name], "patchInexpressible names %q, which is no corpus case", name)
	}
}

func TestClient_EveryFilterCaseIsClassified(t *testing.T) {
	built := filterBuilt()
	for _, c := range readFilterCorpus(t).Cases {
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
	for _, want := range readFilterCorpus(t).Cases {
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
			assert.Equal(t, want.SQLPostgresCode, got.SQLPostgresCode)
			assert.Equal(t, want.SQLSqliteCode, got.SQLSqliteCode)
		})
	}
}

func TestClient_RefusesWhatTheGrammarCannotSay(t *testing.T) {
	tags := confFields.Tags
	built := map[string]client.Filter{
		"err_or_empty":                client.Or(),
		"err_not_empty":               client.Not(client.Filter{}),
		"err_array_element_member":    tags.Some(confLineFields.SKU.Eq("a")),
		"err_array_element_empty_or":  tags.Some(client.Or()),
		"err_array_element_empty_not": tags.Some(client.Not(client.Filter{})),
	}
	for _, c := range readFilterCorpus(t).Cases {
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
	Name     client.Nullable[string]                 `json:"name,omitzero"`
	Nick     client.Nullable[string]                 `json:"nick,omitzero"`
	Status   client.Nullable[string]                 `json:"status,omitzero"`
	Count    client.Nullable[int32]                  `json:"count,omitzero"`
	Small    client.Nullable[int8]                   `json:"small,omitzero"`
	Ratio    client.Nullable[float64]                `json:"ratio,omitzero"`
	Price    client.Nullable[decimal.Decimal]        `json:"price,omitzero"`
	Active   client.Nullable[bool]                   `json:"active,omitzero"`
	Tags     client.Nullable[[]string]               `json:"tags,omitzero"`
	Labels   client.Nullable[map[string]any]         `json:"labels,omitzero"`
	SeenAt   client.Nullable[time.Time]              `json:"seen_at,omitzero"`
	Currency client.Nullable[string]                 `json:"currency,omitzero"`
	Level    client.Nullable[int32]                  `json:"level,omitzero"`
	Rate     client.Nullable[float32]                `json:"rate,omitzero"`
	Amount   client.Nullable[decimal.Decimal]        `json:"amount,omitzero"`
	Codes    client.Nullable[[]string]               `json:"codes,omitzero"`
	Modes    client.Nullable[[]string]               `json:"modes,omitzero"`
	Mode     client.Nullable[string]                 `json:"mode,omitzero"`
	Zones    client.Nullable[[]string]               `json:"zones,omitzero"`
	Serial   client.Nullable[client.Quoted[int64]]   `json:"serial,omitzero"`
	Big      client.Nullable[client.Quoted[uint64]]  `json:"big,omitzero"`
	Quota    client.Nullable[client.Quoted[int64]]   `json:"quota,omitzero"`
	Live     client.Nullable[client.Quoted[bool]]    `json:"live,omitzero"`
	Share    client.Nullable[client.Quoted[float32]] `json:"share,omitzero"`
	Opt      client.Nullable[client.Quoted[int64]]   `json:"opt,omitzero"`
	Members  client.Nullable[[]string]               `json:"members,omitzero"`
	// QuotedTags is a relation whose target key is quoted: its keys are
	// client.Quoted, as a client.Relation[client.Quoted[int64]] sends them.
	QuotedTags client.Nullable[[]client.Quoted[int64]] `json:"quoted_tags,omitzero"`
	Pinned     client.Nullable[string]                 `json:"pinned,omitzero"`
}

var patchBuilt = map[string]confPatchInput{
	"replace_where_remove_is_not_allowed": {Pinned: client.Set("a")},
	"err_remove_not_allowed":              {Pinned: client.Null[string]()},
	"operations_at_the_maximum":           {Labels: client.Set(nestedDocuments(49))},
	"err_too_many_operations":             {Labels: client.Set(nestedDocuments(50))},
	"string":                              {Name: client.Set("a")},
	"err_null_on_value_field":             {Name: client.Null[string]()},
	"null_on_pointer_field":               {Nick: client.Null[string]()},
	"enum":                                {Status: client.Set("active")},
	"err_enum":                            {Status: client.Set("busy")},
	"int":                                 {Count: client.Set[int32](5)},
	"int8_min":                            {Small: client.Set[int8](-128)},
	"float":                               {Ratio: client.Set(0.5)},
	"decimal_string":                      {Price: client.Set(decimal.RequireFromString("10.25"))},
	"err_decimal_null":                    {Price: client.Null[decimal.Decimal]()},
	"bool":                                {Active: client.Set(false)},
	"array":                               {Tags: client.Set([]string{"a", "b"})},
	"clear_array":                         {Tags: client.Null[[]string]()},
	"document_merge":                      {Labels: client.Set(map[string]any{"a": 1, "old": nil})},
	"timestamp":                           {SeenAt: client.Set(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))},
	"several":                             {Name: client.Set("a"), Count: client.Set[int32](1), Active: client.Set(true)},
	"array_enum":                          {Modes: client.Set([]string{"auto", "manual"})},
	"err_array_enum":                      {Modes: client.Set([]string{"auto", "off"})},
	"null_clears_nullable_enum":           {Mode: client.Null[string]()},
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
	"items_bounds":                   {Zones: client.Set([]string{"a", "b"})},
	"err_items_min":                  {Zones: client.Set([]string{})},
	"err_items_max":                  {Zones: client.Set([]string{"a", "b", "c"})},
	"err_items_clear":                {Zones: client.Null[[]string]()},
	"quoted_int64_max":               {Serial: client.Set(client.Quote(int64(math.MaxInt64)))},
	"quoted_int64_min":               {Serial: client.Set(client.Quote(int64(math.MinInt64)))},
	"quoted_uint64_max":              {Big: client.Set(client.Quote(uint64(math.MaxUint64)))},
	"quoted_bound_past_2_53":         {Quota: client.Set(client.Quote(int64(9007199254740993)))},
	"err_quoted_bound_past_2_53":     {Quota: client.Set(client.Quote(int64(9007199254740994)))},
	"quoted_bool":                    {Live: client.Set(client.Quote(false))},
	"quoted_float":                   {Share: client.Set(client.Quote[float32](0.5))},
	"quoted_null_clears_nullable":    {Opt: client.Null[client.Quoted[int64]]()},
	"err_quoted_null_on_value_field": {Serial: client.Null[client.Quoted[int64]]()},
	"relation_list":                  {Members: client.Set([]string{"a", "b"})},
	"relation_null_clears":           {Members: client.Null[[]string]()},
	"relation_empty_clears":          {Members: client.Set([]string{})},
	"quoted_relation_keys":           {QuotedTags: client.Set([]client.Quoted[int64]{client.Quote(int64(5)), client.Quote(int64(9007199254740993))})},
}

var patchInexpressible = map[string]string{
	"err_string_type":                  "Name is a Nullable[string]",
	"err_quoted_bare_number":           "Serial is a Nullable[Quoted[int64]]",
	"err_quoted_past_int64":            "Serial is a Nullable[Quoted[int64]]",
	"err_quoted_not_a_number":          "Serial is a Nullable[Quoted[int64]]",
	"err_quoted_spaces":                "a Quoted marshals its value's JSON text",
	"err_quoted_uint_negative":         "Big is a Nullable[Quoted[uint64]]",
	"quoted_bool_bare_boolean":         "a Quoted[bool] marshals a JSON string",
	"quoted_integral_spelling":         "a Quoted marshals its value's JSON text",
	"err_quoted_leading_plus":          "a Quoted marshals its value's JSON text",
	"err_quoted_leading_zero":          "a Quoted marshals its value's JSON text",
	"err_quoted_float_word":            "Share is a Nullable[Quoted[float32]]",
	"err_int_fraction":                 "Count is a Nullable[int32]",
	"err_int_type":                     "Count is a Nullable[int32]",
	"err_int32_range":                  "Count is a Nullable[int32]",
	"err_int8_range":                   "Small is a Nullable[int8]",
	"err_float_type":                   "Ratio is a Nullable[float64]",
	"decimal_number":                   "a decimal.Decimal marshals as a JSON string",
	"decimal_exponent":                 "a decimal.Decimal marshals plain notation",
	"err_decimal_word":                 "Price is a Nullable[decimal.Decimal]",
	"err_decimal_bool":                 "Price is a Nullable[decimal.Decimal]",
	"err_decimal_leading_plus":         "a decimal.Decimal marshals canonical text",
	"err_decimal_beyond_digits":        "a decimal.Decimal marshals plain notation",
	"err_bool_type":                    "Active is a Nullable[bool]",
	"err_array_type":                   "Tags is a Nullable[[]string]",
	"err_array_element_type":           "Tags is a Nullable[[]string]",
	"err_array_element_range":          "the struct declares no int16 array",
	"err_timestamp_format":             "SeenAt is a Nullable[time.Time]",
	"err_timestamp_type":               "SeenAt is a Nullable[time.Time]",
	"err_not_patchable":                "the patch struct holds patchable members only",
	"err_unknown_field":                "the patch struct holds declared members only",
	"decimal_bound_exponent":           "a decimal.Decimal marshals plain notation",
	"decimal_bound_number":             "a decimal.Decimal marshals as a JSON string",
	"err_relation_not_a_list":          "Members is a Nullable[[]string]",
	"err_relation_element_type":        "Members is a Nullable[[]string]",
	"err_relation_null_element":        "a []string holds no null",
	"err_quoted_relation_bare_number":  "a Quoted marshals a JSON string",
	"err_quoted_relation_not_a_number": "QuotedTags holds Quoted[int64] keys",
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

// opEntity declares every RFC 6902 operation on its members, so each
// constructor's document reaches BuildUpdate.
type opEntity struct {
	ID   string  `json:"id"   db:"id"`
	Name string  `json:"name" db:"name" parse:"patch" patch_ops:"replace,add,move,copy,test"`
	Note *string `json:"note" db:"note" parse:"patch" patch_ops:"replace,add,remove,move,copy,test"`
}

// Every Operation constructor writes a document the server parses, and
// BuildUpdate compiles it or answers the code its docs give.
func TestClient_EveryOperationConstructorParses(t *testing.T) {
	docs, err := parse.NewResource[schemaDocEntity]("doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	ops, err := parse.NewResource[opEntity]("op_entities")
	require.NoError(t, err)
	tags, scores := client.NewRelation[string]("tags"), client.NewOrderedArray[int16]("scores")
	name, note := client.Pointer("name"), client.Pointer("note")
	tests := []struct {
		name string
		op   client.Operation
		docs bool // on schemaDocEntity, whose array and relation take it; else opEntity
		want patch.PatchOp
		code string // BuildUpdate's refusal; empty when it compiles
	}{
		{"replace", client.Replace(name, "pump"), false, patch.PatchOpReplace, ""},
		{"remove", client.Remove(note), false, patch.PatchOpRemove, ""},
		{"add", client.Add(name, "pump"), false, patch.PatchOpAdd, ""},
		{"move", client.Move(note, name), false, patch.PatchOpMove, patch.ECodeOpNotSQLCompilable.String()},
		{"copy", client.Copy(name, note), false, patch.PatchOpCopy, patch.ECodeOpNotSQLCompilable.String()},
		{"test", client.Test(name, "pump"), false, patch.PatchOpTest, patch.ECodeTestOpSQL.String()},
		{"append", client.Append(client.Pointer("scores"), 3), true, patch.PatchOpArrayAppend, ""},
		{"array append", scores.Append(4), true, patch.PatchOpArrayAppend, ""},
		{"relation add", tags.Add("t1"), true, patch.PatchOpAdd, ""},
		{"relation remove", tags.Remove("t1"), true, patch.PatchOpRemove, ""},
		{"relation replace", tags.Replace("t1"), true, patch.PatchOpReplace, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal([]client.Operation{tt.op})
			require.NoError(t, err)
			var parsed *patch.ParsedPatch
			if tt.docs {
				parsed, err = docs.ParsePatch(body)
			} else {
				parsed, err = ops.ParsePatch(body)
			}
			require.NoError(t, err, "%s", body)
			require.Len(t, parsed.Operations, 1)
			assert.Equal(t, tt.want, parsed.Operations[0].Op)

			if tt.docs {
				_, err = docs.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID("e1"), parse.Scope{Tenant: "t1"})
			} else {
				_, err = ops.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID("e1"), parse.Scope{})
			}
			if tt.code == "" {
				assert.NoError(t, err, "%s", body)
				return
			}
			pe, ok := errors.AsType[*parse.ParseError](err)
			require.True(t, ok, "%s: want a ParseError, got %v", body, err)
			assert.Equal(t, tt.code, pe.Code, "%s", body)
		})
	}
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

	body, err := params.Body()
	require.NoError(t, err)
	fromBody, err := res.ParseListBody(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, want, fromBody, "the body is the same request as the query string")
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &members))
	for key := range members {
		assert.True(t, filter.IsListParam(key), "%q is no list parameter", key)
	}
	assert.Len(t, members, 10, "every member encodes to its own body member")

	emptyBody, err := client.ListParams{}.Body()
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(emptyBody), "zero members leave the server's defaults")
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
		confFields.Note.Name(), confFields.Lines.Name(), confFields.Tags.Name(), confFields.Labels.Name(), confFields.Sizes.Name(),
		confFields.Parts.Name(), confFields.Serial.Name(), confFields.Live.Name(), confFields.Big.Name(), confFields.Ratio.Name(),
		confFields.Score.Name(), confFields.Day.Name(), confFields.At.Name(), confFields.Span.Name(), confFields.Addr.Name(),
		confFields.Subnet.Name(), confFields.Mac.Name(), confFields.Key.Name(), confFields.Spot.Name(), confFields.Dims.Name(),
		confFields.Blob.Name(), confFields.Owner.Name(),
	}
	for name := range schema.Fields {
		assert.True(t, slices.Contains(names, name), "the catalog misses %q", name)
	}
}

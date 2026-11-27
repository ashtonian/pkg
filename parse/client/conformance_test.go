//go:build unit

package client

// The client against the corpora parse and the TypeScript SDK are judged by
// (parse's testdata/filter_conformance.json and patch_conformance.json).
// Every corpus case is classified: the client builds it, refuses it itself,
// or cannot express it because its types rule it out. A new corpus case
// fails these tests until it is classified.
//
// What the client builds is recorded in parse's
// testdata/client_requests.json, and parse's conformance_test.go
// holds the server to it: a filter built for a case means what the case's
// input means, with its verdict, code, AST and SQL. This module imports no
// parse package, so the server reads the requests there. Regenerate after
// an intentional change to what the client sends:
//
//	go test -tags=unit -run TestClientRequests -update ./

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"
)

// decimalText is a decimal as an SDK may declare one: its exact text, sent
// as a JSON string, as shopspring/decimal marshals. The client marshals an
// operand's own JSON, so the value types are the SDK's to choose; these
// tests use the standard library's and text where it has none.
type decimalText string

func (d decimalText) MarshalJSON() ([]byte, error) { return json.Marshal(string(d)) }

// confLine is an element of confEntity's lines.
type confLine struct {
	SKU string `json:"sku"`
	Qty int    `json:"qty"`
}

// confDims is confEntity's struct-typed (object) field.
type confDims struct {
	W int `json:"w"`
	H int `json:"h"`
}

// confCatalog is the catalog a generated SDK would declare for the filter
// corpus's resource, conf_entities.
type confCatalog struct {
	ID      Field[string]
	Name    Text
	Status  Field[string]
	Count   Ordered[int]
	Price   Ordered[decimalText]
	Active  Field[bool]
	Created Time
	Meta    Document[map[string]any]
	Region  Text
	Note    Field[string]
	Lines   Elements[[]confLine]
	Tags    TextArray
	Labels  TextArray
	Sizes   OrderedArray[int]
	Parts   Relation[string]
	Serial  Ordered[Quoted[int64]]
	Live    Field[Quoted[bool]]
	Big     Ordered[Quoted[uint64]]
	Ratio   Ordered[Quoted[float64]]
	Score   Ordered[float64]
	Day     Ordered[string]
	At      Ordered[string]
	Span    Field[string]
	Addr    Field[netip.Addr]
	Subnet  Field[netip.Prefix]
	Mac     Field[string]
	Key     Field[string]
	Spot    Geo
	Dims    Document[confDims]
	Blob    Field[[]byte]
	Owner   Reference[string]

	// The element catalog of lines, and the target catalogs of parts and
	// owner.
	LineSKU   Field[string]
	LineQty   Ordered[int]
	PartState Field[string]
	OwnerName Text
}

func newConfCatalog() confCatalog {
	catalog := confCatalog{
		ID:      NewField[string]("id"),
		Name:    NewText("name"),
		Status:  NewField[string]("status"),
		Count:   NewOrdered[int]("count"),
		Price:   NewOrdered[decimalText]("price"),
		Active:  NewField[bool]("active"),
		Created: NewTime("created"),
		Meta:    NewDocument[map[string]any]("meta"),
		Region:  NewText("meta.region"),
		Note:    NewField[string]("note"),
		Lines:   NewElements[[]confLine]("lines"),
		Tags:    NewTextArray("tags"),
		Labels:  NewTextArray("meta.labels"),
		Sizes:   NewOrderedArray[int]("meta.sizes"),
		Parts:   NewRelation[string]("parts"),
		Serial:  NewOrdered[Quoted[int64]]("serial"),
		Live:    NewField[Quoted[bool]]("live"),
		Big:     NewOrdered[Quoted[uint64]]("big"),
		Ratio:   NewOrdered[Quoted[float64]]("ratio"),
		Score:   NewOrdered[float64]("score"),
		Day:     NewOrdered[string]("day"),
		At:      NewOrdered[string]("at"),
		Span:    NewField[string]("span"),
		Addr:    NewField[netip.Addr]("addr"),
		Subnet:  NewField[netip.Prefix]("subnet"),
		Mac:     NewField[string]("mac"),
		Key:     NewField[string]("key"),
		Spot:    NewGeo("spot"),
		Dims:    NewDocument[confDims]("dims"),
		Blob:    NewField[[]byte]("blob"),
		Owner:   NewReference[string]("owner"),

		LineSKU:   NewField[string]("sku"),
		LineQty:   NewOrdered[int]("qty"),
		PartState: NewField[string]("state"),
		OwnerName: NewText("name"),
	}
	return catalog
}

// filterBuilt are the corpus cases the client builds; the server reads each
// as the corpus records, valid or refused with its code.
func filterBuilt() map[string]Filter {
	f := newConfCatalog()
	tag, label, size := f.Tags.Element(), f.Labels.Element(), f.Sizes.Element()
	statusActive := f.Status.Eq("active")
	built := map[string]Filter{
		"eq_string":                         statusActive,
		"eq_number":                         f.Count.Eq(5),
		"eq_bool":                           f.Active.Eq(true),
		"ne_number":                         f.Count.Ne(5),
		"gt_number":                         f.Count.Gt(5),
		"gte_number":                        f.Count.Gte(5),
		"lt_number":                         f.Count.Lt(5),
		"lte_number":                        f.Count.Lte(5),
		"eq_decimal_string":                 f.Price.Eq("10.50"),
		"eq_decimal_number":                 f.Price.Eq("10.5"),
		"gt_decimal_exponent":               f.Price.Gt("1.5E-3"),
		"eq_decimal_beyond_float64":         f.Price.Eq("0.10000000000000001"),
		"in_decimal":                        f.Price.In("9.5", "2", "-0"),
		"err_decimal_beyond_digits":         f.Price.Eq("1e1000"),
		"err_decimal_word":                  f.Price.Eq("ten"),
		"err_decimal_hexadecimal":           f.Price.Eq("0x10"),
		"err_decimal_leading_plus":          f.Price.Eq("+1"),
		"err_decimal_leading_point":         f.Price.Eq(".5"),
		"err_decimal_leading_zero":          f.Price.Eq("007"),
		"err_decimal_exponent_beyond_int32": f.Price.Eq("0e2147483648"),
		"err_decimal_in_member":             f.Price.In("1", "one"),
		"in_array":                          f.Status.In("active", "offline"),
		"nin_array":                         f.Status.Nin("error"),
		"like":                              f.Name.Like("dev%"),
		"ilike":                             f.Name.ILike("DEV%"),
		"contains":                          f.Name.Contains("sensor"),
		"null_true":                         f.Name.Null(),
		"null_false":                        f.Name.NotNull(),
		"exists_true":                       f.Name.NotNull(),
		"not_null_true":                     f.Name.NotNull(),
		"not_null_false":                    f.Name.Null(),
		"null_test_capability_not_null":     f.Note.NotNull(),
		"null_test_capability_null":         f.Note.NotNull(),
		"null_test_capability_exists":       f.Note.NotNull(),
		"and_explicit":                      And(statusActive, f.Count.Gt(5)),
		"or_explicit":                       Or(statusActive, f.Status.Eq("offline")),
		"not_explicit":                      Not(f.Status.Eq("error")),
		"nested_and_or":                     And(f.Active.Eq(true), Or(statusActive, f.Status.Eq("offline"))),
		"time_token_relative":               f.Created.Gt(Ago(7 * 24 * time.Hour)),
		"empty":                             {},
		"err_invalid_enum":                  f.Status.Eq("bogus"),
		"err_nin_enum":                      f.Status.Nin("bogus"),
		"err_like_trailing_escape":          f.Name.Like(`abc\`),
		"json_eq_document":                  f.Meta.Eq(map[string]any{"env": "prod"}),
		"json_eq_dollar_keyed_document":     f.Meta.Eq(map[string]any{"$env": "prod"}),
		"json_eq_mixed_keyed_document":      f.Meta.Eq(map[string]any{"$env": "prod", "region": "eu"}),
		"json_contains":                     f.Meta.Contains(map[string]any{"env": "prod"}),
		"err_json_integer_beyond_int64":     f.Meta.Eq(map[string]any{"n": uint64(math.MaxUint64)}),
		"json_decimal":                      f.Meta.Eq(map[string]any{"n": 0.1}),
		"json_has_key":                      f.Meta.HasKey("env"),
		"path_eq":                           f.Region.Eq("eu"),
		"path_like":                         f.Region.Like("eu-%"),
		"elements_some":                     f.Lines.Some(And(f.LineSKU.Eq("a"), f.LineQty.Gt(2))),
		"elements_none":                     f.Lines.None(f.LineSKU.Eq("a")),
		"array_some_eq":                     f.Tags.Some(tag.Eq("a")),
		"array_some_and":                    f.Sizes.Some(And(size.Gte(1), size.Lt(5))),
		"array_some_or":                     f.Labels.Some(Or(label.Like("a%"), label.Eq("b"))),
		"array_none_not":                    f.Tags.None(Not(tag.In("a", "b"))),
		"array_some_any":                    f.Labels.Some(Filter{}),
		"array_path_contains":               f.Labels.Contains("a", "b"),
		"array_contains_scalar":             f.Tags.Contains("a"),
		"array_path_null":                   f.Sizes.Null(),
		"err_array_element_null_test":       f.Tags.Some(tag.Null()),
		"relation_some":                     f.Parts.Some(f.PartState.Eq("worn")),
		"relation_none_any":                 f.Parts.None(Filter{}),
		"relation_key_shorthand":            f.Parts.Eq("p1"),
		"relation_key_list":                 f.Parts.In("p1", "p2"),
		"err_relation_target_enum":          f.Parts.Some(f.PartState.Eq("new")),
		"quoted_eq_int64_max":               f.Serial.Eq(Quote(int64(math.MaxInt64))),
		"quoted_eq_int64_min":               f.Serial.Eq(Quote(int64(math.MinInt64))),
		"quoted_gt_past_2_53":               f.Serial.Gt(Quote(int64(9007199254740992))),
		"quoted_in":                         f.Serial.In(Quote(int64(1)), Quote(int64(9007199254740993))),
		"quoted_eq_bool":                    f.Live.Eq(Quote(true)),
		"quoted_uint64_max":                 f.Big.Eq(Quote(uint64(math.MaxUint64))),
		"quoted_float":                      f.Ratio.Lt(Quote(0.5)),
		"reference_is":                      f.Owner.Is(f.OwnerName.Eq("acme")),
		"reference_is_not":                  f.Owner.IsNot(f.OwnerName.Like("ac%")),
		"reference_key_shorthand":           f.Owner.Eq("o1"),
		"float_gt":                          f.Score.Gt(0.5),
		"date_gt":                           f.Day.Gt("2026-01-01"),
		"err_date_operator":                 f.Day.Gte("2026-01-01"),
		"err_date_spelling":                 f.Day.Eq("01/02/2026"),
		"time_gte":                          f.At.Gte("09:30:00"),
		"err_time_spelling":                 f.At.Eq("half past nine"),
		"interval_in":                       f.Span.In("P1D", "PT2H30M"),
		"err_interval_spelling":             f.Span.Eq("1 day"),
		"inet_eq":                           f.Addr.Eq(netip.MustParseAddr("192.0.2.1")),
		"cidr_eq":                           f.Subnet.Eq(netip.MustParsePrefix("192.0.2.0/24")),
		"err_cidr_host_bits":                f.Subnet.Eq(netip.MustParsePrefix("192.0.2.1/24")),
		"macaddr_ne":                        f.Mac.Ne("08:00:2b:01:02:03"),
		"err_macaddr_spelling":              f.Mac.Ne("08:00:2b"),
		"uuid_in":                           f.Key.In("6ba7b810-9dad-11d1-80b4-00c04fd430c8"),
		"err_uuid_spelling":                 f.Key.Eq("not-a-uuid"),
		"geo_near":                          f.Spot.Near(Point{Lat: 40.7128, Lon: -74.006}, 1500),
		"geo_within_box":                    f.Spot.Within(Box(Point{Lat: 40.8, Lon: -74.1}, Point{Lat: 40.65, Lon: -73.7})),
		"geo_intersects_polygon":            f.Spot.Intersects(Polygon(Point{}, Point{Lat: 1, Lon: 1}, Point{Lon: 1})),
		"err_geo_intersects_two_points":     f.Spot.Intersects(Polygon(Point{}, Point{Lat: 1, Lon: 1})),
		"object_has_key":                    f.Dims.HasKey("w"),
		"object_eq_document":                f.Dims.Eq(confDims{W: 2, H: 3}),
		"err_object_operator":               f.Dims.Contains(confDims{W: 2}),
		"any_null":                          f.Blob.Null(),
		"err_any_operator":                  f.Blob.Eq([]byte("x")),
	}
	return built
}

// filterRefused are the corpus cases the client refuses to encode, with the
// filter it refuses and its error.
func filterRefused() map[string]struct {
	filter Filter
	err    error
} {
	f := newConfCatalog()
	refused := map[string]struct {
		filter Filter
		err    error
	}{
		"err_or_empty":                {Or(), ErrEmptyOr},
		"err_not_empty":               {Not(Filter{}), ErrNegatedEmpty},
		"err_array_element_member":    {f.Tags.Some(f.LineSKU.Eq("a")), ErrElementCondition},
		"err_array_element_empty_or":  {f.Tags.Some(Or()), ErrEmptyOr},
		"err_array_element_empty_not": {f.Tags.Some(Not(Filter{})), ErrNegatedEmpty},
	}
	return refused
}

// filterInexpressible are the corpus cases the client's types rule out.
func filterInexpressible() map[string]string {
	inexpressible := map[string]string{
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
		"err_decimal_bool":                       "Price holds decimal text",
		"err_decimal_null":                       "Null is the null test; Eq takes a value",
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
		"err_interval_operator":                  "Span is a Field, which has no Lt",
		"err_inet_spelling":                      "a netip.Addr holds a parsed address",
		"err_macaddr_type":                       "Mac is a Field[string]",
		"err_uuid_type":                          "Key is a Field[string]",
		"err_geo_operator":                       "Geo has no Eq",
		"err_geo_near_without_distance":          "Near takes a distance",
		"err_geo_within_not_a_shape":             "Within takes a Shape",
	}
	return inexpressible
}

// confPatchInput is the merge-patch struct a generated SDK would declare for
// the patch corpus's resource.
type confPatchInput struct {
	Name     Nullable[string]          `json:"name,omitzero"`
	Nick     Nullable[string]          `json:"nick,omitzero"`
	Status   Nullable[string]          `json:"status,omitzero"`
	Count    Nullable[int32]           `json:"count,omitzero"`
	Small    Nullable[int8]            `json:"small,omitzero"`
	Ratio    Nullable[float64]         `json:"ratio,omitzero"`
	Price    Nullable[decimalText]     `json:"price,omitzero"`
	Active   Nullable[bool]            `json:"active,omitzero"`
	Tags     Nullable[[]string]        `json:"tags,omitzero"`
	Labels   Nullable[map[string]any]  `json:"labels,omitzero"`
	SeenAt   Nullable[time.Time]       `json:"seen_at,omitzero"`
	Currency Nullable[string]          `json:"currency,omitzero"`
	Level    Nullable[int32]           `json:"level,omitzero"`
	Rate     Nullable[float32]         `json:"rate,omitzero"`
	Amount   Nullable[decimalText]     `json:"amount,omitzero"`
	Codes    Nullable[[]string]        `json:"codes,omitzero"`
	Modes    Nullable[[]string]        `json:"modes,omitzero"`
	Mode     Nullable[string]          `json:"mode,omitzero"`
	Zones    Nullable[[]string]        `json:"zones,omitzero"`
	Serial   Nullable[Quoted[int64]]   `json:"serial,omitzero"`
	Big      Nullable[Quoted[uint64]]  `json:"big,omitzero"`
	Quota    Nullable[Quoted[int64]]   `json:"quota,omitzero"`
	Live     Nullable[Quoted[bool]]    `json:"live,omitzero"`
	Share    Nullable[Quoted[float32]] `json:"share,omitzero"`
	Opt      Nullable[Quoted[int64]]   `json:"opt,omitzero"`
	Members  Nullable[[]string]        `json:"members,omitzero"`
	// QuotedTags is a relation whose target key is quoted: its keys are
	// Quoted, as a Relation[Quoted[int64]] sends them.
	QuotedTags Nullable[[]Quoted[int64]] `json:"quoted_tags,omitzero"`
	Pinned     Nullable[string]          `json:"pinned,omitzero"`
}

// nestedDocuments is a document of n empty documents: a merge patch of it
// is n operations.
func nestedDocuments(n int) map[string]any {
	doc := make(map[string]any, n)
	for i := range n {
		doc[fmt.Sprintf("k%02d", i)] = map[string]any{}
	}
	return doc
}

func patchBuilt() map[string]confPatchInput {
	built := map[string]confPatchInput{
		"replace_where_remove_is_not_allowed": {Pinned: Set("a")},
		"err_remove_not_allowed":              {Pinned: Null[string]()},
		"operations_at_the_maximum":           {Labels: Set(nestedDocuments(49))},
		"err_too_many_operations":             {Labels: Set(nestedDocuments(50))},
		"string":                              {Name: Set("a")},
		"err_null_on_value_field":             {Name: Null[string]()},
		"null_on_pointer_field":               {Nick: Null[string]()},
		"enum":                                {Status: Set("active")},
		"err_enum":                            {Status: Set("busy")},
		"int":                                 {Count: Set[int32](5)},
		"int8_min":                            {Small: Set[int8](-128)},
		"float":                               {Ratio: Set(0.5)},
		"decimal_string":                      {Price: Set[decimalText]("10.25")},
		"decimal_exponent":                    {Price: Set[decimalText]("1.5e2")},
		"err_decimal_word":                    {Price: Set[decimalText]("ten")},
		"err_decimal_leading_plus":            {Price: Set[decimalText]("+1")},
		"err_decimal_beyond_digits":           {Price: Set[decimalText]("1e1000")},
		"err_decimal_null":                    {Price: Null[decimalText]()},
		"bool":                                {Active: Set(false)},
		"array":                               {Tags: Set([]string{"a", "b"})},
		"clear_array":                         {Tags: Null[[]string]()},
		"document_merge":                      {Labels: Set(map[string]any{"a": 1, "old": nil})},
		"timestamp":                           {SeenAt: Set(time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC))},
		"several":                             {Name: Set("a"), Count: Set[int32](1), Active: Set(true)},
		"array_enum":                          {Modes: Set([]string{"auto", "manual"})},
		"err_array_enum":                      {Modes: Set([]string{"auto", "off"})},
		"null_clears_nullable_enum":           {Mode: Null[string]()},
		// Bounds are the server's: the client builds a value outside them.
		"length_bounds":                  {Currency: Set("USD")},
		"length_counts_code_points":      {Currency: Set("€😀ß")},
		"err_maxlen":                     {Currency: Set("USDX")},
		"err_minlen":                     {Currency: Set("US")},
		"int_bounds":                     {Level: Set[int32](10)},
		"err_int_max":                    {Level: Set[int32](11)},
		"err_int_min":                    {Level: Set[int32](0)},
		"float32_bound_at_its_precision": {Rate: Set[float32](0.1)},
		"err_float_max":                  {Rate: Set[float32](0.11)},
		"decimal_bounds":                 {Amount: Set[decimalText]("999.99")},
		"decimal_bound_exponent":         {Amount: Set[decimalText]("9.9999e2")},
		"err_decimal_max_exact":          {Amount: Set[decimalText]("999.990000000000000001")},
		"err_decimal_min":                {Amount: Set[decimalText]("0.001")},
		"array_element_length":           {Codes: Set([]string{"ab", "c"})},
		"err_array_element_length":       {Codes: Set([]string{"ab", "abc"})},
		"items_bounds":                   {Zones: Set([]string{"a", "b"})},
		"err_items_min":                  {Zones: Set([]string{})},
		"err_items_max":                  {Zones: Set([]string{"a", "b", "c"})},
		"err_items_clear":                {Zones: Null[[]string]()},
		"quoted_int64_max":               {Serial: Set(Quote(int64(math.MaxInt64)))},
		"quoted_int64_min":               {Serial: Set(Quote(int64(math.MinInt64)))},
		"quoted_uint64_max":              {Big: Set(Quote(uint64(math.MaxUint64)))},
		"quoted_bound_past_2_53":         {Quota: Set(Quote(int64(9007199254740993)))},
		"err_quoted_bound_past_2_53":     {Quota: Set(Quote(int64(9007199254740994)))},
		"quoted_bool":                    {Live: Set(Quote(false))},
		"quoted_float":                   {Share: Set(Quote[float32](0.5))},
		"quoted_null_clears_nullable":    {Opt: Null[Quoted[int64]]()},
		"err_quoted_null_on_value_field": {Serial: Null[Quoted[int64]]()},
		"relation_list":                  {Members: Set([]string{"a", "b"})},
		"relation_null_clears":           {Members: Null[[]string]()},
		"relation_empty_clears":          {Members: Set([]string{})},
		"quoted_relation_keys":           {QuotedTags: Set([]Quoted[int64]{Quote(int64(5)), Quote(int64(9007199254740993))})},
	}
	return built
}

func patchInexpressible() map[string]string {
	inexpressible := map[string]string{
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
		"decimal_number":                   "Price's decimal text marshals as a JSON string",
		"err_decimal_bool":                 "Price holds decimal text",
		"err_bool_type":                    "Active is a Nullable[bool]",
		"err_array_type":                   "Tags is a Nullable[[]string]",
		"err_array_element_type":           "Tags is a Nullable[[]string]",
		"err_array_element_range":          "the struct declares no int16 array",
		"err_timestamp_format":             "SeenAt is a Nullable[time.Time]",
		"err_timestamp_type":               "SeenAt is a Nullable[time.Time]",
		"err_not_patchable":                "the patch struct holds patchable members only",
		"err_unknown_field":                "the patch struct holds declared members only",
		"decimal_bound_number":             "Amount's decimal text marshals as a JSON string",
		"err_relation_not_a_list":          "Members is a Nullable[[]string]",
		"err_relation_element_type":        "Members is a Nullable[[]string]",
		"err_relation_null_element":        "a []string holds no null",
		"err_quoted_relation_bare_number":  "a Quoted marshals a JSON string",
		"err_quoted_relation_not_a_number": "QuotedTags holds Quoted[int64] keys",
	}
	return inexpressible
}

// corpusCase is a corpus case: its input as recorded, and the server's
// verdict on it.
type corpusCase struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	Valid bool            `json:"valid"`
}

func readCases(t *testing.T, path string) []corpusCase {
	t.Helper()
	var corpus struct {
		Cases []corpusCase `json:"cases"`
	}
	decodeCorpus(t, path, &corpus)
	if len(corpus.Cases) == 0 {
		t.Fatalf("%s holds no cases", path)
	}
	return corpus.Cases
}

// classify fails unless every case is in exactly one of the classes and
// every class names corpus cases only, so a case renamed or removed from
// the corpus leaves its class too.
func classify(t *testing.T, path string, classes map[string][]string) {
	t.Helper()
	inCorpus := map[string]bool{}
	for _, c := range readCases(t, path) {
		inCorpus[c.Name] = true
		var in []string
		for class, names := range classes {
			if slices.Contains(names, c.Name) {
				in = append(in, class)
			}
		}
		if len(in) != 1 {
			t.Errorf("%s case %q is in %v: classify it in exactly one of %v", path, c.Name, in, slices.Sorted(maps.Keys(classes)))
		}
	}
	for class, names := range classes {
		for _, name := range names {
			if !inCorpus[name] {
				t.Errorf("%s names %q, which is no case of %s", class, name, path)
			}
		}
	}
}

func names[V any](m map[string]V) []string { return slices.Collect(maps.Keys(m)) }

func TestConformance_EveryCaseIsClassified(t *testing.T) {
	classify(t, filterConformancePath, map[string][]string{
		"filterBuilt":         names(filterBuilt()),
		"filterRefused":       names(filterRefused()),
		"filterInexpressible": names(filterInexpressible()),
	})
	classify(t, patchConformancePath, map[string][]string{
		"patchBuilt":         names(patchBuilt()),
		"patchInexpressible": names(patchInexpressible()),
	})
}

func TestConformance_RefusesWhatTheGrammarCannotSay(t *testing.T) {
	refused := filterRefused()
	for _, c := range readCases(t, filterConformancePath) {
		want, ok := refused[c.Name]
		if !ok {
			continue
		}
		if c.Valid {
			t.Errorf("%s: the server takes the document the client will not send", c.Name)
		}
		_, err := json.Marshal(want.filter)
		checkErrorIs(t, err, want.err, c.Name)
	}
}

// A merge patch is its JSON, so the client's document equals the corpus
// input and the server's verdict is the corpus's.
func TestConformance_MergePatchesAreTheCorpusDocuments(t *testing.T) {
	built := patchBuilt()
	for _, c := range readCases(t, patchConformancePath) {
		in, ok := built[c.Name]
		if !ok {
			continue
		}
		checkJSON(t, string(c.Input), encode(t, in), c.Name)
	}
}

// The catalog declares every field the corpus's resource filters on.
func TestConformance_CatalogCoversTheCorpusFields(t *testing.T) {
	var corpus struct {
		Schemas map[string]Schema `json:"schemas"`
	}
	decodeCorpus(t, filterConformancePath, &corpus)
	schema, ok := corpus.Schemas["conf_entities"]
	if !ok || schema.Filter == nil {
		t.Fatalf("%s holds no conf_entities filter schema", filterConformancePath)
	}
	f := newConfCatalog()
	declared := []string{
		f.ID.Name(), f.Name.Name(), f.Status.Name(), f.Count.Name(), f.Price.Name(), f.Active.Name(),
		f.Created.Name(), f.Meta.Name(), f.Region.Name(), f.Note.Name(), f.Lines.Name(), f.Tags.Name(),
		f.Labels.Name(), f.Sizes.Name(), f.Parts.Name(), f.Serial.Name(), f.Live.Name(), f.Big.Name(),
		f.Ratio.Name(), f.Score.Name(), f.Day.Name(), f.At.Name(), f.Span.Name(), f.Addr.Name(),
		f.Subnet.Name(), f.Mac.Name(), f.Key.Name(), f.Spot.Name(), f.Dims.Name(), f.Blob.Name(), f.Owner.Name(),
	}
	for name := range schema.Filter.Fields {
		if !slices.Contains(declared, name) {
			t.Errorf("the catalog misses %q", name)
		}
	}
}

// clientRequests is parse's testdata/client_requests.json: the requests
// this client builds for parse's tests to read, by what each is for.
type clientRequests struct {
	Filters    map[string]json.RawMessage `json:"filters"`
	GeoFilters map[string]json.RawMessage `json:"geo_filters"`
	List       struct {
		Query map[string][]string `json:"query"`
		Body  json.RawMessage     `json:"body"`
	} `json:"list"`
	Operations     map[string]json.RawMessage `json:"operations"`
	JSONPatch      json.RawMessage            `json:"json_patch"`
	QuotedRelation struct {
		JSONPatch  json.RawMessage `json:"json_patch"`
		MergePatch json.RawMessage `json:"merge_patch"`
	} `json:"quoted_relation"`
}

func marshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding %v: %v", v, err)
	}
	return b
}

func buildClientRequests(t *testing.T) clientRequests {
	t.Helper()
	var r clientRequests

	r.Filters = map[string]json.RawMessage{}
	for name, f := range filterBuilt() {
		r.Filters[name] = marshal(t, f)
	}

	// Geo filters on parse's tests' place resource: a jsonb location.
	location := NewGeo("location")
	nyc := Point{Lat: 40.7128, Lon: -74.006}
	box := Box(Point{Lat: 40.8, Lon: -74.1}, Point{Lat: 40.65, Lon: -73.7})
	triangle := Polygon(Point{}, Point{Lat: 1, Lon: 1}, Point{Lon: 1})
	r.GeoFilters = map[string]json.RawMessage{
		"near":           marshal(t, location.Near(nyc, 1500.5)),
		"within_box":     marshal(t, location.Within(box)),
		"within_polygon": marshal(t, location.Within(triangle)),
		"intersects_box": marshal(t, location.Intersects(box)),
	}

	// Every list parameter, on parse's tests' widgets resource.
	name, rank := NewSortableText("name"), NewSortableOrdered[int64]("rank")
	params := ListParams{
		Filter:         And(name.Eq("beta"), rank.Gte(1)),
		Sort:           Sort{rank.Desc(), name.Asc()},
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
	must(t, err)
	r.List.Query = q
	r.List.Body, err = params.Body()
	must(t, err)

	// Each Operation constructor, on parse's tests' doc_entities (the array
	// and relation) and op_entities (every RFC 6902 operation).
	tags, scores := NewRelation[string]("tags"), NewOrderedArray[int16]("scores")
	pname, note := Pointer("name"), Pointer("note")
	r.Operations = map[string]json.RawMessage{}
	for constructor, op := range map[string]Operation{
		"replace":          Replace(pname, "pump"),
		"remove":           Remove(note),
		"add":              Add(pname, "pump"),
		"move":             Move(note, pname),
		"copy":             Copy(pname, note),
		"test":             Test(pname, "pump"),
		"append":           Append(Pointer("scores"), 3),
		"array append":     scores.Append(4),
		"relation add":     tags.Add("t1"),
		"relation remove":  tags.Remove("t1"),
		"relation replace": tags.Replace("t1"),
	} {
		r.Operations[constructor] = marshal(t, []Operation{op})
	}
	r.JSONPatch = marshal(t, []Operation{
		Replace(pname, "pump"),
		Remove(note),
		Replace(Pointer("scores"), []int16{1, 2}),
		tags.Add("t1", "t2"),
		tags.Remove("t3"),
		tags.Replace("t4"),
	})

	quotedTags := NewRelation[Quoted[int64]]("tags")
	r.QuotedRelation.JSONPatch = marshal(t, []Operation{
		quotedTags.Add(Quote(int64(5)), Quote(int64(9007199254740993))),
		quotedTags.Remove(Quote(int64(6))),
	})
	r.QuotedRelation.MergePatch = marshal(t, struct {
		Tags Nullable[[]Quoted[int64]] `json:"tags,omitzero"`
	}{Set([]Quoted[int64]{Quote(int64(5))})})
	return r
}

// The requests parse's conformance_test.go reads are the ones this
// client builds.
func TestClientRequests(t *testing.T) {
	out, err := json.MarshalIndent(buildClientRequests(t), "", "  ")
	must(t, err)
	out = append(out, '\n')
	if updating() {
		must(t, os.WriteFile(clientRequestsPath, out, 0o644))
		return
	}
	recorded := readCorpus(t, clientRequestsPath)
	if !bytes.Equal(recorded, out) {
		checkJSON(t, string(recorded), string(out))
		t.Fatalf("%s is stale; regenerate it with -update", clientRequestsPath)
	}
}

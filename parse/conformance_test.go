//go:build unit

package parse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Go⇔TS filter-grammar conformance corpus.
//
// The Go filter parser is authoritative. TestEmitFilterConformance walks a
// canonical set of filter inputs through the real parser + both SQL
// dialects and records the resulting AST / SQL / validity / error code into
// testdata/filter_conformance.json, with the schema documents the registry
// serves for the corpus's resources, and the server's verdict on a set of
// sort strings and list requests. The TypeScript client
// (sdk/ts/src/filter/conformance.test.ts) validates every input against
// those documents and must agree; parse/client's tests classify every case,
// and the TestClient tests read the filters the client builds for them;
// filter's own tests require a valid and an invalid case for every operator
// and every filterable type.
//
// Regenerate the corpus after an intentional grammar change:
//
//	go test -tags=unit -run TestEmitFilterConformance -update ./
//
// Scope: the corpus tests *grammar agreement* — which inputs are valid, what
// AST/SQL they canonicalize to, and which schema/operator errors they raise.
// It deliberately does NOT cover the server-side DoS guards (MaxDepth,
// MaxCost, MaxConditions): those are deployment-tunable parser config, not
// grammar, and the TS client legitimately never enforces them.

var updateRendered = flag.Bool("update", false, "rewrite what is rendered from the code: the conformance corpora, the SDK fixtures and the guides' tables")

const conformanceCorpusPath = "testdata/filter_conformance.json"

// confClock pins the parser's now-anchor so relative time tokens (now-7d)
// resolve to a fixed RFC3339 string — the corpus must be byte-stable.
var confClock = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

// confEntity is the resource the corpus is generated against
// (confEntities), with a field of every filterable type.
type confEntity struct {
	ID      string          `json:"id"      parse:"filter,sort" filter_ops:"eq,ne,in,nin"`
	Name    string          `json:"name"    parse:"filter,sort" filter_ops:"eq,ne,like,ilike,contains,null,not_null,exists"`
	Status  string          `json:"status"  parse:"filter,facet" filter_ops:"eq,ne,in,nin" enum:"active,offline,error" facet:"terms:5"`
	Count   int             `json:"count"   parse:"filter,sort" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin"`
	Price   decimal.Decimal `json:"price"   parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin"`
	Active  bool            `json:"active"  parse:"filter" filter_ops:"eq,ne"`
	Created time.Time       `json:"created" parse:"filter,sort" filter_ops:"eq,gt,gte,lt,lte"`
	Meta    map[string]any  `json:"meta"    parse:"filter" paths:"region:string;labels:[]string;sizes:[]int"`
	Note    *string         `json:"note"    parse:"filter" filter_ops:"not_null"`
	Lines   []confLine      `json:"lines"   parse:"filter"`
	Tags    []string        `json:"tags"    parse:"filter"`
	// Serial and Live travel as JSON strings (json:",string").
	Serial int64          `json:"serial,string" parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin"`
	Live   bool           `json:"live,string"   parse:"filter" filter_ops:"eq,ne"`
	Big    uint64         `json:"big,string"    parse:"filter" filter_ops:"eq,gt"`
	Ratio  float64        `json:"ratio,string"  parse:"filter" filter_ops:"eq,lt"`
	Score  float64        `json:"score"         parse:"filter" filter_ops:"gt,lte"`
	Day    core.Date      `json:"day"           parse:"filter" filter_ops:"eq,gt,lt"`
	At     core.LocalTime `json:"at"            parse:"filter" filter_ops:"eq,gte"`
	Span   core.Interval  `json:"span"          parse:"filter" filter_ops:"eq,in"`
	Addr   core.Inet      `json:"addr"          parse:"filter" filter_ops:"eq,in"`
	Subnet core.CIDR      `json:"subnet"        parse:"filter" filter_ops:"eq"`
	Mac    core.MACAddr   `json:"mac"           parse:"filter" filter_ops:"eq,ne"`
	Key    uuid.UUID      `json:"key"           parse:"filter" filter_ops:"eq,in"`
	Spot   map[string]any `json:"spot"          parse:"filter" filter_ops:"near,within,intersects" geo:"jsonb" geo_srid:"4326"`
	Dims   confDims       `json:"dims"          parse:"filter" filter_ops:"eq,has_key"`
	Blob   []byte         `json:"blob"          parse:"filter" filter_ops:"null"`
	// OwnerID is the key the relation "owner" joins on; no filter field.
	OwnerID string `json:"-" db:"owner_id"`
}

// confLine is an element of confEntity's lines array.
type confLine struct {
	SKU string `json:"sku" parse:"filter" filter_ops:"eq,in"`
	Qty int    `json:"qty" parse:"filter" filter_ops:"eq,gt,lt"`
}

// confDims is confEntity's struct-typed (object) field.
type confDims struct {
	W int `json:"w"`
	H int `json:"h"`
}

// confPart is the target of confEntity's has-many relation "parts"
// (parts.entity_id = conf_entities.id).
type confPart struct {
	ID       string `json:"id"    parse:"filter" filter_ops:"eq,in"`
	EntityID string `json:"-"     db:"entity_id"`
	State    string `json:"state" parse:"filter" filter_ops:"eq,ne" enum:"ok,worn"`
}

// confOwner is the target of confEntity's belongs-to relation "owner"
// (conf_entities.owner_id = owners.id).
type confOwner struct {
	ID   string `json:"id"   parse:"filter" filter_ops:"eq,in"`
	Name string `json:"name" parse:"filter" filter_ops:"eq,like"`
}

// confBareEntity is a resource with nothing to filter, sort or patch: its
// schema document has none of those sections.
type confBareEntity struct {
	ID string `json:"id" db:"id"`
}

// The corpus's resource names, as the registry serves them.
const (
	confEntities = "conf_entities"
	confBare     = "conf_bare"
)

// confCorpusResources are the corpus's resources, registered as a server
// registers them.
type confCorpusResources struct {
	entities *parse.Resource[confEntity]
	bare     *parse.Resource[confBareEntity]
	registry *parse.SchemaRegistry
}

// confResources builds the corpus's resources. The corpus leaves the cost
// guard out (it is deployment configuration, not grammar), so the entity
// lifts it. Its SQL compiles from the filter schema for each dialect
// (buildWhere), so the resources' own provider is any.
func confResources(t *testing.T) confCorpusResources {
	t.Helper()
	parts, err := parse.NewResource[confPart](onSQLite, "parts")
	require.NoError(t, err)
	owners, err := parse.NewResource[confOwner](onSQLite, "owners")
	require.NoError(t, err)
	entities, err := parse.NewResource[confEntity](onSQLite, confEntities, parse.ParserOptions(core.WithClock(confClock), core.WithMaxCost(1<<20)))
	require.NoError(t, err)
	require.NoError(t, entities.Relate("parts", parse.HasMany(parts, "entity_id", parse.Indexed(), parse.Includable())))
	require.NoError(t, entities.Relate("owner", parse.BelongsTo(owners, "owner_id", parse.Indexed())))
	bare, err := parse.NewResource[confBareEntity](onSQLite, confBare)
	require.NoError(t, err)
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, entities))
	require.NoError(t, parse.RegisterResource(reg, parts))
	require.NoError(t, parse.RegisterResource(reg, owners))
	require.NoError(t, parse.RegisterResource(reg, bare))
	return confCorpusResources{entities: entities, bare: bare, registry: reg}
}

// confSchema is the filter schema the corpus's cases are parsed against.
func confSchema(t *testing.T) *filter.ResourceSchema {
	t.Helper()
	return confResources(t).entities.FilterSchema()
}

// astNode is the language-neutral serialization of a filter.FilterNode used in
// the corpus. Logical nodes carry op + conds; leaf nodes carry field/op/value.
type astNode struct {
	Field string     `json:"field,omitempty"`
	Op    string     `json:"op"`
	Value any        `json:"value,omitempty"`
	Conds []*astNode `json:"conds,omitempty"`
}

// conformanceCase is one corpus entry.
type conformanceCase struct {
	Name         string         `json:"name"`
	Input        map[string]any `json:"input"`
	Valid        bool           `json:"valid"`
	CanonicalAST *astNode       `json:"canonicalAst,omitempty"`
	SQLPostgres  string         `json:"sqlPostgres,omitempty"`
	SQLSqlite    string         `json:"sqlSqlite,omitempty"`
	// SQLPostgresCode and SQLSqliteCode are the code a dialect refuses a
	// valid filter with: a value the grammar accepts and the dialect cannot
	// hold, such as an unsigned integer above SQLite's.
	SQLPostgresCode string `json:"sqlPostgresCode,omitempty"`
	SQLSqliteCode   string `json:"sqlSqliteCode,omitempty"`
	ErrorCode       string `json:"errorCode,omitempty"`
	// TSApplicable is false for constructs the TS client legitimately never
	// builds (e.g. server-side geo). The TS conformance test skips these.
	TSApplicable *bool `json:"tsApplicable,omitempty"`
}

// caseInput is a (name, input) pair; everything else is generated from the
// parser so Go can never drift from itself.
type caseInput struct {
	name  string
	input map[string]any
	// tsApplicable defaults to true (nil → omitted in JSON). Set to a pointer
	// to false for Go-only constructs.
	tsApplicable *bool
}

// canonicalCases is the input set. Every input is deterministic to parse:
// single-key field maps, or explicitly-ordered $and/$or arrays — never a
// multi-key field map (Go map iteration order would make the AST/SQL flaky).
func canonicalCases() []caseInput {
	return []caseInput{
		// --- equality / comparison ---
		{name: "eq_string", input: map[string]any{"status": "active"}},
		{name: "eq_number", input: map[string]any{"count": 5}},
		{name: "eq_bool", input: map[string]any{"active": true}},
		{name: "ne_number", input: map[string]any{"count": map[string]any{"$ne": 5}}},
		{name: "gt_number", input: map[string]any{"count": map[string]any{"$gt": 5}}},
		{name: "gte_number", input: map[string]any{"count": map[string]any{"$gte": 5}}},
		{name: "lt_number", input: map[string]any{"count": map[string]any{"$lt": 5}}},
		{name: "lte_number", input: map[string]any{"count": map[string]any{"$lte": 5}}},

		// --- json:",string" fields: operands are JSON strings, exact past 2^53 ---
		{name: "quoted_eq_int64_max", input: map[string]any{"serial": "9223372036854775807"}},
		{name: "quoted_eq_int64_min", input: map[string]any{"serial": "-9223372036854775808"}},
		{name: "quoted_gt_past_2_53", input: map[string]any{"serial": map[string]any{"$gt": "9007199254740992"}}},
		{name: "quoted_in", input: map[string]any{"serial": map[string]any{"$in": []any{"1", "9007199254740993"}}}},
		{name: "quoted_eq_bool", input: map[string]any{"live": "true"}},
		{name: "err_quoted_bare_number", input: map[string]any{"serial": json.Number("9007199254740993")}},
		{name: "err_quoted_bare_number_in_set", input: map[string]any{"serial": map[string]any{"$in": []any{"1", 2}}}},
		{name: "err_quoted_not_a_number", input: map[string]any{"serial": "seven"}},
		{name: "err_quoted_spaces", input: map[string]any{"serial": " 7"}},
		{name: "quoted_bool_bare_boolean", input: map[string]any{"live": true}},
		{name: "quoted_integral_spelling", input: map[string]any{"serial": "1.0e3"}},
		{name: "err_quoted_leading_plus", input: map[string]any{"serial": "+1"}},
		{name: "err_quoted_leading_zero", input: map[string]any{"serial": "01"}},
		{name: "err_quoted_null", input: map[string]any{"serial": nil}},
		{name: "quoted_uint64_max", input: map[string]any{"big": "18446744073709551615"}},
		{name: "quoted_float", input: map[string]any{"ratio": map[string]any{"$lt": "0.5"}}},
		{name: "err_quoted_float_word", input: map[string]any{"ratio": "half"}},
		{name: "err_quoted_uint_negative", input: map[string]any{"big": "-1"}, tsApplicable: ptrFalse()},
		// 64-bit ranges are the server's to check.
		{name: "err_quoted_past_int64", input: map[string]any{"serial": "9223372036854775808"}, tsApplicable: ptrFalse()},

		// --- decimals: a JSON number or a numeric string, canonicalized ---
		{name: "eq_decimal_string", input: map[string]any{"price": "10.50"}},
		{name: "eq_decimal_number", input: map[string]any{"price": 10.5}},
		{name: "gt_decimal_exponent", input: map[string]any{"price": map[string]any{"$gt": "1.5E-3"}}},
		{name: "eq_decimal_beyond_float64", input: map[string]any{"price": "0.10000000000000001"}},
		{name: "in_decimal", input: map[string]any{"price": map[string]any{"$in": []any{"9.5", 2, "-0"}}}},
		{name: "err_decimal_word", input: map[string]any{"price": "ten"}},
		{name: "err_decimal_bool", input: map[string]any{"price": true}},
		{name: "err_decimal_null", input: map[string]any{"price": nil}},
		{name: "err_decimal_hexadecimal", input: map[string]any{"price": "0x10"}},
		{name: "err_decimal_leading_plus", input: map[string]any{"price": "+1"}},
		{name: "err_decimal_leading_point", input: map[string]any{"price": ".5"}},
		{name: "err_decimal_leading_zero", input: map[string]any{"price": "007"}},
		{name: "err_decimal_beyond_digits", input: map[string]any{"price": "1e1000"}},
		{name: "err_decimal_exponent_beyond_int32", input: map[string]any{"price": "0e2147483648"}},
		{name: "err_decimal_in_member", input: map[string]any{"price": map[string]any{"$in": []any{"1", "one"}}}},

		// --- set membership ---
		{name: "in_array", input: map[string]any{"status": map[string]any{"$in": []any{"active", "offline"}}}},
		{name: "nin_array", input: map[string]any{"status": map[string]any{"$nin": []any{"error"}}}},

		// --- string matching ---
		{name: "like", input: map[string]any{"name": map[string]any{"$like": "dev%"}}},
		{name: "ilike", input: map[string]any{"name": map[string]any{"$ilike": "DEV%"}}},
		{name: "contains", input: map[string]any{"name": map[string]any{"$contains": "sensor"}}},

		// --- null / existence ---
		{name: "null_true", input: map[string]any{"name": map[string]any{"$null": true}}},
		{name: "null_false", input: map[string]any{"name": map[string]any{"$null": false}}},
		{name: "exists_true", input: map[string]any{"name": map[string]any{"$exists": true}}},
		{name: "not_null_true", input: map[string]any{"name": map[string]any{"$not_null": true}}},
		{name: "not_null_false", input: map[string]any{"name": map[string]any{"$not_null": false}}},
		// The NULL tests are one capability: declaring not_null allows them all.
		{name: "null_test_capability_not_null", input: map[string]any{"note": map[string]any{"$not_null": true}}},
		{name: "null_test_capability_null", input: map[string]any{"note": map[string]any{"$null": false}}},
		{name: "null_test_capability_exists", input: map[string]any{"note": map[string]any{"$exists": true}}},
		{name: "err_not_null_not_bool", input: map[string]any{"name": map[string]any{"$not_null": 1}}},

		// --- logical composition (ordered arrays → deterministic) ---
		{name: "and_explicit", input: map[string]any{"$and": []any{
			map[string]any{"status": "active"},
			map[string]any{"count": map[string]any{"$gt": 5}},
		}}},
		{name: "or_explicit", input: map[string]any{"$or": []any{
			map[string]any{"status": "active"},
			map[string]any{"status": "offline"},
		}}},
		{name: "not_explicit", input: map[string]any{"$not": map[string]any{"status": "error"}}},
		{name: "nested_and_or", input: map[string]any{"$and": []any{
			map[string]any{"active": true},
			map[string]any{"$or": []any{
				map[string]any{"status": "active"},
				map[string]any{"status": "offline"},
			}},
		}}},

		// --- relative time token (resolved via the pinned clock) ---
		{name: "time_token_relative", input: map[string]any{"created": map[string]any{"$gt": "now-7d"}}},

		// --- empty filter ---
		{name: "empty", input: map[string]any{}},

		// --- errors (schema / operator) ---
		{name: "err_unknown_field", input: map[string]any{"bogus": "x"}},
		{name: "err_operator_not_allowed", input: map[string]any{"active": map[string]any{"$gt": 1}}},
		{name: "err_invalid_enum", input: map[string]any{"status": "bogus"}},
		{name: "err_nin_enum", input: map[string]any{"status": map[string]any{"$nin": []any{"bogus"}}}},
		{name: "err_in_not_array", input: map[string]any{"status": map[string]any{"$in": "active"}}},
		{name: "err_null_not_bool", input: map[string]any{"name": map[string]any{"$null": "yes"}}},
		{name: "err_eq_null", input: map[string]any{"name": nil}},
		{name: "err_int_type", input: map[string]any{"count": "five"}},
		{name: "err_int_fraction", input: map[string]any{"count": 1.5}},
		{name: "err_bool_type", input: map[string]any{"active": "true"}},
		{name: "err_like_trailing_escape", input: map[string]any{"name": map[string]any{"$like": `abc\`}}},
		// An object whose keys all start with $ is an operator object, so an
		// unknown $-key is an unknown operator, on a document field too —
		// on both sides. A document with such keys is written with $eq.
		{name: "err_unknown_dollar_key", input: map[string]any{"count": map[string]any{"$bogus": 1}}},
		{name: "err_misspelt_operator_on_document", input: map[string]any{"meta": map[string]any{"$contain": map[string]any{"env": "prod"}}}},
		{name: "json_eq_dollar_keyed_document", input: map[string]any{"meta": map[string]any{"$eq": map[string]any{"$env": "prod"}}}},
		{name: "json_eq_mixed_keyed_document", input: map[string]any{"meta": map[string]any{"$env": "prod", "region": "eu"}}},
		{name: "err_operator_key_mixed_with_literal", input: map[string]any{"count": map[string]any{"$gt": 1, "x": 2}}},
		{name: "err_or_not_array", input: map[string]any{"$or": map[string]any{"status": "active"}}},
		{name: "err_or_branch_not_object", input: map[string]any{"$or": []any{"active"}}},
		{name: "err_or_empty", input: map[string]any{"$or": []any{}}},
		{name: "err_not_not_object", input: map[string]any{"$not": []any{}}},
		{name: "err_not_empty", input: map[string]any{"$not": map[string]any{}}},

		// --- JSON documents: a plain object is an equality document ---
		{name: "json_eq_document", input: map[string]any{"meta": map[string]any{"env": "prod"}}},
		{name: "json_contains", input: map[string]any{"meta": map[string]any{"$contains": map[string]any{"env": "prod"}}}},
		{name: "err_json_eq_null", input: map[string]any{"meta": map[string]any{"$eq": nil}}},
		// JSON numbers outside the portable domain are rejected rather than
		// rounded by one backend; the client cannot hold the integer exactly.
		{name: "err_json_integer_beyond_int64", input: map[string]any{"meta": map[string]any{"n": json.Number("18446744073709551615")}}, tsApplicable: ptrFalse()},
		{name: "json_decimal", input: map[string]any{"meta": map[string]any{"n": json.Number("0.1")}}},
		{name: "json_has_key", input: map[string]any{"meta": map[string]any{"$has_key": "env"}}},
		{name: "err_json_has_key_not_string", input: map[string]any{"meta": map[string]any{"$has_key": 3}}},

		// --- declared JSON paths are fields like any other ---
		{name: "path_eq", input: map[string]any{"meta.region": "eu"}},
		{name: "path_like", input: map[string]any{"meta.region": map[string]any{"$like": "eu-%"}}},
		{name: "err_path_type", input: map[string]any{"meta.region": 3}},
		{name: "err_undeclared_path", input: map[string]any{"meta.zone": "eu"}},

		// --- array elements: one element holds every predicate ---
		{name: "elements_some", input: map[string]any{"lines": map[string]any{"$some": map[string]any{"$and": []any{
			map[string]any{"sku": "a"}, map[string]any{"qty": map[string]any{"$gt": 2}},
		}}}}},
		{name: "elements_none", input: map[string]any{"lines": map[string]any{"$none": map[string]any{"sku": "a"}}}},
		{name: "err_elements_unknown_member", input: map[string]any{"lines": map[string]any{"$some": map[string]any{"colour": "red"}}}},
		{name: "err_elements_member_type", input: map[string]any{"lines": map[string]any{"$some": map[string]any{"qty": "two"}}}},
		{name: "err_elements_not_object", input: map[string]any{"lines": map[string]any{"$some": "a"}}},
		{name: "err_elements_is", input: map[string]any{"lines": map[string]any{"$is": map[string]any{}}}},

		// --- scalar arrays: one element meets every condition ---
		{name: "array_some_eq", input: map[string]any{"tags": map[string]any{"$some": map[string]any{"$eq": "a"}}}},
		{name: "array_some_and", input: map[string]any{"meta.sizes": map[string]any{"$some": map[string]any{"$and": []any{
			map[string]any{"$gte": 1}, map[string]any{"$lt": 5},
		}}}}},
		{name: "array_some_or", input: map[string]any{"meta.labels": map[string]any{"$some": map[string]any{"$or": []any{
			map[string]any{"$like": "a%"}, map[string]any{"$eq": "b"},
		}}}}},
		{name: "array_none_not", input: map[string]any{"tags": map[string]any{"$none": map[string]any{"$not": map[string]any{"$in": []any{"a", "b"}}}}}},
		{name: "array_some_any", input: map[string]any{"meta.labels": map[string]any{"$some": map[string]any{}}}},
		{name: "array_path_contains", input: map[string]any{"meta.labels": map[string]any{"$contains": []any{"a", "b"}}}},
		{name: "array_path_null", input: map[string]any{"meta.sizes": map[string]any{"$null": true}}},
		{name: "err_array_element_null_test", input: map[string]any{"tags": map[string]any{"$some": map[string]any{"$null": true}}}},
		{name: "err_array_element_operator", input: map[string]any{"meta.sizes": map[string]any{"$some": map[string]any{"$like": "1%"}}}},
		{name: "err_array_element_type", input: map[string]any{"meta.sizes": map[string]any{"$some": map[string]any{"$eq": "two"}}}},
		{name: "err_array_element_member", input: map[string]any{"tags": map[string]any{"$some": map[string]any{"sku": "a"}}}},
		{name: "err_array_element_not_object", input: map[string]any{"tags": map[string]any{"$some": "a"}}},
		{name: "err_array_element_empty_or", input: map[string]any{"tags": map[string]any{"$some": map[string]any{"$or": []any{}}}}},
		{name: "err_array_element_empty_not", input: map[string]any{"tags": map[string]any{"$some": map[string]any{"$not": map[string]any{}}}}},
		{name: "err_array_contains_member_type", input: map[string]any{"meta.sizes": map[string]any{"$contains": []any{"a"}}}},
		{name: "array_contains_scalar", input: map[string]any{"tags": map[string]any{"$contains": "a"}}},

		// --- declared relations filter by the target's own fields ---
		{name: "relation_some", input: map[string]any{"parts": map[string]any{"$some": map[string]any{"state": "worn"}}}},
		{name: "relation_none_any", input: map[string]any{"parts": map[string]any{"$none": map[string]any{}}}},
		{name: "relation_key_shorthand", input: map[string]any{"parts": "p1"}},
		{name: "relation_key_list", input: map[string]any{"parts": map[string]any{"$in": []any{"p1", "p2"}}}},
		{name: "err_relation_unknown_target_field", input: map[string]any{"parts": map[string]any{"$some": map[string]any{"colour": "red"}}}},
		{name: "err_relation_target_enum", input: map[string]any{"parts": map[string]any{"$some": map[string]any{"state": "new"}}}},
		{name: "err_relation_is_on_collection", input: map[string]any{"parts": map[string]any{"$is": map[string]any{}}}},
		{name: "err_relation_not_object", input: map[string]any{"parts": map[string]any{"$some": []any{}}}},
		{name: "err_relation_none_unknown_target_field", input: map[string]any{"parts": map[string]any{"$none": map[string]any{"colour": "red"}}}},
		{name: "err_relation_is_not_on_collection", input: map[string]any{"parts": map[string]any{"$is_not": map[string]any{}}}},

		// --- a belongs-to relation: $is and $is_not filter by its one target ---
		{name: "reference_is", input: map[string]any{"owner": map[string]any{"$is": map[string]any{"name": "acme"}}}},
		{name: "reference_is_not", input: map[string]any{"owner": map[string]any{"$is_not": map[string]any{"name": map[string]any{"$like": "ac%"}}}}},
		{name: "reference_key_shorthand", input: map[string]any{"owner": "o1"}},
		{name: "err_reference_is_unknown_target_field", input: map[string]any{"owner": map[string]any{"$is": map[string]any{"colour": "red"}}}},
		{name: "err_reference_some", input: map[string]any{"owner": map[string]any{"$some": map[string]any{}}}},

		// --- an operator's operand takes the operator's form ---
		{name: "err_ilike_not_string", input: map[string]any{"name": map[string]any{"$ilike": 5}}},
		{name: "err_exists_not_bool", input: map[string]any{"name": map[string]any{"$exists": "yes"}}},
		{name: "err_and_not_array", input: map[string]any{"$and": map[string]any{"status": "active"}}},

		// --- a float that travels as a JSON number ---
		{name: "float_gt", input: map[string]any{"score": map[string]any{"$gt": 0.5}}},
		{name: "err_float_lte_type", input: map[string]any{"score": map[string]any{"$lte": "half"}}},

		// --- timestamps: RFC 3339 or a relative token ---
		{name: "err_timestamp_type", input: map[string]any{"created": 5}},
		{name: "err_timestamp_spelling", input: map[string]any{"created": map[string]any{"$gt": "yesterday"}}, tsApplicable: ptrFalse()},

		// --- dates, times of day and intervals, as their canonical text ---
		{name: "date_gt", input: map[string]any{"day": map[string]any{"$gt": "2026-01-01"}}},
		{name: "err_date_operator", input: map[string]any{"day": map[string]any{"$gte": "2026-01-01"}}},
		{name: "err_date_spelling", input: map[string]any{"day": "01/02/2026"}, tsApplicable: ptrFalse()},
		{name: "time_gte", input: map[string]any{"at": map[string]any{"$gte": "09:30:00"}}},
		{name: "err_time_spelling", input: map[string]any{"at": "half past nine"}, tsApplicable: ptrFalse()},
		{name: "interval_in", input: map[string]any{"span": map[string]any{"$in": []any{"P1D", "PT2H30M"}}}},
		{name: "err_interval_operator", input: map[string]any{"span": map[string]any{"$lt": "P1D"}}},
		{name: "err_interval_spelling", input: map[string]any{"span": "1 day"}, tsApplicable: ptrFalse()},

		// --- network addresses ---
		{name: "inet_eq", input: map[string]any{"addr": "192.0.2.1"}},
		{name: "err_inet_spelling", input: map[string]any{"addr": "192.0.2.300"}, tsApplicable: ptrFalse()},
		{name: "cidr_eq", input: map[string]any{"subnet": "192.0.2.0/24"}},
		{name: "err_cidr_host_bits", input: map[string]any{"subnet": "192.0.2.1/24"}, tsApplicable: ptrFalse()},
		{name: "macaddr_ne", input: map[string]any{"mac": map[string]any{"$ne": "08:00:2b:01:02:03"}}},
		{name: "err_macaddr_spelling", input: map[string]any{"mac": map[string]any{"$ne": "08:00:2b"}}, tsApplicable: ptrFalse()},
		{name: "err_macaddr_type", input: map[string]any{"mac": 8}},

		// --- UUIDs ---
		{name: "uuid_in", input: map[string]any{"key": map[string]any{"$in": []any{"6ba7b810-9dad-11d1-80b4-00c04fd430c8"}}}},
		{name: "err_uuid_type", input: map[string]any{"key": 7}},
		{name: "err_uuid_spelling", input: map[string]any{"key": "not-a-uuid"}, tsApplicable: ptrFalse()},

		// --- geo: the server reads the operand's shape ---
		{name: "geo_near", input: map[string]any{"spot": map[string]any{"$near": map[string]any{"lat": 40.7128, "lon": -74.006, "distance": "1500m"}}}},
		{name: "geo_within_box", input: map[string]any{"spot": map[string]any{"$within": map[string]any{
			"top_left": map[string]any{"lat": 40.8, "lon": -74.1}, "bottom_right": map[string]any{"lat": 40.65, "lon": -73.7},
		}}}},
		{name: "geo_intersects_polygon", input: map[string]any{"spot": map[string]any{"$intersects": map[string]any{"points": []any{
			map[string]any{"lat": 0, "lon": 0}, map[string]any{"lat": 1, "lon": 1}, map[string]any{"lat": 0, "lon": 1},
		}}}}},
		{name: "err_geo_operator", input: map[string]any{"spot": map[string]any{"$eq": "x"}}},
		{name: "err_geo_near_without_distance", input: map[string]any{"spot": map[string]any{"$near": map[string]any{"lat": 1, "lon": 2}}}, tsApplicable: ptrFalse()},
		{name: "err_geo_within_not_a_shape", input: map[string]any{"spot": map[string]any{"$within": 5}}, tsApplicable: ptrFalse()},
		{name: "err_geo_intersects_two_points", input: map[string]any{"spot": map[string]any{"$intersects": map[string]any{"points": []any{
			map[string]any{"lat": 0, "lon": 0}, map[string]any{"lat": 1, "lon": 1},
		}}}}, tsApplicable: ptrFalse()},

		// --- a struct field is a document of a fixed shape ---
		{name: "object_has_key", input: map[string]any{"dims": map[string]any{"$has_key": "w"}}},
		{name: "object_eq_document", input: map[string]any{"dims": map[string]any{"w": 2, "h": 3}}},
		{name: "err_object_operator", input: map[string]any{"dims": map[string]any{"$contains": map[string]any{"w": 2}}}},

		// --- a field with no filter semantics takes the NULL tests only ---
		{name: "any_null", input: map[string]any{"blob": map[string]any{"$null": true}}},
		{name: "err_any_operator", input: map[string]any{"blob": map[string]any{"$eq": "x"}}},
	}
}

// toASTNode serializes a parsed FilterNode into the language-neutral astNode.
func toASTNode(n *filter.FilterNode) *astNode {
	if n == nil {
		return nil
	}
	out := &astNode{Op: string(n.Op), Value: n.Value}
	if n.Field != nil {
		out.Field = n.Field.ExternalName
	}
	for _, c := range n.Children {
		out.Conds = append(out.Conds, toASTNode(c))
	}
	return out
}

// runCase parses one input and records the canonical result. Inputs are
// marshaled to JSON and fed through Parse — the real wire path — so numbers
// decode to float64 exactly as a `?filter=` query string would. Feeding a Go
// map straight to ParseMap would smuggle native int/bool types the wire never
// carries.
func runCase(t *testing.T, schema *filter.ResourceSchema, in caseInput) conformanceCase {
	t.Helper()
	c := conformanceCase{Name: in.name, Input: in.input, TSApplicable: in.tsApplicable}

	body, err := json.Marshal(in.input)
	if err != nil {
		t.Fatalf("case %q: marshal input: %v", in.name, err)
	}

	confCfg := core.DefaultConfig()
	confCfg.Clock = confClock
	parser := filter.NewFilterParser(schema, confCfg)
	ast, err := parser.Parse(string(body))
	if err != nil {
		pe, ok := errors.AsType[*core.ParseError](err)
		if !ok {
			t.Fatalf("case %q: refused with a server fault, not a ParseError: %v", in.name, err)
		}
		c.ErrorCode = pe.Code
		return c
	}

	c.Valid = true
	if ast != nil {
		c.CanonicalAST = toASTNode(ast.Root)
	}
	c.SQLPostgres, c.SQLPostgresCode = buildWhere(t, in.name, schema, ast, filter.PostgresDialect{})
	c.SQLSqlite, c.SQLSqliteCode = buildWhere(t, in.name, schema, ast, filter.SQLiteDialect{})
	return c
}

// buildWhere generates the WHERE fragment for a dialect, "" for an empty
// AST, or the code of the ParseError the dialect refuses the request's
// values with. Any other build error is a defect in the case or the builder:
// a valid request never fails as the server's fault, and a valid filter
// that filters something has SQL.
func buildWhere(t *testing.T, name string, schema *filter.ResourceSchema, ast *filter.FilterAST, dialect filter.Dialect) (sql, code string) {
	t.Helper()
	if ast == nil || ast.Root == nil {
		return "", ""
	}
	res, err := filter.NewSQLBuilder(schema, dialect).Build(ast)
	if err != nil {
		pe, ok := errors.AsType[*core.ParseError](err)
		if !ok {
			t.Fatalf("case %q: %s build: %v", name, dialect.Name(), err)
		}
		return "", pe.Code
	}
	if res.Where == "" {
		t.Fatalf("case %q: %s built no SQL for a filter that filters", name, dialect.Name())
	}
	return res.Where, ""
}

// normalize is v's JSON with its object keys sorted and its numbers exact,
// so an AST read from the corpus and one built live compare equal whatever
// order a struct operand's fields marshal in.
func normalize(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var canonical any
	require.NoError(t, dec.Decode(&canonical))
	out, err := json.Marshal(canonical)
	require.NoError(t, err)
	return string(out)
}

// filterCorpus is testdata/filter_conformance.json.
type filterCorpus struct {
	// Schemas are the documents the registry serves for the corpus's
	// resources, by name: the client validates against them, relation
	// targets included.
	Schemas map[string]*parse.SchemaResponse `json:"schemas"`
	// Cases are filters on confEntities.
	Cases []conformanceCase `json:"cases"`
	// Sort are sort parameters to confEntities' list.
	Sort []sortCase `json:"sort"`
	// List are list requests: their query strings.
	List []listCase `json:"list"`
}

// sortCase is a sort parameter and the server's verdict on it.
type sortCase struct {
	Name         string `json:"name"`
	Input        string `json:"input"`
	Valid        bool   `json:"valid"`
	ErrorCode    string `json:"errorCode,omitempty"`
	TSApplicable *bool  `json:"tsApplicable,omitempty"`
}

// listCase is a list request's query string and the server's verdict on
// it: ParseList and BuildList, as a list handler runs them.
type listCase struct {
	Name string `json:"name"`
	// Resource is the resource listed; empty is confEntities.
	Resource     string     `json:"resource,omitempty"`
	Input        url.Values `json:"input"`
	Valid        bool       `json:"valid"`
	ErrorCode    string     `json:"errorCode,omitempty"`
	TSApplicable *bool      `json:"tsApplicable,omitempty"`
}

func sortInputs() []sortCase {
	return []sortCase{
		{Name: "one_field", Input: "name"},
		{Name: "descending", Input: "-count"},
		{Name: "ascending_sign", Input: "+name"},
		{Name: "several", Input: "name,-created,id"},
		{Name: "empty", Input: ""},
		{Name: "spaces_around_fields", Input: " name , -count "},
		{Name: "empty_slot", Input: "name,,count"},
		{Name: "err_unknown_field", Input: "colour"},
		{Name: "err_filter_field_not_sortable", Input: "status"},
		{Name: "err_descending_not_sortable", Input: "-price"},
		{Name: "err_too_many_fields", Input: "name,count,created,id"},
		{Name: "blank_slots_count_toward_no_limit", Input: "name,,,count"},
		{Name: "blank_slots_only", Input: " , "},
		{Name: "err_sign_alone", Input: "-"},
		{Name: "err_double_sign", Input: "--name"},
		{Name: "err_repeated_field", Input: "name,-name"},
	}
}

func listInputs() []listCase {
	no := false
	return []listCase{
		{Name: "defaults", Input: url.Values{}},
		{Name: "include_relation", Input: url.Values{filter.ParamInclude: {"parts"}}},
		{Name: "err_include_not_a_relation", Input: url.Values{filter.ParamInclude: {"name"}}},
		{Name: "err_include_not_includable", Input: url.Values{filter.ParamInclude: {"owner"}}},
		{Name: "err_include_unknown", Input: url.Values{filter.ParamInclude: {"nope"}}},
		{Name: "facets_field", Input: url.Values{filter.ParamFacets: {"status"}}},
		{Name: "err_facets_not_facetable", Input: url.Values{filter.ParamFacets: {"name"}}},
		{Name: "err_facets_unknown", Input: url.Values{filter.ParamFacets: {"nope"}}},
		{Name: "err_search_without_search_fields", Input: url.Values{filter.ParamSearch: {"drill"}}},
		{Name: "search_blank", Input: url.Values{filter.ParamSearch: {"  "}}},
		{Name: "sort_and_filter", Input: url.Values{filter.ParamSort: {"-count"}, filter.ParamFilter: {`{"status":"active"}`}}},
		{Name: "err_sort_too_many_fields", Input: url.Values{filter.ParamSort: {"name,count,created,id"}}},
		{Name: "err_filter_unknown_field", Input: url.Values{filter.ParamFilter: {`{"colour":"red"}`}}},
		{Name: "page_size", Input: url.Values{filter.ParamPageSize: {"10"}}},
		{Name: "err_page_size_zero", Input: url.Values{filter.ParamPageSize: {"0"}}, TSApplicable: &no},
		{Name: "err_page_size_word", Input: url.Values{filter.ParamPageSize: {"ten"}}, TSApplicable: &no},
		{Name: "fields_mask", Input: url.Values{filter.ParamFields: {"id,name"}}},
		{Name: "err_fields_unknown", Input: url.Values{filter.ParamFields: {"nope"}}, TSApplicable: &no},
		{Name: "err_include_total_word", Input: url.Values{filter.ParamIncludeTotal: {"yes"}}, TSApplicable: &no},
		{Name: "err_cursor_garbage", Input: url.Values{filter.ParamCursor: {"garbage"}}, TSApplicable: &no},
		{Name: "err_list_parameter_twice", Input: url.Values{filter.ParamSort: {"name", "-name"}}, TSApplicable: &no},
		{Name: "bare_defaults", Resource: confBare, Input: url.Values{}},
		{Name: "filter_on_untagged_key", Resource: confBare, Input: url.Values{filter.ParamFilter: {`{"id":"a"}`}}},
		{Name: "untagged_key_range", Resource: confBare, Input: url.Values{filter.ParamFilter: {`{"id":{"$gte":"a"}}`}}},
		{Name: "err_untagged_key_operator", Resource: confBare, Input: url.Values{filter.ParamFilter: {`{"id":{"$ne":"a"}}`}}},
		{Name: "err_bare_unknown_field", Resource: confBare, Input: url.Values{filter.ParamFilter: {`{"name":"a"}`}}},
		{Name: "err_bare_sort", Resource: confBare, Input: url.Values{filter.ParamSort: {"id"}}},
		{Name: "err_bare_include", Resource: confBare, Input: url.Values{filter.ParamInclude: {"id"}}},
		{Name: "err_bare_facets", Resource: confBare, Input: url.Values{filter.ParamFacets: {"id"}}},
	}
}

// listVerdict is a list handler's verdict on q: nil, or the ParseError it
// answers. Anything else fails the test: a request is never refused with a
// server fault.
func listVerdict[T any](t *testing.T, name string, res *parse.Resource[T], q url.Values) (valid bool, code string) {
	t.Helper()
	req, err := res.ParseList(q)
	if err == nil {
		_, err = res.BuildList(req, parse.Scope{})
	}
	if err == nil {
		return true, ""
	}
	pe, ok := errors.AsType[*core.ParseError](err)
	if !ok {
		t.Fatalf("case %q: refused with a server fault, not a ParseError: %v", name, err)
	}
	return false, pe.Code
}

// buildFilterCorpus builds testdata/filter_conformance.json.
func buildFilterCorpus(t *testing.T) []byte {
	t.Helper()
	res := confResources(t)
	schema := res.entities.FilterSchema()
	corpus := filterCorpus{Schemas: map[string]*parse.SchemaResponse{}}
	for _, name := range res.registry.ResourceNames() {
		corpus.Schemas[name] = res.registry.SchemaFor(name)
	}
	for _, in := range canonicalCases() {
		c := runCase(t, schema, in)
		// Determinism guard: re-run and require an identical serialization.
		if normalize(t, c) != normalize(t, runCase(t, schema, in)) {
			t.Fatalf("case %q is non-deterministic across runs; use single-key maps or ordered $and/$or arrays", in.name)
		}
		corpus.Cases = append(corpus.Cases, c)
	}
	for _, c := range sortInputs() {
		c.Valid, c.ErrorCode = listVerdict(t, c.Name, res.entities, url.Values{filter.ParamSort: {c.Input}})
		corpus.Sort = append(corpus.Sort, c)
	}
	for _, c := range listInputs() {
		switch c.Resource {
		case "":
			c.Valid, c.ErrorCode = listVerdict(t, c.Name, res.entities, c.Input)
		case confBare:
			c.Valid, c.ErrorCode = listVerdict(t, c.Name, res.bare, c.Input)
		default:
			t.Fatalf("list case %q: no resource %q", c.Name, c.Resource)
		}
		corpus.List = append(corpus.List, c)
	}
	data, err := json.MarshalIndent(corpus, "", "  ")
	require.NoError(t, err)
	return append(data, '\n')
}

// TestEmitFilterConformance builds the corpus and requires the file to hold
// it byte for byte: a JSON comparison would read the quoted 64-bit integers
// as float64 and miss a change in their low digits. -update writes it.
func TestEmitFilterConformance(t *testing.T) {
	got := buildFilterCorpus(t)
	if *updateRendered {
		require.NoError(t, os.WriteFile(conformanceCorpusPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(conformanceCorpusPath)
	require.NoError(t, err, "missing corpus; regenerate with -update")
	assert.Equal(t, string(want), string(got), "filter corpus is stale; regenerate with -update")
}

// readFilterCorpus reads the corpus with its numbers exact: a quoted int64
// case's AST holds 2^63-1, which a float64 would round.
func readFilterCorpus(t *testing.T) filterCorpus {
	t.Helper()
	data, err := os.ReadFile(conformanceCorpusPath)
	require.NoError(t, err, "read the corpus (regenerate with -run TestEmitFilterConformance -update)")
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var corpus filterCorpus
	require.NoError(t, dec.Decode(&corpus))
	return corpus
}

func ptrFalse() *bool {
	f := false
	return &f
}

// Go⇔TS merge-patch conformance corpus. The Go parser is
// authoritative: each input below is parsed by ParseMergePatch against
// confPatch's schema and its validity and error code recorded in
// testdata/patch_conformance.json, with the registry's schema response the
// TS validator reads. sdk/ts/src/patch/conformance.test.ts must agree on
// every case the client can judge (tsApplicable).
//
// Regenerate after an intentional change:
//
//	go test -tags=unit -run TestEmitPatchConformance -update ./

const patchConformancePath = "testdata/patch_conformance.json"

type confPatch struct {
	ID       string          `json:"id"        db:"id"`
	Name     string          `json:"name"      db:"name"      parse:"patch"`
	Nick     *string         `json:"nick"      db:"nick"      parse:"patch"`
	Status   string          `json:"status"    db:"status"    parse:"patch" enum:"active,offline"`
	Count    int32           `json:"count"     db:"count"     parse:"patch"`
	Small    int8            `json:"small"     db:"small"     parse:"patch"`
	Ratio    float64         `json:"ratio"     db:"ratio"     parse:"patch"`
	Price    decimal.Decimal `json:"price"     db:"price"     parse:"patch"`
	Active   bool            `json:"active"    db:"active"    parse:"patch"`
	Tags     []string        `json:"tags"      db:"tags"      parse:"patch"`
	Scores   []int16         `json:"scores"    db:"scores"    parse:"patch"`
	Labels   map[string]any  `json:"labels"    db:"labels"    parse:"patch"`
	SeenAt   *time.Time      `json:"seen_at"   db:"seen_at"   parse:"patch"`
	Readonly string          `json:"readonly"  db:"readonly"`
	Modes    []string        `json:"modes"     db:"modes"     parse:"patch" enum:"auto,manual"`
	Mode     *string         `json:"mode"      db:"mode"      parse:"patch" enum:"auto,manual"`
	Currency string          `json:"currency"  db:"currency"  parse:"patch" minlen:"3" maxlen:"3"`
	Level    int32           `json:"level"     db:"level"     parse:"patch" min:"1" max:"10"`
	Rate     float32         `json:"rate"      db:"rate"      parse:"patch" min:"0" max:"0.1"`
	Amount   decimal.Decimal `json:"amount"    db:"amount"    parse:"patch" min:"0.01" max:"999.99"`
	Codes    []string        `json:"codes"     db:"codes"     parse:"patch" maxlen:"2"`
	Zones    []string        `json:"zones"     db:"zones"     parse:"patch" minitems:"1" maxitems:"2"`
	// Pinned is nullable, but its only operation is replace: null, a
	// remove, is not one it takes.
	Pinned *string `json:"pinned" db:"pinned" parse:"patch" patch_ops:"replace"`
	// Serial, Big, Quota, Live and Share travel as JSON strings
	// (json:",string").
	Serial int64   `json:"serial,string" db:"serial" parse:"patch"`
	Big    uint64  `json:"big,string"    db:"big"    parse:"patch"`
	Quota  int64   `json:"quota,string"  db:"quota"  parse:"patch" min:"0" max:"9007199254740993"`
	Live   bool    `json:"live,string"   db:"live"   parse:"patch"`
	Share  float32 `json:"share,string"  db:"share"  parse:"patch"`
	Opt    *int64  `json:"opt,string"    db:"opt"    parse:"patch"`
	// Members is a writable relation: its patch value is the membership's
	// target keys.
	Members []string `json:"members" db:"-" parse:"patch" relation:"conf_members,patch_id,member_id"`
}

// confTag is the target of confPatch's writable relation quoted_tags, a key
// that travels as a JSON string.
type confTag struct {
	ID int64 `json:"id,string" db:"id"`
}

type patchCase struct {
	Name         string         `json:"name"`
	Input        map[string]any `json:"input"`
	Valid        bool           `json:"valid"`
	ErrorCode    string         `json:"errorCode,omitempty"`
	TSApplicable *bool          `json:"tsApplicable,omitempty"`
}

type patchCorpus struct {
	Schema *parse.SchemaResponse `json:"schema"`
	Cases  []patchCase           `json:"cases"`
}

// nestedDocuments is a document holding n empty documents: a merge patch
// of n+1 operations on one top-level member.
func nestedDocuments(n int) map[string]any {
	doc := make(map[string]any, n)
	for i := range n {
		doc[fmt.Sprintf("k%02d", i)] = map[string]any{}
	}
	return doc
}

func patchInputs() []patchCase {
	no := false
	cases := []patchCase{
		{Name: "string", Input: map[string]any{"name": "a"}},
		{Name: "err_string_type", Input: map[string]any{"name": 3}},
		{Name: "err_null_on_value_field", Input: map[string]any{"name": nil}},
		{Name: "null_on_pointer_field", Input: map[string]any{"nick": nil}},
		{Name: "enum", Input: map[string]any{"status": "active"}},
		{Name: "err_enum", Input: map[string]any{"status": "busy"}},
		{Name: "int", Input: map[string]any{"count": 5}},
		{Name: "err_int_fraction", Input: map[string]any{"count": 1.5}},
		{Name: "err_int_type", Input: map[string]any{"count": "5"}},
		{Name: "err_int32_range", Input: map[string]any{"count": 2147483648}},
		{Name: "err_int8_range", Input: map[string]any{"small": 128}},
		{Name: "int8_min", Input: map[string]any{"small": -128}},
		{Name: "float", Input: map[string]any{"ratio": 0.5}},
		{Name: "err_float_type", Input: map[string]any{"ratio": "half"}},
		{Name: "decimal_string", Input: map[string]any{"price": "10.25"}},
		{Name: "decimal_number", Input: map[string]any{"price": 10.5}},
		{Name: "decimal_exponent", Input: map[string]any{"price": "1.5e2"}},
		{Name: "err_decimal_word", Input: map[string]any{"price": "ten"}},
		{Name: "err_decimal_bool", Input: map[string]any{"price": true}},
		{Name: "err_decimal_null", Input: map[string]any{"price": nil}},
		{Name: "err_decimal_leading_plus", Input: map[string]any{"price": "+1"}},
		{Name: "err_decimal_beyond_digits", Input: map[string]any{"price": "1e1000"}},
		{Name: "bool", Input: map[string]any{"active": false}},
		{Name: "err_bool_type", Input: map[string]any{"active": "false"}},
		{Name: "array", Input: map[string]any{"tags": []any{"a", "b"}}},
		{Name: "err_array_type", Input: map[string]any{"tags": "a"}},
		{Name: "err_array_element_type", Input: map[string]any{"tags": []any{"a", 1}}},
		{Name: "err_array_element_range", Input: map[string]any{"scores": []any{1, 40000}}},
		{Name: "clear_array", Input: map[string]any{"tags": nil}},
		{Name: "document_merge", Input: map[string]any{"labels": map[string]any{"a": 1, "old": nil}}},
		{Name: "timestamp", Input: map[string]any{"seen_at": "2026-10-19T12:00:00Z"}},
		{Name: "err_timestamp_format", Input: map[string]any{"seen_at": "yesterday"}, TSApplicable: &no},
		{Name: "err_timestamp_type", Input: map[string]any{"seen_at": 5}},
		{Name: "err_not_patchable", Input: map[string]any{"readonly": "x"}},
		{Name: "err_unknown_field", Input: map[string]any{"colour": "red"}},
		{Name: "several", Input: map[string]any{"name": "a", "count": 1, "active": true}},
		{Name: "array_enum", Input: map[string]any{"modes": []any{"auto", "manual"}}},
		{Name: "err_array_enum", Input: map[string]any{"modes": []any{"auto", "off"}}},
		{Name: "null_clears_nullable_enum", Input: map[string]any{"mode": nil}},
		{Name: "length_bounds", Input: map[string]any{"currency": "USD"}},
		{Name: "length_counts_code_points", Input: map[string]any{"currency": "€😀ß"}},
		{Name: "err_maxlen", Input: map[string]any{"currency": "USDX"}},
		{Name: "err_minlen", Input: map[string]any{"currency": "US"}},
		{Name: "int_bounds", Input: map[string]any{"level": 10}},
		{Name: "err_int_max", Input: map[string]any{"level": 11}},
		{Name: "err_int_min", Input: map[string]any{"level": 0}},
		{Name: "float32_bound_at_its_precision", Input: map[string]any{"rate": 0.1}},
		{Name: "err_float_max", Input: map[string]any{"rate": 0.11}},
		{Name: "decimal_bounds", Input: map[string]any{"amount": "999.99"}},
		{Name: "decimal_bound_exponent", Input: map[string]any{"amount": "9.9999e2"}},
		{Name: "decimal_bound_number", Input: map[string]any{"amount": 0.01}},
		{Name: "err_decimal_max_exact", Input: map[string]any{"amount": "999.990000000000000001"}},
		{Name: "err_decimal_min", Input: map[string]any{"amount": "0.001"}},
		{Name: "array_element_length", Input: map[string]any{"codes": []any{"ab", "c"}}},
		{Name: "err_array_element_length", Input: map[string]any{"codes": []any{"ab", "abc"}}},
		{Name: "items_bounds", Input: map[string]any{"zones": []any{"a", "b"}}},
		{Name: "err_items_min", Input: map[string]any{"zones": []any{}}},
		{Name: "err_items_max", Input: map[string]any{"zones": []any{"a", "b", "c"}}},
		{Name: "err_items_clear", Input: map[string]any{"zones": nil}},
		{Name: "quoted_int64_max", Input: map[string]any{"serial": "9223372036854775807"}},
		{Name: "quoted_int64_min", Input: map[string]any{"serial": "-9223372036854775808"}},
		{Name: "err_quoted_bare_number", Input: map[string]any{"serial": json.Number("9007199254740993")}},
		{Name: "err_quoted_past_int64", Input: map[string]any{"serial": "9223372036854775808"}},
		{Name: "err_quoted_not_a_number", Input: map[string]any{"serial": "seven"}},
		{Name: "err_quoted_spaces", Input: map[string]any{"serial": " 7"}},
		{Name: "quoted_uint64_max", Input: map[string]any{"big": "18446744073709551615"}},
		{Name: "err_quoted_uint_negative", Input: map[string]any{"big": "-1"}},
		{Name: "quoted_bound_past_2_53", Input: map[string]any{"quota": "9007199254740993"}},
		{Name: "err_quoted_bound_past_2_53", Input: map[string]any{"quota": "9007199254740994"}},
		{Name: "quoted_bool", Input: map[string]any{"live": "false"}},
		{Name: "quoted_bool_bare_boolean", Input: map[string]any{"live": false}},
		{Name: "quoted_integral_spelling", Input: map[string]any{"serial": "1e3"}},
		{Name: "err_quoted_leading_plus", Input: map[string]any{"serial": "+1"}},
		{Name: "err_quoted_leading_zero", Input: map[string]any{"serial": "01"}},
		{Name: "quoted_null_clears_nullable", Input: map[string]any{"opt": nil}},
		{Name: "err_quoted_null_on_value_field", Input: map[string]any{"serial": nil}},
		{Name: "quoted_float", Input: map[string]any{"share": "0.5"}},
		{Name: "err_quoted_float_word", Input: map[string]any{"share": "half"}},
		{Name: "relation_list", Input: map[string]any{"members": []any{"a", "b"}}},
		{Name: "err_relation_not_a_list", Input: map[string]any{"members": "a"}},
		{Name: "relation_null_clears", Input: map[string]any{"members": nil}},
		{Name: "relation_empty_clears", Input: map[string]any{"members": []any{}}},
		{Name: "err_relation_element_type", Input: map[string]any{"members": []any{1}}},
		{Name: "err_relation_null_element", Input: map[string]any{"members": []any{"a", nil}}},
		{Name: "quoted_relation_keys", Input: map[string]any{"quoted_tags": []any{"5", "9007199254740993"}}},
		{Name: "err_quoted_relation_bare_number", Input: map[string]any{"quoted_tags": []any{json.Number("5")}}},
		{Name: "err_quoted_relation_not_a_number", Input: map[string]any{"quoted_tags": []any{"five"}}},
		{Name: "replace_where_remove_is_not_allowed", Input: map[string]any{"pinned": "a"}},
		{Name: "err_remove_not_allowed", Input: map[string]any{"pinned": nil}},
		{Name: "operations_at_the_maximum", Input: map[string]any{"labels": nestedDocuments(49)}},
		{Name: "err_too_many_operations", Input: map[string]any{"labels": nestedDocuments(50)}},
	}
	return cases
}

func buildPatchCorpus(t *testing.T) []byte {
	t.Helper()
	res := parse.MustResource[confPatch](onSQLite, "conf_patches")
	res.MustRelate("quoted_tags", parse.ManyToMany(parse.MustResource[confTag](onSQLite, "conf_tags"),
		parse.Through("conf_patch_tags", "patch_id", "tag_id"), parse.Writable()))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, res))
	out := patchCorpus{Schema: reg.SchemaFor("conf_patches")}
	for _, c := range patchInputs() {
		body, err := json.Marshal(c.Input)
		require.NoError(t, err)
		_, err = res.ParseMergePatch(body)
		c.Valid = err == nil
		pe, ok := errors.AsType[*core.ParseError](err)
		if ok {
			c.ErrorCode = pe.Code
		} else if err != nil {
			t.Fatalf("case %s: not a parse error: %v", c.Name, err)
		}
		out.Cases = append(out.Cases, c)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	require.NoError(t, err)
	return append(data, '\n')
}

func TestEmitPatchConformance(t *testing.T) {
	got := buildPatchCorpus(t)
	if *updateRendered {
		require.NoError(t, os.WriteFile(patchConformancePath, got, 0o644))
		return
	}
	want, err := os.ReadFile(patchConformancePath)
	require.NoError(t, err, "missing corpus; regenerate with -update")
	require.JSONEq(t, string(want), string(got), "patch corpus is stale; regenerate with -update")
}

func parseErrorCode(err error) string {
	pe, ok := errors.AsType[*core.ParseError](err)
	if ok {
		return pe.Code
	}
	return ""
}

// Every corpus case reaches the same verdict through ?filter= and through a
// POST body's filter member, and that verdict is the corpus's: the same
// validity, error code and canonical AST.
func TestFilterConformance_ThroughBothListTransports(t *testing.T) {
	corpus := readFilterCorpus(t).Cases
	require.NotEmpty(t, corpus)

	res := confResources(t).entities
	for _, c := range corpus {
		t.Run(c.Name, func(t *testing.T) {
			doc, err := json.Marshal(c.Input)
			require.NoError(t, err)
			body, err := json.Marshal(map[string]json.RawMessage{"filter": doc})
			require.NoError(t, err)

			fromQuery, queryErr := res.ParseList(url.Values{"filter": {string(doc)}})
			fromBody, bodyErr := res.ParseListBody(bytes.NewReader(body))

			assert.Equal(t, parseErrorCode(queryErr), parseErrorCode(bodyErr), "both transports refuse alike")
			assert.Equal(t, fromQuery, fromBody, "both transports parse to one request")

			if !c.Valid {
				require.Error(t, queryErr)
				assert.Equal(t, c.ErrorCode, parseErrorCode(queryErr))
				return
			}
			require.NoError(t, queryErr)
			var got *astNode
			if fromQuery.Filter() != nil && fromQuery.Filter().AST() != nil {
				got = toASTNode(fromQuery.Filter().AST().Root)
			}
			assert.Equal(t, normalize(t, c.CanonicalAST), normalize(t, got))
		})
	}
}

// The server's half of parse/client's conformance. parse/client is a module
// of its own that imports no parse package, and parse does not require it:
// its tests build each request the TestClient tests check and record it in
// testdata/client_requests.json, failing when the client builds anything
// else. Here the server reads every recorded request: a filter for a corpus
// case means what the corpus input means, and the list parameters, geo
// filters and JSON Patch documents parse as the server's grammar has them.
//
// Regenerate after an intentional change to the client's encoding, from
// parse/client:
//
//	go test -tags=unit -run TestClientRequests -update ./

const clientRequestsPath = "testdata/client_requests.json"

// clientRequests is testdata/client_requests.json: the requests parse/client
// builds, by what each is for.
type clientRequests struct {
	// Filters are the documents it builds for filter corpus cases, by case.
	Filters map[string]json.RawMessage `json:"filters"`
	// GeoFilters are geo filters on a place's location, by shape.
	GeoFilters map[string]json.RawMessage `json:"geo_filters"`
	// List is one list request with every parameter set, as a query string
	// and as a POST body.
	List struct {
		Query url.Values      `json:"query"`
		Body  json.RawMessage `json:"body"`
	} `json:"list"`
	// Operations are JSON Patch documents of one operation, by the
	// constructor that built it.
	Operations map[string]json.RawMessage `json:"operations"`
	// JSONPatch replaces, removes and appends members of a
	// schemaDocEntity and changes its tags' membership.
	JSONPatch json.RawMessage `json:"json_patch"`
	// QuotedRelation changes the membership of a relation whose target key
	// travels as a JSON string, by JSON Patch and by merge patch.
	QuotedRelation struct {
		JSONPatch  json.RawMessage `json:"json_patch"`
		MergePatch json.RawMessage `json:"merge_patch"`
	} `json:"quoted_relation"`
}

func readClientRequests(t *testing.T) clientRequests {
	t.Helper()
	data, err := os.ReadFile(clientRequestsPath)
	require.NoError(t, err, "missing fixture; regenerate it from parse/client with -update")
	var requests clientRequests
	require.NoError(t, json.Unmarshal(data, &requests))
	return requests
}

// The server reads what the client builds as it reads the corpus input:
// the same verdict and code, the same canonical AST and the same SQL.
func TestClient_FiltersMeanWhatTheCorpusMeans(t *testing.T) {
	schema := confSchema(t)
	built := readClientRequests(t).Filters
	require.NotEmpty(t, built)
	cases := map[string]bool{}
	for _, want := range readFilterCorpus(t).Cases {
		cases[want.Name] = true
		body, ok := built[want.Name]
		if !ok {
			continue
		}
		t.Run(want.Name, func(t *testing.T) {
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
	for name := range built {
		assert.True(t, cases[name], "the client built a filter for %q, which is no corpus case", name)
	}
}

func TestClient_JSONPatchOperationsParse(t *testing.T) {
	res, err := parse.NewResource[schemaDocEntity](onPostgres, "doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	body := readClientRequests(t).JSONPatch
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
	docs, err := parse.NewResource[schemaDocEntity](onPostgres, "doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	ops, err := parse.NewResource[opEntity](onPostgres, "op_entities")
	require.NoError(t, err)
	tests := map[string]struct {
		docs bool // on schemaDocEntity, whose array and relation take it; else opEntity
		want patch.PatchOp
		code string // BuildUpdate's refusal; empty when it compiles
	}{
		"replace":          {false, patch.PatchOpReplace, ""},
		"remove":           {false, patch.PatchOpRemove, ""},
		"add":              {false, patch.PatchOpAdd, ""},
		"move":             {false, patch.PatchOpMove, patch.ECodeOpNotSQLCompilable.String()},
		"copy":             {false, patch.PatchOpCopy, patch.ECodeOpNotSQLCompilable.String()},
		"test":             {false, patch.PatchOpTest, patch.ECodeTestOpSQL.String()},
		"append":           {true, patch.PatchOpArrayAppend, ""},
		"array append":     {true, patch.PatchOpArrayAppend, ""},
		"relation add":     {true, patch.PatchOpAdd, ""},
		"relation remove":  {true, patch.PatchOpRemove, ""},
		"relation replace": {true, patch.PatchOpReplace, ""},
	}
	built := readClientRequests(t).Operations
	require.Equal(t, slices.Sorted(maps.Keys(tests)), slices.Sorted(maps.Keys(built)), "the client builds a document per constructor this test checks")
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			body := built[name]
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
				_, err = docs.BuildUpdate(parsed, parse.ByID("e1"), parse.Scope{Tenant: "t1"})
			} else {
				_, err = ops.BuildUpdate(parsed, parse.ByID("e1"), parse.Scope{})
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
// encodes, ParseList reads as ParseListInput reads the same request, and
// the body is the same request.
func TestClient_ListParamsAreTheServersParameters(t *testing.T) {
	res := widgets(t)
	list := readClientRequests(t).List
	for key := range list.Query {
		assert.True(t, filter.IsListParam(key), "%q is no list parameter", key)
	}
	assert.Len(t, list.Query, 10, "every member encodes to its own parameter")

	got, err := res.ParseList(list.Query)
	require.NoError(t, err)
	want, err := res.ParseListInput(parse.ListInput{
		Filter: `{"$and":[{"name":{"$eq":"beta"}},{"rank":{"$gte":1}}]}`, Sort: "-rank,name", Cursor: "abc",
		PageSize: 3, IncludeTotal: true, IncludeDeleted: true, Search: "b",
		Facets: []string{"name"}, Include: []string{"tags"}, Fields: []string{"id", "name"},
	})
	require.NoError(t, err)
	assert.Equal(t, want, got)

	fromBody, err := res.ParseListBody(bytes.NewReader(list.Body))
	require.NoError(t, err)
	assert.Equal(t, want, fromBody, "the body is the same request as the query string")
	var members map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(list.Body, &members))
	for key := range members {
		assert.True(t, filter.IsListParam(key), "%q is no list parameter", key)
	}
	assert.Len(t, members, 10, "every member encodes to its own body member")
}

func TestClient_GeoFiltersParse(t *testing.T) {
	type place struct {
		ID       string         `json:"id"       db:"id"`
		Location map[string]any `json:"location" parse:"filter" filter_ops:"near,within,intersects" geo:"jsonb" geo_srid:"4326"`
	}
	res, err := parse.NewResource[place](onPostgres, "places")
	require.NoError(t, err)
	built := readClientRequests(t).GeoFilters
	require.NotEmpty(t, built)
	for name, body := range built {
		_, err = res.ParseFilter(string(body))
		assert.NoError(t, err, "%s: %s", name, body)
	}
}

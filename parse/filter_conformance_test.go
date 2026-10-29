//go:build unit

package parse_test

// Go⇔TS filter-grammar conformance corpus.
//
// The Go filter parser is authoritative. This file walks a canonical set of
// filter inputs through the real parser + both SQL dialects and records the
// resulting AST / SQL / validity / error code into
// testdata/filter_conformance.json. The TypeScript DSL client
// (parse/sdk/ts/src/filter) consumes the same JSON and must agree — see
// sdk/ts/src/filter/conformance.test.ts. Drift on either side becomes a red
// build.
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

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

var updateConformance = flag.Bool("update", false, "regenerate the Go-authoritative conformance corpus")

const conformanceCorpusPath = "testdata/filter_conformance.json"

// confClock pins the parser's now-anchor so relative time tokens (now-7d)
// resolve to a fixed RFC3339 string — the corpus must be byte-stable.
var confClock = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

// confEntity is the schema the corpus is generated against. Its field/operator
// set must mirror the schema the TS conformance test validates against.
type confEntity struct {
	ID      string          `json:"id"      parse:"filter" filter_ops:"eq,ne,in,nin"`
	Name    string          `json:"name"    parse:"filter" filter_ops:"eq,ne,like,ilike,contains,null,not_null,exists"`
	Status  string          `json:"status"  parse:"filter" filter_ops:"eq,ne,in,nin" enum:"active,offline,error"`
	Count   int             `json:"count"   parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin"`
	Price   decimal.Decimal `json:"price"   parse:"filter" filter_ops:"eq,ne,gt,gte,lt,lte,in,nin"`
	Active  bool            `json:"active"  parse:"filter" filter_ops:"eq,ne"`
	Created time.Time       `json:"created" parse:"filter" filter_ops:"eq,gt,gte,lt,lte"`
	Meta    map[string]any  `json:"meta"    parse:"filter" paths:"region:string"`
	Note    *string         `json:"note"    parse:"filter" filter_ops:"not_null"`
	Lines   []confLine      `json:"lines"   parse:"filter"`
}

// confLine is an element of confEntity's lines array.
type confLine struct {
	SKU string `json:"sku" parse:"filter" filter_ops:"eq,in"`
	Qty int    `json:"qty" parse:"filter" filter_ops:"eq,gt,lt"`
}

// confPart is the target of confEntity's declared has-many relation
// "parts" (parts.entity_id = entities.id).
type confPart struct {
	ID    string `json:"id"    parse:"filter" filter_ops:"eq,in"`
	State string `json:"state" parse:"filter" filter_ops:"eq,ne" enum:"ok,worn"`
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
	ErrorCode    string         `json:"errorCode,omitempty"`
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
		// An object with no known operator key is a literal value, so an
		// unknown $-key on an int field is a type mismatch, not an operator
		// error — on both sides.
		{name: "err_unknown_dollar_key_is_literal", input: map[string]any{"count": map[string]any{"$bogus": 1}}},
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

		// --- declared relations filter by the target's own fields ---
		{name: "relation_some", input: map[string]any{"parts": map[string]any{"$some": map[string]any{"state": "worn"}}}},
		{name: "relation_none_any", input: map[string]any{"parts": map[string]any{"$none": map[string]any{}}}},
		{name: "relation_key_shorthand", input: map[string]any{"parts": "p1"}},
		{name: "relation_key_list", input: map[string]any{"parts": map[string]any{"$in": []any{"p1", "p2"}}}},
		{name: "err_relation_unknown_target_field", input: map[string]any{"parts": map[string]any{"$some": map[string]any{"colour": "red"}}}},
		{name: "err_relation_target_enum", input: map[string]any{"parts": map[string]any{"$some": map[string]any{"state": "new"}}}},
		{name: "err_relation_is_on_collection", input: map[string]any{"parts": map[string]any{"$is": map[string]any{}}}},
		{name: "err_relation_not_object", input: map[string]any{"parts": map[string]any{"$some": []any{}}}},
	}
}

// confSchema builds the filter schema for confEntity.
func confSchema(t *testing.T) *filter.ResourceSchema {
	t.Helper()
	schema, err := filter.GenerateFilterSchema[confEntity](core.NewSchemaGenerator())
	if err != nil {
		t.Fatalf("generate filter schema: %v", err)
	}
	parts, err := filter.GenerateFilterSchema[confPart](core.NewSchemaGenerator(core.WithSQLTableName("parts")))
	if err != nil {
		t.Fatalf("generate target schema: %v", err)
	}
	schema.Fields["parts"] = &filter.FieldSchema{
		ExternalName: "parts", DataType: core.FieldTypeString, Indexed: true,
		AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpNe, filter.OpIn, filter.OpNin, filter.OpNull, filter.OpNotNull, filter.OpSome, filter.OpNone},
		Relation:         &core.RelationSchema{Kind: core.HasMany, JoinTable: "parts", SourceColumn: "entity_id", TargetColumn: "id", PrimaryKey: "id"},
		Related:          parts,
	}
	return schema
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
		c.Valid = false
		if pe, ok := errors.AsType[*core.ParseError](err); ok {
			c.ErrorCode = pe.Code
		}
		return c
	}

	c.Valid = true
	if ast != nil {
		c.CanonicalAST = toASTNode(ast.Root)
	}
	if sql, ok := buildWhere(schema, ast, filter.PostgresDialect{}); ok {
		c.SQLPostgres = sql
	}
	if sql, ok := buildWhere(schema, ast, filter.SQLiteDialect{}); ok {
		c.SQLSqlite = sql
	}
	return c
}

// buildWhere generates the WHERE fragment for a dialect; ok is false for an
// empty AST (no WHERE) or a build error (recorded as an empty fragment).
func buildWhere(schema *filter.ResourceSchema, ast *filter.FilterAST, dialect filter.Dialect) (string, bool) {
	if ast == nil || ast.Root == nil {
		return "", false
	}
	res, err := filter.NewSQLBuilder(schema, dialect).Build(ast)
	if err != nil || res == nil {
		return "", false
	}
	return res.Where, true
}

// normalize JSON-encodes a value so int/float and field-order differences
// don't break equality comparisons.
func normalize(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestEmitFilterConformance regenerates the corpus when -update is passed.
// Without -update it is a no-op (the corpus is checked by TestFilterConformance).
func TestEmitFilterConformance(t *testing.T) {
	if !*updateConformance {
		t.Skip("pass -update to regenerate the conformance corpus")
	}
	schema := confSchema(t)
	cases := canonicalCases()

	out := make([]conformanceCase, 0, len(cases))
	for _, in := range cases {
		c := runCase(t, schema, in)
		// Determinism guard: re-run and require an identical serialization.
		again := runCase(t, schema, in)
		if normalize(t, c) != normalize(t, again) {
			t.Fatalf("case %q is non-deterministic across runs; use single-key maps or ordered $and/$or arrays", in.name)
		}
		out = append(out, c)
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("marshal corpus: %v", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(conformanceCorpusPath), 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	if err := os.WriteFile(conformanceCorpusPath, data, 0o644); err != nil {
		t.Fatalf("write corpus: %v", err)
	}
	t.Logf("wrote %d cases to %s", len(out), conformanceCorpusPath)
}

// TestFilterConformance re-parses every corpus input and asserts the recorded
// AST / SQL / validity / error code still match the live Go parser. This guards
// accidental Go-side regressions between regenerations.
func TestFilterConformance(t *testing.T) {
	data, err := os.ReadFile(conformanceCorpusPath)
	if err != nil {
		t.Fatalf("read corpus (run `go test -run TestEmitFilterConformance -update`): %v", err)
	}
	var corpus []conformanceCase
	if err := json.Unmarshal(data, &corpus); err != nil {
		t.Fatalf("unmarshal corpus: %v", err)
	}
	if len(corpus) == 0 {
		t.Fatal("corpus is empty")
	}

	// The corpus must stay in lockstep with the case set; a drift here means
	// someone edited canonicalCases without regenerating.
	if len(corpus) != len(canonicalCases()) {
		t.Fatalf("corpus has %d cases but canonicalCases has %d; regenerate with -update",
			len(corpus), len(canonicalCases()))
	}

	schema := confSchema(t)
	for _, want := range corpus {
		t.Run(want.Name, func(t *testing.T) {
			got := runCase(t, schema, caseInput{name: want.Name, input: want.Input, tsApplicable: want.TSApplicable})

			if got.Valid != want.Valid {
				t.Fatalf("valid: got %v want %v", got.Valid, want.Valid)
			}
			if want.ErrorCode != got.ErrorCode {
				t.Fatalf("errorCode: got %q want %q", got.ErrorCode, want.ErrorCode)
			}
			if normalize(t, got.CanonicalAST) != normalize(t, want.CanonicalAST) {
				t.Fatalf("canonicalAst drift:\n got: %s\nwant: %s",
					normalize(t, got.CanonicalAST), normalize(t, want.CanonicalAST))
			}
			if got.SQLPostgres != want.SQLPostgres {
				t.Fatalf("sqlPostgres: got %q want %q", got.SQLPostgres, want.SQLPostgres)
			}
			if got.SQLSqlite != want.SQLSqlite {
				t.Fatalf("sqlSqlite: got %q want %q", got.SQLSqlite, want.SQLSqlite)
			}
		})
	}
}

func ptrFalse() *bool {
	f := false
	return &f
}

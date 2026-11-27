//go:build unit

package parse

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type fieldRuleTag struct {
	ID   int64  `json:"id"   db:"id"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type fieldRuleItem struct {
	ID       int64          `json:"id"       db:"id"       parse:"filter,sort"`
	Owner    uuid.UUID      `json:"owner"    db:"owner"    parse:"filter"`
	Name     string         `json:"name"     db:"name"     parse:"filter,sort,patch" patch_ops:"replace,test,move"`
	Note     *string        `json:"note"     db:"note"     parse:"patch" patch_ops:"replace,remove,move"`
	Memo     *string        `json:"memo"     db:"memo"     parse:"patch" patch_ops:"replace"`
	Title    string         `json:"title"    db:"title"    parse:"patch" maxlen:"3"`
	Status   string         `json:"status"   db:"status"   parse:"filter,patch" enum:"active,offline"`
	Rank     int64          `json:"rank"     db:"rank"     parse:"filter,sort,patch"`
	Count    int64          `json:"count,string" db:"count" parse:"filter"`
	Active   bool           `json:"active"   db:"active"   parse:"filter"`
	Seen     time.Time      `json:"seen"     db:"seen"     parse:"filter"`
	Scores   []int64        `json:"scores"   db:"scores"   parse:"filter"`
	Attrs    map[string]any `json:"attrs"    db:"attrs"    parse:"filter,patch" paths:"size:int"`
	Location map[string]any `json:"location" db:"location" parse:"filter" filter_ops:"near,within" geo:"jsonb"`
	Spot     map[string]any `json:"spot"     db:"spot"     parse:"filter" filter_ops:"near" geo:"geography"`
}

// fieldRuleItems is a resource with a field of every kind a field code
// refuses a value of, a relation, and document members patch may write.
func fieldRuleItems(t *testing.T) *Resource[fieldRuleItem] {
	t.Helper()
	p := compiling(filter.SQLiteDialect{})
	tags, err := NewResource[fieldRuleTag](p, "tags")
	require.NoError(t, err)
	items, err := NewResource[fieldRuleItem](p, "items", CustomizeSchemas(func(s Schemas) error {
		return s.Patch.AddPath("/attrs/**", &patch.PathSchema{
			AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove},
			DataType:   core.FieldTypeAny, SQLColumn: "attrs",
		})
	}))
	require.NoError(t, err)
	require.NoError(t, items.Relate("tags", ManyToMany(tags, Through("item_tags", "item_id", "tag_id"), Writable())))
	return items
}

// fieldRequest is one request that raises a code about one field: the call
// site family it stands for, and the field the error must name.
type fieldRequest struct {
	site  string
	raise func(t *testing.T) error
	field string
}

// fieldRule is what a code's errors carry in details.field: the requests
// that raise it about one field, or why it is about no one field.
type fieldRule struct {
	requests []fieldRequest
	noField  string
}

// Every code is decided against core's field rule: a code raised about one
// field names it in details.field, as the request named it, at every call
// site family; a code about the request as a whole says so. A code added to
// the table fails here until it is decided.
func TestErrorFields_EveryCodeIsDecided(t *testing.T) {
	rules := fieldRules(t, fieldRuleItems(t))
	for _, code := range core.Codes() {
		t.Run(code.String(), func(t *testing.T) {
			rule, ok := rules[code]
			require.True(t, ok, "%s is undecided: name a request raising it and the field it names, or why it is about no one field", code)
			if rule.noField != "" {
				assert.Empty(t, rule.requests, "a code about no one field names no request")
				return
			}
			require.NotEmpty(t, rule.requests, "a field code names the requests that raise it")
			for _, req := range rule.requests {
				t.Run(req.site, func(t *testing.T) {
					err := req.raise(t)
					pe, ok := errors.AsType[*core.ParseError](err)
					require.True(t, ok, "%v is no ParseError", err)
					assert.Equal(t, code.String(), pe.Code, pe.Message)
					assert.Equal(t, req.field, pe.Details[core.DetailField], pe.Message)
				})
			}
		})
	}
	for code := range rules {
		assert.Contains(t, core.Codes(), code, "a rule for a code the table does not define")
	}
}

func fieldRules(t *testing.T, items *Resource[fieldRuleItem]) map[core.Code]fieldRule {
	t.Helper()
	def := core.DefaultConfig()
	repeated := func(n int, item string) string {
		return strings.TrimSuffix(strings.Repeat(item+",", n), ",")
	}
	filterDoc := func(f string) func(*testing.T) error {
		return func(*testing.T) error {
			_, err := items.ParseFilterWithCost(f, 1<<20)
			return err
		}
	}
	list := func(in ListInput) func(*testing.T) error {
		return func(*testing.T) error {
			_, err := items.ParseListInput(in)
			return err
		}
	}
	query := func(q url.Values) func(*testing.T) error {
		return func(*testing.T) error {
			_, err := items.ParseList(q)
			return err
		}
	}
	build := func(f string) func(*testing.T) error {
		return func(t *testing.T) error {
			req, err := items.ParseListInput(ListInput{Filter: f})
			require.NoError(t, err)
			_, err = items.BuildList(req, Scope{})
			return err
		}
	}
	jsonPatch := func(body string, opts ...PatchOption) func(*testing.T) error {
		return func(*testing.T) error {
			_, err := items.ParsePatch([]byte(body), opts...)
			return err
		}
	}
	merge := func(body string) func(*testing.T) error {
		return func(*testing.T) error {
			_, err := items.ParseMergePatch([]byte(body))
			return err
		}
	}
	parsed := func(t *testing.T, body string) *patch.ParsedPatch {
		t.Helper()
		p, err := items.ParsePatch([]byte(body))
		require.NoError(t, err)
		return p
	}
	update := func(body string) func(*testing.T) error {
		return func(t *testing.T) error {
			_, err := items.BuildUpdate(parsed(t, body), ByID(int64(1)), Scope{})
			return err
		}
	}
	apply := func(body string) func(*testing.T) error {
		return func(*testing.T) error {
			_, err := items.ApplyPatch([]byte(body), fieldRuleItem{Name: "a", Attrs: map[string]any{}})
			return err
		}
	}
	policy := func(t *testing.T, opt patch.PolicyOption) PatchOption {
		pc, err := patch.NewPatchPolicyConstraints(opt)
		require.NoError(t, err)
		return WithPatchPolicy(pc, nil)
	}
	deepDoc := strings.Repeat(`{"a":`, def.MaxDepth+1) + "1" + strings.Repeat("}", def.MaxDepth+1)
	distinctKeys := func(n int) string {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = strconv.Itoa(i)
		}
		return strings.Join(keys, ",")
	}

	rules := map[core.Code]fieldRule{
		core.CodeQueryTooExpensive: {noField: "a budget the whole filter spends"},
		core.CodeInvalidArray: {requests: []fieldRequest{
			{"set operand", filterDoc(`{"name":{"$in":"a"}}`), "name"},
		}},
		core.CodeInputTooLarge: {noField: "the body's size"},
		core.CodeOpNotAllowed: {requests: []fieldRequest{
			{"JSON Patch", jsonPatch(`[{"op":"remove","path":"/name"}]`), "name"},
			{"merge patch", merge(`{"memo":null}`), "memo"},
		}},
		core.CodeCursorTooLarge:   {noField: "a page boundary's cursor, which every sort key writes"},
		core.CodeTooManyOps:       {noField: "the patch's operation count"},
		core.CodeInvalidListParam: {noField: "a list parameter, which details.parameter names"},
		core.CodeTestOpSQL: {requests: []fieldRequest{
			{"compiled test", update(`[{"op":"test","path":"/name","value":"x"}]`), "name"},
		}},
		core.CodeOpNotSQLCompilable: {requests: []fieldRequest{
			{"compiled move", update(`[{"op":"move","from":"/note","path":"/name"}]`), "name"},
			{"batch member", func(t *testing.T) error {
				rows := []RowUpdate{{Key: Key{int64(1)}, Patch: parsed(t, `[{"op":"remove","path":"/attrs/old"}]`)}}
				_, err := items.BuildUpdateBatch(rows, Scope{}, UpdateBatchLimits{})
				return err
			}, "attrs"},
		}},
		core.CodeUnknownField: {requests: []fieldRequest{
			{"filter", filterDoc(`{"colour":"red"}`), "colour"},
			{"flat filter", query(url.Values{"colour": {"red"}}), "colour"},
			{"include", list(ListInput{Include: []string{"nope"}}), "nope"},
			{"facet", list(ListInput{Facets: []string{"nope"}}), "nope"},
			{"field mask", list(ListInput{Fields: []string{"nmae"}}), "nmae"},
		}},
		core.CodeInvalidOperator: {requests: []fieldRequest{
			{"operator not allowed", filterDoc(`{"rank":{"$like":"1%"}}`), "rank"},
			{"unknown operator", filterDoc(`{"rank":{"$nope":1}}`), "rank"},
			{"equality not allowed", filterDoc(`{"location":"here"}`), "location"},
			{"relation predicate", filterDoc(`{"tags":{"$some":{"name":{"$near":{}}}}}`), "name"},
			{"repeated flat filter", query(url.Values{"rank_gte": {"1", "2"}}), "rank"},
		}},
		core.CodeValueTypeMismatch: {requests: []fieldRequest{
			{"integer", filterDoc(`{"rank":"high"}`), "rank"},
			{"timestamp", filterDoc(`{"seen":"yesterday"}`), "seen"},
			{"pattern", filterDoc(`{"name":{"$like":"a\\"}}`), "name"},
			{"document", filterDoc(`{"attrs":null}`), "attrs"},
			{"document path", filterDoc(`{"attrs.size":"big"}`), "attrs.size"},
			{"array element", filterDoc(`{"scores":{"$contains":["x"]}}`), "scores"},
			{"quoted wire form", filterDoc(`{"count":5}`), "count"},
			{"null test", filterDoc(`{"scores":{"$null":"yes"}}`), "scores"},
			{"flat boolean", query(url.Values{"active": {"maybe"}}), "active"},
		}},
		core.CodeDepthExceeded: {requests: []fieldRequest{
			{"document depth", filterDoc(`{"attrs":` + deepDoc + `}`), "attrs"},
			{"patch pointer", jsonPatch(`[{"op":"replace","path":"/attrs` + strings.Repeat("/a", def.MaxDepth) + `","value":1}]`), "attrs"},
		}},
		core.CodeInvalidPatchOp: {requests: []fieldRequest{
			{"value required", jsonPatch(`[{"op":"replace","path":"/name"}]`), "name"},
			{"from required", jsonPatch(`[{"op":"move","path":"/name"}]`), "name"},
		}},
		core.CodePatchFieldNotFound: {requests: []fieldRequest{
			{"JSON Patch", jsonPatch(`[{"op":"replace","path":"/colour","value":"x"}]`), "colour"},
			{"merge patch", merge(`{"colour":"x"}`), "colour"},
		}},
		core.CodePatchValueInvalid: {requests: []fieldRequest{
			{"type", jsonPatch(`[{"op":"replace","path":"/name","value":5}]`), "name"},
			{"null", jsonPatch(`[{"op":"replace","path":"/rank","value":null}]`), "rank"},
			{"enum", jsonPatch(`[{"op":"replace","path":"/status","value":"lost"}]`), "status"},
			{"bound", jsonPatch(`[{"op":"replace","path":"/title","value":"long"}]`), "title"},
			{"relation value", jsonPatch(`[{"op":"replace","path":"/tags","value":"x"}]`), "tags"},
			{"merge patch", merge(`{"name":5}`), "name"},
			{"applied test", apply(`[{"op":"test","path":"/name","value":"b"}]`), "name"},
			{"applied member", apply(`[{"op":"remove","path":"/attrs/missing"}]`), "attrs"},
			{"stored member", func(t *testing.T) error {
				plan, err := items.BuildUpdate(parsed(t, `[{"op":"replace","path":"/attrs/color","value":"red"}]`), ByID(int64(1)), Scope{})
				require.NoError(t, err)
				return plan.conflictError(0)
			}, "attrs"},
		}},
		core.CodeSchemaNotFound: {noField: "a resource lookup"},
		core.CodeSortFieldNotAllowed: {requests: []fieldRequest{
			{"not sortable", list(ListInput{Sort: "status"}), "status"},
			{"named twice", list(ListInput{Sort: "name,-name"}), "name"},
		}},
		core.CodeSortFieldLimit:  {noField: "the sort's key count"},
		core.CodeInvalidCursor:   {noField: "the cursor, an opaque token of every sort key"},
		core.CodeInvalidRelation: {requests: []fieldRequest{{"include", list(ListInput{Include: []string{"name"}}), "name"}}},
		core.CodePatchPolicyForbidden: {requests: []fieldRequest{
			{"forbidden path", func(t *testing.T) error {
				return jsonPatch(`[{"op":"replace","path":"/name","value":"x"}]`, policy(t, patch.ForbidPaths("/name")))(t)
			}, "name"},
		}},
		core.CodePatchPolicyValue: {requests: []fieldRequest{
			{"value constraint", func(t *testing.T) error {
				constrain := patch.ConstrainValues("/name", patch.ValueConstraint{AllowedValues: []any{"y"}})
				return jsonPatch(`[{"op":"replace","path":"/name","value":"x"}]`, policy(t, constrain))(t)
			}, "name"},
		}},
		core.CodeInvalidJSON: {requests: []fieldRequest{
			{"relation predicate", filterDoc(`{"tags":{"$some":"x"}}`), "tags"},
			{"element condition", filterDoc(`{"scores":{"$some":{"$or":5}}}`), "scores"},
		}},
		core.CodeInvalidEnumValue: {requests: []fieldRequest{{"enum", filterDoc(`{"status":"lost"}`), "status"}}},
		core.CodeArrayTooLarge: {requests: []fieldRequest{
			{"set operand", filterDoc(`{"name":{"$in":[` + repeated(def.MaxArraySize+1, `"v"`) + `]}}`), "name"},
		}},
		core.CodeMaxConditions: {noField: "the filter's condition count"},
		core.CodeInvalidGeoCoords: {requests: []fieldRequest{
			{"point", filterDoc(`{"location":{"$near":{"lat":999,"lon":-74.006,"distance":"5km"}}}`), "location"},
		}},
		core.CodeInvalidDistance: {requests: []fieldRequest{
			{"distance", filterDoc(`{"location":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5parsecs"}}}`), "location"},
		}},
		core.CodeInvalidGeoPolygon: {requests: []fieldRequest{
			{"polygon", filterDoc(`{"location":{"$within":{"points":[{"lat":40.77,"lon":-73.98},{"lat":40.73,"lon":-74.02}]}}}`), "location"},
		}},
		core.CodeGeoNotSupported: {requests: []fieldRequest{
			{"dialect", build(`{"spot":{"$near":{"lat":40.71,"lon":-74.00,"distance":"5km"}}}`), "spot"},
		}},
		core.CodeInvalidUUID: {requests: []fieldRequest{{"uuid", filterDoc(`{"owner":"not-a-uuid"}`), "owner"}}},
		core.CodeRelationVerbNotAllowed: {requests: []fieldRequest{
			{"compiled relation test", func(t *testing.T) error {
				schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{
					"/tags": {
						AllowedOps: []patch.PatchOp{patch.PatchOpTest},
						DataType:   core.FieldTypeArray, GoType: reflect.TypeFor[[]string](),
						Relation: &core.RelationSchema{JoinTable: "item_tags", SourceColumns: []string{"item_id"}, TargetColumns: []string{"tag_id"}},
					},
				})
				require.NoError(t, err)
				p, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"test","path":"/tags","value":["a"]}]`))
				require.NoError(t, err)
				_, err = patch.BuildPatchSQL(p, filter.SQLiteDialect{})
				return err
			}, "tags"},
		}},
		core.CodeRelationValueInvalid: {requests: []fieldRequest{
			{"empty add", jsonPatch(`[{"op":"add","path":"/tags","value":[]}]`), "tags"},
		}},
		core.CodeRelationPatchTooLarge: {requests: []fieldRequest{
			{"keys", jsonPatch(`[{"op":"add","path":"/tags","value":[` + distinctKeys(patch.DefaultMaxRelationValues+1) + `]}]`), "tags"},
		}},
		core.CodeSchemaEncodingFailed: {noField: "a schema document the server could not encode"},
	}
	return rules
}

// The import paths a ParseError is named through.
const (
	rootPackagePath = "github.com/ashtonian/pkg/parse"
	corePackagePath = rootPackagePath + "/core"
)

// Every request error is built by core.NewError, so none picks its own
// shape: the module's Go files, tests and every build tag included, hold no
// composite literal of core.ParseError outside core itself. Other modules
// (client, the example server) are their own.
func TestSource_BuildsParseErrorsThroughNewError(t *testing.T) {
	fset := token.NewFileSet()
	var literals []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return moduleDir(path, entry)
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		root := filepath.Dir(path) == "." && file.Name.Name == "parse"
		for _, pos := range parseErrorLiterals(file, root) {
			literals = append(literals, fset.Position(pos).String())
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, literals, "build each ParseError with core.NewError")
}

// moduleDir is the walk's decision on a directory: core, which defines the
// type, and what ./... skips (hidden and _-prefixed directories, testdata,
// node_modules and other modules) are left out.
func moduleDir(path string, entry fs.DirEntry) error {
	if path == "." {
		return nil
	}
	name := entry.Name()
	if path == "core" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules" {
		return fs.SkipDir
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	if err == nil {
		return fs.SkipDir
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// parseErrorLiterals are the positions of file's composite literals of
// core.ParseError: through core's or the root package's name for it (the
// root's ParseError is an alias), whatever the import is named, unqualified
// in the root package or under a dot import, and as the elided element of a
// slice, array or map literal.
func parseErrorLiterals(file *ast.File, rootPackage bool) []token.Pos {
	qualifiers := map[string]bool{}
	unqualified := rootPackage
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || (path != corePackagePath && path != rootPackagePath) {
			continue
		}
		name := pathpkg.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." {
			unqualified = true
		}
		qualifiers[name] = true
	}
	isParseError := func(t ast.Expr) bool {
		star, ok := t.(*ast.StarExpr)
		if ok {
			t = star.X
		}
		switch t := t.(type) {
		case *ast.SelectorExpr:
			x, ok := t.X.(*ast.Ident)
			return ok && qualifiers[x.Name] && t.Sel.Name == "ParseError"
		case *ast.Ident:
			return unqualified && t.Name == "ParseError"
		}
		return false
	}
	var found []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if lit.Type != nil && isParseError(lit.Type) {
			found = append(found, lit.Pos())
			return true
		}
		var elem ast.Expr
		switch t := lit.Type.(type) {
		case *ast.ArrayType:
			elem = t.Elt
		case *ast.MapType:
			elem = t.Value
		}
		if elem == nil || !isParseError(elem) {
			return true
		}
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if ok {
				e = kv.Value
			}
			u, ok := e.(*ast.UnaryExpr)
			if ok && u.Op == token.AND {
				e = u.X
			}
			inner, ok := e.(*ast.CompositeLit)
			if ok && inner.Type == nil {
				found = append(found, inner.Pos())
			}
		}
		return true
	})
	return found
}

// The guard finds each spelling of a literal, so a passing walk means there
// is none, and finds none in what builds or names the type.
func TestParseErrorLiterals_FindsEverySpelling(t *testing.T) {
	tests := []struct {
		name string
		root bool
		src  string
		want int
	}{
		{"pointer", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = &core.ParseError{Code: "E2071"}`, 1},
		{"value", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = core.ParseError{}`, 1},
		{"renamed import", false, `import c "github.com/ashtonian/pkg/parse/core"; var _ = &c.ParseError{}`, 1},
		{"root alias", false, `import "github.com/ashtonian/pkg/parse"; var _ = &parse.ParseError{}`, 1},
		{"unqualified in the root package", true, `var _ = &ParseError{}`, 1},
		{"dot import", false, `import . "github.com/ashtonian/pkg/parse/core"; var _ = ParseError{}`, 1},
		{"elided slice elements", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = []*core.ParseError{{}, {Code: "E2071"}}`, 2},
		{"elided map values", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = map[string]core.ParseError{"a": {}}`, 1},
		{"constructor", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = core.NewError(core.CodeUnknownField, "m")`, 0},
		{"slice of errors", false, `import "github.com/ashtonian/pkg/parse/core"; var pe *core.ParseError; var _ = []*core.ParseError{pe}`, 0},
		{"another package's ParseError", false, `import "example.com/other"; var _ = &other.ParseError{}`, 0},
		{"unqualified outside the root package", false, `var _ = &ParseError{}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "x.go", "package x; "+tt.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			assert.Len(t, parseErrorLiterals(file, tt.root), tt.want)
		})
	}
}

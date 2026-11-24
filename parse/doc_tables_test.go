//go:build unit

package parse_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// A renderedTable is a guide's table whose rows the code determines. It
// sits between the lines
//
//	<!-- BEGIN rendered NAME: edit the code, then go test -tags=unit -run TestDocTables -update -->
//	<!-- END rendered NAME -->
//
// Cells the code holds are rendered; the others are prose, kept from the
// guide's row with the same key, so a guide reads as written while its
// values cannot drift. A row the code adds starts with its prose empty,
// which fails until someone writes it.
type renderedTable struct {
	file, name string
	header     []string
	rows       func(t *testing.T, kept keptRows) [][]string
}

// keptRows are a table's current rows, by their first cell.
type keptRows map[string][]string

// cell is column col of the row keyed key, "" when there is none.
func (k keptRows) cell(key string, col int) string {
	if row, ok := k[key]; ok && col < len(row) {
		return row[col]
	}
	return ""
}

func TestDocTables(t *testing.T) {
	tables := []renderedTable{
		{file: "docs/queries.md", name: "operators", header: []string{"Type", "Operators", "Default when `filter_ops` is omitted"}, rows: operatorRows},
		{file: "docs/patches.md", name: "patch-operations", header: []string{"Type", "Default operations"}, rows: patchOperationRows},
		{file: "docs/schema-configuration.md", name: "parser-options", header: []string{"Option", "Type", "Default", "Description"}, rows: parserOptionRows},
		{file: "docs/http-errors.md", name: "error-codes", header: []string{"Code", "HTTP", "Meaning"}, rows: errorCodeRows},
		{file: "docs/grammar-mapping.md", name: "mapping-operators", header: []string{"Operator", "Flat parameter", "PostgREST", "django-filter", "ent"}, rows: mappingOperatorRows},
		{file: "docs/grammar-mapping.md", name: "mapping-list-parameters", header: []string{"Parameter", "PostgREST", "django-filter and DRF", "ent"}, rows: mappingListParameterRows},
		{file: "docs/grammar-mapping.md", name: "mapping-patch-operations", header: []string{"Operation", "parse", "PostgREST", "DRF", "ent"}, rows: mappingPatchOperationRows},
	}
	for _, table := range tables {
		t.Run(table.name, func(t *testing.T) {
			raw, err := os.ReadFile(table.file)
			require.NoError(t, err)
			doc := string(raw)
			begin := fmt.Sprintf("<!-- BEGIN rendered %s: edit the code, then go test -tags=unit -run TestDocTables -update -->\n", table.name)
			end := fmt.Sprintf("<!-- END rendered %s -->\n", table.name)
			before, rest, ok := strings.Cut(doc, begin)
			require.True(t, ok, "%s has no line %q", table.file, begin)
			current, after, ok := strings.Cut(rest, end)
			require.True(t, ok, "%s has no line %q", table.file, end)

			rows := table.rows(t, parseTableRows(current))
			want := renderTable(table.header, rows)
			if *updateRendered && current != want {
				require.NoError(t, os.WriteFile(table.file, []byte(before+begin+want+end+after), 0o644))
				current = want
			}
			assert.Equal(t, want, current, "%s's %s table differs from the code: run go test -tags=unit -run TestDocTables -update, then review the diff", table.file, table.name)
			for _, row := range rows {
				for i, c := range row {
					assert.NotEmpty(t, c, "%s: the %s row's %q cell is empty: write it", table.file, row[0], table.header[i])
				}
			}
		})
	}
}

// operatorRows is a row per field type: the operators it may declare and
// those a bare parse:"filter" field gets.
func operatorRows(t *testing.T, kept keptRows) [][]string {
	defaults := filter.DefaultFilterOps()
	var rows [][]string
	for _, ft := range fieldTypes(t) {
		rows = append(rows, []string{string(ft), words(filter.OperatorsFor(ft, false)), words(defaults[ft])})
		// The relation row says a relation compares its target's key with
		// the key type's operators but $contains.
		withoutContains := slices.DeleteFunc(filter.OperatorsFor(ft, false), func(op filter.FilterOperator) bool { return op == filter.OpContains })
		assert.Equal(t, withoutContains, filter.OperatorsFor(ft, true), "relation operators on a %s key", ft)
	}
	schema, err := filter.GenerateFilterSchema[docRelationOwner](core.NewSchemaGenerator())
	require.NoError(t, err)
	tags, ok := schema.RequestField("tags")
	require.True(t, ok)
	return append(rows, []string{"relation", kept.cell("relation", 1), words(tags.AllowedOperators)})
}

// patchOperationRows is a row per field type: the operations a bare
// parse:"patch" field gets.
func patchOperationRows(t *testing.T, _ keptRows) [][]string {
	defaults := patch.DefaultPatchOps()
	var rows [][]string
	for _, ft := range fieldTypes(t) {
		rows = append(rows, []string{string(ft), words(defaults[ft])})
	}
	schema, err := patch.GeneratePatchSchema[docRelationOwner](core.NewSchemaGenerator())
	require.NoError(t, err)
	tags, ok := schema.Path("/tags")
	require.True(t, ok)
	return append(rows, []string{"relation", words(tags.AllowedOps) + " (junction rows)"})
}

// docRelationOwner has a relation the operator tables read their relation
// row from.
type docRelationOwner struct {
	ID   string   `json:"id"   db:"id"`
	Tags []string `json:"tags" db:"-" parse:"filter,patch" relation:"owner_tags,owner_id,tag_id"`
}

// parserOption is a row of the parser options table: the option, and the
// option applied to a value, by which the test finds the field it sets.
type parserOption struct {
	cell string
	fn   any
	set  core.Option
}

var parserOptions = []parserOption{
	{"`WithMaxDepth(n)`", core.WithMaxDepth, core.WithMaxDepth(7)},
	{"`WithMaxCost(n)`", core.WithMaxCost, core.WithMaxCost(7)},
	{"`WithMaxInputSize(n)`", core.WithMaxInputSize, core.WithMaxInputSize(7)},
	{"`WithMaxListBodySize(n)`", core.WithMaxListBodySize, core.WithMaxListBodySize(7)},
	{"`WithMaxConditions(n)`", core.WithMaxConditions, core.WithMaxConditions(7)},
	{"`WithMaxArraySize(n)`", core.WithMaxArraySize, core.WithMaxArraySize(7)},
	{"`WithMaxPatchCopyBytes(n)`", core.WithMaxPatchCopyBytes, core.WithMaxPatchCopyBytes(7)},
	{"`WithCodec(c)`", core.WithCodec, core.WithCodec(core.StdlibJSON{})},
	{"`WithClock(fn)`", core.WithClock, core.WithClock(time.Now)},
	{"`WithGeneratorOptions(o...)`", core.WithGeneratorOptions, core.WithGeneratorOptions(core.WithSQLTableName("t"))},
}

// parserOptionRows is a row per Option: its parameter's type, and the
// default DefaultConfig holds for a limit. The other defaults are fallbacks
// a nil field selects, described in prose.
func parserOptionRows(t *testing.T, kept keptRows) [][]string {
	def := reflect.ValueOf(*core.DefaultConfig())
	configType := def.Type()
	setBy := map[string]string{}
	var rows [][]string
	for _, o := range parserOptions {
		var cfg core.ParserConfig
		o.set(&cfg)
		v := reflect.ValueOf(cfg)
		var fields []string
		for i := range configType.NumField() {
			if !v.Field(i).IsZero() {
				fields = append(fields, configType.Field(i).Name)
			}
		}
		require.Len(t, fields, 1, "%s sets one ParserConfig field", o.cell)
		field := fields[0]
		require.NotContains(t, setBy, field, "%s and %s both set %s", setBy[field], o.cell, field)
		setBy[field] = o.cell

		value := def.FieldByName(field)
		var dflt string
		switch {
		case value.CanInt() && value.Int() != 0:
			dflt = fmt.Sprintf("`%d`", value.Int())
		case value.CanInt() && field == "MaxListBodySize":
			cfg := core.DefaultConfig()
			unlimited := &core.ParserConfig{MaxInputSize: 0}
			dflt = fmt.Sprintf("`0`: derived, `MaxInputSize` + `%d` (`%d` at the default input size), or `%d` when `MaxInputSize` is unlimited",
				cfg.ListBodyLimit()-cfg.MaxInputSize, cfg.ListBodyLimit(), unlimited.ListBodyLimit())
		case value.IsZero():
			dflt = kept.cell(o.cell, 2)
		default:
			t.Fatalf("DefaultConfig sets %s: render its default", field)
		}
		rows = append(rows, []string{o.cell, parameterType(reflect.TypeOf(o.fn)), dflt, kept.cell(o.cell, 3)})
	}
	for i := range configType.NumField() {
		if f := configType.Field(i); f.IsExported() {
			assert.Contains(t, setBy, f.Name, "no row's option sets ParserConfig.%s", f.Name)
		}
	}
	return rows
}

// parameterType is an option's parameter type as the table writes it: a
// basic type bare, any other in code, core's types unqualified.
func parameterType(fn reflect.Type) string {
	p := fn.In(0)
	if fn.IsVariadic() {
		return "`..." + strings.TrimPrefix(p.Elem().String(), "core.") + "`"
	}
	if p.PkgPath() == "" && p.Kind() <= reflect.Complex128 {
		return p.String()
	}
	return "`" + strings.TrimPrefix(p.String(), "core.") + "`"
}

// errorCodeRows is a row per code: its HTTP status, and its meaning in
// prose, starting from the code's default message.
func errorCodeRows(_ *testing.T, kept keptRows) [][]string {
	var rows [][]string
	for _, c := range parse.Codes() {
		meaning := kept.cell(c.String(), 2)
		if meaning == "" {
			r, size := utf8.DecodeRuneInString(c.ErrorMessage())
			meaning = string(unicode.ToUpper(r)) + c.ErrorMessage()[size:]
		}
		rows = append(rows, []string{c.String(), strconv.Itoa(c.ErrorHTTPStatus()), meaning})
	}
	return rows
}

// mappingOperatorRows is a row per operator of the filter grammar: its flat
// parameter, and its counterparts in prose.
func mappingOperatorRows(t *testing.T, kept keptRows) [][]string {
	var rows [][]string
	for _, op := range grammarOperators(t) {
		key := "`$" + string(op) + "`"
		rows = append(rows, []string{key, flatParameter(op), kept.cell(key, 2), kept.cell(key, 3), kept.cell(key, 4)})
	}
	return rows
}

// grammarOperators are the operators the filter grammar reads, in the order
// its vocabulary is documented: those a field may declare (Operators), then
// those only the grammar names, the relation predicates and logical
// operators. The latter are filter's FilterOperator constants ParseOperator
// reads, so an operator added to the grammar is a row of the table.
func grammarOperators(t *testing.T) []filter.FilterOperator {
	ops := filter.Operators()
	declared := declaredConstants(t, "filter", func(_ string, typ ast.Expr) bool {
		ident, ok := typ.(*ast.Ident)
		return ok && ident.Name == "FilterOperator"
	})
	for _, op := range ops {
		require.Contains(t, declared, string(op), "operator %s has no FilterOperator constant", op)
	}
	for _, name := range declared {
		op, ok := filter.ParseOperator("$" + name)
		if ok && !slices.Contains(ops, op) {
			ops = append(ops, op)
		}
	}
	return ops
}

// flatParameter is the query parameter that filters a field f with op. A
// bare field name is equality; any other operator with a flat form is the
// name followed by "_" and the operator's name, the suffix FlatParamFields
// strips to find the field.
func flatParameter(op filter.FilterOperator) string {
	if op == filter.OpEq {
		return "`f`"
	}
	param := "f_" + string(op)
	if slices.Contains(filter.FlatParamFields(param), "f") {
		return "`" + param + "`"
	}
	return "—"
}

// mappingListParameterRows is a row per list request parameter
// (filter.Param*), with its counterparts in prose.
func mappingListParameterRows(t *testing.T, kept keptRows) [][]string {
	var rows [][]string
	for _, name := range declaredConstants(t, "filter", func(name string, _ ast.Expr) bool { return strings.HasPrefix(name, "Param") }) {
		require.True(t, filter.IsListParam(name), "filter.Param* %q is no list parameter", name)
		key := "`" + name + "`"
		rows = append(rows, []string{key, kept.cell(key, 1), kept.cell(key, 2), kept.cell(key, 3)})
	}
	return rows
}

// mappingPatchOperationRows is a row per patch operation, with what it does
// in parse and its counterparts in prose.
func mappingPatchOperationRows(_ *testing.T, kept keptRows) [][]string {
	var rows [][]string
	for _, op := range patch.Ops() {
		key := "`" + string(op) + "`"
		rows = append(rows, []string{key, kept.cell(key, 1), kept.cell(key, 2), kept.cell(key, 3), kept.cell(key, 4)})
	}
	return rows
}

// fieldTypes are core's FieldType constants in declaration order: core lists
// them nowhere else.
func fieldTypes(t *testing.T) []core.FieldType {
	var types []core.FieldType
	for _, s := range declaredConstants(t, "core", func(_ string, typ ast.Expr) bool {
		ident, ok := typ.(*ast.Ident)
		return ok && ident.Name == "FieldType"
	}) {
		types = append(types, core.FieldType(s))
	}
	return types
}

// declaredConstants are the values of the string constants the package in
// dir declares, in declaration order, whose name and declared type keep
// selects (the type is nil for an untyped constant). They are read from the
// package's source.
func declaredConstants(t *testing.T, dir string, keep func(name string, typ ast.Expr) bool) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	fset := token.NewFileSet()
	var values []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, ident := range vs.Names {
					if i >= len(vs.Values) || !keep(ident.Name, vs.Type) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					require.True(t, ok && lit.Kind == token.STRING, "%s: constant %s is no string literal", name, ident.Name)
					s, err := strconv.Unquote(lit.Value)
					require.NoError(t, err)
					values = append(values, s)
				}
			}
		}
	}
	require.NotEmpty(t, values, "no constant of %s matches", dir)
	return values
}

// words is a list of operators as the tables write it, "none" for none.
func words[S ~string](ops []S) string {
	if len(ops) == 0 {
		return "none"
	}
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = string(op)
	}
	return strings.Join(out, " ")
}

func renderTable(header []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("\n| " + strings.Join(header, " | ") + " |\n|")
	b.WriteString(strings.Repeat("---|", len(header)) + "\n")
	for _, row := range rows {
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
	}
	b.WriteString("\n")
	return b.String()
}

// parseTableRows reads a region's body rows, by their first cell.
func parseTableRows(region string) keptRows {
	rows := keptRows{}
	for i, line := range strings.Split(strings.TrimSpace(region), "\n") {
		if i < 2 || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := splitTableRow(line)
		if len(cells) > 0 {
			rows[cells[0]] = cells
		}
	}
	return rows
}

// splitTableRow splits a Markdown table row at the pipes outside code spans
// and not escaped.
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	var cells []string
	var cell strings.Builder
	inCode := false
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line):
			cell.WriteByte(c)
			cell.WriteByte(line[i+1])
			i++
		case c == '`':
			inCode = !inCode
			cell.WriteByte(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
		default:
			cell.WriteByte(c)
		}
	}
	return append(cells, strings.TrimSpace(cell.String()))
}

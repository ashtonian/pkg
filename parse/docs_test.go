//go:build unit

package parse_test

import (
	"cmp"
	"errors"
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// exampleMarker precedes a fenced block that shows what an example prints:
//
//	<!-- output of Example_jsonDocuments -->
//	<!-- output of parsesql.Example_quickStart -->
//
// An example of this package is named alone, and one of a provider module's
// package by its directory first: an example that runs on a database is the
// provider's. The block is that example's Output under one elision rule:
// "…" stands for any run of characters, line breaks included, and nothing
// else is elided. The example's own Output comment is checked by go test,
// so a change to what the compiler prints fails the example, then the
// guide.
var exampleMarker = regexp.MustCompile(`^<!-- output of (?:(parsesql|parsepgx)\.)?(Example\w*) -->$`)

// compilerOutput is a line only compiler output holds: SQL, bound
// arguments' dialect labels, or a refused request's code.
var compilerOutput = regexp.MustCompile(`\b(SELECT|INSERT INTO|UPDATE|DELETE FROM|WHERE|SET)\b|^(postgres|sqlite)\b|\[E\d{4}\]`)

func TestGuideBlocksMatchTheirExamples(t *testing.T) {
	read := map[string]map[string]string{}
	examples := func(pkg string) (map[string]string, bool) {
		outputs, ok := read[pkg]
		if !ok {
			outputs = exampleOutputs(t, pkg)
			read[pkg] = outputs
		}
		return outputs, outputs != nil
	}
	guides, err := filepath.Glob(filepath.Join("docs", "*.md"))
	require.NoError(t, err)
	for _, guide := range append([]string{"README.md"}, guides...) {
		raw, err := os.ReadFile(guide)
		require.NoError(t, err)
		for _, problem := range guideProblems(string(raw), examples) {
			t.Errorf("%s:%s", guide, problem)
		}
	}
}

// guideProblems are a guide's blocks that show compiler output without
// naming an example, name an example its package lacks, or are not its
// output. examples returns the Output of a package's examples by name ("" is
// this package), and false for a provider package whose module is not
// beside this one, as in this module's archive: its blocks are not checked.
func guideProblems(doc string, examples func(pkg string) (map[string]string, bool)) []string {
	var problems []string
	for _, b := range fencedBlocks(doc) {
		if b.example == "" {
			if b.lang != "go" && compilerOutput.MatchString(b.body) {
				problems = append(problems, fmt.Sprintf("%d: a block showing compiler output names no example: precede it with <!-- output of ExampleName -->", b.line))
			}
			continue
		}
		outputs, beside := examples(b.pkg)
		if !beside {
			continue
		}
		output, ok := outputs[b.example]
		if !ok {
			problems = append(problems, fmt.Sprintf("%d: %s is no example of %s with an Output comment", b.line, b.example, cmp.Or(b.pkg, "this package")))
			continue
		}
		if !elided(b.body).MatchString(output) {
			problems = append(problems, fmt.Sprintf("%d: the block is not %s's output, with … for any run of characters; the example prints:\n%s", b.line, b.example, output))
		}
	}
	return problems
}

func TestGuideProblems(t *testing.T) {
	outputs := map[string]string{"Example_x": "{\"a\":1}\npostgres WHERE (a = $1) ORDER BY id [1]\nsqlite   WHERE (a = ?1) ORDER BY id [1]"}
	examples := func(pkg string) (map[string]string, bool) {
		switch pkg {
		case "":
			return outputs, true
		case "parsesql":
			provider := map[string]string{"Example_quickStart": "d1 switch"}
			return provider, true
		}
		return nil, false
	}
	pinned := func(body string) string { return "Text.\n\n<!-- output of Example_x -->\n```text\n" + body + "\n```\n" }
	tests := []struct {
		name     string
		doc      string
		problems int
	}{
		{"the whole output", pinned("{\"a\":1}\npostgres WHERE (a = $1) ORDER BY id [1]\nsqlite   WHERE (a = ?1) ORDER BY id [1]"), 0},
		{"an elision within a line", pinned("{\"a\":1}\npostgres WHERE (a = $1) … [1]\nsqlite   WHERE (a = ?1) … [1]"), 0},
		{"an elision across lines", pinned("{\"a\":1}\npostgres WHERE (a = $1) …"), 0},
		{"trailing spaces", pinned("{\"a\":1}  \npostgres … [1]\t"), 0},
		{"a block that stops short", pinned("{\"a\":1}\npostgres WHERE (a = $1) ORDER BY id [1]"), 1},
		{"a block that starts late", pinned("postgres WHERE (a = $1) …"), 1},
		{"a changed character", pinned("{\"a\":1}\npostgres WHERE (a = $2) …"), 1},
		{"metacharacters are literal", pinned("{.\"a\":1}\npostgres WHERE (a = $1) …"), 1},
		{"an example the package lacks", "<!-- output of Example_y -->\n```text\nx\n```\n", 1},
		{"a provider's example", "<!-- output of parsesql.Example_quickStart -->\n```text\nd1 switch\n```\n", 0},
		{"a provider's example it lacks", "<!-- output of parsesql.Example_y -->\n```text\nd1 switch\n```\n", 1},
		{"a provider module not beside this one", "<!-- output of parsepgx.Example_y -->\n```text\nx\n```\n", 0},
		{"a package that is no provider's", "<!-- output of core.Example_x -->\n```text\nWHERE x\n```\n", 1},
		{"compiler output with no marker", "```text\npostgres WHERE (a = $1)\n```\n", 1},
		{"a marker a line away from its block", "<!-- output of Example_x -->\n\n```sql\nSELECT 1\n```\n", 1},
		{"a refused request with no marker", "```text\n[E2071] field 'x' is not filterable\n```\n", 1},
		{"Go code is not output", "```go\nq := `SELECT 1 WHERE x`\n```\n", 0},
		{"requests are not output", "```text\n{\"tags\":{\"$some\":{}}}\n```\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, guideProblems(tt.doc, examples), tt.problems)
		})
	}
}

type fencedBlock struct {
	line    int // of the opening fence, 1-based
	lang    string
	pkg     string // the example's package: "" for this one
	example string
	body    string
}

// fencedBlocks are a Markdown file's fenced code blocks, each with the
// example the line before its fence names.
func fencedBlocks(doc string) []fencedBlock {
	lines := strings.Split(doc, "\n")
	var blocks []fencedBlock
	for i := 0; i < len(lines); i++ {
		fence, ok := strings.CutPrefix(lines[i], "```")
		if !ok {
			continue
		}
		b := fencedBlock{line: i + 1, lang: strings.TrimSpace(fence)}
		if i > 0 {
			m := exampleMarker.FindStringSubmatch(lines[i-1])
			if m != nil {
				b.pkg, b.example = m[1], m[2]
			}
		}
		var body []string
		for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
			body = append(body, lines[i])
		}
		b.body = strings.Join(body, "\n")
		blocks = append(blocks, b)
	}
	return blocks
}

// elided is the pattern a block's text stands for: itself, each "…" any run
// of characters. Trailing spaces are not compared, as go test compares an
// example's output.
func elided(block string) *regexp.Regexp {
	parts := strings.Split(trimLines(block), "…")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`\A` + strings.Join(parts, `(?s:.*?)`) + `\z`)
}

func trimLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.Join(lines, "\n")
}

// exampleOutputs are the Output comments of the examples of pkg ("" for
// this package), by function name, as go test reads them; nil when pkg's
// directory is not beside this module.
func exampleOutputs(t *testing.T, pkg string) map[string]string {
	t.Helper()
	dir := cmp.Or(pkg, ".")
	_, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	names, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	require.NoError(t, err)
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		require.NoError(t, err)
		files = append(files, f)
	}
	outputs := map[string]string{}
	for _, ex := range doc.Examples(files...) {
		if !ex.EmptyOutput && ex.Output != "" {
			outputs["Example"+ex.Name] = trimLines(ex.Output)
		}
	}
	return outputs
}

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
		{file: "docs/queries.md", name: "operators", header: []string{"Type", "Operators", "Default when `filter_ops` is omitted", "As the key"}, rows: operatorRows},
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

// operatorRows is a row per field type: the operators it may declare,
// those a bare parse:"filter" field gets, and those a key column of the
// type takes.
func operatorRows(t *testing.T, kept keptRows) [][]string {
	defaults := filter.DefaultFilterOps()
	var rows [][]string
	for _, ft := range fieldTypes(t) {
		key := "not a key"
		if core.IsKeyType(ft) {
			key = words(filter.KeyOperators(ft))
		}
		rows = append(rows, []string{string(ft), words(filter.OperatorsFor(ft, false)), words(defaults[ft]), key})
		// The relation row says a relation compares its target's key with
		// the key type's operators but $contains.
		withoutContains := slices.DeleteFunc(filter.OperatorsFor(ft, false), func(op filter.FilterOperator) bool { return op == filter.OpContains })
		assert.Equal(t, withoutContains, filter.OperatorsFor(ft, true), "relation operators on a %s key", ft)
	}
	schema, err := filter.GenerateFilterSchema[docRelationOwner](core.NewSchemaGenerator())
	require.NoError(t, err)
	tags, ok := schema.RequestField("tags")
	require.True(t, ok)
	return append(rows, []string{"relation", kept.cell("relation", 1), words(tags.AllowedOperators), "not a key"})
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

package filter

import (
	"cmp"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// SearchField describes a single field included in full-text search.
// It searches Column of Table, quoted by the dialect, unless Expr is set,
// as a FieldSchema does.
type SearchField struct {
	Name  string
	Table string
	// Column is the row column the field searches, set by schema generation
	// as FieldSchema.Column is.
	Column string
	// Expr is the searched SQL when it is not Column of Table (a column
	// mapper's or a computed column's), emitted as written.
	Expr string
}

// RowColumn returns the row column the field searches; "" for a computed
// expression that names none.
func (f SearchField) RowColumn() string { return f.Column }

func (f SearchField) sql(d Dialect) string { return columnSQL(d, f.Table, f.Column, f.Expr) }

// isTextSearchConfig reports whether lang names a text search
// configuration, optionally schema-qualified: it is written into a string
// literal, so it is held to identifiers.
func isTextSearchConfig(lang string) bool {
	schema, name, qualified := strings.Cut(lang, ".")
	if !qualified {
		return core.IsIdentifier(lang)
	}
	return core.IsIdentifier(schema) && core.IsIdentifier(name)
}

// defaultSearchLanguage is the PostgreSQL text search configuration a
// search schema without a Language uses.
const defaultSearchLanguage = "english"

// SearchSchema defines full-text search configuration.
type SearchSchema struct {
	Fields []SearchField
	// Language is the PostgreSQL text search configuration; empty is
	// "english".
	Language string
	// Param is the query-string parameter the free-text search box binds
	// to: GeneratorOptions.SearchParam, defaulting to ParamSearch — the
	// parameter list handlers read.
	Param string
}

// GenerateSearchSchema generates a SearchSchema from struct tags on type T.
func GenerateSearchSchema[T any](g *core.SchemaGenerator) (*SearchSchema, error) {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return generateSearchSchema(g, t)
}

func generateSearchSchema(g *core.SchemaGenerator, t reflect.Type) (*SearchSchema, error) {
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct type, got %s", t.Kind())
	}

	tableName, err := g.TableName(t)
	if err != nil {
		return nil, err
	}
	walk, err := g.StructFields(t)
	if err != nil {
		return nil, err
	}
	var fields []SearchField

	for _, field := range walk.Fields {
		if !field.Tags.Parse.Search {
			continue
		}
		fieldName := field.External
		if fieldName == "" {
			continue
		}
		info, err := g.FieldType(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		fieldType := info.Type
		if fieldType != core.FieldTypeString && g.Opts.ColumnMapper == nil {
			// Search compiles to full text over the column's text
			// (to_tsvector on PostgreSQL, LIKE on SQLite); a document, an
			// array or a number has none, and the query would fail at run
			// time. A ColumnMapper owns its expression and may make one.
			return nil, fmt.Errorf("field %s: only string fields are searchable, not %s", field.GoName, fieldType)
		}
		expr, err := g.MappedExpr(tableName, fieldName, fieldType, field.Tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}

		column, err := rowColumn(g, field, fieldName, expr)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}
		fields = append(fields, SearchField{Name: fieldName, Table: tableName, Column: column, Expr: expr})
	}

	if len(fields) == 0 {
		return nil, nil
	}

	param := g.Opts.SearchParam
	if param == "" {
		param = ParamSearch
	}
	lang := cmp.Or(g.Opts.SearchLanguage, defaultSearchLanguage)
	if !isTextSearchConfig(lang) {
		return nil, fmt.Errorf("search language %q is not a text search configuration name", lang)
	}
	return &SearchSchema{Fields: fields, Language: lang, Param: param}, nil
}

// searchCondition is the generated SQL for full-text search: a WHERE-only
// predicate (it never reorders results, so keyset pagination stays stable).
type searchCondition struct {
	Where string
	Args  []any
}

// AndSearch ANDs a free-text search onto a WHERE fragment, numbering the
// search's placeholders after args (themselves after offset earlier
// placeholders). The fragment is parenthesized, so one whose top level is an
// OR stays one operand. An empty query, or a schema with no searchable
// fields, returns the fragment unchanged. Count, list and facet queries fold
// search through it, so all three describe the same population.
func AndSearch(schema *SearchSchema, d Dialect, where string, args []any, offset int, query string) (string, []any, error) {
	sc, err := buildSearchCondition(schema, d, query, offset+len(args))
	if err != nil || sc == nil {
		return where, args, err
	}
	return "((" + where + ") AND " + sc.Where + ")", append(slices.Clone(args), sc.Args...), nil
}

// buildSearchCondition generates dialect-specific full-text search SQL. It
// returns nil when there is nothing to search. The schema's Language is
// emitted into SQL, so anything but a (schema-qualified) identifier is an
// error.
func buildSearchCondition(schema *SearchSchema, dialect Dialect, query string, startParamIdx int) (*searchCondition, error) {
	if schema == nil || query == "" || len(schema.Fields) == 0 {
		return nil, nil
	}

	return dialect.search(schema, query, startParamIdx)
}

func buildPostgresSearch(schema *SearchSchema, dialect Dialect, query string, startIdx int) (*searchCondition, error) {
	lang := cmp.Or(schema.Language, defaultSearchLanguage)
	if !isTextSearchConfig(lang) {
		return nil, fmt.Errorf("search language %q is not a text search configuration name", lang)
	}

	var tsvParts []string
	for _, f := range schema.Fields {
		tsvParts = append(tsvParts, fmt.Sprintf("coalesce(%s, '')", f.sql(dialect)))
	}
	tsvExpr := fmt.Sprintf("to_tsvector('%s', %s)", lang, strings.Join(tsvParts, " || ' ' || "))
	tsqExpr := fmt.Sprintf("plainto_tsquery('%s', %s)", lang, dialect.Placeholder(startIdx+1))

	return &searchCondition{
		Where: fmt.Sprintf("%s @@ %s", tsvExpr, tsqExpr),
		Args:  []any{query},
	}, nil
}

func buildLikeSearch(schema *SearchSchema, dialect Dialect, query string, startIdx int) *searchCondition {
	var parts []string
	var args []any
	paramIdx := startIdx

	// A search for "50%" matches the literal text, not a wildcard. The
	// ESCAPE clause makes the backslash portable across Postgres and SQLite.
	escaped := EscapeLike(query)
	for _, f := range schema.Fields {
		paramIdx++
		ph := dialect.Placeholder(paramIdx)
		parts = append(parts, fmt.Sprintf(`%s LIKE %s ESCAPE '\'`, f.sql(dialect), ph))
		args = append(args, "%"+escaped+"%")
	}

	return &searchCondition{
		Where: "(" + strings.Join(parts, " OR ") + ")",
		Args:  args,
	}
}

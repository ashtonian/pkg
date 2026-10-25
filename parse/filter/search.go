package filter

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// SearchWeight controls the relative importance of a field in full-text search.
type SearchWeight string

const (
	SearchWeightA SearchWeight = "A"
	SearchWeightB SearchWeight = "B"
	SearchWeightC SearchWeight = "C"
	SearchWeightD SearchWeight = "D"
)

// SearchField describes a single field included in full-text search.
type SearchField struct {
	Name    string
	SQLExpr string
	Weight  SearchWeight
}

// SearchSchema defines full-text search configuration.
type SearchSchema struct {
	Fields   []SearchField
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
		sqlExpr, err := g.ColumnExpr(tableName, fieldName, fieldType, field.Tags.Column)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.GoName, err)
		}

		weight := SearchWeightB
		if w := field.Tags.SearchWeight; w != "" {
			weight = SearchWeight(w)
		}

		fields = append(fields, SearchField{Name: fieldName, SQLExpr: sqlExpr, Weight: weight})
	}

	if len(fields) == 0 {
		return nil, nil
	}

	param := g.Opts.SearchParam
	if param == "" {
		param = ParamSearch
	}
	return &SearchSchema{Fields: fields, Language: "english", Param: param}, nil
}

// searchCondition is the generated SQL for full-text search: a WHERE-only
// predicate (it never reorders results, so keyset pagination stays stable).
type searchCondition struct {
	Where string
	Args  []any
}

// AndSearch ANDs a free-text search onto a WHERE fragment, numbering the
// search's placeholders after args (themselves after offset earlier
// placeholders). An empty query, or a schema with no searchable fields,
// returns the fragment unchanged. Count, list and facet queries fold search
// through it, so all three describe the same population.
func AndSearch(schema *SearchSchema, d Dialect, where string, args []any, offset int, query string) (string, []any, error) {
	sc, err := buildSearchCondition(schema, d, query, offset+len(args))
	if err != nil || sc == nil {
		return where, args, err
	}
	return "(" + where + " AND " + sc.Where + ")", append(slices.Clone(args), sc.Args...), nil
}

// buildSearchCondition generates dialect-specific full-text search SQL. It
// returns nil when there is nothing to search. The schema's Language is
// emitted into SQL, so anything but a (schema-qualified) identifier is an
// error.
func buildSearchCondition(schema *SearchSchema, dialect Dialect, query string, startParamIdx int) (*searchCondition, error) {
	if schema == nil || query == "" || len(schema.Fields) == 0 {
		return nil, nil
	}

	switch dialect.Name() {
	case "postgres":
		return buildPostgresSearch(schema, dialect, query, startParamIdx)
	default:
		return buildFallbackSearch(schema, dialect, query, startParamIdx), nil
	}
}

func buildPostgresSearch(schema *SearchSchema, dialect Dialect, query string, startIdx int) (*searchCondition, error) {
	lang := schema.Language
	if lang == "" {
		lang = "english"
	}
	if !identifierRe.MatchString(lang) {
		return nil, fmt.Errorf("search language %q is not a text search configuration name", lang)
	}

	var tsvParts []string
	for _, f := range schema.Fields {
		tsvParts = append(tsvParts, fmt.Sprintf("coalesce(%s, '')", f.SQLExpr))
	}
	tsvExpr := fmt.Sprintf("to_tsvector('%s', %s)", lang, strings.Join(tsvParts, " || ' ' || "))
	tsqExpr := fmt.Sprintf("plainto_tsquery('%s', %s)", lang, dialect.Placeholder(startIdx+1))

	return &searchCondition{
		Where: fmt.Sprintf("%s @@ %s", tsvExpr, tsqExpr),
		Args:  []any{query},
	}, nil
}

func buildFallbackSearch(schema *SearchSchema, dialect Dialect, query string, startIdx int) *searchCondition {
	var parts []string
	var args []any
	paramIdx := startIdx

	// Escape LIKE metacharacters in the user query:
	// a search for "50%" must match the literal string, not act as a
	// wildcard. The ESCAPE clause makes the backslash portable across
	// Postgres and SQLite.
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query)
	for _, f := range schema.Fields {
		paramIdx++
		ph := dialect.Placeholder(paramIdx)
		parts = append(parts, fmt.Sprintf(`%s LIKE %s ESCAPE '\'`, f.SQLExpr, ph))
		args = append(args, "%"+escaped+"%")
	}

	return &searchCondition{
		Where: "(" + strings.Join(parts, " OR ") + ")",
		Args:  args,
	}
}

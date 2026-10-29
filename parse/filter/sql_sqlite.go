package filter

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// SQLiteDialect generates SQLite-compatible SQL. JSON documents and arrays
// are stored as JSON text; document operators compile the needle into
// json_each predicates with PostgreSQL jsonb semantics.
type SQLiteDialect struct{ dialect.SQLite }

// Condition renders one predicate; see Dialect.Condition.
func (d SQLiteDialect) Condition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, error) {
	expr := FieldExpr(d, field)
	if op == OpNull || op == OpNotNull {
		sql, _, err := comparison(expr, op, nil, bind)
		return sql, err
	}
	switch {
	case field.DataType == core.FieldTypeGeo:
		return d.geoCondition(field, op, value, bind)
	case core.IsDocument(field.DataType):
		switch op {
		case OpEq:
			return sqliteJSONMatch(expr, value, false, bind), nil
		case OpNe:
			return "NOT " + sqliteJSONMatch(expr, value, false, bind), nil
		case OpContains:
			return sqliteJSONMatch(expr, value, true, bind), nil
		case OpHasKey:
			key, err := textValue(op, value)
			if err != nil {
				return "", err
			}
			return sqliteHasKey(expr, key, bind), nil
		}
		return "", unsupportedOperator(field, op)
	case field.DataType == core.FieldTypeArray:
		if op != OpContains {
			return "", unsupportedOperator(field, op)
		}
		elems, ok := core.AnySlice(value)
		if !ok {
			return "", fmt.Errorf("$contains on an array requires an array value, got %T", value)
		}
		return sqliteJSONMatch(expr, elems, true, bind), nil
	}

	switch op {
	case OpContains:
		s, err := textValue(op, value)
		if err != nil {
			return "", err
		}
		return "instr(" + expr + ", " + bind(s) + ") > 0", nil
	case OpLike:
		s, err := textValue(op, value)
		if err != nil {
			return "", err
		}
		// SQLite's LIKE ignores ASCII case; GLOB is the case-sensitive form.
		glob, err := likeToGlob(s)
		if err != nil {
			return "", err
		}
		return expr + " GLOB " + bind(glob), nil
	case OpILike:
		s, err := textValue(op, value)
		if err != nil {
			return "", err
		}
		if _, err := likeTokens(s); err != nil {
			return "", err
		}
		return expr + " LIKE " + bind(s) + ` ESCAPE '\'`, nil
	}
	if isOrdering(op) && orderedAsText(field) {
		expr = d.CodePointOrder(expr)
	}
	if field.DataType == core.FieldTypeDecimal {
		bind = decimalBinder(d, bind)
	}
	if sql, ok, err := comparison(expr, op, value, bind); ok {
		return sql, err
	}
	return "", unsupportedOperator(field, op)
}

// sqliteJSONMatch compiles document equality (contains=false) or jsonb
// containment (contains=true) of needle against the JSON text in expr. A
// NULL column yields NULL, as PostgreSQL's operators do.
func sqliteJSONMatch(expr string, needle any, contains bool, bind func(any) string) string {
	c := &jsonCompiler{bind: bind}
	t := jsonTarget{typ: "json_type(" + expr + ")", atom: "json_extract(" + expr + ", '$')", doc: expr}
	var body string
	if contains {
		body = c.contains(t, needle, true)
	} else {
		body = c.equal(t, needle)
	}
	return "(CASE WHEN " + expr + " IS NULL THEN NULL ELSE " + body + " END)"
}

// jsonTarget is a JSON value in SQL: its json_type, its SQL atom (scalars)
// and its JSON text (containers). A column and a json_each row expose all
// three.
type jsonTarget struct{ typ, atom, doc string }

func rowTarget(alias string) jsonTarget {
	return jsonTarget{typ: alias + ".type", atom: alias + ".atom", doc: alias + ".value"}
}

// jsonCompiler turns a needle document into SQL. Container tests sit in
// CASE branches so json_each only ever reads a value already known to be a
// container — SQLite may evaluate the terms of a WHERE conjunction in any
// order, and json_each on a scalar's text errors.
type jsonCompiler struct {
	bind func(any) string
	n    int
}

func (c *jsonCompiler) alias() string {
	c.n++
	return "je" + strconv.Itoa(c.n)
}

func (c *jsonCompiler) each(t jsonTarget, alias string) string {
	return "json_each(" + t.doc + ") " + alias
}

// equal is jsonb equality: objects by key set, arrays in order, numbers by
// value.
func (c *jsonCompiler) equal(t jsonTarget, v any) string {
	switch x := v.(type) {
	case map[string]any:
		terms := []string{"(SELECT count(*) FROM json_each(" + t.doc + ")) = " + strconv.Itoa(len(x))}
		for _, k := range slices.Sorted(maps.Keys(x)) {
			a := c.alias()
			terms = append(terms, "EXISTS (SELECT 1 FROM "+c.each(t, a)+" WHERE "+a+".key = "+c.bind(k)+" AND "+c.equal(rowTarget(a), x[k])+")")
		}
		return caseWhen(t.typ+" = 'object'", terms)
	case []any:
		terms := []string{"json_array_length(" + t.doc + ") = " + strconv.Itoa(len(x))}
		for i, e := range x {
			a := c.alias()
			terms = append(terms, "EXISTS (SELECT 1 FROM "+c.each(t, a)+" WHERE "+a+".key = "+strconv.Itoa(i)+" AND "+c.equal(rowTarget(a), e)+")")
		}
		return caseWhen(t.typ+" = 'array'", terms)
	}
	return c.scalar(t, v)
}

// contains is jsonb `@>`; top enables the rule that an array contains a
// scalar that is one of its elements.
func (c *jsonCompiler) contains(t jsonTarget, v any, top bool) string {
	switch x := v.(type) {
	case map[string]any:
		var terms []string
		for _, k := range slices.Sorted(maps.Keys(x)) {
			a := c.alias()
			terms = append(terms, "EXISTS (SELECT 1 FROM "+c.each(t, a)+" WHERE "+a+".key = "+c.bind(k)+" AND "+c.contains(rowTarget(a), x[k], false)+")")
		}
		return caseWhen(t.typ+" = 'object'", terms)
	case []any:
		var terms []string
		for _, e := range x {
			a := c.alias()
			terms = append(terms, "EXISTS (SELECT 1 FROM "+c.each(t, a)+" WHERE "+c.contains(rowTarget(a), e, false)+")")
		}
		return caseWhen(t.typ+" = 'array'", terms)
	}
	eq := c.scalar(t, v)
	if !top {
		return eq
	}
	a := c.alias()
	member := caseWhen(t.typ+" = 'array'", []string{"EXISTS (SELECT 1 FROM " + c.each(t, a) + " WHERE " + c.scalar(rowTarget(a), v) + ")"})
	return "(" + eq + " OR " + member + ")"
}

func (c *jsonCompiler) scalar(t jsonTarget, v any) string {
	switch x := v.(type) {
	case nil:
		return "(" + t.typ + " = 'null')"
	case bool:
		if x {
			return "(" + t.typ + " = 'true')"
		}
		return "(" + t.typ + " = 'false')"
	case string:
		return "(" + t.typ + " = 'text' AND " + t.atom + " = " + c.bind(x) + ")"
	}
	return "(" + t.typ + " IN ('integer', 'real') AND " + t.atom + " = " + c.bind(v) + ")"
}

// caseWhen evaluates terms only when guard holds; no terms means the guard
// alone decides.
func caseWhen(guard string, terms []string) string {
	body := "1"
	if len(terms) > 0 {
		body = "(" + strings.Join(terms, " AND ") + ")"
	}
	return "(CASE WHEN " + guard + " THEN " + body + " ELSE 0 END)"
}

// earthRadiusMeters is the mean Earth radius (IUGG). SQLite's `$near` is
// spherical; PostgreSQL's geography distance is spheroidal, so the two can
// disagree by up to ~0.5% of the distance at the boundary.
const earthRadiusMeters = "6371008.8"

// geoCondition supports `$near` (haversine) and `$within` a bounding box
// over JSONB points. Polygons and `$intersects` need PostGIS: E2115.
func (d SQLiteDialect) geoCondition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, error) {
	if err := validateGeoValue(value); err != nil {
		return "", err
	}
	if field.GeoConfig != nil && field.GeoConfig.StorageType != GeoStorageJSONB {
		return "", geoUnsupported(d, op, "native "+string(field.GeoConfig.StorageType)+" storage")
	}
	lat, lon := sqliteGeoExprs(field)
	switch v := value.(type) {
	case *GeoDistance:
		if op != OpNear {
			break
		}
		meters, err := ParseDistance(v.Distance)
		if err != nil {
			return "", err
		}
		pLat, pLon := bind(v.Lat), bind(v.Lon)
		// min(1, …) keeps asin in its domain when rounding lands a hair
		// above 1 at the antipode.
		return fmt.Sprintf(
			"(2 * %s * asin(min(1.0, sqrt(power(sin(radians(%s - %s) / 2), 2) + cos(radians(%s)) * cos(radians(%s)) * power(sin(radians(%s - %s) / 2), 2)))) <= %s)",
			earthRadiusMeters, lat, pLat, pLat, lat, lon, pLon, bind(meters),
		), nil
	case *GeoBoundingBox:
		if op != OpWithin {
			return "", geoUnsupported(d, op, "bounding boxes")
		}
		return fmt.Sprintf("(%s BETWEEN %s AND %s AND %s BETWEEN %s AND %s)",
			lat, bind(v.BottomRight.Lat), bind(v.TopLeft.Lat),
			lon, bind(v.TopLeft.Lon), bind(v.BottomRight.Lon)), nil
	case *GeoPolygon:
		return "", geoUnsupported(d, op, "polygons")
	}
	return "", fmt.Errorf("geo operator %s: unexpected value %T", op, value)
}

func sqliteGeoExprs(field *FieldSchema) (lat, lon string) {
	latKey, lonKey := "lat", "lon"
	if field.GeoConfig != nil {
		latKey, lonKey = field.GeoConfig.LatKey, field.GeoConfig.LonKey
	}
	return "json_extract(" + field.SQLExpr + ", '$." + latKey + "')",
		"json_extract(" + field.SQLExpr + ", '$." + lonKey + "')"
}

func geoUnsupported(d dialect.Dialect, op FilterOperator, shape string) error {
	return &core.ParseError{
		Kind:    core.ParseErrorKindFilter,
		Code:    ECodeGeoNotSupported.String(),
		Message: fmt.Sprintf("geo operator '%s' on %s is not supported by the %s dialect", op, shape, d.Name()),
	}
}

// MarshalValue serializes a value for SQLite parameter binding.
// JSON/Array/Object types are marshaled to JSON strings for TEXT storage.
func (d SQLiteDialect) MarshalValue(value any, t core.TypeInfo) (any, error) {
	if t.Type == core.FieldTypeDate {
		return nullableDateValue(value)
	}

	if _, ok := value.(driver.Valuer); ok && !t.SQLType.IsJSON() {
		return value, nil // a database value the driver encodes, not a document
	}
	switch t.Type {
	case core.FieldTypeJSON, core.FieldTypeArray, core.FieldTypeObject:
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	default:
		return value, nil
	}
}

// CTEMaterializeHint returns "" — SQLite always materializes CTEs.
func (SQLiteDialect) CTEMaterializeHint() string { return "" }

// RowLock is empty: SQLite serializes writers, so a write transaction
// already holds what a row lock would.
func (SQLiteDialect) RowLock() string { return "" }

// ReadColumn returns column: SQLite stores network and interval values as
// TEXT, which a string field scans.
func (SQLiteDialect) ReadColumn(column string, t core.SQLType) string {
	if t == core.SQLDate {
		// Legacy SQLite DATE adapters stored midnight timestamps in TEXT.
		// Keep their written calendar components in reads, filters and keysets;
		// SQLite date() would instead apply the timestamp's UTC offset.
		return "substr(" + column + ", 1, 10)"
	}
	return column
}

// DecimalParam casts the parameter to NUMERIC: an integer when the text
// holds one exactly, otherwise a REAL — how a NUMERIC or REAL column stores
// the value, so the two compare as numbers, never as text. A bare text
// parameter compares as text against a path's number or a column of no
// numeric affinity.
func (SQLiteDialect) DecimalParam(placeholder string) string {
	return "CAST(" + placeholder + " AS NUMERIC)"
}

// NativeRowCompare reports false: row-value comparison is unevenly
// supported across the SQLite build matrix, and the expansion stays
// planner-friendly on single-column indexes.
func (SQLiteDialect) NativeRowCompare() bool { return false }

// CodePointOrder applies BINARY collation, overriding a column declared
// NOCASE or RTRIM.
func (SQLiteDialect) CodePointOrder(expr string) string { return expr + " COLLATE BINARY" }

// sqliteHasKey is jsonb `?` over JSON text: a top-level object key, a
// string element of a top-level array, or a scalar string equal to key; a
// NULL column is NULL.
func sqliteHasKey(expr, key string, bind func(any) string) string {
	t := "json_type(" + expr + ")"
	return "(CASE WHEN " + expr + " IS NULL THEN NULL" +
		" WHEN " + t + " = 'object' THEN EXISTS (SELECT 1 FROM json_each(" + expr + ") hk WHERE hk.key = " + bind(key) + ")" +
		" WHEN " + t + " = 'array' THEN EXISTS (SELECT 1 FROM json_each(" + expr + ") hk WHERE hk.type = 'text' AND hk.value = " + bind(key) + ")" +
		" WHEN " + t + " = 'text' THEN json_extract(" + expr + ", '$') = " + bind(key) +
		" ELSE 0 END)"
}

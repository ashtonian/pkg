package filter

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/decimals"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/internal/geo"
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
	if field.DataType != core.FieldTypeGeo && !core.IsDocument(field.DataType) {
		if err := sqliteIntegers(field, value); err != nil {
			return "", err
		}
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
			return "", fmt.Errorf("$contains on an array requires an array value, got %s", gowalker.TypeText(reflect.TypeOf(value)))
		}
		// The column's JSON text is read as its element type, as `$some`
		// reads it; a NULL column is Unknown, as PostgreSQL's `@>` is.
		held, err := containsAll(d, field, elems, "elem1__", bind)
		if err != nil {
			return "", err
		}
		return "(CASE WHEN " + expr + " IS NULL THEN NULL WHEN json_type(" + expr + ") = 'array' THEN " + held + " ELSE 0 END)", nil
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
	if op == OpIn || op == OpNin {
		return membership(op, value, func(members []any, in bool) (string, error) {
			return d.memberSet(field, members, in, bind)
		})
	}
	if isOrdering(op) && orderedAsText(field) {
		expr = d.CodePointOrder(expr)
	}
	if field.DataType == core.FieldTypeDecimal {
		if sql, ok, err := decimalComparison(d, field, op, value, bind); ok {
			return sql, err
		}
		return "", unsupportedOperator(field, op)
	}
	if sql, ok, err := comparison(expr, op, value, bind); ok {
		return sql, err
	}
	return "", unsupportedOperator(field, op)
}

// memberSet renders a set bound as one JSON array. A decimal's set is
// compared as its equality is (decimalComparison): the field's key against
// each member's, both read through DecimalOrder, so the index that serves
// its ranges, sorts and seeks serves the set too. Any other set is its
// rows (setRows).
func (d SQLiteDialect) memberSet(field *FieldSchema, members []any, in bool, bind func(any) string) (string, error) {
	keyword := " IN "
	if !in {
		keyword = " NOT IN "
	}
	if field.DataType == core.FieldTypeDecimal {
		param, err := sqliteSetParam(field, members, bind)
		if err != nil {
			return "", err
		}
		return d.DecimalOrder(fieldDecimal(d, field)) + keyword + "(SELECT " + d.DecimalOrder(DecimalParam("value")) + " FROM json_each(" + param + "))", nil
	}
	set, err := d.setRows(field, members, bind)
	if err != nil {
		return "", err
	}
	return FieldExpr(d, field) + keyword + "(" + set + ")", nil
}

// setRows is a set's members, bound as one JSON array, read back by
// json_each as rows of v holding the values a scalar parameter binds:
// integers as INTEGER, floats as REAL, booleans as 1 and 0, text as TEXT,
// and decimals as their canonical text, the value FieldExpr reads a decimal
// as.
func (SQLiteDialect) setRows(field *FieldSchema, members []any, bind func(any) string) (string, error) {
	param, err := sqliteSetParam(field, members, bind)
	if err != nil {
		return "", err
	}
	return "SELECT value AS v FROM json_each(" + param + ")", nil
}

// sqliteSetParam binds a set's members as one JSON array (sqliteSetMembers)
// and returns its placeholder.
func sqliteSetParam(field *FieldSchema, members []any, bind func(any) string) (string, error) {
	doc, err := sqliteSetMembers(field, members)
	if err != nil {
		return "", fmt.Errorf("field %s: %w", field.ExternalName, err)
	}
	arr, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("field %s: marshal set: %w", field.ExternalName, err)
	}
	return bind(string(arr)), nil
}

// sqliteIntegers refuses an unsigned operand above int64, alone or in a
// list: SQLite's integers end there and its driver cannot bind one, so no
// row holds the value as an integer. It is E2073, the request's error, as a
// typed field whose Go kind cannot hold a value is.
func sqliteIntegers(field *FieldSchema, value any) error {
	values := []any{value}
	if items, ok := core.AnySlice(value); ok {
		values = items
	}
	for _, v := range values {
		if n, ok := v.(uint64); ok && n > math.MaxInt64 {
			return valueMismatch(field, fmt.Errorf("%d exceeds SQLite's 64-bit signed integers", n))
		}
	}
	return nil
}

// sqliteSetMembers are a set's members as its JSON array holds them.
func sqliteSetMembers(field *FieldSchema, members []any) (any, error) {
	if field.DataType == core.FieldTypeDecimal {
		return typedSlice(members, core.DecimalText)
	}
	if err := sqliteIntegers(field, members); err != nil {
		return nil, err
	}
	doc := make([]any, len(members))
	for i, m := range members {
		switch v := m.(type) {
		case float64:
			// Always a REAL: encoding/json writes an integral float below
			// 1e21 as an integer literal, which SQLite reads as an INTEGER
			// unequal to the float itself above 2^53.
			doc[i] = json.Number(strconv.FormatFloat(v, 'g', -1, 64))
		default:
			doc[i] = v
		}
	}
	return doc, nil
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

// geoCondition renders the geo operators over a JSONB point through
// parsesqlite's functions (internal/geo), so SQLite decides them as PostGIS
// and Evaluate do: $near by the WGS 84 geodesic distance, $within and
// $intersects a box or polygon planar in lon/lat, boundary included. Each
// function reads the point from its JSON text, with the member names bound,
// and is NULL when the point is. Native geometry storage needs PostGIS:
// E2115.
func (d SQLiteDialect) geoCondition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, error) {
	if err := validateGeoValue(value); err != nil {
		return "", err
	}
	if field.GeoConfig != nil && field.GeoConfig.StorageType != GeoStorageJSONB {
		return "", geoUnsupported(d, op, "native "+string(field.GeoConfig.StorageType)+" storage")
	}
	latKey, lonKey := geoKeys(field)
	call := func(fn string, operands ...any) string {
		args := []string{"json(" + field.sql(d) + ")", bind(latKey), bind(lonKey)}
		for _, o := range operands {
			args = append(args, bind(o))
		}
		return fn + "(" + strings.Join(args, ", ") + ")"
	}
	switch v := value.(type) {
	case *GeoDistance:
		if op != OpNear {
			break
		}
		meters, err := ParseDistance(v.Distance)
		if err != nil {
			return "", err
		}
		return call(geo.SQLiteNearFunc, v.Lat, v.Lon, meters), nil
	case *GeoBoundingBox:
		if op != OpWithin && op != OpIntersects {
			break
		}
		box := geoBox(v)
		return call(geo.SQLiteInBoxFunc, box.South, box.North, box.West, box.East), nil
	case *GeoPolygon:
		if op != OpWithin && op != OpIntersects {
			break
		}
		return call(geo.SQLiteCoveredByFunc, geo.AppendPolygonWKB(nil, geoRing(v))), nil
	}
	return "", fmt.Errorf("geo operator %s does not take %s", op, gowalker.TypeText(reflect.TypeOf(value)))
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
// A timestamp, and each element of a list of them, is its canonical text
// (core.FormatTimestamp), which SQLite stores: UTC at a fixed width, so
// text order is time order.
func (d SQLiteDialect) MarshalValue(value any, t core.TypeInfo) (any, error) {
	switch t.Type {
	case core.FieldTypeDate:
		return nullableDateValue(value)
	case core.FieldTypeTimestamp:
		return timestampText(value)
	}

	if _, ok := value.(driver.Valuer); ok && !t.SQLType.IsJSON() {
		return value, nil // a database value the driver encodes, not a document
	}
	switch t.Type {
	case core.FieldTypeJSON, core.FieldTypeArray, core.FieldTypeObject:
		if t.Type == core.FieldTypeArray && t.Elem == core.FieldTypeTimestamp {
			var err error
			if value, err = timestampTexts(value); err != nil {
				return nil, err
			}
		}
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	default:
		return value, nil
	}
}

// StoresAsText reports the values MarshalValue writes as text: a
// timestamp's canonical text, a document's or an array's JSON.
func (SQLiteDialect) StoresAsText(t core.FieldType) bool {
	return t == core.FieldTypeTimestamp || core.IsDocument(t) || t == core.FieldTypeArray
}

// timestampText is a timestamp's canonical text, from a time.Time, a
// pointer to one, a driver.Valuer holding one (sql.NullTime) or RFC 3339
// text; nil and NULL are NULL. The driver would store a time.Time as its
// own text in the value's offset, which neither compares nor keys as the
// instant.
func timestampText(value any) (any, error) {
	v, null, err := core.StoredValue(value)
	if err != nil || null {
		return nil, err
	}
	return core.NormalizeScalar(core.FieldTypeTimestamp, reflect.Invalid, v)
}

// timestampTexts is a list of timestamps, or a pointer to one, with each
// element its canonical text: json.Marshal writes a time.Time in its own
// offset, and an element reads as a timestamp only in UTC (PathExpr). A
// nil list, or a value that is no list, is unchanged.
func timestampTexts(value any) (any, error) {
	list := core.Deref(value)
	if list == nil {
		return value, nil
	}
	if rv := reflect.ValueOf(list); rv.Kind() == reflect.Slice && rv.IsNil() {
		return value, nil
	}
	items, ok := core.AnySlice(list)
	if !ok {
		return value, nil
	}
	out := make([]any, len(items))
	for i, item := range items {
		text, err := timestampText(item)
		if err != nil {
			return nil, fmt.Errorf("timestamp element %d: %w", i, err)
		}
		out[i] = text
	}
	return out, nil
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

// DecimalValue reads a column or a member through parse_decimal
// (parsesqlite): its canonical decimal text, exact for an INTEGER or for
// TEXT holding the number, where CAST(… AS NUMERIC) keeps 15 significant
// digits. A parameter is the canonical text already.
func (d SQLiteDialect) DecimalValue(o DecimalOperand) string {
	switch o.source {
	case decimalFromColumn:
		return decimals.SQLiteValueFunc + "(" + o.sql + ")"
	case decimalFromMember:
		return d.PathExpr(o.path, core.FieldTypeDecimal)
	case decimalFromParam:
		return o.sql
	}
	panic(o.invalid())
}

// DecimalOrder reads the operand through parse_decimal_key (parsesqlite), a
// BLOB that orders bytewise as the decimal does. parse_decimal_key reads
// what parse_decimal reads, so a column and a member are keyed from their
// own text, one function call per row, not two.
func (d SQLiteDialect) DecimalOrder(o DecimalOperand) string {
	var operand string
	switch o.source {
	case decimalFromColumn, decimalFromParam:
		operand = o.sql
	case decimalFromMember:
		_, _, operand = sqlitePathMember(d, o.path)
	default:
		panic(o.invalid())
	}
	return decimals.SQLiteKeyFunc + "(" + operand + ")"
}

// codePointPair collates the operand: SQLite seeks an index only on a row
// value's bare columns, and BINARY on the operand still overrides a column
// declared NOCASE or RTRIM, an explicit collation outranking a column's.
// The index must be BINARY-collated to serve the seek.
func (d SQLiteDialect) codePointPair(column, operand string) (string, string) {
	return column, d.CodePointOrder(operand)
}

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

// elementRows reads a JSON array, an array path's member or an array
// column's JSON text, with json_each when it is an array: json_each would
// read an object's members, or a scalar as one row.
func (d SQLiteDialect) elementRows(f *FieldSchema, alias string) string {
	if p := f.Path; p != nil {
		path := sqlitePathLiteral(p.Segments)
		doc := p.document(d)
		return "json_each(CASE WHEN json_type(" + doc + ", " + path + ") = 'array' THEN " + doc + " END, " + path + ") AS " + alias
	}
	return "json_each(CASE WHEN json_type(" + f.sql(d) + ") = 'array' THEN " + f.sql(d) + " END) AS " + alias
}

// objectElement is an object element's JSON text; json_extract would refuse
// a scalar's text.
func (SQLiteDialect) objectElement(alias string) string {
	return "(CASE WHEN " + alias + ".type = 'object' THEN " + alias + ".value END)"
}

// scalarElement reads every element as an element path (PathExpr): an
// array column is JSON text too.
func (SQLiteDialect) scalarElement(f *FieldSchema, alias string) *FieldSchema {
	e := bound(elementOf(f))
	e.Table, e.Column, e.Expr = "", "", ""
	e.Path = &JSONPath{alias: alias, Storage: StorageJSONB, element: true}
	return &e
}

// numberKey reads a number member's JSON literal as written through
// parse_decimal, which json_extract would round to a double, and a REAL
// column through parse_decimal (a double) or parse_decimal_float32 (a
// float32 field's). A decimal's FieldExpr is already its canonical text.
func (d SQLiteDialect) numberKey(f *FieldSchema) string {
	switch {
	case f.DataType == core.FieldTypeDecimal:
		return FieldExpr(d, f)
	case f.Path != nil:
		kind, _, literal := sqlitePathMember(d, f.Path)
		return "(CASE WHEN " + kind + " IN ('integer', 'real') THEN " + decimals.SQLiteValueFunc + "(" + literal + ") END)"
	case f.DataType == core.FieldTypeInt:
		return "CAST(" + f.sql(d) + " AS TEXT)"
	case f.NumberKind == reflect.Float32:
		return decimals.SQLiteFloat32Func + "(" + f.sql(d) + ")"
	}
	return decimals.SQLiteValueFunc + "(" + f.sql(d) + ")"
}

// PathExpr renders p's typed value for SQLite, whose documents are JSON
// text: json_type selects the member's kind; json_extract reads it. A
// decimal member is read as the JSON literal `->` returns, which keeps the
// number as written where json_extract would round it to a double, through
// parse_decimal (parsesqlite). An element path reads its json_each row: its
// type and value are json_type's and json_extract's of the element, and
// its literal is the source document's (the hidden json column) at its
// fullkey.
func (d SQLiteDialect) PathExpr(p *JSONPath, t core.FieldType) string {
	kind, val, literal := sqlitePathMember(d, p)
	return sqliteTypedMember(t, kind, val, literal)
}

// TwoValuedCondition has no second rendering on SQLite.
func (SQLiteDialect) TwoValuedCondition(*FieldSchema, FilterOperator, any, func(any) string) (string, bool, error) {
	return "", false, nil
}

// containsElement reads the element table: SQLite has no containment an
// index serves.
func (SQLiteDialect) containsElement(*FieldSchema, *FilterNode, func(any) string) (string, bool, error) {
	return "", false, nil
}

// timestampKey is the column's text: SQLite holds a timestamp as the
// canonical text (MarshalValue).
func (d SQLiteDialect) timestampKey(expr string) string { return d.textKey(expr) }

func (SQLiteDialect) textKey(expr string) string { return "CAST(" + expr + " AS TEXT)" }

// search matches each field with LIKE: SQLite has no text search the
// module can assume.
func (d SQLiteDialect) search(schema *SearchSchema, query string, startIdx int) (*searchCondition, error) {
	return buildLikeSearch(schema, d, query, startIdx), nil
}

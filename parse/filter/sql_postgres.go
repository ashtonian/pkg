package filter

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"time"
	"uuid"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// PostgresDialect generates PostgreSQL-compatible SQL. JSON documents are
// jsonb columns and arrays native array columns (core.TypeOf); geo
// operators use PostGIS. Collation is the code-point collation string sorts,
// keyset seeks and string ranges compile under; the zero value is
// pg_c_utf8 (PostgreSQL 17+).
//
// ContainmentMaxValues bounds the `$in` sets a jsonb path compares by
// containment, which a GIN index serves member by member
// (TwoValuedCondition): 0 is DefaultContainmentMaxValues, and a negative
// value compares no set by containment.
type PostgresDialect struct {
	dialect.Postgres
	Collation            PostgresCollation
	ContainmentMaxValues int
}

// DefaultContainmentMaxValues is the largest `$in` set a jsonb path
// compares by containment when PostgresDialect.ContainmentMaxValues is 0.
// A larger set joins its members as one unnested array, which a hash
// serves in any plan.
const DefaultContainmentMaxValues = 256

// PostgresCollation is a PostgreSQL collation that orders strings by code
// point — the order SQLite's BINARY, the memory backend and cursor values
// use — so the values are closed: CollationPgCUTF8 and CollationC. Another
// integer converted to one names no collation, and SQL rendered with it
// panics.
type PostgresCollation uint8

const (
	// CollationPgCUTF8 is pg_c_utf8 (PostgreSQL 17+), the zero value:
	// code-point order with Unicode case mapping, so ILIKE on a column
	// declared with it still folds non-ASCII letters.
	CollationPgCUTF8 PostgresCollation = iota
	// CollationC is "C", for PostgreSQL 14–16: the same order (UTF-8 bytes
	// sort as their code points), but a column declared with it folds only
	// ASCII letters under ILIKE.
	CollationC
)

// String is the collation's SQL name.
func (c PostgresCollation) String() string {
	switch c {
	case CollationPgCUTF8:
		return "pg_c_utf8"
	case CollationC:
		return `"C"`
	}
	panic("parse: PostgresCollation(" + strconv.Itoa(int(c)) + ") is no collation")
}

// Condition renders one predicate; see Dialect.Condition.
func (d PostgresDialect) Condition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, error) {
	expr := FieldExpr(d, field)
	if op == OpNull || op == OpNotNull {
		sql, _, err := comparison(expr, op, nil, bind)
		return sql, err
	}
	switch {
	case field.DataType == core.FieldTypeGeo:
		return d.geoCondition(field, op, value, bind)
	case core.IsDocument(field.DataType):
		if op == OpHasKey {
			key, err := textValue(op, value)
			if err != nil {
				return "", err
			}
			// The ? operator, not jsonb_exists(): only the operator can use a
			// GIN index on the column.
			return pgDocument(d, field) + " ? " + bind(key), nil
		}
		doc, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("marshal %s document: %w", field.ExternalName, err)
		}
		col := pgDocument(d, field)
		switch op {
		case OpEq:
			return col + " = " + bind(string(doc)) + "::jsonb", nil
		case OpNe:
			return col + " <> " + bind(string(doc)) + "::jsonb", nil
		case OpContains:
			return col + " @> " + bind(string(doc)) + "::jsonb", nil
		}
		return "", unsupportedOperator(field, op)
	case field.DataType == core.FieldTypeArray:
		if op != OpContains {
			return "", unsupportedOperator(field, op)
		}
		arr, err := pgFieldArray(field, field.Elem, value)
		if err != nil {
			return "", err
		}
		// The parameter takes the column's array type; the typed Go slice
		// encodes into it.
		return expr + " @> " + bind(arr), nil
	}

	switch op {
	case OpContains:
		s, err := textValue(op, value)
		if err != nil {
			return "", err
		}
		return expr + " LIKE " + bind("%"+EscapeLike(s)+"%") + ` ESCAPE '\'`, nil
	case OpLike, OpILike:
		s, err := textValue(op, value)
		if err != nil {
			return "", err
		}
		if _, err := likeTokens(s); err != nil {
			return "", err
		}
		keyword := " LIKE "
		if op == OpILike {
			keyword = " ILIKE "
		}
		return expr + keyword + bind(s) + ` ESCAPE '\'`, nil
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

// membership renders a set, bound as one array parameter. A column compares
// with `= ANY`, the parameter taking the column's own array type, so the
// column's equality applies and its index serves the set. A path's value
// has the type PathExpr renders, so its set joins the array unnested to
// that type (setRows): a hash serves it in every plan, where under a
// generic plan `= ANY` probes a parameter's array member by member for each
// row.
func (d PostgresDialect) memberSet(field *FieldSchema, members []any, in bool, bind func(any) string) (string, error) {
	expr := FieldExpr(d, field)
	if field.Path != nil {
		set, err := d.setRows(field, members, bind)
		if err != nil {
			return "", err
		}
		if in {
			return expr + " IN (" + set + ")", nil
		}
		return expr + " NOT IN (" + set + ")", nil
	}
	var param string
	if field.DataType == core.FieldTypeDecimal {
		arr, err := typedSlice(members, core.DecimalText)
		if err != nil {
			return "", fmt.Errorf("field %s: %w", field.ExternalName, err)
		}
		param = bind(arr) + "::text[]::numeric[]"
	} else {
		arr, err := pgFieldArray(field, field.DataType, members)
		if err != nil {
			return "", err
		}
		param = bind(arr)
	}
	if in {
		return expr + " = ANY(" + param + ")", nil
	}
	return expr + " <> ALL(" + param + ")", nil
}

// setRows is a path's set members, bound as one array parameter, unnested
// to rows of v, the type PathExpr reads the path as.
func (d PostgresDialect) setRows(field *FieldSchema, members []any, bind func(any) string) (string, error) {
	if field.Path == nil {
		return "", fmt.Errorf("field %s: a set is read as rows only for a path", field.ExternalName)
	}
	var set string
	var arr any
	var err error
	switch field.DataType {
	case core.FieldTypeInt, core.FieldTypeFloat, core.FieldTypeDecimal:
		// Exact decimal text, cast as the path's number is.
		arr, err = typedSlice(members, core.DecimalText)
		set = "unnest(" + bind(arr) + "::text[])::numeric"
	case core.FieldTypeBool:
		arr, err = pgArray(core.FieldTypeBool, members)
		set = "unnest(" + bind(arr) + "::boolean[])"
	default: // string, timestamp and UUID members compare as their canonical text
		arr, err = typedSlice(members, func(v any) (string, error) { return textValue(OpIn, v) })
		set = "unnest(" + bind(arr) + "::text[])"
	}
	if err != nil {
		return "", fmt.Errorf("field %s: %w", field.ExternalName, err)
	}
	return "SELECT " + set + " AS v", nil
}

// TwoValuedCondition compares a string, boolean or number member of a jsonb
// path by containment, which a GIN index on the document serves: `$eq` as
// `doc @> fragment`, and `$in` with at most ContainmentMaxValues members as
// `doc @> ANY(fragments)`, each fragment the path's objects down to the
// value. Containment is False where the typed comparison is Unknown (a
// missing member, or one of another JSON type), and compares numbers by
// value and a member holding an array apart from its elements, as the typed
// comparison does. An array member of such elements takes `$contains` as
// `doc @> fragment`, the fragment's array holding the listed elements: it is
// False where the member has no element of its type, where `$contains` is
// Unknown. Decimals keep the typed comparison (a decimal member may be held
// as a string), as does an array's element: containment of a scalar holds
// for an element that is an array holding it. A fragment's members are
// object keys, as the typed comparison's are (pgMember).
func (d PostgresDialect) TwoValuedCondition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, bool, error) {
	p := field.Path
	if p == nil || p.element || p.Storage != StorageJSONB {
		return "", false, nil
	}
	if field.DataType == core.FieldTypeArray {
		elems, ok := core.AnySlice(value)
		if op != OpContains || !containable(field.Elem) || !ok || len(elems) == 0 || slices.Contains(elems, nil) {
			return "", false, nil // `[]` is in every array, an empty one too
		}
		fragment, err := containmentFragment(p.Segments, elems)
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", field.ExternalName, err)
		}
		return p.document(d) + " @> " + bind(fragment) + "::jsonb", true, nil
	}
	if !containable(field.DataType) {
		return "", false, nil
	}
	switch op {
	case OpEq:
		if value == nil {
			return "", false, nil // `= NULL` is Unknown; {"k": null} would hold
		}
		fragment, err := containmentFragment(p.Segments, value)
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", field.ExternalName, err)
		}
		return p.document(d) + " @> " + bind(fragment) + "::jsonb", true, nil
	case OpIn:
		members, ok := core.AnySlice(value)
		if !ok || len(members) == 0 || len(members) > d.containmentMaxValues() || slices.Contains(members, nil) {
			return "", false, nil
		}
		fragments, err := typedSlice(members, func(v any) (string, error) { return containmentFragment(p.Segments, v) })
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", field.ExternalName, err)
		}
		return p.document(d) + " @> ANY(" + bind(fragments) + "::jsonb[])", true, nil
	}
	return "", false, nil
}

func (d PostgresDialect) containmentMaxValues() int {
	if d.ContainmentMaxValues == 0 {
		return DefaultContainmentMaxValues
	}
	return d.ContainmentMaxValues // negative: no set qualifies
}

// containable reports the member types whose typed comparison containment
// reproduces.
func containable(t core.FieldType) bool {
	switch t {
	case core.FieldTypeString, core.FieldTypeBool, core.FieldTypeInt, core.FieldTypeFloat:
		return true
	}
	return false
}

// containmentFragment is the JSON document holding value at segs, objects
// nested from the outermost member down.
func containmentFragment(segs []string, value any) (string, error) {
	doc := value
	for i := len(segs) - 1; i >= 0; i-- {
		doc = map[string]any{segs[i]: doc}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal containment fragment: %w", err)
	}
	return string(b), nil
}

// errNoCommonInteger is a set of integers no PostgreSQL integer array holds:
// a negative value beside one above int64.
var errNoCommonInteger = errors.New("no PostgreSQL integer type holds every value")

// pgFieldArray is pgArray for a request operand of field: a set no
// PostgreSQL integer array holds is the request's E2073, as a typed field's
// Go kind refuses one of its values.
func pgFieldArray(field *FieldSchema, elem core.FieldType, value any) (any, error) {
	arr, err := pgArray(elem, value)
	if errors.Is(err, errNoCommonInteger) {
		return nil, valueMismatch(field, err)
	}
	if err != nil {
		return nil, fmt.Errorf("field %s: %w", field.ExternalName, err)
	}
	return arr, nil
}

// pgArray converts normalized array elements into the typed slice pgx
// encodes as the column's array type. UUIDs bind as their canonical text:
// pgx encodes a []string into uuid[] in every query mode, while a slice of a
// UUID type needs a type registration the caller's connection may not have.
func pgArray(elem core.FieldType, value any) (any, error) {
	items, ok := core.AnySlice(value)
	if !ok {
		return nil, fmt.Errorf("an array column requires an array value, got %s", gowalker.TypeText(reflect.TypeOf(value)))
	}
	switch elem {
	case core.FieldTypeString:
		return typedSlice(items, func(v any) (string, error) { return textValue(OpContains, v) })
	case core.FieldTypeUUID:
		return typedSlice(items, func(v any) (string, error) {
			s, err := textValue(OpContains, v)
			if err != nil {
				return "", err
			}
			id, err := uuid.Parse(s)
			if err != nil {
				return "", err
			}
			return id.String(), nil
		})
	case core.FieldTypeDate:
		return typedSlice(items, func(v any) (time.Time, error) {
			normalized, err := core.NormalizeScalar(core.FieldTypeDate, reflect.Invalid, v)
			if err != nil {
				return time.Time{}, err
			}
			return time.Parse(time.DateOnly, normalized.(string))
		})
	case core.FieldTypeTimestamp:
		return typedSlice(items, func(v any) (time.Time, error) {
			s, err := textValue(OpContains, v)
			if err != nil {
				return time.Time{}, err
			}
			return time.Parse(time.RFC3339Nano, s)
		})
	case core.FieldTypeTime, core.FieldTypeInterval, core.FieldTypeInet, core.FieldTypeCIDR, core.FieldTypeMACAddr:
		// Canonical text, which the column's array type parses, and which an
		// interval's canonical text array compares as written.
		return typedSlice(items, func(v any) (string, error) {
			canonical, err := core.NormalizeScalar(elem, reflect.Invalid, v)
			if err != nil {
				return "", err
			}
			s, ok := canonical.(string)
			if !ok {
				return "", fmt.Errorf("array element %v has no %s text", v, elem)
			}
			return s, nil
		})
	case core.FieldTypeInt:
		if i := slices.IndexFunc(items, func(v any) bool { n, ok := v.(uint64); return ok && n > math.MaxInt64 }); i >= 0 {
			// An unsigned field's values above int64, for a numeric column.
			return typedSlice(items, func(v any) (uint64, error) {
				switch n := v.(type) {
				case uint64:
					return n, nil
				case int64:
					if n >= 0 {
						return uint64(n), nil
					}
					return 0, fmt.Errorf("%w: %d and %v", errNoCommonInteger, n, items[i])
				}
				return 0, fmt.Errorf("array element %v is not an integer", v)
			})
		}
		return typedSlice(items, func(v any) (int64, error) {
			switch n := v.(type) {
			case int64:
				return n, nil
			case uint64:
				return int64(n), nil // at most math.MaxInt64: checked above
			}
			return 0, fmt.Errorf("array element %v is not an integer", v)
		})
	case core.FieldTypeFloat:
		return typedSlice(items, func(v any) (float64, error) { return core.NormalizeFloat(v, 0) })
	case core.FieldTypeBool:
		return typedSlice(items, func(v any) (bool, error) {
			b, ok := v.(bool)
			if !ok {
				return false, fmt.Errorf("array element %v is not a boolean", v)
			}
			return b, nil
		})
	}
	return nil, fmt.Errorf("arrays of %s have no PostgreSQL array type", elem)
}

// elementType is the one scalar type every normalized element of value has,
// or "" when they differ or value is not an array.
func elementType(value any) core.FieldType {
	items, ok := core.AnySlice(value)
	if !ok {
		return ""
	}
	var elem core.FieldType
	for _, v := range items {
		var t core.FieldType
		switch v.(type) {
		case string:
			t = core.FieldTypeString
		case int64, uint64:
			t = core.FieldTypeInt
		case float64:
			t = core.FieldTypeFloat
		case bool:
			t = core.FieldTypeBool
		default:
			return ""
		}
		if elem != "" && t != elem {
			return ""
		}
		elem = t
	}
	return elem
}

func typedSlice[T any](items []any, conv func(any) (T, error)) ([]T, error) {
	out := make([]T, len(items))
	for i, v := range items {
		t, err := conv(v)
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}

// geoCondition renders PostGIS predicates. Filter coordinates are WGS 84:
// `$near` measures on the geography type (meters); `$within` (boundary
// inclusive) and `$intersects` compare planar geometry in the column's
// SRID. SQLiteDialect and Evaluate decide JSONB points alike through
// internal/geo.
func (d PostgresDialect) geoCondition(field *FieldSchema, op FilterOperator, value any, bind func(any) string) (string, error) {
	if err := validateGeoValue(value); err != nil {
		return "", err
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
		center := "ST_SetSRID(ST_MakePoint(" + bind(v.Lon) + ", " + bind(v.Lat) + "), 4326)::geography"
		return "ST_DWithin(" + pgGeography(d, field) + ", " + center + ", " + bind(meters) + ")", nil
	case *GeoBoundingBox:
		envelope := "ST_MakeEnvelope(" + bind(v.TopLeft.Lon) + ", " + bind(v.BottomRight.Lat) + ", " +
			bind(v.BottomRight.Lon) + ", " + bind(v.TopLeft.Lat) + ", 4326)"
		return pgShapePredicate(d, field, op, envelope)
	case *GeoPolygon:
		return pgShapePredicate(d, field, op, "ST_GeomFromText("+bind(polygonWKT(v))+", 4326)")
	}
	return "", fmt.Errorf("geo operator %s: unexpected value %s", op, gowalker.TypeText(reflect.TypeOf(value)))
}

func pgShapePredicate(d dialect.Dialect, field *FieldSchema, op FilterOperator, shape string) (string, error) {
	geom, srid := pgGeometry(d, field)
	if srid != wgs84 {
		shape = "ST_Transform(" + shape + ", " + strconv.Itoa(srid) + ")"
	}
	switch op {
	case OpWithin:
		return "ST_CoveredBy(" + geom + ", " + shape + ")", nil
	case OpIntersects:
		return "ST_Intersects(" + geom + ", " + shape + ")", nil
	}
	return "", fmt.Errorf("geo operator %s does not take a shape", op)
}

// pgGeometry returns the field as geometry and the SRID it is in.
func pgGeometry(d dialect.Dialect, field *FieldSchema) (string, int) {
	cfg := field.GeoConfig
	switch {
	case cfg == nil || cfg.StorageType == GeoStorageJSONB:
		return pgJSONBPoint(d, field), wgs84
	case cfg.StorageType == GeoStorageGeography:
		return field.sql(d) + "::geometry", wgs84
	}
	return field.sql(d), cfg.SRID
}

// pgGeography returns the field as WGS 84 geography.
func pgGeography(d dialect.Dialect, field *FieldSchema) string {
	cfg := field.GeoConfig
	switch {
	case cfg == nil || cfg.StorageType == GeoStorageJSONB:
		return pgJSONBPoint(d, field) + "::geography"
	case cfg.StorageType == GeoStorageGeography:
		return field.sql(d)
	case cfg.SRID != wgs84:
		return "ST_Transform(" + field.sql(d) + ", 4326)::geography"
	}
	return field.sql(d) + "::geography"
}

// pgJSONBPoint builds the point geometry of a JSONB {lat, lon} column. The
// keys were validated as identifiers at schema generation.
func pgJSONBPoint(d dialect.Dialect, field *FieldSchema) string {
	latKey, lonKey := geoKeys(field)
	return fmt.Sprintf("ST_SetSRID(ST_MakePoint((%s->>'%s')::float8, (%s->>'%s')::float8), 4326)",
		field.sql(d), lonKey, field.sql(d), latKey)
}

// MarshalValue serializes a value for PostgreSQL parameter binding.
//
// pgx handles per-column-type encoding from Go values: Go slices like
// []string / []int become native PG arrays (text[], int[], uuid[]),
// while maps and unknown structs become JSONB via pgx's default codec.
// Pre-marshaling to JSON text breaks native array columns, which reject
// the JSON literal `"[]"` (PG expects `'{}'`).
//
// Map/object payloads still need an up-front JSON encode because pgx
// can't decide between a hstore and a JSONB column for an arbitrary
// map[string]any without an OID hint, and the project standardizes on
// JSONB for those.
func (d PostgresDialect) MarshalValue(value any, t core.TypeInfo) (any, error) {
	if t.Type == core.FieldTypeDate {
		normalized, err := nullableDateValue(value)
		if err != nil || normalized == nil {
			return normalized, err
		}
		return time.Parse(time.DateOnly, normalized.(string))
	}

	if _, ok := value.(driver.Valuer); ok && !t.SQLType.IsJSON() {
		return value, nil // a database value the driver encodes, not a document
	}
	switch t.Type {
	case core.FieldTypeJSON, core.FieldTypeObject:
		b, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case core.FieldTypeArray:
		// A typed slice: pgx encodes it in every query mode, including
		// QueryExecModeExec and the simple protocol, where no statement
		// description tells it the column's type and []any cannot be encoded.
		elem := t.Elem
		if elem == "" {
			elem = elementType(value) // a hand-built schema naming no element type
		}
		if elem == "" {
			return value, nil
		}
		return pgArray(elem, value)
	default:
		return value, nil
	}
}

// CTEMaterializeHint returns " MATERIALIZED" so PG ≥ 12 doesn't inline a
// multi-referenced CTE. Critical for BuildFacetQuery so the policy-merged
// filter scan happens exactly once for the whole facet response.
func (PostgresDialect) CTEMaterializeHint() string { return " MATERIALIZED" }

// RowLock locks selected rows until the transaction ends.
func (PostgresDialect) RowLock() string { return " FOR UPDATE" }

// ReadColumn reads network and interval values as text, since pgx scans
// their binary encoding into no string: an inet in its display form (abbrev
// drops a host's /32 or /128), a cidr in its output form, an interval as
// its canonical ISO 8601 text (isoInterval), and an array element by
// element, in order, with NULL kept.
func (PostgresDialect) ReadColumn(column string, t core.SQLType) string {
	switch t {
	case core.SQLInet:
		return "abbrev(" + column + ")"
	case core.SQLCIDR:
		return column + "::text"
	case core.SQLInterval:
		return isoInterval(column)
	case core.SQLInetArray:
		return pgArrayOf(column, "abbrev(a)")
	case core.SQLCIDRArray:
		return column + "::text[]"
	case core.SQLIntervalArray:
		return pgArrayOf(column, isoInterval("a"))
	}
	return column
}

// pgArrayOf maps each element a of an array column through expr, in order,
// NULL elements and a NULL array kept.
func pgArrayOf(column, expr string) string {
	return "CASE WHEN " + column + " IS NULL THEN NULL ELSE ARRAY(SELECT " + expr + " FROM unnest(" + column +
		") WITH ORDINALITY AS u(a, n) ORDER BY n) END"
}

// isoInterval renders an interval as the text IntervalStyle iso_8601 writes,
// which is core.Interval's canonical text, under whatever style the session
// has: a cast to text would follow the session. Each field is extracted and
// written with its own sign; trim_scale drops the seconds' trailing zeros.
// An infinite interval reads as "infinity" or "-infinity", which
// core.Interval refuses.
func isoInterval(column string) string {
	return "(SELECT CASE WHEN " + column + " IS NULL THEN NULL WHEN NOT isfinite(" + column + ") THEN " + column + "::text" +
		" WHEN iso_y = 0 AND iso_mo = 0 AND iso_d = 0 AND iso_h = 0 AND iso_mi = 0 AND iso_s = 0 THEN 'PT0S'" +
		" ELSE 'P' || CASE WHEN iso_y <> 0 THEN iso_y || 'Y' ELSE '' END || CASE WHEN iso_mo <> 0 THEN iso_mo || 'M' ELSE '' END" +
		" || CASE WHEN iso_d <> 0 THEN iso_d || 'D' ELSE '' END || CASE WHEN iso_h <> 0 OR iso_mi <> 0 OR iso_s <> 0 THEN 'T' ELSE '' END" +
		" || CASE WHEN iso_h <> 0 THEN iso_h || 'H' ELSE '' END || CASE WHEN iso_mi <> 0 THEN iso_mi || 'M' ELSE '' END" +
		" || CASE WHEN iso_s <> 0 THEN trim_scale(iso_s) || 'S' ELSE '' END END" +
		" FROM (SELECT extract(year FROM " + column + ") AS iso_y, extract(month FROM " + column + ") AS iso_mo, extract(day FROM " + column + ") AS iso_d," +
		" extract(hour FROM " + column + ") AS iso_h, extract(minute FROM " + column + ") AS iso_mi, extract(second FROM " + column + ") AS iso_s) AS iso_fields)"
}

// DecimalValue reads a decimal column as it is (numeric is exact), a member
// as PathExpr's numeric, and a parameter cast to numeric: pgx sends a string
// in text format, which numeric parses exactly.
func (d PostgresDialect) DecimalValue(o DecimalOperand) string {
	switch o.source {
	case decimalFromColumn:
		return o.sql
	case decimalFromMember:
		return d.PathExpr(o.path, core.FieldTypeDecimal)
	case decimalFromParam:
		return o.sql + "::numeric"
	}
	panic(o.invalid())
}

// DecimalOrder is DecimalValue: numeric orders exactly.
func (d PostgresDialect) DecimalOrder(o DecimalOperand) string { return d.DecimalValue(o) }

// NativeRowCompare reports true: PostgreSQL evaluates row comparison
// lexicographically with full composite-index support.
func (PostgresDialect) NativeRowCompare() bool { return true }

// CodePointOrder applies the dialect's Collation. A column declared with the
// same collation is indexed
// in the order every keyset sort and string range filter compiles to.
func (d PostgresDialect) CodePointOrder(expr string) string {
	return expr + " COLLATE " + d.Collation.String()
}

// pgDocument is a document column read as jsonb, whatever its storage.
func pgDocument(d dialect.Dialect, field *FieldSchema) string {
	if field.Storage == "" || field.Storage == StorageJSONB {
		return field.sql(d)
	}
	return "(" + field.sql(d) + ")::jsonb"
}

// elementRows reads a JSON array as jsonb_array_elements, an array path's
// member when it is one, and an array column with unnest.
func (d PostgresDialect) elementRows(f *FieldSchema, alias string) string {
	var array string
	switch {
	case f.Path != nil:
		array = pgMember(pgPathDocument(d, f.Path), f.Path.Segments)
	case f.DataType == core.FieldTypeArray:
		return "unnest(" + f.sql(d) + ") AS " + alias + "(v)"
	default:
		array = pgDocument(d, f)
	}
	return "jsonb_array_elements(CASE WHEN jsonb_typeof(" + array + ") = 'array' THEN " + array + " END) AS " + alias + "(v)"
}

func (PostgresDialect) objectElement(alias string) string { return alias + ".v" }

// scalarElement reads a native array column's element as it is, and a JSON
// array's as an element path (PathExpr).
func (PostgresDialect) scalarElement(f *FieldSchema, alias string) *FieldSchema {
	e := bound(elementOf(f))
	if f.Path == nil {
		e.Table, e.Column, e.Expr = "", "", alias+".v"
		return &e
	}
	e.Table, e.Column, e.Expr = "", "", ""
	e.Path = &JSONPath{alias: alias, Storage: StorageJSONB, element: true}
	return &e
}

// containsElement renders `$some` of a string, boolean or number element of
// a jsonb array path equal to a value (`$eq`) as `doc @> fragment`, and to
// one of at most ContainmentMaxValues values (`$in`) as `doc @> ANY(...)`,
// each fragment the path's objects down to a one-element array. Containment
// holds exactly when the member is an array with an equal element; the
// document's NULL test keeps a NULL document False rather than Unknown, so
// `$some` stays two-valued under any negation. Decimals keep the element
// table (a decimal element may be held as a string).
func (d PostgresDialect) containsElement(f *FieldSchema, cond *FilterNode, bind func(any) string) (string, bool, error) {
	p := f.Path
	if p == nil || p.Storage != StorageJSONB || !containable(f.Elem) ||
		cond == nil || cond.Field == nil || !isElement(cond.Field) {
		return "", false, nil
	}
	fragment := func(v any) (string, error) { return containmentFragment(p.Segments, []any{v}) }
	switch cond.Op {
	case OpEq:
		if cond.Value == nil {
			return "", false, nil // `= NULL` holds for no element; [null] would contain it
		}
		frag, err := fragment(cond.Value)
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", f.ExternalName, err)
		}
		return "(" + p.document(d) + " IS NOT NULL AND " + p.document(d) + " @> " + bind(frag) + "::jsonb)", true, nil
	case OpIn:
		members, ok := core.AnySlice(cond.Value)
		if !ok || len(members) == 0 || len(members) > d.containmentMaxValues() || slices.Contains(members, nil) {
			return "", false, nil
		}
		frags, err := typedSlice(members, fragment)
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", f.ExternalName, err)
		}
		return "(" + p.document(d) + " IS NOT NULL AND " + p.document(d) + " @> ANY(" + bind(frags) + "::jsonb[]))", true, nil
	}
	return "", false, nil
}

// timestampKey renders a TIMESTAMPTZ as its UTC wall time, which no session
// DateStyle or TimeZone changes, at microseconds, PostgreSQL's precision,
// padded to the canonical nine digits. An infinite timestamp has no key.
func (PostgresDialect) timestampKey(expr string) string {
	return "to_char((" + expr + ") AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"000Z\"')"
}

// numberKey reads a path member and a decimal as the numeric FieldExpr
// renders, exactly; trim_scale drops the scale a literal was written with
// (7.0, 1.50). A float column's key is its shortest text at the field's
// precision, which float8 and float4 output give while extra_float_digits
// is positive (PostgreSQL's default), read as numeric for plain notation.
// A non-finite float has none.
func (d PostgresDialect) numberKey(f *FieldSchema) string {
	expr := FieldExpr(d, f)
	switch {
	case f.Path != nil || f.DataType == core.FieldTypeDecimal:
		return "trim_scale(" + expr + ")::text"
	case f.DataType == core.FieldTypeInt:
		return "(" + expr + ")::text"
	}
	text := "(" + expr + ")::float8::text"
	if f.NumberKind == reflect.Float32 {
		text = "(" + expr + ")::float4::text"
	}
	return "(CASE WHEN " + text + " IN ('NaN', 'Infinity', '-Infinity') THEN NULL ELSE " + text + "::numeric::text END)"
}

// PathExpr renders p's typed value for PostgreSQL. The document is read as
// jsonb and the member one object key at a time (pgMember); jsonb_typeof
// selects the member's JSON kind before any cast, so a wrong-typed member is
// NULL rather than a cast error. An element path
// reads its row's element, jsonb_array_elements' v.
func (d PostgresDialect) PathExpr(p *JSONPath, t core.FieldType) string {
	if p.element {
		v := p.document(d) + ".v"
		return pgTypedMember(t, "jsonb_typeof("+v+")", "("+v+" #>> '{}')")
	}
	doc := pgPathDocument(d, p)
	return pgTypedMember(t, "jsonb_typeof("+pgMember(doc, p.Segments)+")", "("+pgMemberText(doc, p.Segments)+")")
}

func (PostgresDialect) textKey(expr string) string { return expr + "::text" }

// search matches the fields' text search vector against the query's.
func (d PostgresDialect) search(schema *SearchSchema, query string, startIdx int) (*searchCondition, error) {
	return buildPostgresSearch(schema, d, query, startIdx)
}

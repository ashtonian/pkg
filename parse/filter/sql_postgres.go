package filter

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
	"uuid"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// PostgresDialect generates PostgreSQL-compatible SQL. JSON documents are
// jsonb columns and arrays native array columns (core.TypeOf); geo
// operators use PostGIS. Collation is the code-point collation string sorts,
// keyset seeks and string ranges compile under; the zero value is
// pg_c_utf8 (PostgreSQL 17+).
type PostgresDialect struct {
	dialect.Postgres
	Collation PostgresCollation
}

// PostgresCollation is a PostgreSQL collation that orders strings by code
// point — the order SQLite's BINARY, the memory backend and cursor values
// use — so the values are closed: CollationPgCUTF8 and CollationC.
type PostgresCollation struct{ name string }

var (
	// CollationPgCUTF8 is pg_c_utf8 (PostgreSQL 17+), the zero value:
	// code-point order with Unicode case mapping, so ILIKE on a column
	// declared with it still folds non-ASCII letters.
	CollationPgCUTF8 = PostgresCollation{}
	// CollationC is "C", for PostgreSQL 14–16: the same order (UTF-8 bytes
	// sort as their code points), but a column declared with it folds only
	// ASCII letters under ILIKE.
	CollationC = PostgresCollation{name: `"C"`}
)

// String is the collation's SQL name.
func (c PostgresCollation) String() string {
	if c.name == "" {
		return "pg_c_utf8"
	}
	return c.name
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
			return pgDocument(field) + " ? " + bind(key), nil
		}
		doc, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("marshal %s document: %w", field.ExternalName, err)
		}
		col := pgDocument(field)
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
		arr, err := pgArray(field.Elem, value)
		if err != nil {
			return "", fmt.Errorf("field %s: %w", field.ExternalName, err)
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
	if isOrdering(op) && orderedAsText(field) {
		expr = d.CodePointOrder(expr)
	}
	if sql, ok, err := comparison(expr, op, value, bind); ok {
		return sql, err
	}
	return "", unsupportedOperator(field, op)
}

// pgArray converts normalized array elements into the typed slice pgx
// encodes as the column's array type. UUIDs bind as their canonical text:
// pgx encodes a []string into uuid[] in every query mode, while a slice of a
// UUID type needs a type registration the caller's connection may not have.
func pgArray(elem core.FieldType, value any) (any, error) {
	items, ok := core.AnySlice(value)
	if !ok {
		return nil, fmt.Errorf("an array column requires an array value, got %T", value)
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
	case core.FieldTypeInt:
		return typedSlice(items, func(v any) (int64, error) {
			switch n := v.(type) {
			case int64:
				return n, nil
			case uint64:
				if n <= math.MaxInt64 {
					return int64(n), nil
				}
			}
			return 0, fmt.Errorf("array element %v does not fit a PostgreSQL integer", v)
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
// inclusive, like SQLite's BETWEEN) and `$intersects` compare planar
// geometry in the column's SRID.
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
		return "ST_DWithin(" + pgGeography(field) + ", " + center + ", " + bind(meters) + ")", nil
	case *GeoBoundingBox:
		envelope := "ST_MakeEnvelope(" + bind(v.TopLeft.Lon) + ", " + bind(v.BottomRight.Lat) + ", " +
			bind(v.BottomRight.Lon) + ", " + bind(v.TopLeft.Lat) + ", 4326)"
		return pgShapePredicate(field, op, envelope)
	case *GeoPolygon:
		return pgShapePredicate(field, op, "ST_GeomFromText("+bind(polygonWKT(v))+", 4326)")
	}
	return "", fmt.Errorf("geo operator %s: unexpected value %T", op, value)
}

func pgShapePredicate(field *FieldSchema, op FilterOperator, shape string) (string, error) {
	geom, srid := pgGeometry(field)
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
func pgGeometry(field *FieldSchema) (string, int) {
	cfg := field.GeoConfig
	switch {
	case cfg == nil || cfg.StorageType == GeoStorageJSONB:
		return pgJSONBPoint(field), wgs84
	case cfg.StorageType == GeoStorageGeography:
		return field.SQLExpr + "::geometry", wgs84
	}
	return field.SQLExpr, cfg.SRID
}

// pgGeography returns the field as WGS 84 geography.
func pgGeography(field *FieldSchema) string {
	cfg := field.GeoConfig
	switch {
	case cfg == nil || cfg.StorageType == GeoStorageJSONB:
		return pgJSONBPoint(field) + "::geography"
	case cfg.StorageType == GeoStorageGeography:
		return field.SQLExpr
	case cfg.SRID != wgs84:
		return "ST_Transform(" + field.SQLExpr + ", 4326)::geography"
	}
	return field.SQLExpr + "::geography"
}

// pgJSONBPoint builds the point geometry of a JSONB {lat, lon} column. The
// keys were validated as identifiers at schema generation.
func pgJSONBPoint(field *FieldSchema) string {
	latKey, lonKey := "lat", "lon"
	if field.GeoConfig != nil {
		latKey, lonKey = field.GeoConfig.LatKey, field.GeoConfig.LonKey
	}
	return fmt.Sprintf("ST_SetSRID(ST_MakePoint((%s->>'%s')::float8, (%s->>'%s')::float8), 4326)",
		field.SQLExpr, lonKey, field.SQLExpr, latKey)
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
// drops a host's /32 or /128), cidr and interval in their output form, and
// an array element by element, in order, with NULL kept.
func (PostgresDialect) ReadColumn(column string, t core.SQLType) string {
	switch t {
	case core.SQLInet:
		return "abbrev(" + column + ")"
	case core.SQLCIDR, core.SQLInterval:
		return column + "::text"
	case core.SQLInetArray:
		return "CASE WHEN " + column + " IS NULL THEN NULL ELSE ARRAY(SELECT abbrev(a) FROM unnest(" + column +
			") WITH ORDINALITY AS u(a, n) ORDER BY n) END"
	case core.SQLCIDRArray, core.SQLIntervalArray:
		return column + "::text[]"
	}
	return column
}

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
func pgDocument(field *FieldSchema) string {
	if field.Storage == "" || field.Storage == StorageJSONB {
		return field.SQLExpr
	}
	return "(" + field.SQLExpr + ")::jsonb"
}

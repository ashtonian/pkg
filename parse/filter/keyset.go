package filter

import (
	"encoding"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/cursor"
)

// Keyset pagination.
//
// Ordering model: NULL sorts greatest — after every value ascending, before
// every value descending. Nullable keys emit explicit NULLS LAST / NULLS
// FIRST so PostgreSQL and SQLite (whose defaults disagree) order alike, and
// the seek predicate is NULL-aware. A backward seek (previous page) inverts
// the whole ORDER BY and the predicate; NewPage restores presentation order.

// CursorKey is one component of the effective keyset: the API-facing name
// carried in tokens, the SQL expression emitted into ORDER BY and seek
// predicates, the row column a token value is read from, and how the key
// orders.
//
// A key reads Column of Table, quoted by the dialect, unless Expr is set, as
// a FieldSchema does; Column is also the result column a cursor reads the
// key's value from, so a computed key (Expr) names its result column there.
type CursorKey struct {
	Name     string
	Table    string
	Column   string
	Expr     string
	DataType core.FieldType
	// NumberKind retains the schema numeric range (Invalid uses int64/float64).
	NumberKind reflect.Kind
	Direction  SortDirection
	Nullable   bool
	// Path is the document member the key orders by (SortFieldSchema.Path).
	Path *JSONPath
}

// Keyset derives the effective keys for a sort: its fields followed by each
// tie-breaker the sort does not already name, in the order given — a
// composite key's columns, in key order. A tie-breaker without a name is
// none. The tie-breakers inherit the primary direction so a
// uniform-direction list keeps native row-value comparison available. SQL
// repositories get this from ListQuery.Build; the memory repository calls it
// directly so every backend walks one key list.
func Keyset(sort *SortResult, tieBreakers ...CursorKey) []CursorKey {
	var keys []CursorKey
	if sort != nil {
		keys = make([]CursorKey, 0, len(sort.Fields)+len(tieBreakers))
		for _, f := range sort.Fields {
			keys = append(keys, CursorKey{
				Name:       f.Name,
				Table:      f.Schema.Table,
				Column:     f.Schema.RowColumn(),
				Expr:       f.Schema.Expr,
				DataType:   f.Schema.DataType,
				NumberKind: f.Schema.NumberKind,
				Direction:  f.Direction,
				Nullable:   f.Schema.Nullable || f.Schema.Path != nil,
				Path:       f.Schema.Path,
			})
		}
	}
	direction := SortAsc
	if len(keys) > 0 {
		direction = keys[0].Direction
	}
	for _, tieBreaker := range tieBreakers {
		named := slices.ContainsFunc(keys, func(k CursorKey) bool { return k.Name == tieBreaker.Name })
		if tieBreaker.Name == "" || named {
			continue
		}
		tieBreaker.Direction = direction
		keys = append(keys, tieBreaker)
	}
	return keys
}

// CursorPlan is what a repository needs to turn a fetched row set into a
// page: the effective keyset in presentation order, whether the query
// walked backwards, and whether the request carried a cursor.
type CursorPlan struct {
	Keys      []CursorKey
	Reverse   bool
	HasCursor bool
}

// Token mints the PageToken positioned at a row. get resolves a key's value
// from the row by row column. reverse marks a token that seeks backwards
// (a page's start cursor).
func (p CursorPlan) Token(reverse bool, get func(column string) (any, error)) (*PageToken, error) {
	if len(p.Keys) == 0 {
		return nil, errors.New("cursor: no sort fields and no tie-breaker")
	}
	fields := make([]CursorField, len(p.Keys))
	for i, k := range p.Keys {
		if k.Column == "" {
			return nil, fmt.Errorf("cursor key %q has no row column", k.Name)
		}
		raw, err := get(k.Column)
		if err != nil {
			return nil, fmt.Errorf("cursor key %q: %w", k.Name, err)
		}
		v, err := KeyValue(k, raw)
		if err != nil {
			return nil, fmt.Errorf("cursor key %q: %w", k.Name, err)
		}
		fields[i] = CursorField{Name: k.Name, Value: v, Direction: k.Direction}
	}
	return &PageToken{Fields: fields, Reverse: reverse}, nil
}

// KeyValue is a row's canonical value for key k, given raw, the row's value
// at k.Column, or for a path key the member the path rule extracts from the
// column's document, read as PathValue reads it (nil when absent). A key of
// a scalar type (core.IsKeyType) reads it as its type's stored value
// (core.StoredScalar), whatever its column scans to — a date key's
// time.Time as its day, a decimal key's text, INTEGER or REAL as its
// canonical text — so ValidateCursor accepts the value it mints; a key of
// no scalar type (a codec's) reads it through CursorValue. An int or float
// path's member is its canonical decimal text, exactly as its JSON number
// holds it, since PostgreSQL orders the member as numeric. Cursors and a
// memory backend's ordering read keys through it alike.
func KeyValue(k CursorKey, raw any) (any, error) {
	if k.Path != nil {
		root, err := decodeDocument(raw)
		if err != nil {
			return nil, err
		}
		if k.numberPath() {
			return pathNumberText(root, k.Path.Segments)
		}
		member, present, err := decodedPathValue(root, k.Path.Segments, k.DataType, "")
		if err != nil || !present {
			return nil, err
		}
		raw = member
	}
	if !core.IsKeyType(k.DataType) {
		return CursorValue(raw)
	}
	v, _, err := core.StoredScalar(k.DataType, k.NumberKind, raw)
	if err != nil {
		return nil, fmt.Errorf("cursor: %s key: %w", k.DataType, err)
	}
	return v, nil
}

// CursorValue reduces a value of a key of no scalar type (KeyValue) to the
// canonical form a token carries and SQL binds: nil, string, bool, int64,
// uint64 or float64, NaN and the infinities included, which a float column
// may hold. The value is read
// as the database holds it first (core.StoredValue: pointers followed, a
// driver.Valuer read, NULL as nil); then decimals (core.IsDecimalType)
// become their canonical text, times RFC 3339 UTC strings (the form SQLite
// stores), and text-marshalling types such as uuid.UUID their text.
// Anything else cannot resume a page and is an error, never an omitted
// cursor.
func CursorValue(v any) (any, error) {
	v, null, err := core.StoredValue(v)
	if err != nil {
		return nil, fmt.Errorf("cursor: %w", err)
	}
	if null {
		return nil, nil
	}
	switch t := v.(type) {
	case string, bool, int64:
		return t, nil
	case time.Time:
		return core.FormatTimestamp(t), nil
	case []byte:
		return nil, errors.New("cursor: byte-slice keys are not supported")
	case core.Decimal:
		return cursorDecimalValue(t)
	case encoding.TextMarshaler:
		return cursorTextValue(t)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return u, nil
		}
		return int64(u), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Struct:
		if core.IsDecimalType(rv.Type()) {
			return cursorDecimalValue(v) // its pointer has the method set
		}
		// Sort schema generation also accepts pointer-receiver scalar
		// codecs. A value extracted from a row need not be addressable;
		// give the codec a copy instead of losing its pointer methods.
		if reflect.PointerTo(rv.Type()).Implements(textMarshalerType) {
			pv := reflect.New(rv.Type())
			pv.Elem().Set(rv)
			if p, ok := pv.Interface().(encoding.TextMarshaler); ok {
				return cursorTextValue(p)
			}
		}
	}
	return nil, fmt.Errorf("cursor: unsupported key type %s", gowalker.TypeText(reflect.TypeOf(v)))
}

func cursorDecimalValue(v any) (any, error) {
	text, err := core.DecimalText(v)
	if err != nil {
		return nil, fmt.Errorf("cursor: %s: %w", gowalker.TypeText(reflect.TypeOf(v)), err)
	}
	return text, nil
}

func cursorTextValue(v encoding.TextMarshaler) (any, error) {
	b, err := v.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("cursor: marshalling %s: %w", gowalker.TypeText(reflect.TypeOf(v)), err)
	}
	return string(b), nil
}

// CompareKeyValues orders two key values (KeyValue) of key k in the
// keyset's order, its type's one order (core.CompareCanonical): NULL
// greatest, numbers and decimals exactly, timestamps by instant, strings
// by code point, false before true. Direction is not applied. Two values
// k's type cannot compare are an error, never ordered by their text.
func CompareKeyValues(k CursorKey, a, b any) (int, error) {
	c, err := core.CompareCanonical(k.valueType(), a, b)
	if err != nil {
		return 0, fmt.Errorf("comparing values of key %q: %w", k.Name, err)
	}
	return c, nil
}

// numberPath reports an int or float path key, whose values are its
// members' canonical decimal text (KeyValue).
func (k CursorKey) numberPath() bool {
	return k.Path != nil && (k.DataType == core.FieldTypeInt || k.DataType == core.FieldTypeFloat)
}

// valueType is the type k's values (KeyValue) are of: a decimal for a
// number path, otherwise k's own.
func (k CursorKey) valueType() core.TypeInfo {
	if k.numberPath() {
		return core.TypeInfo{Type: core.FieldTypeDecimal}
	}
	return core.TypeInfo{Type: k.DataType, NumberKind: k.NumberKind}
}

// effectiveDirection is the direction a key is walked in: flipped for a
// backward seek.
func effectiveDirection(k CursorKey, reverse bool) SortDirection {
	if reverse {
		return ReverseDirection(k.Direction)
	}
	return k.Direction
}

// keyValue is a key's value expression: its column, or a path's typed
// extraction.
func keyValue(d Dialect, k CursorKey) string {
	if k.Path != nil {
		return d.PathExpr(k.Path, k.DataType)
	}
	switch k.DataType {
	case core.FieldTypeDate:
		return d.ReadColumn(k.sql(d), core.SQLDate)
	case core.FieldTypeDecimal:
		return d.DecimalValue(keyDecimal(d, k))
	}
	return k.sql(d)
}

// sql is the SQL that reads the key's column (columnSQL).
func (k CursorKey) sql(d Dialect) string { return columnSQL(d, k.Table, k.Column, k.Expr) }

// keyDecimal is the operand a decimal key's value is read from.
func keyDecimal(d Dialect, k CursorKey) DecimalOperand {
	if k.Path != nil {
		return DecimalMember(k.Path)
	}
	return DecimalColumn(k.sql(d))
}

// keyExpr is a key's ordering expression: text keys — strings, and a path's
// timestamp and UUID members — order by code point, as filters and the
// memory backend compare; decimals order through Dialect.DecimalOrder.
func keyExpr(d Dialect, k CursorKey) string {
	switch {
	case k.DataType == core.FieldTypeString || (k.Path != nil && (k.DataType == core.FieldTypeTimestamp || k.DataType == core.FieldTypeUUID)):
		return d.CodePointOrder(keyValue(d, k))
	case k.DataType == core.FieldTypeDecimal:
		return d.DecimalOrder(keyDecimal(d, k))
	}
	return keyValue(d, k)
}

// OrderByKeys renders the ORDER BY clause that walks keys forwards, or
// backwards when reverse is set, in the keyset's order: NULL greatest on
// nullable keys, text by code point, decimals through
// Dialect.DecimalOrder. It is "" for no keys. ListQuery orders its pages
// with it; a statement ListQuery does not build, such as one that also
// orders by a computed key (a CursorKey whose Expr is the expression),
// pages the same way with OrderByKeys and SeekPredicate.
func OrderByKeys(d Dialect, keys []CursorKey, reverse bool) string {
	if len(keys) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("ORDER BY ")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(keyExpr(d, k))
		if effectiveDirection(k, reverse) == SortDesc {
			sb.WriteString(" DESC")
			if k.Nullable {
				sb.WriteString(" NULLS FIRST")
			}
		} else {
			sb.WriteString(" ASC")
			if k.Nullable {
				sb.WriteString(" NULLS LAST")
			}
		}
	}
	return sb.String()
}

// SeekPredicate renders the predicate holding the rows strictly after
// token in the walk order of keys (token.Reverse walks backwards), for a
// statement ordered by OrderByKeys(d, keys, token.Reverse). It checks token
// against keys first (ValidateCursor), normalizing its values, and refuses
// a token minted under another keyset with E2082 (ECodeInvalidCursor).
// bind binds one value and returns its placeholder; every non-null value is
// bound, never spliced into the SQL.
func SeekPredicate(d Dialect, keys []CursorKey, token *PageToken, bind func(any) string) (string, error) {
	if err := ValidateCursor(token, keys); err != nil {
		return "", err
	}
	return seekPredicate(d, keys, token, bind), nil
}

// seekPredicate renders "rows strictly after the token" in the walk order:
// the lexicographic expansion OR over depth i of (key_0 = v_0 AND … AND
// key_i after v_i), NULL-aware. bind binds one value and returns its
// placeholder; every non-null token value is bound once and its placeholder
// reused across depths. A native row-value comparison is used when the
// dialect supports it and no key or value involves NULL. A boundary the
// database reads as NULL, a float's NaN or a decimal's NaN or infinity on
// SQLite, is a NULL boundary (readsAsNull); a number path's decimal text
// is read as the dialect compares the member (pathNumberBound).
func seekPredicate(d Dialect, keys []CursorKey, token *PageToken, bind func(any) string) string {
	values := make([]any, len(keys))
	phs := make([]string, len(keys))
	native := d.NativeRowCompare() && len(keys) > 1
	for i, k := range keys {
		v := token.Fields[i].Value
		if d.readsAsNull(k.DataType, v) {
			v = nil
		}
		values[i] = v
		if v != nil {
			phs[i] = bind(v)
			switch {
			case k.DataType == core.FieldTypeDecimal:
				phs[i] = d.DecimalOrder(DecimalParam(phs[i]))
			case k.numberPath():
				phs[i] = d.pathNumberBound(phs[i])
			}
		}
		if k.Nullable || v == nil || effectiveDirection(k, token.Reverse) != effectiveDirection(keys[0], token.Reverse) {
			native = false
		}
	}
	if native {
		exprs := make([]string, len(keys))
		for i, k := range keys {
			exprs[i] = keyExpr(d, k)
		}
		op := ">"
		if effectiveDirection(keys[0], token.Reverse) == SortDesc {
			op = "<"
		}
		return "(" + strings.Join(exprs, ", ") + ") " + op + " (" + strings.Join(phs, ", ") + ")"
	}

	var branches []string
	for i, k := range keys {
		after := afterExpr(keyExpr(d, k), k.Nullable, values[i], phs[i], effectiveDirection(k, token.Reverse))
		if after == "" {
			continue // nothing orders after a NULL boundary on this key
		}
		parts := make([]string, 0, i+1)
		for j := range i {
			if values[j] == nil {
				parts = append(parts, keyValue(d, keys[j])+" IS NULL")
			} else {
				parts = append(parts, keyExpr(d, keys[j])+" = "+phs[j])
			}
		}
		parts = append(parts, after)
		branches = append(branches, "("+strings.Join(parts, " AND ")+")")
	}
	if len(branches) == 0 {
		return "1=0"
	}
	return "(" + strings.Join(branches, " OR ") + ")"
}

// readsAsNull reports false: float8 and numeric hold NaN and the
// infinities, NaN after +Infinity and equal to NaN.
func (PostgresDialect) readsAsNull(core.FieldType, any) bool { return false }

// readsAsNull reports a float's NaN, which SQLite stores as NULL, and a
// decimal key's NaN or infinity: a decimal column holds a number literal,
// and parse_decimal reads any other text as NULL.
func (SQLiteDialect) readsAsNull(t core.FieldType, v any) bool {
	f, isFloat := v.(float64)
	return (isFloat && math.IsNaN(f)) || (t == core.FieldTypeDecimal && core.NonFinite(t, v))
}

// pathNumberBound reads the bound through DecimalOrder: a number member
// orders as numeric, exactly.
func (d PostgresDialect) pathNumberBound(ph string) string { return d.DecimalOrder(DecimalParam(ph)) }

// pathNumberBound casts the bound AS NUMERIC: an INTEGER where its text is
// one within 64 bits, otherwise the REAL nearest it, as json_extract reads
// a number member.
func (SQLiteDialect) pathNumberBound(ph string) string { return "CAST(" + ph + " AS NUMERIC)" }

// afterExpr is "expr orders strictly after v" in direction dir, with NULL
// greatest; "" means no value orders after v.
func afterExpr(expr string, nullable bool, v any, ph string, dir SortDirection) string {
	if dir == SortAsc {
		if v == nil {
			return ""
		}
		if nullable {
			return "(" + expr + " > " + ph + " OR " + expr + " IS NULL)"
		}
		return expr + " > " + ph
	}
	if v == nil {
		return expr + " IS NOT NULL"
	}
	return expr + " < " + ph
}

// ValidateCursor rejects a token whose field list disagrees with the active
// keyset: a cursor minted under one sort must not be replayed under
// another. Values must match each key's type, numeric range and
// nullability. On success values are normalized in place; on failure the
// token is unchanged. Keys without scalar schema metadata (including custom
// scalar marshalers) are checked only for a canonical scalar and nullability.
func ValidateCursor(token *PageToken, keys []CursorKey) error {
	mismatch := token == nil || len(token.Fields) != len(keys) || len(keys) == 0
	if !mismatch {
		for i, f := range token.Fields {
			if f.Name != keys[i].Name || f.Direction != keys[i].Direction {
				mismatch = true
				break
			}
		}
	}
	if mismatch {
		return &core.ParseError{
			Kind:    core.ParseErrorKindSchema,
			Code:    ECodeInvalidCursor.String(),
			Message: "cursor does not match the active sort",
		}
	}
	values := make([]any, len(keys))
	for i, k := range keys {
		v, err := cursorKeyValue(k, token.Fields[i].Value)
		if err != nil {
			return &core.ParseError{
				Kind:    core.ParseErrorKindSchema,
				Code:    ECodeInvalidCursor.String(),
				Message: fmt.Sprintf("invalid cursor value for field %q", k.Name),
				Cause:   err,
			}
		}
		values[i] = v
	}
	for i, v := range values {
		token.Fields[i].Value = v
	}
	return nil
}

// cursorKeyValue is a token's value for key k, normalized as a value k's
// column holds (core.NormalizeStored): a float column's NaN or infinity,
// on which a page may end, is kept.
func cursorKeyValue(k CursorKey, raw any) (any, error) {
	v, err := CursorValue(raw)
	switch {
	case err != nil:
		return nil, err
	case v == nil:
		if !k.Nullable {
			return nil, errors.New("null is not allowed")
		}
		return nil, nil
	case k.numberPath():
		// An earlier release's token carries an int64 or float64: it resumes.
		return core.NormalizeScalar(core.FieldTypeDecimal, reflect.Invalid, v)
	}
	v, err = core.NormalizeStored(k.DataType, k.NumberKind, v)
	if err != nil {
		return nil, err
	}
	text, isText := v.(string)
	if !isText {
		return v, nil
	}
	err = core.CheckText(text)
	if err != nil {
		return nil, err
	}
	return text, nil
}

// --- Tokens ---

// CursorField is one keyset component of a PageToken: the API sort-field
// name, the boundary row's canonical value (see CursorValue), and the
// presentation direction.
type CursorField struct {
	Name      string        `msgpack:"n" json:"name"`
	Value     any           `msgpack:"v" json:"value"`
	Direction SortDirection `msgpack:"d" json:"direction"`
}

// PageToken encodes a composite keyset cursor position. The field list
// mirrors the effective keyset; Reverse marks a backward seek (a page's
// start cursor).
type PageToken struct {
	Fields  []CursorField `msgpack:"f" json:"fields"`
	Reverse bool          `msgpack:"r,omitempty" json:"reverse,omitempty"`
}

// cursorGeneration versions the parse cursor wire format. Generation 3:
// canonical scalar values and the NULL / reverse seek model.
//
// A release reads its own generation only. DecodeCursor answers a token of
// any other generation with E2082 "unsupported cursor version", whatever the
// token holds, and the client starts again from the first page. A token of
// this generation is held to PageToken's and CursorField's members exactly: a
// member neither declares is E2082 "invalid cursor data", and a member it
// lacks decodes as the zero value. Adding, removing or retyping a member of
// either is therefore a format change and takes a new generation. Without
// one, during a rolling deploy a replica on the older release refuses the
// newer release's cursors as malformed, or misreads them. A member's value
// an older release refuses changes no member: that release answers the
// token with E2082 and the client starts again, as it would from a
// generation the release does not read, while every other token still
// resumes. A float key's NaN or infinity, which a release that predates
// carrying them refuses to decode, is "invalid cursor data"; a number
// path's decimal text, which a release that predates it refuses for the
// key, is "invalid cursor value for field".
const cursorGeneration uint32 = 3

// maxCursorLen bounds a cursor both ways: DecodeCursor rejects a longer
// token before decoding, and EncodeCursor refuses to mint one, so the server
// never hands out a cursor its next request would reject. Tokens carry a
// handful of scalars and ride in a query string.
const maxCursorLen = 4096

// EncodeCursor encodes a PageToken to an opaque base64url token. Values
// must already be canonical. A token over the decode limit is E2065.
func EncodeCursor(token *PageToken) (string, error) {
	if token == nil {
		return "", nil
	}
	cur, err := cursor.Encode(*token, cursor.WithGeneration(cursorGeneration))
	if err != nil {
		return "", fmt.Errorf("encoding cursor: %w", err)
	}
	if len(cur) > maxCursorLen {
		names := make([]string, len(token.Fields))
		for i, f := range token.Fields {
			names[i] = f.Name
		}
		return "", &core.ParseError{
			Kind:    core.ParseErrorKindSort,
			Code:    ECodeCursorTooLarge.String(),
			Message: fmt.Sprintf("the sort values at this page boundary (%s) encode to a %d-byte cursor, over the %d-byte limit; sort by a shorter field", strings.Join(names, ", "), len(cur), maxCursorLen),
		}
	}
	return cur, nil
}

// DecodeCursor decodes an opaque cursor to a PageToken whose values are
// canonical (see CursorValue); a token carrying any other value type is
// rejected as malformed.
func DecodeCursor(encoded string) (*PageToken, error) {
	invalid := func(msg string, cause error) error {
		return &core.ParseError{
			Kind: core.ParseErrorKindSchema, Code: ECodeInvalidCursor.String(),
			Message: msg, Cause: cause,
		}
	}
	if encoded == "" {
		return nil, invalid("cursor is empty", nil)
	}
	if len(encoded) > maxCursorLen {
		return nil, invalid("cursor is too long", nil)
	}
	token, err := cursor.Decode[PageToken](
		encoded,
		cursor.ExpectGeneration(cursorGeneration),
	)
	if err != nil {
		msg := "invalid cursor data"
		if errors.Is(err, cursor.ErrCursorGeneration) {
			msg = "unsupported cursor version"
		}
		return nil, invalid(msg, err)
	}
	for i, f := range token.Fields {
		switch f.Value.(type) {
		case nil, string, bool, float64, float32,
			int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64:
		default:
			return nil, invalid("invalid cursor data", fmt.Errorf("field %q holds %s", f.Name, gowalker.TypeText(reflect.TypeOf(f.Value))))
		}
		v, err := CursorValue(f.Value)
		if err != nil {
			return nil, invalid("invalid cursor data", err)
		}
		token.Fields[i].Value = v
	}
	return &token, nil
}

// --- Pages ---

// Page is the list-response envelope, with cursor metadata and optional
// relation and facet projections. Its JSON members are flat.
type Page[T any] struct {
	// Items are the page's rows in presentation order. NewPage never leaves
	// it nil, so a page with no rows encodes "items":[], as a client reading
	// an array expects.
	Items    []T      `json:"items"`
	PageInfo PageInfo `json:"page_info"`

	// Relations holds projected M2M relation IDs grouped by owner PK,
	// keyed by include-field name, when the request asked for `include`
	// projections.
	Relations map[string]map[string][]string `json:"_relations,omitempty"`
	// Truncated names, per include field, the owners whose related IDs
	// were cut at the per-owner cap (IncludeRows.Truncated).
	Truncated map[string][]string `json:"_truncated,omitempty"`
	// Facets counts the matched population by each requested facet field
	// (FacetMap), when the request asked for `facets`.
	Facets map[string][]FacetBucket `json:"facets,omitempty"`
}

// NewPage builds a Page from rows fetched with a Build result's query:
// up to limit+1 rows in query order. It truncates to limit, restores
// presentation order after a backward seek, derives has_next_page /
// has_prev_page, and mints start and end cursors. value resolves a key's
// value from a row by row column (e.g. CursorValueByTag with the "db" tag).
// A cursor that cannot be minted is an error — a page that reports more
// rows but carries no cursor strands the client. An empty page, from nil
// rows or none, has empty non-nil Items and reports neither direction: it
// has no row to mint a cursor from, so a client whose cursor outlived its
// rows starts again from the first page. A limit below 1 is an error: a
// page holds at least one row, and the query fetched limit+1.
func NewPage[T any](items []T, limit int, plan CursorPlan, value func(item T, column string) (any, error)) (*Page[T], error) {
	if limit < 1 {
		return nil, fmt.Errorf("building page: limit %d is below 1", limit)
	}
	if len(items) == 0 {
		// A query that matches nothing leaves its rows nil.
		empty := &Page[T]{Items: []T{}}
		return empty, nil
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	if plan.Reverse {
		items = slices.Clone(items)
		slices.Reverse(items)
	}

	page := &Page[T]{}
	page.Items = items
	if plan.Reverse {
		page.PageInfo.HasPrevPage = hasMore
		page.PageInfo.HasNextPage = true // the backward seek started from a later page
	} else {
		page.PageInfo.HasNextPage = hasMore
		page.PageInfo.HasPrevPage = plan.HasCursor
	}
	if page.PageInfo.HasNextPage {
		last := items[len(items)-1]
		cur, err := mintCursor(plan, false, func(col string) (any, error) { return value(last, col) })
		if err != nil {
			return nil, fmt.Errorf("building end cursor: %w", err)
		}
		page.PageInfo.EndCursor = cur
	}
	if page.PageInfo.HasPrevPage {
		first := items[0]
		cur, err := mintCursor(plan, true, func(col string) (any, error) { return value(first, col) })
		if err != nil {
			return nil, fmt.Errorf("building start cursor: %w", err)
		}
		page.PageInfo.StartCursor = cur
	}
	return page, nil
}

func mintCursor(plan CursorPlan, reverse bool, get func(string) (any, error)) (Cursor, error) {
	token, err := plan.Token(reverse, get)
	if err != nil {
		return "", err
	}
	s, err := EncodeCursor(token)
	if err != nil {
		return "", err
	}
	return Cursor(s), nil
}

// CursorValueByTag extracts a struct field value by matching a struct tag,
// for use as NewPage's value function (tagKey "db", tagValue the row
// column). Fields promoted from embedded structs match too. Returns an
// error if no field matches.
func CursorValueByTag(entity any, tagKey, tagValue string) (any, error) {
	rv := reflect.ValueOf(entity)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, fmt.Errorf("cursor: entity is nil")
		}
		rv = rv.Elem()
	}
	walk, err := core.StructFieldsOf(rv.Type(), core.JSONTagKey, tagKey)
	if err != nil {
		return nil, fmt.Errorf("cursor: %w", err)
	}
	f, ok := walk.ColumnTagged(tagValue)
	if !ok {
		return nil, fmt.Errorf("cursor: no field with tag %s=%q", tagKey, tagValue)
	}
	fv, err := f.Value(rv)
	if err != nil {
		return nil, fmt.Errorf("cursor: %w", err)
	}
	return fv.Interface(), nil
}

// ReverseDirection returns the opposite sort direction.
func ReverseDirection(d SortDirection) SortDirection {
	if d == SortDesc {
		return SortAsc
	}
	return SortDesc
}

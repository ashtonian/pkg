package filter

import (
	"database/sql/driver"
	"encoding"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"

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
type CursorKey struct {
	Name     string
	SQLExpr  string
	Column   string
	DataType core.FieldType
	// NumberKind retains the schema numeric range (Invalid uses int64/float64).
	NumberKind reflect.Kind
	Direction  SortDirection
	Nullable   bool
	// Path is the document member the key orders by (SortFieldSchema.Path).
	Path *JSONPath
}

// Keyset derives the effective keys for a sort: its fields followed by the
// tie-breaker unless the sort already contains it. The tie-breaker inherits
// the primary direction so a uniform-direction list keeps native row-value
// comparison available. SQL repositories get this from ListQuery.Build; the
// memory repository calls it directly so every backend walks one key list.
func Keyset(sort *SortResult, tieBreaker CursorKey) []CursorKey {
	var keys []CursorKey
	if sort != nil {
		keys = make([]CursorKey, 0, len(sort.Fields)+1)
		for _, f := range sort.Fields {
			keys = append(keys, CursorKey{
				Name:       f.Name,
				SQLExpr:    f.Schema.SQLExpr,
				Column:     f.Schema.RowColumn(),
				DataType:   f.Schema.DataType,
				NumberKind: f.Schema.NumberKind,
				Direction:  f.Direction,
				Nullable:   f.Schema.Nullable || f.Schema.Path != nil,
				Path:       f.Schema.Path,
			})
		}
	}
	if tieBreaker.Name == "" {
		return keys
	}
	for _, k := range keys {
		if k.Name == tieBreaker.Name {
			return keys
		}
	}
	tieBreaker.Direction = SortAsc
	if len(keys) > 0 {
		tieBreaker.Direction = keys[0].Direction
	}
	return append(keys, tieBreaker)
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
// at k.Column: CursorValue of it, or for a path key the member the path rule
// extracts (nil when absent). Cursors and a memory backend's ordering read
// keys through it alike.
func KeyValue(k CursorKey, raw any) (any, error) {
	if k.Path != nil {
		member, present, err := pathValueOf(raw, k.Path, k.DataType)
		if err != nil || !present {
			return nil, err
		}
		raw = member
	}
	return CursorValue(raw)
}

// CursorValue reduces a row value to the canonical form a token carries and
// SQL binds: nil, string, bool, int64, uint64 or float64. Pointers are
// dereferenced (nil → NULL), driver.Valuer values are resolved, times become
// RFC 3339 UTC strings (the form SQLite stores), and text-marshalling types
// such as uuid.UUID become their text. Anything else cannot resume a page
// and is an error, never an omitted cursor.
func CursorValue(v any) (any, error) {
	for {
		rv := reflect.ValueOf(v)
		if !rv.IsValid() {
			return nil, nil
		}
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return nil, nil
			}
			v = rv.Elem().Interface()
			continue
		}
		break
	}
	switch t := v.(type) {
	case string, bool, int64:
		return t, nil
	case time.Time:
		return core.FormatTimestamp(t), nil
	case []byte:
		return nil, errors.New("cursor: byte-slice keys are not supported")
	case driver.Valuer:
		return cursorDriverValue(t)
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
		f := rv.Float()
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, errors.New("cursor: non-finite float key")
		}
		return f, nil
	case reflect.Struct:
		// Sort schema generation also accepts pointer-receiver scalar
		// codecs. A value extracted from a row need not be addressable;
		// give the codec a copy instead of losing its pointer methods.
		pt := reflect.PointerTo(rv.Type())
		if pt.Implements(valuerType) || pt.Implements(textMarshalerType) {
			pv := reflect.New(rv.Type())
			pv.Elem().Set(rv)
			switch p := pv.Interface().(type) {
			case driver.Valuer:
				return cursorDriverValue(p)
			case encoding.TextMarshaler:
				return cursorTextValue(p)
			}
		}
	}
	return nil, fmt.Errorf("cursor: unsupported key type %T", v)
}

func cursorDriverValue(v driver.Valuer) (any, error) {
	dv, err := v.Value()
	if err != nil {
		return nil, fmt.Errorf("cursor: resolving %T: %w", v, err)
	}
	return CursorValue(dv)
}

func cursorTextValue(v encoding.TextMarshaler) (any, error) {
	b, err := v.MarshalText()
	if err != nil {
		return nil, fmt.Errorf("cursor: marshalling %T: %w", v, err)
	}
	return string(b), nil
}

// CompareKeyValues orders two canonical key values (see CursorValue) under
// the keyset model: NULL greatest, numbers exactly, timestamps by instant,
// strings bytewise, false before true. Direction is not applied.
func CompareKeyValues(k CursorKey, a, b any) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	if c, ok := core.CompareNumbers(a, b); ok {
		return c
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		if k.DataType == core.FieldTypeTimestamp {
			at, aerr := time.Parse(time.RFC3339Nano, as)
			bt, berr := time.Parse(time.RFC3339Nano, bs)
			if aerr == nil && berr == nil {
				return at.Compare(bt)
			}
		}
		return strings.Compare(as, bs)
	}
	ab, aok := a.(bool)
	bb, bok := b.(bool)
	if aok && bok {
		switch {
		case ab == bb:
			return 0
		case !ab:
			return -1
		}
		return 1
	}
	return strings.Compare(fmt.Sprint(a), fmt.Sprint(b))
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
	if k.Path == nil && k.DataType == core.FieldTypeDate {
		return d.ReadColumn(k.SQLExpr, core.SQLDate)
	}
	if k.Path != nil {
		return d.PathExpr(k.Path, k.DataType)
	}
	return k.SQLExpr
}

// keyExpr is a key's ordering expression: text keys — strings, and a path's
// timestamp and UUID members — order by code point, as filters and the
// memory backend compare.
func keyExpr(d Dialect, k CursorKey) string {
	if k.DataType == core.FieldTypeString || (k.Path != nil && (k.DataType == core.FieldTypeTimestamp || k.DataType == core.FieldTypeUUID)) {
		return d.CodePointOrder(keyValue(d, k))
	}
	return keyValue(d, k)
}

// orderByKeys renders ORDER BY for the keyset, walked forwards or backwards.
func orderByKeys(d Dialect, keys []CursorKey, reverse bool) string {
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

// seekPredicate renders "rows strictly after the token" in the walk order:
// the lexicographic expansion OR over depth i of (key_0 = v_0 AND … AND
// key_i after v_i), NULL-aware. bind binds one value and returns its
// placeholder; every non-null token value is bound once and its placeholder
// reused across depths. A native row-value comparison is used when the
// dialect supports it and no key or value involves NULL.
func seekPredicate(d Dialect, keys []CursorKey, token *PageToken, bind func(any) string) string {
	phs := make([]string, len(keys))
	native := d.NativeRowCompare() && len(keys) > 1
	for i, k := range keys {
		v := token.Fields[i].Value
		if v != nil {
			phs[i] = bind(v)
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
		after := afterExpr(keyExpr(d, k), k.Nullable, token.Fields[i].Value, phs[i], effectiveDirection(k, token.Reverse))
		if after == "" {
			continue // nothing orders after a NULL boundary on this key
		}
		parts := make([]string, 0, i+1)
		for j := range i {
			if token.Fields[j].Value == nil {
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
// keyset — a cursor minted under one sort must not be replayed under another
// . Values must match each key's type, numeric range and
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
		v, err := CursorValue(token.Fields[i].Value)
		if err == nil {
			if v == nil {
				if !k.Nullable {
					err = errors.New("null is not allowed")
				}
			} else if k.Path != nil && (k.DataType == core.FieldTypeInt || k.DataType == core.FieldTypeFloat) {
				v, err = pathNumber(v)
			} else {
				v, err = core.NormalizeScalar(k.DataType, k.NumberKind, v)
			}
		}
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
// canonical scalar values and the NULL / reverse seek model; older tokens
// fail the generation check and clients restart from the first page.
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
			return nil, invalid("invalid cursor data", fmt.Errorf("field %q holds %T", f.Name, f.Value))
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
// rows but carries no cursor strands the client.
func NewPage[T any](items []T, limit int, plan CursorPlan, value func(item T, column string) (any, error)) (*Page[T], error) {
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
	if len(items) == 0 {
		return page, nil
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
	walk, err := core.StructFieldsOf(rv.Type(), "json", tagKey)
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

//go:build unit

package parse_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/patch"
)

var errStopScan = errors.New("stop")

type storedSettings struct {
	Theme string `json:"theme"`
}

// storedRow holds every kind of value SQLite stores as text: timestamps
// and, as JSON, documents and arrays.
type storedRow struct {
	ID       string          `json:"id"       db:"id"`
	At       time.Time       `json:"at"       db:"at"`
	Until    *time.Time      `json:"until"    db:"until"`
	Counts   map[string]int  `json:"counts"   db:"counts"`
	Settings *storedSettings `json:"settings" db:"settings"`
	Tags     []string        `json:"tags"     db:"tags"`
	Spans    []core.Interval `json:"spans"    db:"spans"`
	Raw      json.RawMessage `json:"raw"      db:"raw"`
	Dynamic  any             `json:"dynamic"  db:"dynamic"`
	Every    core.Interval   `json:"every"    db:"every"`
}

// TestRowScanner_DecodesWhatTheDialectStoresAsText: a row scan decodes a
// column itself, rather than scanning it into its field through the
// driver, only where the dialect stores the field's value as text and the
// driver cannot scan that text into the field.
func TestRowScanner_DecodesWhatTheDialectStoresAsText(t *testing.T) {
	fields := map[string]reflect.Type{}
	row := reflect.TypeFor[storedRow]()
	for i := range row.NumField() {
		fields[row.Field(i).Tag.Get("db")] = row.Field(i).Type
	}
	for _, tc := range []struct {
		name    string
		p       *parse.Provider
		decoded []string
	}{
		{"sqlite", onSQLite, []string{"at", "until", "counts", "settings", "tags", "spans", "raw", "dynamic"}},
		{"postgres", onPostgres, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := parse.MustResource[storedRow](tc.p, "stored")
			scanner, err := r.RowScanner()
			require.NoError(t, err)
			var decoded []string
			_, err = scanner.Scan(func(dest ...any) error {
				for i, name := range r.ColumnNames() {
					if reflect.TypeOf(dest[i]) != reflect.PointerTo(fields[name]) {
						decoded = append(decoded, name)
					}
				}
				return errStopScan
			})
			require.ErrorIs(t, err, errStopScan)
			assert.Equal(t, tc.decoded, decoded)
		})
	}
}

// A row scan fills a fresh row, where an unexported embedded pointer is nil
// and reflection cannot allocate it. A read column whose field is promoted
// through one, or a stored document holding a member promoted so, is refused
// when the resource is built.

type hiddenColumns struct {
	Extra string `json:"extra" db:"extra"`
}

type hiddenColumnRow struct {
	ID string `json:"id" db:"id"`
	*hiddenColumns
}

// HiddenColumnHolder is exported, and so settable, but holds the unexported
// pointer the column's field is promoted through.
type HiddenColumnHolder struct {
	*hiddenColumns
}

type deepHiddenColumnRow struct {
	ID string `json:"id" db:"id"`
	*HiddenColumnHolder
}

type hiddenMembers struct {
	X string `json:"x"`
}

// hiddenDoc is a document encoding/json writes x into while the pointer is
// set, and cannot decode x out of into a fresh value.
type hiddenDoc struct {
	*hiddenMembers
	Y string `json:"y"`
}

type hiddenDocRow struct {
	ID  string    `json:"id"  db:"id"`
	Doc hiddenDoc `json:"doc" db:"doc"`
}

type hiddenDocsRow struct {
	ID   string                 `json:"id"   db:"id"`
	Docs map[string][]hiddenDoc `json:"docs" db:"docs"`
}

type patchedHiddenDocRow struct {
	ID  string    `json:"id"  db:"id"`
	Doc hiddenDoc `json:"doc" db:"doc" parse:"patch"`
}

// scannedHiddenDoc decodes the driver's value itself, so its column is a
// stored document only when ScanJSON names it.
type scannedHiddenDoc struct {
	*hiddenMembers
}

func (d *scannedHiddenDoc) Scan(any) error { return nil }

type scannedHiddenDocRow struct {
	ID  string           `json:"id"  db:"id"`
	Doc scannedHiddenDoc `json:"doc" db:"doc"`
}

func TestNewResource_RefusesAColumnNoRowScanCanFill(t *testing.T) {
	const fix = "export the type or embed it by value"
	for _, tt := range []struct {
		name  string
		build func() error
		// want are the column, the field and the embedded type the error
		// names, each as it reads there.
		want []string
	}{
		{
			name: "value row",
			build: func() error {
				_, err := parse.NewResource[hiddenColumnRow](onSQLite, "rows")
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "pointer row",
			build: func() error {
				_, err := parse.NewResource[*hiddenColumnRow](onSQLite, "rows")
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "behind an exported embedded pointer",
			build: func() error {
				_, err := parse.NewResource[deepHiddenColumnRow](onSQLite, "rows")
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "computed column",
			build: func() error {
				_, err := parse.NewResource[hiddenColumnRow](onSQLite, "rows", parse.ReadExpr("extra", parse.SQL("'computed'")))
				return err
			},
			want: []string{"column extra: field Extra:", "unexported embedded *parse_test.hiddenColumns"},
		},
		{
			name: "stored document",
			build: func() error {
				_, err := parse.NewResource[hiddenDocRow](onSQLite, "rows")
				return err
			},
			want: []string{"column doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "declared JSON document",
			build: func() error {
				_, err := parse.NewResource[hiddenDocRow](onSQLite, "rows", parse.ColumnType("doc", core.SQLJSONB))
				return err
			},
			want: []string{"column doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "document held in a map of slices",
			build: func() error {
				_, err := parse.NewResource[hiddenDocsRow](onSQLite, "rows")
				return err
			},
			want: []string{"column docs: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "patched document, resource",
			build: func() error {
				_, err := parse.NewResource[patchedHiddenDocRow](onSQLite, "rows")
				return err
			},
			want: []string{"field Doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
		{
			name: "patched document, standalone patch schema",
			build: func() error {
				_, err := patch.GeneratePatchSchema[patchedHiddenDocRow](core.NewSchemaGenerator(core.WithSQLTableName("rows")))
				return err
			},
			want: []string{"field Doc: parse_test.hiddenDoc: field X:", "unexported embedded *parse_test.hiddenMembers"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build()
			require.Error(t, err)
			for _, want := range tt.want {
				require.ErrorContains(t, err, want)
			}
			require.ErrorContains(t, err, fix)
		})
	}
}

func TestRowScanner_RefusesScanJSONOnADocumentItCannotDecode(t *testing.T) {
	for _, p := range []*parse.Provider{onPostgres, onSQLite} {
		r, err := parse.NewResource[scannedHiddenDocRow](p, "rows")
		require.NoError(t, err, "the driver hands the column to its sql.Scanner")
		_, err = r.RowScanner()
		require.NoError(t, err)
		_, err = r.RowScanner(parse.ScanJSON("doc"))
		require.ErrorContains(t, err, `ScanJSON names "doc": parse_test.scannedHiddenDoc: field X:`)
		require.ErrorContains(t, err, "unexported embedded *parse_test.hiddenMembers")
	}
}

func TestRowScanner_RefusesScanJSONOfAColumnItDoesNotRead(t *testing.T) {
	r := parse.MustResource[scannedHiddenDocRow](onSQLite, "rows")
	_, err := r.RowScanner(parse.ScanJSON("nope"))
	assert.ErrorContains(t, err, `ScanJSON names "nope", which the resource does not read`)
}

// sealedCode keeps its Valuer on the pointer, as sort schema generation
// admits: a row's copy of it is not addressable.
type sealedCode struct{ code string }

func (c *sealedCode) Value() (driver.Value, error) { return "code-" + c.code, nil }

// A value is read as the database holds it (core.StoredValue) by facets,
// cursors and relation binds alike: each of the types parse keeps as they
// are, and Valuers, which stand for what they write. Each reading ends in
// the value's canonical text as its column's type.
func TestStoredValues_FacetsCursorsAndRelationsAgree(t *testing.T) {
	at := time.Date(2026, time.January, 2, 3, 4, 5, 600, time.FixedZone("", 3600))
	ref := "0198a3c2-7a1e-7c00-8000-00000000000a"
	for name, tc := range map[string]struct {
		typ  core.FieldType
		v    any
		want string
	}{
		"a time":                    {core.FieldTypeTimestamp, at, "2026-01-02T02:04:05.000000600Z"},
		"a NULL-able time":          {core.FieldTypeTimestamp, sql.NullTime{Time: at, Valid: true}, "2026-01-02T02:04:05.000000600Z"},
		"a date":                    {core.FieldTypeDate, core.Date("2026-01-02"), "2026-01-02"},
		"a decimal":                 {core.FieldTypeDecimal, decimal.RequireFromString("1.50"), "1.5"},
		"a pointer to a decimal":    {core.FieldTypeDecimal, new(decimal.RequireFromString("12345678901234567890.10")), "12345678901234567890.1"},
		"a UUID with a Valuer":      {core.FieldTypeUUID, googleuuid.MustParse(ref), ref},
		"a UUID":                    {core.FieldTypeUUID, uuid.MustParse(ref), ref},
		"a UUID as text":            {core.FieldTypeString, googleuuid.MustParse(ref), ref},
		"a time of day":             {core.FieldTypeTime, core.LocalTime("03:04:05.5"), "03:04:05.5"},
		"a MAC address":             {core.FieldTypeMACAddr, core.MACAddr("08:00:2b:01:02:03"), "08:00:2b:01:02:03"},
		"a Valuer's text":           {core.FieldTypeString, sql.NullString{String: "x", Valid: true}, "x"},
		"a Valuer's integer":        {core.FieldTypeInt, sql.NullInt64{Int64: 5, Valid: true}, "5"},
		"a pointer-receiver Valuer": {core.FieldTypeString, sealedCode{code: "a"}, "code-a"},
		"a scanned interface":       {core.FieldTypeString, new(any("x")), "x"},
	} {
		t.Run(name, func(t *testing.T) {
			info := core.TypeInfo{Type: tc.typ}

			facets, err := filter.FacetKeys(tc.v, &filter.FieldSchema{ExternalName: "f", DataType: tc.typ})
			require.NoError(t, err, "facet")
			assert.Equal(t, []string{tc.want}, facets, "facet")

			key := filter.CursorKey{Name: "f", Column: "f", DataType: tc.typ}
			value, err := filter.KeyValue(key, tc.v)
			require.NoError(t, err, "cursor")
			token := &filter.PageToken{Fields: []filter.CursorField{{Name: "f", Value: value}}}
			require.NoError(t, filter.ValidateCursor(token, []filter.CursorKey{key}), "cursor")
			cursorText, err := core.CanonicalText(info, token.Fields[0].Value)
			require.NoError(t, err, "cursor")
			assert.Equal(t, tc.want, cursorText, "cursor")

			rel := &core.RelationSchema{JoinTable: "pairs", SourceColumns: []string{"owner_id"}, TargetColumns: []string{"target"},
				SourceTypes: []core.TypeInfo{{Type: core.FieldTypeString}}, TargetTypes: []core.TypeInfo{info}}
			op := patch.RelationPatchOp{Field: "f", Schema: rel, Verb: patch.RelationVerbRemove, Values: []any{tc.v}}
			stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []core.Key{{"w1"}}, patch.RenderRelationOpOptions{})
			require.NoError(t, err, "relation")
			require.Len(t, stmts, 1)
			bound := reflect.ValueOf(stmts[0].Args[len(stmts[0].Args)-1])
			require.Equal(t, reflect.Slice, bound.Kind(), "the targets bind as one array")
			require.Equal(t, 1, bound.Len())
			relationText, err := core.CanonicalText(info, bound.Index(0).Interface())
			require.NoError(t, err, "relation")
			assert.Equal(t, tc.want, relationText, "relation")
		})
	}
}

// The same instant written through SQLiteDialect.MarshalValue in any form
// is one canonical text, so SQLite stores and compares instants.
func TestSQLiteDialect_MarshalValueWritesCanonicalTimestamps(t *testing.T) {
	instant := time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, time.FixedZone("", 2*60*60))
	const canonical = "2026-01-02T01:04:05.500000000Z"
	info := core.TypeInfo{Type: core.FieldTypeTimestamp}
	for _, tc := range []struct {
		name  string
		value any
		want  any
	}{
		{"a time", instant, canonical},
		{"a pointer", &instant, canonical},
		{"a valid NullTime", sql.NullTime{Time: instant, Valid: true}, canonical},
		{"RFC 3339 text in an offset", "2026-01-02T03:04:05.5+02:00", canonical},
		{"nil", nil, nil},
		{"a nil pointer", (*time.Time)(nil), nil},
		{"a NULL NullTime", sql.NullTime{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filter.SQLiteDialect{}.MarshalValue(tc.value, info)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := filter.SQLiteDialect{}.MarshalValue(instant.String(), info)
	require.Error(t, err, "the driver's own text is no RFC 3339 timestamp")

	list := core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeTimestamp}
	got, err := filter.SQLiteDialect{}.MarshalValue([]time.Time{instant, instant.UTC()}, list)
	require.NoError(t, err)
	assert.Equal(t, `["`+canonical+`","`+canonical+`"]`, got, "each element in UTC, as an element reads")
	got, err = filter.SQLiteDialect{}.MarshalValue([]*time.Time{&instant, nil}, list)
	require.NoError(t, err)
	assert.Equal(t, `["`+canonical+`",null]`, got)
	got, err = filter.SQLiteDialect{}.MarshalValue([]time.Time(nil), list)
	require.NoError(t, err)
	assert.Equal(t, `null`, got, "a nil list is JSON null")
	got, err = filter.SQLiteDialect{}.MarshalValue(&[]time.Time{instant}, list)
	require.NoError(t, err)
	assert.Equal(t, `["`+canonical+`"]`, got, "a pointer to a list")
}

func TestCivilDateRequestContract(t *testing.T) {
	resource := parse.MustResource[parsetest.CivilTask](onSQLite, "tasks")
	_, err := parse.NewResource[parsetest.CivilTask](onSQLite, "tasks", parse.ColumnType("due_on", core.SQLDate))
	require.NoError(t, err)
	_, err = parse.NewResource[struct {
		ID  string `db:"id"`
		Day string `db:"day"`
	}](onSQLite, "bad", parse.ColumnType("day", core.SQLDate))
	require.Error(t, err)

	for _, provider := range []*parse.Provider{onPostgres, onSQLite} {
		tasks := parse.MustResource[parsetest.CivilTask](provider, "tasks")
		for _, day := range []string{"2026-10-26", "2028-02-29"} {
			query, err := tasks.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"` + day + `"}}`}, "sort": {"due_on,id"}})
			require.NoError(t, err)
			_, err = tasks.BuildList(query, parse.Scope{})
			require.NoError(t, err, provider.Name())
		}
	}
	for _, day := range []string{"2026-10-26", "2028-02-29"} {
		patch, err := resource.ParseMergePatch([]byte(`{"due_on":"` + day + `"}`))
		require.NoError(t, err)
		target, err := resource.ApplyValidatedPatch(patch, parsetest.CivilTask{ID: "one"})
		require.NoError(t, err)
		require.NotNil(t, target.DueOn)
		assert.Equal(t, core.Date(day), *target.DueOn)
		wire, err := json.Marshal(target)
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"one","due_on":"`+day+`"}`, string(wire))
	}
	for _, invalid := range []string{"2026-02-29", "2026-10-26T00:00:00Z", "now-7d"} {
		_, err := resource.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"` + invalid + `"}}`}})
		require.Error(t, err)
		_, err = resource.ParseMergePatch([]byte(`{"due_on":"` + invalid + `"}`))
		require.Error(t, err)
	}
}

func TestCivilDateNullableBindings(t *testing.T) {
	for _, dialect := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		var value *core.Date
		normalized, err := dialect.MarshalValue(value, core.TypeInfo{Type: core.FieldTypeDate})
		require.NoError(t, err)
		assert.Nil(t, normalized)
		value = new(core.Date("2028-02-29"))
		_, err = dialect.MarshalValue(value, core.TypeInfo{Type: core.FieldTypeDate})
		require.NoError(t, err)
	}
}

func TestDecimalRequestContract(t *testing.T) {
	for _, tt := range []struct {
		provider *parse.Provider
		amount   string
		fee      string
		order    string
	}{
		{onPostgres, `"ledger_lines"."amount" > $`, `"ledger_lines"."fee" <= $`, `"ledger_lines"."amount" DESC`},
		{onSQLite, `parse_decimal_key("ledger_lines"."amount") > parse_decimal_key(?`, `parse_decimal_key("ledger_lines"."fee") <= parse_decimal_key(?`, `parse_decimal_key("ledger_lines"."amount") DESC`},
	} {
		resource := parse.MustResource[parsetest.LedgerLine](tt.provider, "ledger_lines")
		query, err := resource.ParseList(url.Values{
			"filter":  {`{"amount":{"$gt":"9.50"}}`},
			"fee_lte": {"1.0"},
			"sort":    {"-amount,id"},
		})
		require.NoError(t, err)
		plan, err := resource.BuildList(query, parse.Scope{})
		require.NoError(t, err)
		name := tt.provider.Name()
		assert.Contains(t, plan.Page.SQL, tt.amount, name)
		assert.Contains(t, plan.Page.SQL, tt.fee, name)
		assert.Contains(t, plan.Page.SQL, tt.order, name)
		assert.Subset(t, plan.Page.Args, []any{"9.5", "1"}, name)
	}

	resource := parse.MustResource[parsetest.LedgerLine](onSQLite, "ledger_lines")

	for _, bad := range []url.Values{
		{"filter": {`{"amount":"ten"}`}},
		{"amount_gt": {"1e5000"}},
	} {
		_, err := resource.ParseList(bad)
		var pe *core.ParseError
		require.ErrorAs(t, err, &pe, "%v: %v", bad, err)
		assert.Equal(t, filter.ECodeValueTypeMismatch.String(), pe.Code, bad)
	}
}

func TestDecimalMergePatch(t *testing.T) {
	resource := parse.MustResource[parsetest.LedgerLine](onSQLite, "ledger_lines")
	tests := []struct {
		body       string
		wantAmount string
		wantFee    *string
	}{
		{`{"amount":"10.50","fee":"0.25"}`, "10.5", new("0.25")},
		{`{"amount":10.5,"fee":2}`, "10.5", new("2")},
		{`{"amount":"0.10000000000000001","fee":null}`, "0.10000000000000001", nil},
		{`{"amount":"-1.5e1"}`, "-15", new("3")},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			p, err := resource.ParseMergePatch([]byte(tt.body))
			require.NoError(t, err)
			target, err := resource.ApplyValidatedPatch(p, parsetest.LedgerLine{ID: "one", Fee: new(decimal.RequireFromString("3"))})
			require.NoError(t, err)
			assert.Equal(t, tt.wantAmount, target.Amount.String())
			if tt.wantFee == nil {
				assert.Nil(t, target.Fee)
			} else {
				require.NotNil(t, target.Fee)
				assert.Equal(t, *tt.wantFee, target.Fee.String())
			}

			update, err := resource.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, update.Owner.Args, tt.wantAmount, "SET binds the canonical text")
		})
	}

	for _, body := range []string{`{"amount":"ten"}`, `{"amount":true}`, `{"amount":null}`, `{"fee":"1e5000"}`} {
		_, err := resource.ParseMergePatch([]byte(body))
		var pe *core.ParseError
		require.ErrorAs(t, err, &pe, "%s: %v", body, err)
		assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code, body)
	}
}

// The wire form a decimal marshals to is the form a patch and a filter
// accept back.
func TestDecimalWireRoundTrip(t *testing.T) {
	resource := parse.MustResource[parsetest.LedgerLine](onSQLite, "ledger_lines")
	wire, err := json.Marshal(map[string]any{"amount": decimal.RequireFromString("10.25")})
	require.NoError(t, err)
	require.JSONEq(t, `{"amount":"10.25"}`, string(wire))
	p, err := resource.ParseMergePatch(wire)
	require.NoError(t, err)
	target, err := resource.ApplyValidatedPatch(p, parsetest.LedgerLine{})
	require.NoError(t, err)
	assert.True(t, decimal.RequireFromString("10.25").Equal(target.Amount))

	_, err = resource.ParseList(url.Values{"filter": {string(wire)}})
	require.NoError(t, err)
}

func TestSQLTextValueFilters(t *testing.T) {
	resource := parse.MustResource[parsetest.NetworkEvent](onSQLite, "network_events")
	for _, tt := range []struct {
		filter, canonical string
	}{
		{`{"at":{"$gte":"10:30:00.500"}}`, "10:30:00.5"},
		{`{"retention":"PT90M"}`, "PT1H30M"},
		{`{"actor_ip":"2001:0DB8::0001"}`, "2001:db8::1"},
		{`{"actor_ip":{"$in":["192.168.0.5/32"]}}`, "192.168.0.5"},
		{`{"network":"2001:DB8::/32"}`, "2001:db8::/32"},
		{`{"device":"08-00-2B-01-02-03"}`, "08:00:2b:01:02:03"},
		{`{"allowed_ips":{"$contains":["10.0.0.1/32"]}}`, "10.0.0.1"},
		{`{"spans":{"$contains":["PT90M"]}}`, "PT1H30M"},
	} {
		t.Run(tt.filter, func(t *testing.T) {
			for _, provider := range []*parse.Provider{onPostgres, onSQLite} {
				events := parse.MustResource[parsetest.NetworkEvent](provider, "network_events")
				query, err := events.ParseList(url.Values{"filter": {tt.filter}})
				require.NoError(t, err)
				plan, err := events.BuildList(query, parse.Scope{})
				require.NoError(t, err)
				assert.Contains(t, boundText(plan.Page.Args), tt.canonical, "%s binds the canonical text", provider.Name())
			}
		})
	}

	for _, tt := range []struct {
		name, filter, code string
	}{
		{"time with an offset", `{"at":"10:30:00+02:00"}`, "E2073"},
		{"24:00:00", `{"at":"24:00:00"}`, "E2073"},
		{"interval in postgres style", `{"retention":"1 day"}`, "E2073"},
		{"inet with a zone", `{"actor_ip":"fe80::1%eth0"}`, "E2073"},
		{"cidr with host bits", `{"network":"10.0.0.1/8"}`, "E2073"},
		{"EUI-64", `{"device":"08:00:2b:ff:fe:01:02:03"}`, "E2073"},
		{"inet range", `{"actor_ip":{"$gt":"10.0.0.1"}}`, ""},
		{"interval range", `{"retention":{"$lt":"P1D"}}`, ""},
		{"inet pattern", `{"actor_ip":{"$like":"10.%"}}`, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resource.ParseList(url.Values{"filter": {tt.filter}})
			require.Error(t, err)
			if tt.code != "" {
				var parseErr *core.ParseError
				require.ErrorAs(t, err, &parseErr, "%v", err)
				assert.Equal(t, tt.code, parseErr.Code)
			}
		})
	}

	types := map[string]string{}
	for _, f := range resource.FilterSchema().Fields {
		types[f.ExternalName] = string(f.DataType)
	}
	assert.Equal(t, map[string]string{
		"id": "string", "at": "time", "retention": "interval", "actor_ip": "inet", "network": "cidr", "device": "macaddr",
		"allowed_ips": "array", "spans": "array",
	}, types)
}

// TestSQLTextValueIntervalEquality checks that PostgreSQL compares an
// interval's canonical text, field by field: its own equality would match
// P1M against P30D, which SQLite's text comparison does not.
func TestSQLTextValueIntervalEquality(t *testing.T) {
	resource := parse.MustResource[parsetest.NetworkEvent](onPostgres, "network_events")
	query, err := resource.ParseList(url.Values{"filter": {`{"retention":"P30D"}`}})
	require.NoError(t, err)
	plan, err := resource.BuildList(query, parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, plan.Page.SQL, `isfinite("network_events"."retention")`)
	assert.Contains(t, strings.Join(resource.Columns(), ","), `isfinite("network_events"."retention")`,
		"an Interval column reads as ISO 8601 text under any IntervalStyle")
	declared, ok := resource.ColumnSQLType("retention")
	require.True(t, ok)
	assert.Equal(t, core.SQLInterval, declared)
}

func TestSQLTextValuePatch(t *testing.T) {
	const body = `{"at":"10:30:00.500","retention":"PT90M","actor_ip":"192.168.0.5/32",
		"network":"10.0.0.0/8","device":"08-00-2B-01-02-03","allowed_ips":["10.0.0.1","2001:0db8::1"],"spans":["P14M"]}`
	resource := parse.MustResource[parsetest.NetworkEvent](onSQLite, "network_events")
	patch, err := resource.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	target, err := resource.ApplyValidatedPatch(patch, parsetest.NetworkEvent{ID: "one"})
	require.NoError(t, err)
	wire, err := json.Marshal(target)
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"one","at":"10:30:00.5","retention":"PT1H30M","actor_ip":"192.168.0.5",
		"network":"10.0.0.0/8","device":"08:00:2b:01:02:03","allowed_ips":["10.0.0.1","2001:db8::1"],"spans":["P1Y2M"]}`, string(wire))
	for _, provider := range []*parse.Provider{onPostgres, onSQLite} {
		events := parse.MustResource[parsetest.NetworkEvent](provider, "network_events")
		patch, err := events.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := events.BuildUpdate(patch, parse.ByID("one"), parse.Scope{})
		require.NoError(t, err)
		args := boundText(plan.Owner.Args)
		for _, canonical := range []string{"10:30:00.5", "PT1H30M", "192.168.0.5", "10.0.0.0/8", "08:00:2b:01:02:03", "P1Y2M"} {
			assert.Contains(t, args, canonical, "%s writes the canonical text", provider.Name())
		}
	}

	for _, body := range []string{`{"at":"10:30"}`, `{"retention":"P1.5D"}`, `{"actor_ip":"not-an-ip"}`,
		`{"network":"10.0.0.0"}`, `{"device":"08:00:2b"}`, `{"allowed_ips":["10.0.0.256"]}`} {
		_, err := resource.ParseMergePatch([]byte(body))
		assert.Error(t, err, body)
	}
}

func TestSQLTextValueDeclarations(t *testing.T) {
	_, err := parse.NewResource[parsetest.NetworkEvent](onPostgres, "network_events",
		parse.ColumnType("actor_ip", core.SQLInet), parse.ColumnType("network", core.SQLCIDR),
		parse.ColumnType("retention", core.SQLInterval), parse.ColumnType("allowed_ips", core.SQLInetArray))
	require.NoError(t, err)
	for _, tt := range []struct {
		column  string
		storage core.SQLType
	}{
		{"actor_ip", core.SQLCIDR},
		{"network", core.SQLInet},
		{"at", core.SQLInterval},
		{"device", core.SQLTextArray},
		{"allowed_ips", core.SQLCIDRArray},
		{"retention", core.SQLDate},
	} {
		_, err := parse.NewResource[parsetest.NetworkEvent](onPostgres, "network_events", parse.ColumnType(tt.column, tt.storage))
		require.Error(t, err, "%s declared %s", tt.column, tt.storage)
	}

	_, err = parse.NewResource[struct {
		ID string     `db:"id"`
		IP *core.Inet `db:"ip" parse:"sort"`
	}](onSQLite, "unsortable")
	require.ErrorContains(t, err, "inet fields are not sortable")
	_, err = parse.NewResource[struct {
		ID  string        `db:"id"`
		TTL core.Interval `db:"ttl" parse:"filter,facet"`
	}](onSQLite, "unfacetable")
	require.ErrorContains(t, err, "interval values have no facet key")
	_, err = parse.NewResource[struct {
		ID  string      `db:"id"`
		IPs []core.CIDR `db:"ips" parse:"filter,facet"`
	}](onSQLite, "unfacetable")
	assert.ErrorContains(t, err, "cidr values have no facet key")
}

// boundText is the text of each bound argument, with the elements of an
// array argument and of a JSON array text, which SQLite binds sets as.
func boundText(args []any) []string {
	var out []string
	for _, arg := range args {
		switch v := arg.(type) {
		case string:
			out = append(out, v)
			var items []string
			if json.Unmarshal([]byte(v), &items) == nil {
				out = append(out, items...)
			}
		case []string:
			out = append(out, v...)
		}
	}
	return out
}

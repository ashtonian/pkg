//go:build unit

package parse_test

import (
	"database/sql"
	"database/sql/driver"
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
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

// keyTextRow has a column of each scalar type a key's text is read as.
type keyTextRow struct {
	S   string          `json:"s"   db:"s"`
	I8  int8            `json:"i8"  db:"i8"`
	I64 int64           `json:"i64" db:"i64"`
	U16 uint16          `json:"u16" db:"u16"`
	U64 uint64          `json:"u64" db:"u64"`
	F32 float32         `json:"f32" db:"f32"`
	F64 float64         `json:"f64" db:"f64"`
	B   bool            `json:"b"   db:"b"`
	U   uuid.UUID       `json:"u"   db:"u"`
	TS  time.Time       `json:"ts"  db:"ts"`
	D   core.Date       `json:"d"   db:"d"`
	Dec decimal.Decimal `json:"dec" db:"dec"`
	Qty int             `json:"qty" db:"qty" parse:"patch"`
}

// ParseKey reads each column's text by the column's type and normalizes the
// key as ByKey does. A text its column cannot hold, U+0000 and invalid UTF-8
// included, is the request's ErrNoSuchKey naming the column; another number
// of texts than columns is the caller's ErrKeyInvalid.
func TestResource_ParseKey(t *testing.T) {
	tests := []struct {
		name    string
		key     []string
		texts   []string
		want    parse.Key
		wantErr string
		// callers marks the caller's mistake (ErrKeyInvalid) rather than
		// the request's (ErrNoSuchKey).
		callers bool
	}{
		{name: "string", key: []string{"s"}, texts: []string{"a/b c"}, want: parse.Key{"a/b c"}},
		{name: "negative int64", key: []string{"i64"}, texts: []string{"-42"}, want: parse.Key{int64(-42)}},
		{name: "largest uint64", key: []string{"u64"}, texts: []string{"18446744073709551615"}, want: parse.Key{uint64(18446744073709551615)}},
		{name: "leading zeros", key: []string{"i64"}, texts: []string{"007"}, want: parse.Key{int64(7)}},
		{name: "float64", key: []string{"f64"}, texts: []string{"1e3"}, want: parse.Key{float64(1000)}},
		{name: "float32 at its precision", key: []string{"f32"}, texts: []string{"0.1"}, want: parse.Key{float64(float32(0.1))}},
		{name: "bool", key: []string{"b"}, texts: []string{"true"}, want: parse.Key{true}},
		{name: "bool as ParseBool reads it", key: []string{"b"}, texts: []string{"0"}, want: parse.Key{false}},
		{name: "uuid canonical", key: []string{"u"}, texts: []string{"0198A3C2-7A1E-7C00-8000-000000000001"}, want: parse.Key{"0198a3c2-7a1e-7c00-8000-000000000001"}},
		{name: "timestamp canonical", key: []string{"ts"}, texts: []string{"2026-01-02T05:04:05+02:00"}, want: parse.Key{"2026-01-02T03:04:05.000000000Z"}},
		{name: "date", key: []string{"d"}, texts: []string{"2028-02-29"}, want: parse.Key{"2028-02-29"}},
		{name: "decimal", key: []string{"dec"}, texts: []string{"10.25"}, want: parse.Key{"10.25"}},
		{name: "composite", key: []string{"s", "i64"}, texts: []string{"w1", "42"}, want: parse.Key{"w1", int64(42)}},

		{name: "int8 overflow", key: []string{"i8"}, texts: []string{"128"}, wantErr: "key column i8: 128 overflows int8"},
		{name: "uint64 overflow", key: []string{"u64"}, texts: []string{"18446744073709551616"}, wantErr: "key column u64: 18446744073709551616 overflows uint64"},
		{name: "negative unsigned", key: []string{"u16"}, texts: []string{"-1"}, wantErr: "key column u16: -1 is negative"},
		{name: "plus sign", key: []string{"i64"}, texts: []string{"+5"}, wantErr: `key column i64: "+5" is not a decimal integer`},
		{name: "exponent integer", key: []string{"i64"}, texts: []string{"1e3"}, wantErr: `key column i64: "1e3" is not a decimal integer`},
		{name: "empty integer", key: []string{"i64"}, texts: []string{""}, wantErr: `key column i64: "" is not a decimal integer`},
		{name: "NaN", key: []string{"f64"}, texts: []string{"NaN"}, wantErr: "key column f64:"},
		{name: "hexadecimal float", key: []string{"f64"}, texts: []string{"0x1p-2"}, wantErr: "key column f64:"},
		{name: "not a bool", key: []string{"b"}, texts: []string{"yes"}, wantErr: `key column b: "yes" is not true or false`},
		{name: "not a uuid", key: []string{"u"}, texts: []string{"nope"}, wantErr: "key column u: invalid UUID"},
		{name: "not a date", key: []string{"d"}, texts: []string{"2027-02-29"}, wantErr: "key column d:"},
		{name: "composite column", key: []string{"s", "i64"}, texts: []string{"w1", "x"}, wantErr: `key column i64: "x" is not a decimal integer`},
		{name: "U+0000", key: []string{"s"}, texts: []string{"a\x00b"}, wantErr: "key column s: invalid text: it holds U+0000"},
		{name: "invalid UTF-8", key: []string{"s", "i64"}, texts: []string{"a\xff", "1"}, wantErr: "key column s: invalid text: it is not valid UTF-8"},
		{name: "too few texts", key: []string{"s", "i64"}, texts: []string{"w1"}, wantErr: "1 texts for the 2 key columns (s, i64)", callers: true},
		{name: "too many texts", key: []string{"s"}, texts: []string{"w1", "a"}, wantErr: "2 texts for the 1 key columns (s)", callers: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := parse.NewResource[keyTextRow](onPostgres, "key_text_rows", parse.PrimaryKey(tt.key...))
			require.NoError(t, err)
			got, err := r.ParseKey(tt.texts...)
			if tt.wantErr != "" {
				want, not := parse.ErrNoSuchKey, parse.ErrKeyInvalid
				if tt.callers {
					want, not = not, want
				}
				require.ErrorIs(t, err, want)
				require.NotErrorIs(t, err, not)
				require.ErrorContains(t, err, "parse: key_text_rows: ")
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// A key ParseKey reads from text targets the row the same key's values
// target through ByKey.
func TestResource_ParseKeyTargetsAsByKey(t *testing.T) {
	r, err := parse.NewResource[keyTextRow](onPostgres, "key_text_rows", parse.PrimaryKey("s", "i64"))
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	parsed, err := r.ParseKey("w1", "42")
	require.NoError(t, err)

	fromText, err := r.BuildUpdate(p, parse.ByKey(parsed), parse.Scope{})
	require.NoError(t, err)
	fromValues, err := r.BuildUpdate(p, parse.ByKey(parse.Key{"w1", 42}), parse.Scope{})
	require.NoError(t, err)
	assert.Equal(t, fromValues.Owner, fromText.Owner)
}

// A key field takes equality, the range operators and $in, those its type
// takes: a key of a type without an order, such as an inet or an interval,
// offers no range.
func TestKeyField_OperatorsFollowItsType(t *testing.T) {
	ordered := []filter.FilterOperator{filter.OpEq, filter.OpGt, filter.OpLt, filter.OpGte, filter.OpLte, filter.OpIn}
	unordered := []filter.FilterOperator{filter.OpEq, filter.OpIn}
	tests := []struct {
		name string
		ops  func(t *testing.T) []filter.FilterOperator
		want []filter.FilterOperator
	}{
		{"string", keyOperators[struct {
			ID string `json:"id" db:"id"`
		}], ordered},
		{"int", keyOperators[struct {
			ID int64 `json:"id" db:"id"`
		}], ordered},
		{"decimal", keyOperators[struct {
			ID decimal.Decimal `json:"id" db:"id"`
		}], ordered},
		{"timestamp", keyOperators[struct {
			ID time.Time `json:"id" db:"id"`
		}], ordered},
		{"uuid", keyOperators[struct {
			ID googleuuid.UUID `json:"id" db:"id"`
		}], ordered},
		{"bool", keyOperators[struct {
			ID bool `json:"id" db:"id"`
		}], unordered},
		{"inet", keyOperators[struct {
			ID core.Inet `json:"id" db:"id"`
		}], unordered},
		{"cidr", keyOperators[struct {
			ID core.CIDR `json:"id" db:"id"`
		}], unordered},
		{"macaddr", keyOperators[struct {
			ID core.MACAddr `json:"id" db:"id"`
		}], unordered},
		{"interval", keyOperators[struct {
			ID core.Interval `json:"id" db:"id"`
		}], unordered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.ops(t))
		})
	}
}

// keyOperators are the operators the key field of a resource of T takes.
func keyOperators[T any](t *testing.T) []filter.FilterOperator {
	r, err := parse.NewResource[T](onSQLite, "keyed")
	require.NoError(t, err)
	key, ok := r.FilterSchema().RequestField("id")
	require.True(t, ok)
	return key.AllowedOperators
}

type inetKeyed struct {
	ID   core.Inet `json:"id"   db:"id"`
	Name string    `json:"name" db:"name" parse:"filter"`
}

// An inet key's $gt is refused as the field's operator: SQLite would
// compare its text (10.0.0.10 < 10.0.0.9) and PostgreSQL its address.
func TestKeyField_RefusesAnUnorderedTypesRange(t *testing.T) {
	r, err := parse.NewResource[inetKeyed](onSQLite, "inet_keyed")
	require.NoError(t, err)
	_, err = r.ParseListInput(parse.ListInput{Filter: `{"id":{"$gt":"10.0.0.9"}}`})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidOperator.String(), pe.Code)
	_, err = r.ParseListInput(parse.ListInput{FieldFilters: map[string][]string{"id_gt": {"10.0.0.9"}}})
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, filter.ECodeInvalidOperator.String(), pe.Code)
	_, err = r.ParseListInput(parse.ListInput{Filter: `{"id":{"$in":["10.0.0.9","10.0.0.10"]}}`})
	require.NoError(t, err, "an inet key keeps equality and $in")
}

type stockRow struct {
	WarehouseID string     `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string     `json:"sku"          db:"sku"          parse:"filter,sort"`
	TenantID    string     `json:"tenant_id"    db:"tenant_id"`
	Qty         int64      `json:"qty"          db:"qty"          parse:"filter,sort,patch"`
	CountedAt   time.Time  `json:"counted_at"   db:"counted_at"`
	DeletedAt   *time.Time `json:"deleted_at"   db:"deleted_at"`
}

func stockRows(t *testing.T, p *parse.Provider) *parse.Resource[stockRow] {
	t.Helper()
	r, err := parse.NewResource[stockRow](p, "stock_rows", parse.PrimaryKey("warehouse_id", "sku"),
		parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	return r
}

// The key's columns are declared once, each a column T reads as it is
// stored; the tenant column may be one of them.
func TestPrimaryKey_RefusesBadKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []parse.ResourceOption
		want string
	}{
		{"no column", []parse.ResourceOption{parse.PrimaryKey()}, "the key names no column"},
		{"repeated", []parse.ResourceOption{parse.PrimaryKey("sku", "sku")}, `key column "sku" is listed twice`},
		{"unknown", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "bin")}, `key column "bin" is not a column of parse_test.stockRow`},
		{"computed", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "sku"), parse.ReadExpr("sku", parse.SQL("'x'"))}, `computed column "sku"`},
		{"unread", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "sku"), parse.ReadColumns("warehouse_id", "qty")}, `the read columns omit the key column "sku"`},
		{"soft-delete column", []parse.ResourceOption{parse.PrimaryKey("warehouse_id", "sku"), parse.DeletedColumn("sku")}, `deleted column "sku" is a key column`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse.NewResource[stockRow](onSQLite, "stock_rows", tc.opts...)
			assert.ErrorContains(t, err, tc.want)
		})
	}
	type documentKeyed struct {
		ID   string         `json:"id"   db:"id"`
		Spec map[string]any `json:"spec" db:"spec"`
	}
	_, err := parse.NewResource[documentKeyed](onSQLite, "documents", parse.PrimaryKey("id", "spec"))
	require.ErrorContains(t, err, `key column "spec" is json; a key column is a scalar`)
	_, err = parse.NewResource[stockRow](onSQLite, "stock_rows", parse.PrimaryKey("warehouse_id", "counted_at"))
	require.NoError(t, err, "a timestamp is a key column")
	r, err := parse.NewResource[stockRow](onSQLite, "stock_rows", parse.PrimaryKey("tenant_id", "sku"), parse.TenantColumn("tenant_id"))
	require.NoError(t, err, "the tenant column is a key column")
	assert.Equal(t, []string{"tenant_id", "sku"}, r.PrimaryKeyColumns())
}

func TestCompositeKey_ReadsAndDeclaresTheKey(t *testing.T) {
	r := stockRows(t, onSQLite)
	assert.Equal(t, []string{"warehouse_id", "sku"}, r.PrimaryKeyColumns())
	assert.Equal(t, []string{"warehouse_id", "sku"}, r.FilterSchema().PrimaryKey)
	for _, item := range []any{stockRow{WarehouseID: "w1", SKU: "a"}, &stockRow{WarehouseID: "w1", SKU: "a"}} {
		key, err := r.Key(item)
		require.NoError(t, err)
		assert.Equal(t, parse.Key{"w1", "a"}, key)
	}
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, r))
	assert.Equal(t, []string{"warehouse_id", "sku"}, reg.SchemaFor("stock_rows").Key)
	for _, name := range []string{"warehouse_id", "sku"} {
		assert.Contains(t, reg.SchemaFor("stock_rows").Filter.Fields, name, "every key column is a request field")
	}
}

// One key is an AND of the columns' equalities, several one bound set; a
// key of another length than the key's is ErrKeyInvalid, stating both.
func TestCompositeKey_UpdateTargets(t *testing.T) {
	r := stockRows(t, onPostgres)
	p, err := r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}

	plan, err := r.BuildUpdate(p, parse.ByKey(parse.Key{"w1", "a"}), scope)
	require.NoError(t, err)
	assert.Contains(t, plan.Owner.SQL, `WHERE (("stock_rows"."tenant_id" = $2 AND "stock_rows"."deleted_at" IS NULL) AND ("stock_rows"."warehouse_id" = $3 AND "stock_rows"."sku" = $4))`)
	assert.Equal(t, []any{int64(7), "t1", "w1", "a"}, plan.Owner.Args)

	plan, err = r.BuildUpdate(p, parse.ByKeys(parse.Key{"w1", "a"}, parse.Key{"w2", "b"}), scope)
	require.NoError(t, err)
	assert.Contains(t, plan.Owner.SQL, `("stock_rows"."warehouse_id", "stock_rows"."sku") IN (SELECT "k"."warehouse_id", "k"."sku" FROM jsonb_populate_recordset(NULL::"stock_rows", $3) AS "k")`)
	assert.Len(t, plan.Owner.Args, 3, "the set binds one parameter however many keys it holds")

	for _, tc := range []struct {
		name   string
		target parse.Target
		want   string
	}{
		{"ByID", parse.ByID("w1"), "1 values for the 2 key columns (warehouse_id, sku)"},
		{"ByIDs", parse.ByIDs("w1", "w2"), "target key 0: not a value of the key column's type: 1 values for the 2 key columns (warehouse_id, sku)"},
		{"long key", parse.ByKey(parse.Key{"w1", "a", "x"}), "3 values for the 2 key columns"},
		{"mistyped column", parse.ByKey(parse.Key{"w1", 5}), "key column sku: expected string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.BuildUpdate(p, tc.target, scope)
			require.ErrorIs(t, err, parse.ErrKeyInvalid)
			assert.ErrorContains(t, err, tc.want)
		})
	}
	_, err = r.BuildUpdate(p, parse.ByKeys(), scope)
	assert.ErrorIs(t, err, parse.ErrNoTarget)
}

// stockDB is a provider on a fresh SQLite database whose stock_rows hold
// every warehouse's every SKU, so either column alone matches two rows and
// only the pair names one.
func stockDB(t *testing.T) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := providertest.SQLite(t)
	_, err := db.ExecContext(t.Context(), `CREATE TABLE stock_rows (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, tenant_id TEXT NOT NULL,
		qty INTEGER NOT NULL, counted_at TEXT, deleted_at TEXT, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	for _, row := range [][2]string{{"w1", "a"}, {"w1", "b"}, {"w2", "a"}, {"w2", "b"}} {
		_, err := db.ExecContext(t.Context(), `INSERT INTO stock_rows (warehouse_id, sku, tenant_id, qty, counted_at) VALUES (?, ?, 't1', 0, '2026-01-02T03:04:05Z')`, row[0], row[1])
		require.NoError(t, err)
	}
	return p, db
}

func stockQuantities(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT warehouse_id, sku, qty FROM stock_rows`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]int64{}
	for rows.Next() {
		var w, sku string
		var qty int64
		require.NoError(t, rows.Scan(&w, &sku, &qty))
		out[w+"/"+sku] = qty
	}
	require.NoError(t, rows.Err())
	return out
}

// An update by key, by keys and a batch each change exactly the rows their
// keys name.
func TestCompositeKey_UpdatesReachTheirRows(t *testing.T) {
	scope := parse.Scope{Tenant: "t1"}

	p, db := stockDB(t)
	r := stockRows(t, p)
	seven, err := r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	res, err := r.Update(t.Context(), seven, parse.ByKey(parse.Key{"w1", "b"}), scope)
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	assert.Equal(t, map[string]int64{"w1/a": 0, "w1/b": 7, "w2/a": 0, "w2/b": 0}, stockQuantities(t, db))

	p, db = stockDB(t)
	r = stockRows(t, p)
	seven, err = r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	res, err = r.Update(t.Context(), seven, parse.ByKeys(parse.Key{"w1", "b"}, parse.Key{"w2", "a"}, parse.Key{"w9", "a"}), scope)
	require.NoError(t, err)
	assert.Len(t, res.Changed, 2)
	assert.Equal(t, map[string]int64{"w1/a": 0, "w1/b": 7, "w2/a": 7, "w2/b": 0}, stockQuantities(t, db))

	p, db = stockDB(t)
	r = stockRows(t, p)
	seven, err = r.ParseMergePatch([]byte(`{"qty":7}`))
	require.NoError(t, err)
	one, err := r.ParseMergePatch([]byte(`{"qty":1}`))
	require.NoError(t, err)
	rows := []parse.RowUpdate{{Key: parse.Key{"w2", "a"}, Patch: one}, {Key: parse.Key{"w1", "b"}, Patch: seven}}
	batch, err := r.BuildUpdateBatch(rows, scope, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.Len(t, batch.Chunks, 1)
	assert.Equal(t, []parse.Key{{"w1", "b"}, {"w2", "a"}}, batch.Chunks[0].Keys, "rows lock in key order")
	assert.Contains(t, batch.Chunks[0].Current.SQL, `ORDER BY "stock_rows"."warehouse_id" COLLATE BINARY, "stock_rows"."sku" COLLATE BINARY`)
	res, err = r.UpdateBatch(t.Context(), rows, scope, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	assert.Len(t, res.Changed, 2)
	assert.Equal(t, map[string]int64{"w1/a": 0, "w1/b": 7, "w2/a": 1, "w2/b": 0}, stockQuantities(t, db))

	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{"w1", "b"}, Patch: one}, {Key: parse.Key{"w1", "b"}, Patch: seven}}, scope, parse.UpdateBatchLimits{})
	assert.ErrorContains(t, err, "update batch row 1 key: duplicate key")
}

// skuCode is a key whose stored value is not its Go value: the driver
// writes Value(), upper-cased, and scans the text back as it is.
type skuCode string

func (c skuCode) Value() (driver.Value, error) { return strings.ToUpper(string(c)), nil }

type skuItem struct {
	ID   skuCode  `json:"id"   db:"id"  parse:"filter,sort"`
	Qty  int64    `json:"qty"  db:"qty" parse:"patch"`
	Tags []string `json:"tags" db:"-"   parse:"patch" relation:"sku_tags,sku_id,tag_id"`
}

// A key is normalized by one rule (core.NormalizeKey) wherever it binds: a
// Valuer key stands for the value it writes, as an update's target, as the
// owner of the relation rows it writes and as a relation's member, so each
// names the row the driver stored.
func TestValuerKey_BindsAsTheValueItWrites(t *testing.T) {
	ctx := t.Context()
	provider, db := providertest.SQLite(t)
	items, err := parse.NewResource[skuItem](provider, "sku_items")
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE sku_items (id TEXT PRIMARY KEY, qty INTEGER NOT NULL)`,
		`CREATE TABLE sku_tags (sku_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (sku_id, tag_id))`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO sku_items VALUES (?, 0)`, skuCode("ab"))
	require.NoError(t, err)

	parsed, err := items.ParsePatch([]byte(`[{"op":"replace","path":"/qty","value":7},{"op":"add","path":"/tags","value":["t1"]}]`))
	require.NoError(t, err)
	res, err := items.Update(ctx, parsed, parse.ByKey(parse.Key{skuCode("ab")}), parse.Scope{})
	require.NoError(t, err)
	require.Len(t, res.Changed, 1, "the target names the stored row")

	var qty int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT qty FROM sku_items WHERE id = 'AB'`).Scan(&qty))
	assert.Equal(t, int64(7), qty)

	assert.Equal(t, []string{"AB:t1"}, dbtest.QueryRows[string](t, db, `SELECT sku_id || ':' || tag_id FROM sku_tags`), "an owner key read back binds as the target did")

	rel := &core.RelationSchema{JoinTable: "sku_tags", SourceColumns: []string{"tag_id"}, TargetColumns: []string{"sku_id"},
		SourceTypes: []core.TypeInfo{{Type: core.FieldTypeString}}, TargetTypes: []core.TypeInfo{{Type: core.FieldTypeString}}}
	members, err := patch.RenderRelationOp(patch.RelationPatchOp{Field: "items", Schema: rel, Verb: patch.RelationVerbRemove, Values: []any{skuCode("ab")}},
		filter.SQLiteDialect{}, []core.Key{{"t1"}}, patch.RenderRelationOpOptions{})
	require.NoError(t, err)
	require.Len(t, members, 1)
	removed, err := db.ExecContext(ctx, members[0].SQL, members[0].Args...)
	require.NoError(t, err)
	n, err := removed.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the member names the pair the update wrote")
}

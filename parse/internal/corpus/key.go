//go:build integration

package corpus

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/patch"
)

type ckStock struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Qty         int64  `json:"qty"          db:"qty"          parse:"filter,sort,patch"`
	Name        string `json:"name"         db:"name"         parse:"filter,sort"`
}

func ckStockResource(t *testing.T, p *parse.Provider) *parse.Resource[ckStock] {
	t.Helper()
	r, err := parse.NewResource[ckStock](p, "ck_stock", parse.PrimaryKey("warehouse_id", "sku"))
	require.NoError(t, err)
	return r
}

// ckStockDB holds every warehouse's every SKU: either key column alone, the
// quantity and the name all tie across rows, so only the whole key orders
// and names one.
func ckStockDB(t *testing.T, db *sql.DB, d filter.Dialect) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `CREATE TABLE ck_stock (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty BIGINT NOT NULL, name TEXT NOT NULL, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	insert := "INSERT INTO ck_stock VALUES (" + d.Placeholder(1) + ", " + d.Placeholder(2) + ", " + d.Placeholder(3) + ", " + d.Placeholder(4) + ")"
	i := 0
	for _, w := range []string{"w1", "w2", "w3"} {
		for _, sku := range []string{"a", "b", "c"} {
			_, err := db.ExecContext(t.Context(), insert, w, sku, int64(i%2), []string{"x", "y"}[i%3%2])
			require.NoError(t, err)
			i++
		}
	}
}

func compositeKeyPaging(t *testing.T, engine Engine) {
	runOn(t, engine, testCompositeKeyPaging)
}

// Every sort, ascending and descending, pages every row once at every page
// size, forwards and back.
func testCompositeKeyPaging(t *testing.T, b sqlBackend) {
	provider, db := b.open(t)
	r := ckStockResource(t, provider)
	ckStockDB(t, db, b.dialect)
	const rows = 9
	page := func(t *testing.T, sort string, size int, cursor string) *filter.Page[ckStock] {
		t.Helper()
		q := url.Values{filter.ParamPageSize: {strconv.Itoa(size)}}
		if sort != "" {
			q.Set(filter.ParamSort, sort)
		}
		if cursor != "" {
			q.Set(filter.ParamCursor, cursor)
		}
		req, err := r.ParseList(q)
		require.NoError(t, err)
		return listPage(t, r, req, parse.Scope{})
	}
	keyOf := func(s ckStock) string { return s.WarehouseID + "/" + s.SKU }
	for _, field := range []string{"", "warehouse_id", "sku", "qty", "name"} {
		for _, sort := range []string{field, "-" + field} {
			if sort == "-" {
				continue
			}
			want := page(t, sort, rows, "").Items
			require.Len(t, want, rows)
			distinct := map[string]bool{}
			for _, s := range want {
				distinct[keyOf(s)] = true
			}
			require.Len(t, distinct, rows, "sort %q", sort)
			for size := 1; size <= rows; size++ {
				t.Run(fmt.Sprintf("sort=%q/size=%d", sort, size), func(t *testing.T) {
					var forward []ckStock
					var last *filter.Page[ckStock]
					cursor := ""
					for range rows + 1 {
						last = page(t, sort, size, cursor)
						forward = append(forward, last.Items...)
						if !last.PageInfo.HasNextPage {
							break
						}
						cursor = string(last.PageInfo.EndCursor)
					}
					assert.Equal(t, want, forward)

					backward := []ckStock{}
					for cursor = string(last.PageInfo.StartCursor); cursor != ""; {
						prev := page(t, sort, size, cursor)
						backward = append(append([]ckStock{}, prev.Items...), backward...)
						cursor = ""
						if prev.PageInfo.HasPrevPage {
							cursor = string(prev.PageInfo.StartCursor)
						}
					}
					assert.Equal(t, want[:rows-len(last.Items)], backward, "walking back from the last page")
				})
			}
		}
	}
}

func compositeKeyUpdates(t *testing.T, engine Engine) {
	runOn(t, engine, testCompositeKeyUpdates)
}

// An update by key, by keys and a batch each write exactly the rows their
// keys name, though every row shares each key column with others.
func testCompositeKeyUpdates(t *testing.T, b sqlBackend) {
	quantities := func(t *testing.T, db *sql.DB) map[string]int64 {
		t.Helper()
		out := map[string]int64{}
		for _, row := range dbtest.QueryRows[[]any](t, db, "SELECT warehouse_id, sku, qty FROM ck_stock") {
			out[fmt.Sprint(row[0])+"/"+fmt.Sprint(row[1])] = asInt64(t, row[2])
		}
		return out
	}
	written := func(t *testing.T, db *sql.DB, before map[string]int64) []string {
		t.Helper()
		var keys []string
		for k, v := range quantities(t, db) {
			if v != before[k] {
				keys = append(keys, k)
			}
		}
		return keys
	}
	// open is a provider on a fresh ck_stock, the resource on it, and the
	// patch that sets a row's quantity to 70.
	open := func(t *testing.T) (*sql.DB, *parse.Resource[ckStock], *patch.ParsedPatch) {
		t.Helper()
		provider, db := b.open(t)
		ckStockDB(t, db, b.dialect)
		r := ckStockResource(t, provider)
		setQty, err := r.ParseMergePatch([]byte(`{"qty":70}`))
		require.NoError(t, err)
		return db, r, setQty
	}

	for _, tc := range []struct {
		name   string
		target parse.Target
		want   []string
	}{
		{"by key", parse.ByKey(parse.Key{"w2", "b"}), []string{"w2/b"}},
		{"by keys", parse.ByKeys(parse.Key{"w1", "c"}, parse.Key{"w3", "a"}, parse.Key{"w9", "a"}), []string{"w1/c", "w3/a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, r, setQty := open(t)
			before := quantities(t, db)
			res, err := applyUpdate(t, r, false, setQty, tc.target, parse.Scope{})
			require.NoError(t, err)
			assert.Len(t, res.Changed, len(tc.want))
			assert.ElementsMatch(t, tc.want, written(t, db, before))
		})
	}

	t.Run("batch", func(t *testing.T) {
		db, r, setQty := open(t)
		before := quantities(t, db)
		other, err := r.ParseMergePatch([]byte(`{"qty":80}`))
		require.NoError(t, err)
		res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{
			{Key: parse.Key{"w3", "b"}, Patch: other},
			{Key: parse.Key{"w1", "b"}, Patch: setQty},
		}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		assert.Len(t, res.Changed, 2)
		after := quantities(t, db)
		assert.ElementsMatch(t, []string{"w1/b", "w3/b"}, written(t, db, before))
		assert.Equal(t, int64(70), after["w1/b"])
		assert.Equal(t, int64(80), after["w3/b"])
	})
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

func valuerKeys(t *testing.T, engine Engine) {
	runOn(t, engine, runValuerKeys)
}

// A key is normalized by one rule (core.NormalizeKey) wherever it binds: a
// Valuer key stands for the value it writes, as an update's target, as the
// owner of the relation rows it writes and as a relation's member, so each
// names the row the driver stored.
func runValuerKeys(t *testing.T, b sqlBackend) {
	ctx := t.Context()
	provider, db := b.open(t)
	items, err := parse.NewResource[skuItem](provider, "sku_items")
	require.NoError(t, err)
	dbtest.Exec(t, db,
		`CREATE TABLE sku_items (id TEXT PRIMARY KEY, qty BIGINT NOT NULL)`,
		`CREATE TABLE sku_tags (sku_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (sku_id, tag_id))`)
	_, err = db.ExecContext(ctx, `INSERT INTO sku_items VALUES (`+b.dialect.Placeholder(1)+`, 0)`, skuCode("ab"))
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
		b.dialect, []core.Key{{"t1"}}, patch.RenderRelationOpOptions{})
	require.NoError(t, err)
	require.Len(t, members, 1)
	removed, err := db.ExecContext(ctx, members[0].SQL, members[0].Args...)
	require.NoError(t, err)
	n, err := removed.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the member names the pair the update wrote")
}

// uuidKeyed is a resource keyed by a UUID, which a request filter may range
// over.
type uuidKeyed struct {
	ID   uuid.UUID `json:"id"   db:"id"`
	Name string    `json:"name" db:"name" parse:"filter"`
}

// uuidKeys straddle the boundaries a UUID order could disagree on: a digit
// and a letter in the first position, a first byte below and above 0x80 (a
// signed comparison puts 0x80 first), and the last byte alone.
func uuidKeys() []uuid.UUID {
	keys := []uuid.UUID{
		uuid.MustParse("09000000-0000-4000-8000-000000000000"),
		uuid.MustParse("7f000000-0000-4000-8000-000000000000"),
		uuid.MustParse("80000000-0000-4000-8000-000000000000"),
		uuid.MustParse("a0000000-0000-4000-8000-000000000000"),
		uuid.MustParse("a0000000-0000-4000-8000-00000000000f"),
		uuid.MustParse("ff000000-0000-4000-8000-000000000000"),
	}
	return keys
}

type uuidKeyRange struct {
	name   string
	filter string
	want   []int
}

// uuidKeyRanges are range filters on the key, operands in either case, and
// the keys (indexes into uuidKeys) each selects in PostgreSQL's uuid order:
// the bytes', which is the canonical lowercase text's.
func uuidKeyRanges() []uuidKeyRange {
	ranges := []uuidKeyRange{
		{"gt across the sign bit", `{"id":{"$gt":"7FFFFFFF-FFFF-4FFF-BFFF-FFFFFFFFFFFF"}}`, []int{2, 3, 4, 5}},
		{"gte a key", `{"id":{"$gte":"a0000000-0000-4000-8000-000000000000"}}`, []int{3, 4, 5}},
		{"lt a letter", `{"id":{"$lt":"a0000000-0000-4000-8000-000000000000"}}`, []int{0, 1, 2}},
		{"lte the last byte", `{"id":{"$lte":"A0000000-0000-4000-8000-00000000000E"}}`, []int{0, 1, 2, 3}},
		{"a range", `{"id":{"$gt":"09000000-0000-4000-8000-000000000000","$lt":"ff000000-0000-4000-8000-000000000000"}}`, []int{1, 2, 3, 4}},
	}
	return ranges
}

// A UUID key's range selects the same rows in filter.Evaluate and on each
// backend: SQLite holds the canonical lowercase text, and PostgreSQL's uuid
// order is its bytes'.
func uuidKeyRangesCase(t *testing.T, engine Engine) {
	runOn(t, engine, runUUIDKeyRanges)
}

// runUUIDKeyRanges loads uuidKeys on b, then holds each range to select the
// same keys in SQL and in filter.Evaluate over the rows.
func runUUIDKeyRanges(t *testing.T, b sqlBackend) {
	t.Helper()
	provider, db := b.open(t)
	keys := uuidKeys()
	rows := make([][]any, len(keys))
	for i, id := range keys {
		rows[i] = []any{id, string(rune('a' + i))}
	}
	b.load(t, db, sqlTable{name: "uuid_keyed", key: []string{"id"}, columns: []sqlColumn{column[uuid.UUID]("id"), column[string]("name").required()}}, rows)
	r, err := parse.NewResource[uuidKeyed](provider, "uuid_keyed")
	require.NoError(t, err)

	for _, tc := range uuidKeyRanges() {
		t.Run(tc.name, func(t *testing.T) {
			want := make([]uuid.UUID, len(tc.want))
			for i, k := range tc.want {
				want[i] = keys[k]
			}
			req, err := r.ParseListInput(parse.ListInput{Filter: tc.filter})
			require.NoError(t, err)

			var evaluated []uuid.UUID
			for _, id := range keys {
				row := filter.ColumnRow(func(column string) (any, bool) {
					_, name, _ := strings.Cut(column, ".")
					return id, name == "id"
				})
				truth, err := filter.Evaluate(req.Filter().AST().Root, row)
				require.NoError(t, err)
				if truth == filter.True {
					evaluated = append(evaluated, id)
				}
			}
			assert.Equal(t, want, evaluated, "filter.Evaluate")

			plan, err := r.BuildList(req, parse.Scope{})
			require.NoError(t, err)
			selected := parsetest.ItemIDs(parsetest.ResourceRows(t, r, plan.Page), func(k uuidKeyed) uuid.UUID { return k.ID })
			assert.Equal(t, want, selected, provider.Name())
			assert.True(t, slices.IsSortedFunc(selected, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }), "sorted by id")
		})
	}
}

// Prices are keyed by a decimal, ticks by a timestamp and readings by a
// device and a timestamp. Every place a key is compared — a relation's
// join, a key set, an include, a facet, a batch update and a membership —
// compares it as a filter compares its type.
type ktPrice struct {
	ID   decimal.Decimal `json:"id"   db:"id"   parse:"filter,sort"`
	Name string          `json:"name" db:"name" parse:"filter,patch"`
}

type ktNote struct {
	ID      string          `json:"id"       db:"id"       parse:"filter,sort"`
	PriceID decimal.Decimal `json:"price_id" db:"price_id" parse:"filter"`
}

type ktTag struct {
	ID string `json:"id" db:"id" parse:"filter,sort"`
}

type ktTick struct {
	At   time.Time `json:"at"   db:"at"   parse:"filter,sort"`
	Name string    `json:"name" db:"name" parse:"filter,patch"`
}

type ktReading struct {
	DeviceID string    `json:"device_id" db:"device_id" parse:"filter,sort"`
	At       time.Time `json:"at"        db:"at"        parse:"filter,sort"`
	Value    int64     `json:"value"     db:"value"     parse:"filter,patch"`
}

type ktAlert struct {
	ID       string    `json:"id"        db:"id"        parse:"filter,sort"`
	DeviceID string    `json:"device_id" db:"device_id" parse:"filter"`
	At       time.Time `json:"at"        db:"at"        parse:"filter"`
}

type ktGraph struct {
	prices   *parse.Resource[ktPrice]
	notes    *parse.Resource[ktNote]
	tags     *parse.Resource[ktTag]
	ticks    *parse.Resource[ktTick]
	readings *parse.Resource[ktReading]
	alerts   *parse.Resource[ktAlert]
}

// ktResources declares the key-type resources on p and relates them.
func ktResources(t *testing.T, p *parse.Provider) ktGraph {
	t.Helper()
	g := ktGraph{
		prices:   parse.MustResource[ktPrice](p, "kt_prices"),
		notes:    parse.MustResource[ktNote](p, "kt_notes"),
		tags:     parse.MustResource[ktTag](p, "kt_tags"),
		ticks:    parse.MustResource[ktTick](p, "kt_ticks", parse.PrimaryKey("at")),
		readings: parse.MustResource[ktReading](p, "kt_readings", parse.PrimaryKey("device_id", "at")),
		alerts:   parse.MustResource[ktAlert](p, "kt_alerts"),
	}
	all := []parse.RelationOption{parse.Indexed(), parse.Includable(), parse.Facetable()}
	writable := slices.Concat(all, []parse.RelationOption{parse.Writable()})
	reading := []string{"device_id", "at"}
	require.NoError(t, g.prices.Relate("notes", parse.HasMany(g.notes, "price_id", all...)))
	require.NoError(t, g.prices.Relate("tags", parse.ManyToMany(g.tags, parse.Through("kt_price_tags", "price_id", "tag_id"), writable...)))
	require.NoError(t, g.notes.Relate("price", parse.BelongsTo(g.prices, "price_id")))
	require.NoError(t, g.tags.Relate("prices", parse.ManyToMany(g.prices, parse.Through("kt_price_tags", "tag_id", "price_id"), writable...)))
	require.NoError(t, g.ticks.Relate("tags", parse.ManyToMany(g.tags, parse.Through("kt_tick_tags", "tick_at", "tag_id"), writable...)))
	require.NoError(t, g.tags.Relate("ticks", parse.ManyToMany(g.ticks, parse.Through("kt_tick_tags", "tag_id", "tick_at"), writable...)))
	require.NoError(t, g.readings.Relate("alerts", parse.HasMany(g.alerts, reading, all...)))
	require.NoError(t, g.readings.Relate("tags", parse.ManyToMany(g.tags, parse.Through("kt_reading_tags", reading, "tag_id"), writable...)))
	require.NoError(t, g.alerts.Relate("reading", parse.BelongsTo(g.readings, reading)))
	require.NoError(t, g.tags.Relate("readings", parse.ManyToMany(g.readings, parse.Through("kt_reading_tags", "tag_id", reading), writable...)))
	return g
}

// ktInstants are the instants the fixture keys rows by: t1 a microsecond,
// PostgreSQL's precision, after t0, and t2 a second after it.
func ktInstants() (t0, t1, t2 time.Time) {
	t0 = time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	return t0, t0.Add(time.Microsecond), t0.Add(time.Second)
}

// ktText is a timestamp as a key's text names it (core.KeyTextOf).
func ktText(at time.Time) string { return core.FormatTimestamp(at) }

// ktReadingText is a reading's key as a key's text names it.
func ktReadingText(device string, at time.Time) string {
	return `["` + device + `","` + ktText(at) + `"]`
}

// ktFixture stores decimals under other spellings than their canonical
// text — "1.50" for 1.5, "2.00" for 2 — in text columns on SQLite and in
// numeric columns of two scales on PostgreSQL, and two 20-digit keys one
// apart, which a float64 holds as one. Timestamps are stored as each
// backend stores them (Dialect.MarshalValue); t1 is t0 and a microsecond.
func ktFixture(t *testing.T, db *sql.DB, b sqlBackend) {
	t.Helper()
	d := b.dialect
	ktT0, ktT1, ktT2 := ktInstants()
	price, scaled, at := "TEXT", "TEXT", "TEXT"
	if b.engine == Postgres {
		price, scaled, at = "NUMERIC(30,2)", "NUMERIC", "TIMESTAMPTZ"
	}
	for _, stmt := range []string{
		`CREATE TABLE kt_prices (id ` + price + ` PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE kt_notes (id TEXT PRIMARY KEY, price_id ` + scaled + ` NOT NULL)`,
		`CREATE TABLE kt_tags (id TEXT PRIMARY KEY)`,
		`CREATE TABLE kt_price_tags (price_id ` + scaled + ` NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (price_id, tag_id))`,
		`CREATE TABLE kt_allowed_prices (price_id ` + scaled + ` NOT NULL)`,
		`CREATE TABLE kt_ticks (at ` + at + ` PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE kt_tick_tags (tick_at ` + at + ` NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (tick_at, tag_id))`,
		`CREATE TABLE kt_readings (device_id TEXT NOT NULL, at ` + at + ` NOT NULL, value BIGINT NOT NULL, PRIMARY KEY (device_id, at))`,
		`CREATE TABLE kt_alerts (id TEXT PRIMARY KEY, device_id TEXT NOT NULL, at ` + at + ` NOT NULL)`,
		`CREATE TABLE kt_reading_tags (device_id TEXT NOT NULL, at ` + at + ` NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (device_id, at, tag_id))`,
		`CREATE TABLE kt_allowed_at (at ` + at + ` NOT NULL)`,
		`INSERT INTO kt_prices VALUES ('1.50', 'one'), ('2.00', 'two'), ('12345678901234567890', 'big'), ('12345678901234567891', 'bigger')`,
		`INSERT INTO kt_notes VALUES ('n1', '1.5'), ('n2', '12345678901234567890'), ('n3', '1.500'), ('n4', '12345678901234567891')`,
		`INSERT INTO kt_tags VALUES ('red'), ('blue'), ('green')`,
		`INSERT INTO kt_price_tags VALUES ('1.5', 'red'), ('1.50', 'blue'), ('2', 'red'), ('12345678901234567890.0', 'blue'), ('12345678901234567891', 'green')`,
		`INSERT INTO kt_allowed_prices VALUES ('1.5000'), ('12345678901234567890')`,
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
	stamp := func(at time.Time) any {
		v, err := d.MarshalValue(at, core.TypeInfo{Type: core.FieldTypeTimestamp})
		require.NoError(t, err)
		return v
	}
	ph := d.Placeholder
	for _, row := range []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO kt_ticks VALUES (` + ph(1) + `, 'zero'), (` + ph(2) + `, 'one'), (` + ph(3) + `, 'two')`, []any{stamp(ktT0), stamp(ktT1), stamp(ktT2)}},
		{`INSERT INTO kt_tick_tags VALUES (` + ph(1) + `, 'red'), (` + ph(2) + `, 'blue')`, []any{stamp(ktT0), stamp(ktT1)}},
		{`INSERT INTO kt_readings VALUES ('d1', ` + ph(1) + `, 1), ('d1', ` + ph(2) + `, 2), ('d2', ` + ph(3) + `, 3)`, []any{stamp(ktT0), stamp(ktT1), stamp(ktT0)}},
		{`INSERT INTO kt_alerts VALUES ('a1', 'd1', ` + ph(1) + `), ('a2', 'd1', ` + ph(2) + `), ('a3', 'd2', ` + ph(3) + `), ('a4', 'd2', ` + ph(4) + `)`, []any{stamp(ktT0), stamp(ktT1), stamp(ktT0), stamp(ktT1)}},
		{`INSERT INTO kt_reading_tags VALUES ('d1', ` + ph(1) + `, 'red'), ('d1', ` + ph(2) + `, 'blue'), ('d2', ` + ph(3) + `, 'red')`, []any{stamp(ktT0), stamp(ktT1), stamp(ktT0)}},
		{`INSERT INTO kt_allowed_at VALUES (` + ph(1) + `)`, []any{stamp(ktT1)}},
	} {
		_, err := db.ExecContext(t.Context(), row.stmt, row.args...)
		require.NoError(t, err, row.stmt)
	}
}

func keyTypes(t *testing.T, engine Engine) {
	runOn(t, engine, testKeyTypes)
}

func testKeyTypes(t *testing.T, b sqlBackend) {
	d := b.dialect
	ktT0, ktT1, ktT2 := ktInstants()
	// setup is a fresh fixture database and the resources on its provider.
	setup := func(t *testing.T) (*sql.DB, ktGraph) {
		t.Helper()
		provider, db := b.open(t)
		ktFixture(t, db, b)
		g := ktResources(t, provider)
		return db, g
	}
	db, g := setup(t)
	// An instant in another zone, a microsecond after t0: t1.
	t1Elsewhere := ktT1.In(time.FixedZone("", 3600)).Format(time.RFC3339Nano)

	t.Run("relations", func(t *testing.T) {
		for _, tt := range []struct {
			name, filter string
			list         func(t *testing.T, doc string) []string
			want         []string
		}{
			{name: "decimal pair table", filter: `{"tags":"blue"}`, list: crxList(g.prices, ktPriceKey), want: []string{"1.5", "12345678901234567890"}},
			{name: "decimal has-many", filter: `{"notes":{"$some":{"id":"n3"}}}`, list: crxList(g.prices, ktPriceKey), want: []string{"1.5"}},
			{name: "decimal has-many $none", filter: `{"notes":{"$none":{}}}`, list: crxList(g.prices, ktPriceKey), want: []string{"2"}},
			{name: "20-digit belongs-to", filter: `{"price":{"$is":{"name":"big"}}}`, list: crxList(g.notes, ktNoteKey), want: []string{"n2"}},
			{name: "decimal pair table target", filter: `{"prices":{"$some":{"name":"one"}}}`, list: crxList(g.tags, crxTagKeyOf), want: []string{"blue", "red"}},
			{name: "decimal key set", filter: `{"prices":{"$in":["1.500","12345678901234567891"]}}`, list: crxList(g.tags, crxTagKeyOf), want: []string{"blue", "green", "red"}},
			{name: "20-digit key set", filter: `{"prices":"12345678901234567891"}`, list: crxList(g.tags, crxTagKeyOf), want: []string{"green"}},
			{name: "timestamp pair table", filter: `{"tags":"blue"}`, list: crxList(g.ticks, ktTickKey), want: []string{ktText(ktT1)}},
			{name: "timestamp key set", filter: `{"ticks":{"$in":["` + t1Elsewhere + `"]}}`, list: crxList(g.tags, crxTagKeyOf), want: []string{"blue"}},
			{name: "composite has-many", filter: `{"alerts":{"$some":{"id":"a2"}}}`, list: crxList(g.readings, ktReadingKey), want: []string{"d1/" + ktText(ktT1)}},
			{name: "composite pair table", filter: `{"tags":"red"}`, list: crxList(g.readings, ktReadingKey), want: []string{"d1/" + ktText(ktT0), "d2/" + ktText(ktT0)}},
			{name: "composite belongs-to $null", filter: `{"reading":{"$null":true}}`, list: crxList(g.alerts, ktAlertKey), want: []string{"a4"}},
			{name: "composite pair table target", filter: `{"readings":{"$some":{"device_id":"d2"}}}`, list: crxList(g.tags, crxTagKeyOf), want: []string{"red"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, tt.list(t, tt.filter))
			})
		}
	})

	t.Run("resolved outside SQL", func(t *testing.T) {
		store, lookups := resolvedStores(t, db, d, "kt_prices", "kt_notes", "kt_tags", "kt_price_tags",
			"kt_ticks", "kt_tick_tags", "kt_readings", "kt_alerts", "kt_reading_tags")
		assertResolvedFilters(t, store, lookups, g.prices,
			`{"tags":"blue"}`, `{"notes":{"$some":{"id":"n3"}}}`, `{"notes":{"$none":{}}}`)
		assertResolvedFilters(t, store, lookups, g.notes, `{"price":{"$is":{"name":"big"}}}`)
		assertResolvedFilters(t, store, lookups, g.tags,
			`{"prices":{"$some":{"name":"one"}}}`, `{"prices":{"$in":["1.500","12345678901234567891"]}}`, `{"prices":"12345678901234567891"}`,
			`{"ticks":{"$in":["`+t1Elsewhere+`"]}}`, `{"readings":{"$some":{"device_id":"d2"}}}`, `{"readings":{"$none":{"at":"`+ktText(ktT1)+`"}}}`)
		assertResolvedFilters(t, store, lookups, g.ticks, `{"tags":"blue"}`)
		assertResolvedFilters(t, store, lookups, g.readings,
			`{"alerts":{"$some":{"id":"a2"}}}`, `{"tags":"red"}`, `{"tags":{"$ne":"red"}}`)
		assertResolvedFilters(t, store, lookups, g.alerts, `{"reading":{"$null":true}}`, `{"reading":{"$is":{"value":2}}}`)
		assertResolvedIncludes(t, lookups, g.prices, "notes", "tags")
		assertResolvedIncludes(t, lookups, g.tags, "prices", "ticks", "readings")
		assertResolvedIncludes(t, lookups, g.ticks, "tags")
		assertResolvedIncludes(t, lookups, g.readings, "alerts", "tags")
	})

	t.Run("includes and facets", func(t *testing.T) {
		prices := crxPage(t, g.prices, url.Values{filter.ParamInclude: {"notes,tags"}, filter.ParamFacets: {"tags"}})
		assert.Equal(t, map[string][]string{"1.5": {"n1", "n3"}, "12345678901234567890": {"n2"}, "12345678901234567891": {"n4"}}, prices.Relations["notes"])
		assert.Equal(t, map[string][]string{
			"1.5": {"blue", "red"}, "2": {"red"}, "12345678901234567890": {"blue"}, "12345678901234567891": {"green"},
		}, prices.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 2}, {Key: "red", Count: 2}, {Key: "green", Count: 1}}, prices.Facets["tags"])

		tags := crxPage(t, g.tags, url.Values{filter.ParamInclude: {"prices,ticks,readings"}, filter.ParamFacets: {"prices,ticks,readings"}})
		assert.Equal(t, map[string][]string{
			"blue": {"1.5", "12345678901234567890"}, "green": {"12345678901234567891"}, "red": {"1.5", "2"},
		}, tags.Relations["prices"])
		assert.ElementsMatch(t, []filter.FacetBucket{
			{Key: "1.5", Count: 2}, {Key: "2", Count: 1}, {Key: "12345678901234567890", Count: 1}, {Key: "12345678901234567891", Count: 1},
		}, tags.Facets["prices"])
		assert.Equal(t, map[string][]string{"blue": {ktText(ktT1)}, "red": {ktText(ktT0)}}, tags.Relations["ticks"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: ktText(ktT0), Count: 1}, {Key: ktText(ktT1), Count: 1}}, tags.Facets["ticks"])
		assert.Equal(t, map[string][]string{
			"blue": {ktReadingText("d1", ktT1)}, "red": {ktReadingText("d1", ktT0), ktReadingText("d2", ktT0)},
		}, tags.Relations["readings"])
		assert.ElementsMatch(t, []filter.FacetBucket{
			{Key: ktReadingText("d1", ktT0), Count: 1}, {Key: ktReadingText("d1", ktT1), Count: 1}, {Key: ktReadingText("d2", ktT0), Count: 1},
		}, tags.Facets["readings"])

		ticks := crxPage(t, g.ticks, url.Values{filter.ParamInclude: {"tags"}, filter.ParamFacets: {"tags"}})
		assert.Equal(t, map[string][]string{ktText(ktT0): {"red"}, ktText(ktT1): {"blue"}}, ticks.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 1}, {Key: "red", Count: 1}}, ticks.Facets["tags"])

		readings := crxPage(t, g.readings, url.Values{filter.ParamInclude: {"alerts,tags"}, filter.ParamFacets: {"tags,alerts"}})
		assert.Equal(t, map[string][]string{
			ktReadingText("d1", ktT0): {"a1"}, ktReadingText("d1", ktT1): {"a2"}, ktReadingText("d2", ktT0): {"a3"},
		}, readings.Relations["alerts"])
		assert.Equal(t, map[string][]string{
			ktReadingText("d1", ktT0): {"red"}, ktReadingText("d1", ktT1): {"blue"}, ktReadingText("d2", ktT0): {"red"},
		}, readings.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 1}, {Key: "red", Count: 2}}, readings.Facets["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "a1", Count: 1}, {Key: "a2", Count: 1}, {Key: "a3", Count: 1}}, readings.Facets["alerts"])

		// The include names each owner by its key's text (core.KeyTextOf).
		for _, item := range readings.Items {
			key, err := g.readings.Key(&item)
			require.NoError(t, err)
			text, err := core.KeyTextOf([]core.TypeInfo{{Type: core.FieldTypeString}, {Type: core.FieldTypeTimestamp}}, key)
			require.NoError(t, err)
			assert.Contains(t, readings.Relations["tags"], text)
		}
	})

	t.Run("memberships", func(t *testing.T) {
		member := func(field, table, project string) parse.Scope {
			scope := parse.Scope{Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{
				{Field: field, Member: &filter.Lookup{Table: table, Project: project}},
			}}}
			return scope
		}
		assert.Equal(t, []string{"1.5", "12345678901234567890"}, ktKeys(t, g.prices, member("id", "kt_allowed_prices", "price_id"), ktPriceKey))
		assert.Equal(t, []string{"d1/" + ktText(ktT1)}, ktKeys(t, g.readings, member("at", "kt_allowed_at", "at"), ktReadingKey))
		assert.Equal(t, []string{ktText(ktT1)}, ktKeys(t, g.ticks, member("at", "kt_allowed_at", "at"), ktTickKey))
	})

	t.Run("batch updates", func(t *testing.T) {
		_, g := setup(t)
		prices := ktBatch(t, g.prices, []parse.RowUpdate{
			{Key: parse.Key{"12345678901234567890"}, Patch: ktPatch(t, g.prices, `{"name":"b"}`)},
			{Key: parse.Key{1.5}, Patch: ktPatch(t, g.prices, `{"name":"a"}`)},
		}, ktPriceKey)
		assert.Equal(t, []string{"1.5", "12345678901234567890"}, prices)
		assert.Equal(t, []string{"1.5=a", "2=two", "12345678901234567890=b", "12345678901234567891=bigger"},
			ktNames(t, g.prices, func(p ktPrice) string { return ktPriceKey(p) + "=" + p.Name }))

		readings := ktBatch(t, g.readings, []parse.RowUpdate{
			{Key: parse.Key{"d2", ktT0}, Patch: ktPatch(t, g.readings, `{"value":8}`)},
			{Key: parse.Key{"d1", t1Elsewhere}, Patch: ktPatch(t, g.readings, `{"value":7}`)},
		}, ktReadingKey)
		assert.Equal(t, []string{"d1/" + ktText(ktT1), "d2/" + ktText(ktT0)}, readings)
		assert.Equal(t, []string{"d1/" + ktText(ktT0) + "=1", "d1/" + ktText(ktT1) + "=7", "d2/" + ktText(ktT0) + "=8"},
			ktNames(t, g.readings, func(r ktReading) string { return ktReadingKey(r) + "=" + fmt.Sprint(r.Value) }))

		ticks := ktBatch(t, g.ticks, []parse.RowUpdate{
			{Key: parse.Key{ktT2.In(time.FixedZone("", 7200))}, Patch: ktPatch(t, g.ticks, `{"name":"deux"}`)},
		}, ktTickKey)
		assert.Equal(t, []string{ktText(ktT2)}, ticks)

		// PostgreSQL holds microseconds and rounds a finer key to them, so a
		// key 400ns past t0 names t0's row there and no row on SQLite.
		finer := []parse.RowUpdate{{Key: parse.Key{ktT0.Add(400 * time.Nanosecond)}, Patch: ktPatch(t, g.ticks, `{"name":"zéro"}`)}}
		res, err := g.ticks.UpdateBatch(t.Context(), finer, parse.Scope{}, parse.UpdateBatchLimits{})
		if b.engine == Postgres {
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)
			assert.Equal(t, ktText(ktT0), ktTickKey(res.Changed[0]))
		} else {
			assert.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
		}
	})

	// A batch locks its rows in the order its Current statement reads them,
	// which is the order its keys are chunked in: decimals by value, not by
	// their text, and timestamps by instant.
	t.Run("batch lock order", func(t *testing.T) {
		db, g := setup(t)
		rename := func(r parse.Key) parse.RowUpdate {
			update := parse.RowUpdate{Key: r, Patch: ktPatch(t, g.prices, `{"name":"n"}`)}
			return update
		}
		prices, err := g.prices.BuildUpdateBatch([]parse.RowUpdate{
			rename(parse.Key{"12345678901234567891"}), rename(parse.Key{"2"}), rename(parse.Key{"12345678901234567890"}), rename(parse.Key{"1.50"}),
		}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		require.Len(t, prices.Chunks, 1)
		chunk := prices.Chunks[0]
		want := []string{"1.5", "2", "12345678901234567890", "12345678901234567891"}
		assert.Equal(t, want, ktChunkKeys(t, chunk.Keys))
		assert.Equal(t, want, ktCurrentKeys(t, db, chunk.Current, 1))

		revalue := func(r parse.Key) parse.RowUpdate {
			update := parse.RowUpdate{Key: r, Patch: ktPatch(t, g.readings, `{"value":0}`)}
			return update
		}
		readings, err := g.readings.BuildUpdateBatch([]parse.RowUpdate{
			revalue(parse.Key{"d2", ktT0}), revalue(parse.Key{"d1", t1Elsewhere}), revalue(parse.Key{"d1", ktT0}),
		}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		require.Len(t, readings.Chunks, 1)
		chunk = readings.Chunks[0]
		want = []string{"d1/" + ktText(ktT0), "d1/" + ktText(ktT1), "d2/" + ktText(ktT0)}
		assert.Equal(t, want, ktChunkKeys(t, chunk.Keys))
		assert.Equal(t, want, ktCurrentKeys(t, db, chunk.Current, 2))
	})

	t.Run("updates by keys", func(t *testing.T) {
		_, g := setup(t)
		crxUpdate(t, g.readings, `{"value":9}`, parse.ByKeys(parse.Key{"d1", t1Elsewhere}, parse.Key{"d2", ktT0}))
		assert.Equal(t, []string{"d1/" + ktText(ktT0) + "=1", "d1/" + ktText(ktT1) + "=9", "d2/" + ktText(ktT0) + "=9"},
			ktNames(t, g.readings, func(r ktReading) string { return ktReadingKey(r) + "=" + fmt.Sprint(r.Value) }))
		crxUpdate(t, g.prices, `{"name":"x"}`, parse.ByKeys(parse.Key{"1.500"}, parse.Key{"12345678901234567891"}))
		assert.Equal(t, []string{"1.5=x", "2=two", "12345678901234567890=big", "12345678901234567891=x"},
			ktNames(t, g.prices, func(p ktPrice) string { return ktPriceKey(p) + "=" + p.Name }))
	})

	t.Run("relation writes", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			update func(t *testing.T, g ktGraph)
			pairs  string
			want   []string
		}{
			{
				// 1.5 is red's under another spelling; 2 leaves.
				name: "decimal targets replace",
				update: func(t *testing.T, g ktGraph) {
					crxUpdate(t, g.tags, `{"prices":["1.50","12345678901234567891"]}`, parse.ByID("red"))
				},
				pairs: "SELECT price_id, tag_id FROM kt_price_tags",
				want:  []string{"1.5/blue", "1.5/red", "12345678901234567890/blue", "12345678901234567891/green", "12345678901234567891/red"},
			},
			{
				name: "decimal owner adds what it holds under another spelling",
				update: func(t *testing.T, g ktGraph) {
					crxUpdatePatch(t, g.prices, `[{"op":"add","path":"/tags","value":["red","green"]}]`, parse.ByKey(parse.Key{"1.5"}))
				},
				pairs: "SELECT price_id, tag_id FROM kt_price_tags",
				want:  []string{"1.5/blue", "1.5/green", "1.5/red", "2/red", "12345678901234567890/blue", "12345678901234567891/green"},
			},
			{
				name: "decimal targets remove",
				update: func(t *testing.T, g ktGraph) {
					crxUpdatePatch(t, g.tags, `[{"op":"remove","path":"/prices","value":["1.5","12345678901234567890"]}]`, parse.ByID("blue"))
				},
				pairs: "SELECT price_id, tag_id FROM kt_price_tags",
				want:  []string{"1.5/red", "2/red", "12345678901234567891/green"},
			},
			{
				name: "composite targets replace",
				update: func(t *testing.T, g ktGraph) {
					crxUpdate(t, g.tags, `{"readings":[["d1","`+t1Elsewhere+`"],["d2","`+ktText(ktT0)+`"]]}`, parse.ByID("red"))
				},
				pairs: "SELECT device_id, at, tag_id FROM kt_reading_tags",
				want:  []string{"d1/" + ktText(ktT1) + "/blue", "d1/" + ktText(ktT1) + "/red", "d2/" + ktText(ktT0) + "/red"},
			},
			{
				name: "timestamp owner adds",
				update: func(t *testing.T, g ktGraph) {
					crxUpdatePatch(t, g.ticks, `[{"op":"add","path":"/tags","value":["green"]}]`, parse.ByKey(parse.Key{t1Elsewhere}))
				},
				pairs: "SELECT tick_at, tag_id FROM kt_tick_tags",
				want:  []string{ktText(ktT0) + "/red", ktText(ktT1) + "/blue", ktText(ktT1) + "/green"},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				db, g := setup(t)
				tt.update(t, g)
				assert.ElementsMatch(t, tt.want, ktPairs(t, db, tt.pairs))
			})
		}
	})
}

func ktPriceKey(p ktPrice) string { return p.ID.String() }

func ktNoteKey(n ktNote) string { return n.ID }

func ktTickKey(k ktTick) string { return ktText(k.At) }

func ktReadingKey(r ktReading) string { return r.DeviceID + "/" + ktText(r.At) }

func ktAlertKey(a ktAlert) string { return a.ID }

func crxTagKeyOf(t ktTag) string { return t.ID }

// ktKeys lists the rows of r a scope admits, each as key names it.
func ktKeys[T any](t *testing.T, r *parse.Resource[T], scope parse.Scope, key func(T) string) []string {
	t.Helper()
	req, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	var out []string
	for _, item := range listPage(t, r, req, scope).Items {
		out = append(out, key(item))
	}
	return out
}

// ktNames lists every row of r, each as name writes it, in key order.
func ktNames[T any](t *testing.T, r *parse.Resource[T], name func(T) string) []string {
	t.Helper()
	return ktKeys(t, r, parse.Scope{}, name)
}

func ktPatch[T any](t *testing.T, r *parse.Resource[T], raw string) *patch.ParsedPatch {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(raw))
	require.NoError(t, err)
	return p
}

// ktBatch applies one update batch and returns the changed rows' keys, as
// key names them, in the order the batch returns them.
func ktBatch[T any](t *testing.T, r *parse.Resource[T], rows []parse.RowUpdate, key func(T) string) []string {
	t.Helper()
	res, err := r.UpdateBatch(t.Context(), rows, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	var out []string
	for _, row := range res.Changed {
		out = append(out, key(row))
	}
	return out
}

// ktPairs reads a pair table's rows, each column read as the key it holds
// and joined by "/": a decimal by value, a timestamp as its canonical text.
func ktPairs(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	var out []string
	for _, row := range dbtest.QueryRows[[]any](t, db, query) {
		out = append(out, ktJoin(t, row))
	}
	return out
}

// ktChunkKeys are a batch chunk's keys as ktPairs writes a row's.
func ktChunkKeys(t *testing.T, keys []parse.Key) []string {
	t.Helper()
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = ktJoin(t, k)
	}
	return out
}

// ktCurrentKeys runs a chunk's Current statement and returns the keys of
// the rows it reads, in its order, the key being the first width columns.
func ktCurrentKeys(t *testing.T, db *sql.DB, current parse.Statement, width int) []string {
	t.Helper()
	var out []string
	for _, row := range dbtest.QueryRows[[]any](t, db, current.SQL, current.Args...) {
		out = append(out, ktJoin(t, row[:width]))
	}
	return out
}

func ktJoin(t *testing.T, values []any) string {
	t.Helper()
	var text strings.Builder
	for i, v := range values {
		if i > 0 {
			text.WriteString("/")
		}
		text.WriteString(ktPairValue(t, v))
	}
	return text.String()
}

func ktPairValue(t *testing.T, v any) string {
	t.Helper()
	switch x := v.(type) {
	case time.Time:
		return ktText(x)
	case []byte:
		v = string(x)
	}
	s := fmt.Sprint(v)
	d, err := decimal.NewFromString(s)
	if err == nil {
		return d.String()
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return ktText(at)
	}
	return s
}

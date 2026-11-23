//go:build integration

package parse_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

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

func ktResources(t *testing.T) ktGraph {
	t.Helper()
	g := ktGraph{
		prices:   parse.MustResource[ktPrice]("kt_prices"),
		notes:    parse.MustResource[ktNote]("kt_notes"),
		tags:     parse.MustResource[ktTag]("kt_tags"),
		ticks:    parse.MustResource[ktTick]("kt_ticks", parse.PrimaryKey("at")),
		readings: parse.MustResource[ktReading]("kt_readings", parse.PrimaryKey("device_id", "at")),
		alerts:   parse.MustResource[ktAlert]("kt_alerts"),
	}
	all := []parse.RelationOption{parse.Indexed(), parse.Includable(), parse.Facetable()}
	writable := append(all, parse.Writable())
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

var (
	ktT0 = time.Date(2026, time.January, 2, 3, 4, 5, 123456000, time.UTC)
	ktT1 = ktT0.Add(time.Microsecond)
	ktT2 = ktT0.Add(time.Second)
)

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
func ktFixture(t *testing.T, db *sql.DB, d filter.Dialect) {
	t.Helper()
	price, scaled, at := "TEXT", "TEXT", "TEXT"
	if isPostgres(d) {
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

func TestKeyTypes_SQLite(t *testing.T) {
	testKeyTypes(t, func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{})
}

func TestKeyTypes_Postgres(t *testing.T) {
	testKeyTypes(t, func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{})
}

func testKeyTypes(t *testing.T, open func(*testing.T) *sql.DB, d filter.Dialect) {
	g := ktResources(t)
	setup := func(t *testing.T) *sql.DB {
		t.Helper()
		db := open(t)
		ktFixture(t, db, d)
		return db
	}
	db := setup(t)
	// An instant in another zone, a microsecond after t0: t1.
	t1Elsewhere := ktT1.In(time.FixedZone("", 3600)).Format(time.RFC3339Nano)

	t.Run("relations", func(t *testing.T) {
		for _, tt := range []struct {
			name, filter string
			list         func(t *testing.T, doc string) []string
			want         []string
		}{
			{name: "decimal pair table", filter: `{"tags":"blue"}`, list: crxList(db, d, g.prices, ktPriceKey), want: []string{"1.5", "12345678901234567890"}},
			{name: "decimal has-many", filter: `{"notes":{"$some":{"id":"n3"}}}`, list: crxList(db, d, g.prices, ktPriceKey), want: []string{"1.5"}},
			{name: "decimal has-many $none", filter: `{"notes":{"$none":{}}}`, list: crxList(db, d, g.prices, ktPriceKey), want: []string{"2"}},
			{name: "20-digit belongs-to", filter: `{"price":{"$is":{"name":"big"}}}`, list: crxList(db, d, g.notes, ktNoteKey), want: []string{"n2"}},
			{name: "decimal pair table target", filter: `{"prices":{"$some":{"name":"one"}}}`, list: crxList(db, d, g.tags, crxTagKeyOf), want: []string{"blue", "red"}},
			{name: "decimal key set", filter: `{"prices":{"$in":["1.500","12345678901234567891"]}}`, list: crxList(db, d, g.tags, crxTagKeyOf), want: []string{"blue", "green", "red"}},
			{name: "20-digit key set", filter: `{"prices":"12345678901234567891"}`, list: crxList(db, d, g.tags, crxTagKeyOf), want: []string{"green"}},
			{name: "timestamp pair table", filter: `{"tags":"blue"}`, list: crxList(db, d, g.ticks, ktTickKey), want: []string{ktText(ktT1)}},
			{name: "timestamp key set", filter: `{"ticks":{"$in":["` + t1Elsewhere + `"]}}`, list: crxList(db, d, g.tags, crxTagKeyOf), want: []string{"blue"}},
			{name: "composite has-many", filter: `{"alerts":{"$some":{"id":"a2"}}}`, list: crxList(db, d, g.readings, ktReadingKey), want: []string{"d1/" + ktText(ktT1)}},
			{name: "composite pair table", filter: `{"tags":"red"}`, list: crxList(db, d, g.readings, ktReadingKey), want: []string{"d1/" + ktText(ktT0), "d2/" + ktText(ktT0)}},
			{name: "composite belongs-to $null", filter: `{"reading":{"$null":true}}`, list: crxList(db, d, g.alerts, ktAlertKey), want: []string{"a4"}},
			{name: "composite pair table target", filter: `{"readings":{"$some":{"device_id":"d2"}}}`, list: crxList(db, d, g.tags, crxTagKeyOf), want: []string{"red"}},
		} {
			t.Run(tt.name, func(t *testing.T) {
				assert.Equal(t, tt.want, tt.list(t, tt.filter))
			})
		}
	})

	t.Run("includes and facets", func(t *testing.T) {
		prices := crxPage(t, db, d, g.prices, url.Values{filter.ParamInclude: {"notes,tags"}, filter.ParamFacets: {"tags"}})
		assert.Equal(t, map[string][]string{"1.5": {"n1", "n3"}, "12345678901234567890": {"n2"}, "12345678901234567891": {"n4"}}, prices.Relations["notes"])
		assert.Equal(t, map[string][]string{
			"1.5": {"blue", "red"}, "2": {"red"}, "12345678901234567890": {"blue"}, "12345678901234567891": {"green"},
		}, prices.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 2}, {Key: "red", Count: 2}, {Key: "green", Count: 1}}, prices.Facets["tags"])

		tags := crxPage(t, db, d, g.tags, url.Values{filter.ParamInclude: {"prices,ticks,readings"}, filter.ParamFacets: {"prices,ticks,readings"}})
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

		ticks := crxPage(t, db, d, g.ticks, url.Values{filter.ParamInclude: {"tags"}, filter.ParamFacets: {"tags"}})
		assert.Equal(t, map[string][]string{ktText(ktT0): {"red"}, ktText(ktT1): {"blue"}}, ticks.Relations["tags"])
		assert.ElementsMatch(t, []filter.FacetBucket{{Key: "blue", Count: 1}, {Key: "red", Count: 1}}, ticks.Facets["tags"])

		readings := crxPage(t, db, d, g.readings, url.Values{filter.ParamInclude: {"alerts,tags"}, filter.ParamFacets: {"tags,alerts"}})
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
			return parse.Scope{Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{
				{Field: field, Member: &filter.Lookup{Table: table, Project: project}},
			}}}
		}
		assert.Equal(t, []string{"1.5", "12345678901234567890"}, ktKeys(t, db, d, g.prices, member("id", "kt_allowed_prices", "price_id"), ktPriceKey))
		assert.Equal(t, []string{"d1/" + ktText(ktT1)}, ktKeys(t, db, d, g.readings, member("at", "kt_allowed_at", "at"), ktReadingKey))
		assert.Equal(t, []string{ktText(ktT1)}, ktKeys(t, db, d, g.ticks, member("at", "kt_allowed_at", "at"), ktTickKey))
	})

	t.Run("batch updates", func(t *testing.T) {
		db := setup(t)
		prices := ktBatch(t, db, d, g.prices, []parse.RowUpdate{
			{Key: parse.Key{"12345678901234567890"}, Patch: ktPatch(t, g.prices, `{"name":"b"}`)},
			{Key: parse.Key{1.5}, Patch: ktPatch(t, g.prices, `{"name":"a"}`)},
		}, ktPriceKey)
		assert.Equal(t, []string{"1.5", "12345678901234567890"}, prices)
		assert.Equal(t, []string{"1.5=a", "2=two", "12345678901234567890=b", "12345678901234567891=bigger"},
			ktNames(t, db, d, g.prices, func(p ktPrice) string { return ktPriceKey(p) + "=" + p.Name }))

		readings := ktBatch(t, db, d, g.readings, []parse.RowUpdate{
			{Key: parse.Key{"d2", ktT0}, Patch: ktPatch(t, g.readings, `{"value":8}`)},
			{Key: parse.Key{"d1", t1Elsewhere}, Patch: ktPatch(t, g.readings, `{"value":7}`)},
		}, ktReadingKey)
		assert.Equal(t, []string{"d1/" + ktText(ktT1), "d2/" + ktText(ktT0)}, readings)
		assert.Equal(t, []string{"d1/" + ktText(ktT0) + "=1", "d1/" + ktText(ktT1) + "=7", "d2/" + ktText(ktT0) + "=8"},
			ktNames(t, db, d, g.readings, func(r ktReading) string { return ktReadingKey(r) + "=" + fmt.Sprint(r.Value) }))

		ticks := ktBatch(t, db, d, g.ticks, []parse.RowUpdate{
			{Key: parse.Key{ktT2.In(time.FixedZone("", 7200))}, Patch: ktPatch(t, g.ticks, `{"name":"deux"}`)},
		}, ktTickKey)
		assert.Equal(t, []string{ktText(ktT2)}, ticks)

		// PostgreSQL holds microseconds and rounds a finer key to them, so a
		// key 400ns past t0 names t0's row there and no row on SQLite.
		finer := []parse.RowUpdate{{Key: parse.Key{ktT0.Add(400 * time.Nanosecond)}, Patch: ktPatch(t, g.ticks, `{"name":"zéro"}`)}}
		plan, err := g.ticks.BuildUpdateBatch(d, finer, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		res, err := applyUpdateBatch(t, db, g.ticks, d, plan)
		if isPostgres(d) {
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
		db := setup(t)
		rename := func(r parse.Key) parse.RowUpdate {
			return parse.RowUpdate{Key: r, Patch: ktPatch(t, g.prices, `{"name":"n"}`)}
		}
		prices, err := g.prices.BuildUpdateBatch(d, []parse.RowUpdate{
			rename(parse.Key{"12345678901234567891"}), rename(parse.Key{"2"}), rename(parse.Key{"12345678901234567890"}), rename(parse.Key{"1.50"}),
		}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		require.Len(t, prices.Chunks, 1)
		chunk := prices.Chunks[0]
		want := []string{"1.5", "2", "12345678901234567890", "12345678901234567891"}
		assert.Equal(t, want, ktChunkKeys(t, chunk.Keys))
		assert.Equal(t, want, ktCurrentKeys(t, db, chunk.Current, 1))

		revalue := func(r parse.Key) parse.RowUpdate {
			return parse.RowUpdate{Key: r, Patch: ktPatch(t, g.readings, `{"value":0}`)}
		}
		readings, err := g.readings.BuildUpdateBatch(d, []parse.RowUpdate{
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
		db := setup(t)
		crxUpdate(t, db, d, g.readings, `{"value":9}`, parse.ByKeys(parse.Key{"d1", t1Elsewhere}, parse.Key{"d2", ktT0}))
		assert.Equal(t, []string{"d1/" + ktText(ktT0) + "=1", "d1/" + ktText(ktT1) + "=9", "d2/" + ktText(ktT0) + "=9"},
			ktNames(t, db, d, g.readings, func(r ktReading) string { return ktReadingKey(r) + "=" + fmt.Sprint(r.Value) }))
		crxUpdate(t, db, d, g.prices, `{"name":"x"}`, parse.ByKeys(parse.Key{"1.500"}, parse.Key{"12345678901234567891"}))
		assert.Equal(t, []string{"1.5=x", "2=two", "12345678901234567890=big", "12345678901234567891=x"},
			ktNames(t, db, d, g.prices, func(p ktPrice) string { return ktPriceKey(p) + "=" + p.Name }))
	})

	t.Run("relation writes", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			update func(t *testing.T, db *sql.DB)
			pairs  string
			want   []string
		}{
			{
				// 1.5 is red's under another spelling; 2 leaves.
				name: "decimal targets replace",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdate(t, db, d, g.tags, `{"prices":["1.50","12345678901234567891"]}`, parse.ByID("red"))
				},
				pairs: "SELECT price_id, tag_id FROM kt_price_tags",
				want:  []string{"1.5/blue", "1.5/red", "12345678901234567890/blue", "12345678901234567891/green", "12345678901234567891/red"},
			},
			{
				name: "decimal owner adds what it holds under another spelling",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdatePatch(t, db, d, g.prices, `[{"op":"add","path":"/tags","value":["red","green"]}]`, parse.ByKey(parse.Key{"1.5"}))
				},
				pairs: "SELECT price_id, tag_id FROM kt_price_tags",
				want:  []string{"1.5/blue", "1.5/green", "1.5/red", "2/red", "12345678901234567890/blue", "12345678901234567891/green"},
			},
			{
				name: "decimal targets remove",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdatePatch(t, db, d, g.tags, `[{"op":"remove","path":"/prices","value":["1.5","12345678901234567890"]}]`, parse.ByID("blue"))
				},
				pairs: "SELECT price_id, tag_id FROM kt_price_tags",
				want:  []string{"1.5/red", "2/red", "12345678901234567891/green"},
			},
			{
				name: "composite targets replace",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdate(t, db, d, g.tags, `{"readings":[["d1","`+t1Elsewhere+`"],["d2","`+ktText(ktT0)+`"]]}`, parse.ByID("red"))
				},
				pairs: "SELECT device_id, at, tag_id FROM kt_reading_tags",
				want:  []string{"d1/" + ktText(ktT1) + "/blue", "d1/" + ktText(ktT1) + "/red", "d2/" + ktText(ktT0) + "/red"},
			},
			{
				name: "timestamp owner adds",
				update: func(t *testing.T, db *sql.DB) {
					crxUpdatePatch(t, db, d, g.ticks, `[{"op":"add","path":"/tags","value":["green"]}]`, parse.ByKey(parse.Key{t1Elsewhere}))
				},
				pairs: "SELECT tick_at, tag_id FROM kt_tick_tags",
				want:  []string{ktText(ktT0) + "/red", ktText(ktT1) + "/blue", ktText(ktT1) + "/green"},
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				db := setup(t)
				tt.update(t, db)
				assert.ElementsMatch(t, tt.want, ktPairs(t, db, tt.pairs))
			})
		}
	})
}

func ktPriceKey(p ktPrice) string     { return p.ID.String() }
func ktNoteKey(n ktNote) string       { return n.ID }
func ktTickKey(k ktTick) string       { return ktText(k.At) }
func ktReadingKey(r ktReading) string { return r.DeviceID + "/" + ktText(r.At) }
func ktAlertKey(a ktAlert) string     { return a.ID }
func crxTagKeyOf(t ktTag) string      { return t.ID }

// ktKeys lists the rows of r a scope admits, each as key names it.
func ktKeys[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], scope parse.Scope, key func(T) string) []string {
	t.Helper()
	req, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	plan, err := r.BuildList(d, req, scope)
	require.NoError(t, err)
	var out []string
	for _, item := range listPage(t, db, r, d, plan).Items {
		out = append(out, key(item))
	}
	return out
}

// ktNames lists every row of r, each as name writes it, in key order.
func ktNames[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], name func(T) string) []string {
	t.Helper()
	return ktKeys(t, db, d, r, parse.Scope{}, name)
}

func ktPatch[T any](t *testing.T, r *parse.Resource[T], raw string) *patch.ParsedPatch {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(raw))
	require.NoError(t, err)
	return p
}

// ktBatch applies one update batch and returns the changed rows' keys, as
// key names them, in the order the batch returns them.
func ktBatch[T any](t *testing.T, db *sql.DB, d filter.Dialect, r *parse.Resource[T], rows []parse.RowUpdate, key func(T) string) []string {
	t.Helper()
	plan, err := r.BuildUpdateBatch(d, rows, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	res, err := applyUpdateBatch(t, db, r, d, plan)
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
	for _, row := range queryRows(t, db, query, nil) {
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
	for _, row := range queryRows(t, db, current.SQL, current.Args) {
		out = append(out, ktJoin(t, row[:width]))
	}
	return out
}

func ktJoin(t *testing.T, values []any) string {
	t.Helper()
	text := ""
	for i, v := range values {
		if i > 0 {
			text += "/"
		}
		text += ktPairValue(t, v)
	}
	return text
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
	if d, err := decimal.NewFromString(s); err == nil {
		return d.String()
	}
	if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ktText(at)
	}
	return s
}

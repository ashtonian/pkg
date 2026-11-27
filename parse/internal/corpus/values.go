//go:build integration

package corpus

import (
	"database/sql"
	"encoding/json"
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

func civilDateSQL(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, backend sqlBackend) {
		provider, db := backend.open(t)
		_, err := db.ExecContext(t.Context(), `CREATE TABLE tasks (id TEXT PRIMARY KEY, due_on DATE)`)
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `INSERT INTO tasks VALUES ('leap','2028-02-29'),('today','2026-10-26'),('empty',NULL)`)
		require.NoError(t, err)
		if backend.engine == SQLite {
			_, err = db.ExecContext(t.Context(), `INSERT INTO tasks VALUES ('legacy','2026-10-26T00:00:00Z')`)
			require.NoError(t, err)
		}
		resource := parse.MustResource[parsetest.CivilTask](provider, "tasks")
		query, err := resource.ParseList(url.Values{"filter": {`{"due_on":{"$lte":"2026-10-26"}}`}, "sort": {"due_on,id"}})
		require.NoError(t, err)
		found := listPage(t, resource, query, parse.Scope{}).Items
		if backend.engine == SQLite {
			require.Len(t, found, 2)
			assert.Equal(t, "legacy", found[0].ID)
			found = found[1:]
		} else {
			require.Len(t, found, 1)
		}
		wire, err := json.Marshal(found[0])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"today","due_on":"2026-10-26"}`, string(wire))
		for _, body := range []string{`{"due_on":"2028-02-29"}`, `{"due_on":null}`} {
			patch, err := resource.ParseMergePatch([]byte(body))
			require.NoError(t, err)
			res, err := applyUpdate(t, resource, false, patch, parse.ByID("today"), parse.Scope{})
			require.NoError(t, err)
			require.Len(t, res.Changed, 1)
			row := res.Changed[0]
			if body == `{"due_on":null}` {
				assert.Nil(t, row.DueOn)
			} else {
				require.NotNil(t, row.DueOn)
				assert.Equal(t, core.Date("2028-02-29"), *row.DueOn)
			}
		}
	})
}

// Decimal filters select by value and a decimal patch writes back exactly,
// over every column type a decimal is held in: PostgreSQL numeric, with and
// without a declared scale, and SQLite NUMERIC and REAL.
func decimalSQL(t *testing.T, engine Engine) {
	columnTypes := map[Engine][]string{SQLite: {"NUMERIC", "REAL"}, Postgres: {"NUMERIC", "NUMERIC(12,4)"}}
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		for _, columnType := range columnTypes[b.engine] {
			t.Run(columnType, func(t *testing.T) { runDecimalSQL(t, b.with(storing(core.FieldTypeDecimal, columnType))) })
		}
	})
}

func ledgerTable() sqlTable {
	table := sqlTable{name: "ledger_lines", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[decimal.Decimal]("amount").required(), column[decimal.Decimal]("fee"),
	}}
	return table
}

func runDecimalSQL(t *testing.T, b sqlBackend) {
	provider, db := b.open(t)
	b.create(t, db, ledgerTable())
	_, err := db.ExecContext(t.Context(), `INSERT INTO ledger_lines VALUES
		('a','10.25',NULL), ('b','9.5','0.25'), ('c','-1',NULL),
		('d','2.00','1'), ('e','100',NULL), ('g','2','2.0')`)
	require.NoError(t, err)
	resource := parse.MustResource[parsetest.LedgerLine](provider, "ledger_lines")

	for filterDoc, want := range map[string]string{
		`{"amount":{"$gt":"9.5"}}`:                                  "ae",
		`{"amount":{"$gt":9.5}}`:                                    "ae",
		`{"amount":{"$lt":"10.25"}}`:                                "bcdg",
		`{"amount":"2"}`:                                            "dg",
		`{"amount":{"$in":["0.5e1",100,"-1.0"]}}`:                   "ce",
		`{"amount":{"$nin":["2.000","10.25"]}}`:                     "bce",
		`{"fee":{"$lt":"1.0"}}`:                                     "b",
		`{"fee":{"$gte":"1"}}`:                                      "dg",
		`{"$and":[{"amount":{"$gte":-1}},{"amount":{"$lte":"2"}}]}`: "cdg",
	} {
		got := listLedger(t, resource, url.Values{"filter": {filterDoc}})
		assert.Equal(t, want, got, filterDoc)
	}

	p, err := resource.ParseMergePatch([]byte(`{"amount":"10.50","fee":"0.10"}`))
	require.NoError(t, err)
	res, err := applyUpdate(t, resource, false, p, parse.ByID("c"), parse.Scope{})
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	row := res.Changed[0]
	assert.True(t, decimal.RequireFromString("10.5").Equal(row.Amount), row.Amount.String())
	require.NotNil(t, row.Fee)
	assert.True(t, decimal.RequireFromString("0.1").Equal(*row.Fee), row.Fee.String())
	assert.Equal(t, "c", listLedger(t, resource, url.Values{"amount": {"10.5"}}))
}

// PostgreSQL holds and compares decimals beyond float64's precision.
func decimalSQLPostgresIsExact(t *testing.T, engine Engine) {
	b := sqlBackendOf(Postgres)
	provider, db := b.open(t)
	b.create(t, db, ledgerTable())
	_, err := db.ExecContext(t.Context(), `INSERT INTO ledger_lines VALUES ('a','0.1',NULL), ('b','0.10000000000000001',NULL),
		('c','123456789012345678901234567890.5',NULL), ('d','123456789012345678901234567890.25',NULL)`)
	require.NoError(t, err)
	resource := parse.MustResource[parsetest.LedgerLine](provider, "ledger_lines")
	for filterDoc, want := range map[string]string{
		`{"amount":"0.1"}`:                                       "a",
		`{"amount":"0.10000000000000001"}`:                       "b",
		`{"amount":{"$gt":0.1,"$lt":1}}`:                         "b",
		`{"amount":{"$gt":"123456789012345678901234567890.25"}}`: "c",
	} {
		assert.Equal(t, want, listLedger(t, resource, url.Values{"filter": {filterDoc}}), filterDoc)
	}

	// Paging resumes from a cursor that holds the exact key.
	var walked []string
	cursor := ""
	for n := 0; ; n++ {
		require.Less(t, n, 5, "walk did not terminate")
		q := url.Values{"sort": {"amount"}, "page_size": {"1"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req, err := resource.ParseList(q)
		require.NoError(t, err)
		page := listPage(t, resource, req, parse.Scope{})
		walked = append(walked, parsetest.ItemIDs(page.Items, ledgerLineID)...)
		if !page.PageInfo.HasNextPage {
			break
		}
		cursor = string(page.PageInfo.EndCursor)
	}
	assert.Equal(t, []string{"a", "b", "d", "c"}, walked)
}

func ledgerLineID(l parsetest.LedgerLine) string { return l.ID }

// listLedger runs one list request and returns the matched ids, sorted.
func listLedger(t *testing.T, resource *parse.Resource[parsetest.LedgerLine], q url.Values) string {
	t.Helper()
	req, err := resource.ParseList(q)
	require.NoError(t, err)
	return strings.Join(sortedIDs(listPage(t, resource, req, parse.Scope{}).Items, ledgerLineID), "")
}

func float32FilterAndPatchSQLiteParity(t *testing.T, engine Engine) {
	type row struct {
		ID    int64   `json:"id" db:"id" parse:"filter,sort"`
		Ratio float32 `json:"ratio" db:"ratio" parse:"filter,patch"`
	}
	p, err := parse.NewResource[row](providertest.Compile(filter.SQLiteDialect{}), "ratios")
	require.NoError(t, err)
	db := dbtest.SQLite(t)
	_, err = db.ExecContext(t.Context(), "CREATE TABLE ratios (id INTEGER PRIMARY KEY, ratio REAL)")
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "INSERT INTO ratios VALUES (1, ?)", float64(float32(0.1)))
	require.NoError(t, err)

	// A request literal must select a value stored from the declared Go type.
	parsed, err := p.ParseFilter(`{"ratio":0.1}`)
	require.NoError(t, err)
	where, err := filter.NewSQLBuilder(p.FilterSchema(), filter.SQLiteDialect{}).Build(parsed.AST())
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM ratios WHERE "+where.Where, where.Args...).Scan(&count))
	require.Equal(t, 1, count)

	// SQL writes and in-memory application must keep the same precision.
	change, err := p.ParseMergePatch([]byte(`{"ratio":0.2}`))
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(change, row{ID: 1})
	require.NoError(t, err)
	update, err := patch.BuildPatchSQL(change, filter.SQLiteDialect{})
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "UPDATE ratios SET "+update.SetClause+" WHERE id = 1", update.Args...)
	require.NoError(t, err)
	var stored float64
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT ratio FROM ratios WHERE id = 1").Scan(&stored))
	require.Equal(t, math.Float64bits(float64(got.Ratio)), math.Float64bits(stored), "the stored float and the applied one are the same bits")
}

// The row shapes below are ones whose columns a by-name driver mapping (the
// db tag, else the Go name) assigns to the wrong field or to none. Each
// table holds one row whose every column holds "value-of-<column>".

type ContractStamps struct {
	CreatedBy string `json:"created_by" db:"created_by"`
}

// contractEmbedded promotes a column from an embedded pointer.
type contractEmbedded struct {
	ID string `json:"id" db:"id" parse:"filter,sort"`
	*ContractStamps
}

// contractConvention names a column by its JSON name, untagged.
type contractConvention struct {
	ID    string `json:"id" db:"id" parse:"filter,sort"`
	Title string `json:"name" parse:"filter"`
}

// contractColumnTag names its columns by the sql tag.
type contractColumnTag struct {
	ID   string `json:"id" sql:"doc_id" parse:"filter,sort"`
	Name string `json:"name" sql:"doc_name"`
}

// contractCrossed's untagged JSON names cross its Go names.
type contractCrossed struct {
	ID    string `json:"id" db:"id" parse:"filter,sort"`
	Left  string `json:"right"`
	Right string `json:"left"`
}

func rowContractFixture() []string {
	stmts := []string{
		`CREATE TABLE "embedded" ("id" TEXT PRIMARY KEY, "created_by" TEXT)`,
		`INSERT INTO "embedded" VALUES ('value-of-id', 'value-of-created_by')`,
		`CREATE TABLE "convention" ("id" TEXT PRIMARY KEY, "name" TEXT)`,
		`INSERT INTO "convention" VALUES ('value-of-id', 'value-of-name')`,
		`CREATE TABLE "column_tag" ("doc_id" TEXT PRIMARY KEY, "doc_name" TEXT)`,
		`INSERT INTO "column_tag" VALUES ('value-of-doc_id', 'value-of-doc_name')`,
		`CREATE TABLE "crossed" ("id" TEXT PRIMARY KEY, "left" TEXT, "right" TEXT)`,
		`INSERT INTO "crossed" ("id", "left", "right") VALUES ('value-of-id', 'value-of-left', 'value-of-right')`,
	}
	return stmts
}

// Each shape pages on each backend with every field holding the column the
// resource compiled it from.
func rowContract(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := b.open(t)
		dbtest.Exec(t, db, rowContractFixture()...)
		runRowContract(t, p)
	})
}

func runRowContract(t *testing.T, p *parse.Provider) {
	t.Helper()
	t.Run("embedded pointer", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractEmbedded](p, "embedded"))
		require.NotNil(t, items[0].ContractStamps, "the embedded pointer is allocated")
		assert.Equal(t, contractEmbedded{ID: "value-of-id", ContractStamps: &ContractStamps{CreatedBy: "value-of-created_by"}}, items[0])
	})
	t.Run("convention-named column", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractConvention](p, "convention"))
		assert.Equal(t, contractConvention{ID: "value-of-id", Title: "value-of-name"}, items[0])
	})
	t.Run("configured column tag", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractColumnTag](p, "column_tag", parse.PrimaryKey("doc_id"),
			parse.ParserOptions(core.WithGeneratorOptions(core.WithColumnTag("sql")))))
		assert.Equal(t, contractColumnTag{ID: "value-of-doc_id", Name: "value-of-doc_name"}, items[0])
	})
	t.Run("crossed names", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractCrossed](p, "crossed"))
		assert.Equal(t, contractCrossed{ID: "value-of-id", Left: "value-of-right", Right: "value-of-left"}, items[0],
			"each field holds the column its JSON name compiles to")
	})
}

func contractPage[T any](t *testing.T, r *parse.Resource[T]) []T {
	t.Helper()
	req, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	page := listPage(t, r, req, parse.Scope{})
	require.Len(t, page.Items, 1)
	return page.Items
}

func networkEventTable() sqlTable {
	table := sqlTable{name: "network_events", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[core.LocalTime]("at"), column[core.Interval]("retention"), column[core.Inet]("actor_ip"),
		column[core.CIDR]("network"), column[core.MACAddr]("device"), column[[]core.Inet]("allowed_ips"), column[[]core.Interval]("spans"),
	}}
	return table
}

// sqlTextValuesSQL writes each value type, reads back its canonical text,
// and checks the rows every filter the type offers selects; each engine is
// held to the same rows.
func sqlTextValuesSQL(t *testing.T, engine Engine) {
	// PostgreSQL's connections write intervals in another style than its
	// default: reads must not depend on it.
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		provider, db := b.open(t)
		table := networkEventTable()
		b.create(t, db, table)
		resource := parse.MustResource[parsetest.NetworkEvent](provider, "network_events")
		insert := func(id string, at core.LocalTime, retention core.Interval, actorIP, network string, device any, spans []string, allowed ...string) {
			t.Helper()
			ip, err := core.ParseInet(actorIP)
			require.NoError(t, err)
			cidr, err := core.ParseCIDR(network)
			require.NoError(t, err)
			b.insert(t, db, table, []any{id, at, retention, ip, cidr, device, allowed, spans})
		}
		// LocalTime and MACAddr values converted from non-canonical text
		// write their canonical text.
		insert("a", "10:30:00.500", core.Interval{Months: 1}, "192.168.0.5/32", "10.0.0.0/8", core.MACAddr("08-00-2B-01-02-03"),
			[]string{"P1M"}, "10.0.0.1", "2001:db8::1")
		insert("b", "09:00:00", core.Interval{Days: 30}, "2001:db8::1", "2001:db8::/32", core.MACAddr("08:00:2b:01:02:04"),
			[]string{"P30D", "PT1H"}, "10.0.0.2")
		insert("c", "23:59:59.999999", core.Interval{Days: 1, Microseconds: -3_600_000_000}, "192.168.0.5/24", "192.168.0.0/24", nil, nil)

		list := func(filterJSON, sort string) []parsetest.NetworkEvent {
			t.Helper()
			query, err := resource.ParseList(url.Values{"filter": {filterJSON}, "sort": {sort}})
			require.NoError(t, err)
			plan, err := resource.BuildList(query, parse.Scope{})
			require.NoError(t, err)
			return parsetest.ResourceRows(t, resource, plan.Page)
		}
		ids := func(filterJSON string) []string {
			t.Helper()
			return parsetest.ItemIDs(list(filterJSON, "id"), func(e parsetest.NetworkEvent) string { return e.ID })
		}

		for _, tt := range []struct {
			filter string
			want   []string
		}{
			// PostgreSQL's own interval equality matches P30D with P1M.
			{`{"retention":"P30D"}`, []string{"b"}},
			{`{"retention":{"$in":["P1M"]}}`, []string{"a"}},
			{`{"retention":{"$ne":"P1M"}}`, []string{"b", "c"}},
			{`{"retention":"P1DT-1H"}`, []string{"c"}},
			// A host and the same address with a /24 prefix differ.
			{`{"actor_ip":"192.168.0.5"}`, []string{"a"}},
			{`{"actor_ip":"2001:0DB8::0001"}`, []string{"b"}},
			{`{"actor_ip":{"$in":["192.168.0.5/24","2001:db8::1"]}}`, []string{"b", "c"}},
			{`{"network":"2001:DB8::/32"}`, []string{"b"}},
			{`{"device":"08-00-2B-01-02-03"}`, []string{"a"}},
			{`{"device":{"$ne":"08:00:2b:01:02:03"}}`, []string{"b"}},
			{`{"at":{"$gte":"10:30:00.500"}}`, []string{"a", "c"}},
			{`{"at":{"$lt":"10:30:00.5"}}`, []string{"b"}},
			{`{"allowed_ips":{"$contains":["2001:0db8::1"]}}`, []string{"a"}},
			{`{"allowed_ips":{"$some":{"$in":["10.0.0.2","192.0.2.1"]}}}`, []string{"b"}},
			{`{"allowed_ips":{"$none":{"$eq":"10.0.0.1"}}}`, []string{"b", "c"}},
			// As for a scalar interval, P30D is not P1M.
			{`{"spans":{"$contains":["P30D"]}}`, []string{"b"}},
			{`{"spans":{"$some":{"$eq":"P1M"}}}`, []string{"a"}},
			{`{"spans":{"$some":{"$in":["PT60M"]}}}`, []string{"b"}},
		} {
			assert.Equal(t, tt.want, ids(tt.filter), tt.filter)
		}

		ordered := list(`{}`, "at")
		require.Len(t, ordered, 3)
		assert.Equal(t, []string{"b", "a", "c"}, []string{ordered[0].ID, ordered[1].ID, ordered[2].ID}, "time sorts chronologically")
		wire, err := json.Marshal(ordered[1])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"a","at":"10:30:00.5","retention":"P1M","actor_ip":"192.168.0.5","network":"10.0.0.0/8",
			"device":"08:00:2b:01:02:03","allowed_ips":["10.0.0.1","2001:db8::1"],"spans":["P1M"]}`, string(wire))
		wire, err = json.Marshal(ordered[2])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"c","at":"23:59:59.999999","retention":"P1DT-1H","actor_ip":"192.168.0.5/24","network":"192.168.0.0/24",
			"device":null,"allowed_ips":null,"spans":null}`, string(wire))

		patch, err := resource.ParseMergePatch([]byte(`{"at":"07:15:00.250000","retention":"PT90M","actor_ip":"::ffff:10.0.0.9",
			"network":"10.1.0.0/16","device":"0800.2b01.0205","allowed_ips":["2001:0db8:0:0:0:0:0:2"],"spans":["P1W","PT3600S"]}`))
		require.NoError(t, err)
		update, err := resource.BuildUpdate(patch, parse.ByID("b"), parse.Scope{})
		require.NoError(t, err)
		updated := parsetest.ResourceRows(t, resource, update.Owner)
		require.Len(t, updated, 1)
		wire, err = json.Marshal(updated[0])
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"b","at":"07:15:00.25","retention":"PT1H30M","actor_ip":"::ffff:10.0.0.9","network":"10.1.0.0/16",
			"device":"08:00:2b:01:02:05","allowed_ips":["2001:db8::2"],"spans":["P7D","PT1H"]}`, string(wire))
		assert.Equal(t, []string{"b"}, ids(`{"retention":"PT90M"}`), "the patch stored the canonical text")
	}, sessionParams(map[string]string{"IntervalStyle": "sql_standard"}))
}

// Timestamps on each backend. SQLite's column holds each row's canonical
// text, declared TEXT or DATETIME; modernc.org/sqlite scans the DATETIME
// column's text into a time.Time of the same instant. PostgreSQL's
// TIMESTAMPTZ column is read on sessions whose TimeZone and DateStyle are
// neither UTC nor ISO, which the facet keys must not depend on.
func timestamps(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine == Postgres {
			provider, db := b.open(t)
			var zone, style string
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT current_setting('TimeZone'), current_setting('DateStyle')").Scan(&zone, &style))
			require.Equal(t, "Asia/Kolkata", zone)
			require.Equal(t, "SQL, DMY", style)
			runTimestamps(t, b, provider, db)
			return
		}
		for _, atType := range []string{"TEXT", "DATETIME"} {
			t.Run(atType, func(t *testing.T) {
				b := b.with(storing(core.FieldTypeTimestamp, atType))
				provider, db := b.open(t)
				runTimestamps(t, b, provider, db)
				if atType != "DATETIME" {
					return
				}
				schema, err := filtertest.TimestampSchema()
				require.NoError(t, err)
				var at time.Time
				require.NoError(t, db.QueryRowContext(t.Context(), "SELECT at FROM "+schema.Name+" WHERE id = 'a'").Scan(&at))
				assert.True(t, filtertest.TimestampRecords()[0].At.Equal(at), "scanned %v", at)
			})
		}
	}, sessionParams(map[string]string{"TimeZone": "Asia/Kolkata", "DateStyle": "SQL, DMY"}))
}

// runTimestamps writes TimestampRecords through the dialect's MarshalValue,
// each row in its own offset, then checks that facets, filters, sorts and
// keyset walks on the column read instants: the buckets, rows and orders
// the fixture names, the same on every backend. provider is the one on db.
func runTimestamps(t *testing.T, b sqlBackend, provider *parse.Provider, db *sql.DB) {
	t.Helper()
	schema, err := filtertest.TimestampSchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.TimestampRecords()))
	res, err := parse.NewResource[filtertest.TimestampRecord](provider, schema.Name, parse.PageSize(walkPage, walkPage))
	require.NoError(t, err)

	t.Run("facet", func(t *testing.T) {
		got := facetCounts(t, db, b.dialect, schema, "at")
		assert.Equal(t, map[string]map[string]int64{"at": filtertest.TimestampFacets}, got)
	})

	for _, doc := range slices.Sorted(maps.Keys(filtertest.TimestampFilters)) {
		t.Run("filter "+doc, func(t *testing.T) {
			f, err := res.ParseFilter(doc)
			require.NoError(t, err)
			where, err := filter.NewSQLBuilder(schema, b.dialect).Build(f.AST())
			require.NoError(t, err)
			ids := dbtest.QueryRows[string](t, db, "SELECT id FROM "+schema.Name+" WHERE "+where.Where+" ORDER BY id", where.Args...)
			assert.Equal(t, filtertest.TimestampFilters[doc], strings.Join(ids, ""))
		})
	}

	walkOrders(t, walker[filtertest.TimestampRecord]{resource: res,
		id: func(r filtertest.TimestampRecord) string { return r.ID }}, filtertest.TimestampOrders)
}

func typedDocumentSQLiteParity(t *testing.T, engine Engine) {
	type dimensions struct {
		W int `json:"w"`
		H int `json:"h"`
	}
	type document struct {
		Dims     dimensions            `json:"dims"`
		Optional *dimensions           `json:"optional,omitempty"`
		Label    string                `json:"label,omitempty"`
		ByName   map[string]dimensions `json:"by_name"`
		Labels   map[string]string     `json:"labels,omitempty"`
	}
	type row struct {
		ID  string   `json:"id,omitempty" db:"id"`
		Doc document `json:"doc" db:"doc" parse:"patch"`
	}
	p, err := parse.NewResource[row](providertest.Compile(filter.SQLiteDialect{}), "rows")
	require.NoError(t, err)
	for _, tc := range []struct {
		name, body string
		merge      bool
	}{
		{"replacement canonicalizes missing fields", `[{"op":"replace","path":"/doc","value":{"dims":{"w":9}}}]`, false},
		{"partial nested merge preserves sibling", `{"doc":{"dims":{"w":9}}}`, true},
		{"nullable complete creation", `{"doc":{"optional":{"w":9,"h":0}}}`, true},
		{"omitted member deletion", `{"doc":{"label":null}}`, true},
		{"typed map complete insertion", `{"doc":{"by_name":{"new":{"w":9,"h":0}}}}`, true},
		{"omitted map safe removal and insertion", `{"doc":{"labels":{"old":null,"new":"value"}}}`, true},
		{"omitted map canonical replacement", `[{"op":"replace","path":"/doc","value":{"dims":{"w":9},"labels":{}}}]`, false},
		{"typed map deletion", `{"doc":{"by_name":{"old":null}}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := dbtest.SQLite(t)
			ctx := t.Context()
			_, err := db.ExecContext(ctx, `CREATE TABLE docs (doc TEXT NOT NULL)`)
			require.NoError(t, err)
			initial := row{Doc: document{Dims: dimensions{W: 2, H: 7}, Label: "old", Labels: map[string]string{"old": "value"}, ByName: map[string]dimensions{"old": {W: 3, H: 4}}}}
			start, err := json.Marshal(initial.Doc)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO docs (doc) VALUES (?)`, string(start))
			require.NoError(t, err)
			var parsed *patch.ParsedPatch
			if tc.merge {
				parsed, err = p.ParseMergePatch([]byte(tc.body))
			} else {
				parsed, err = p.ParsePatch([]byte(tc.body))
			}
			require.NoError(t, err)
			set, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, "UPDATE docs SET "+set.SetClause, set.Args...)
			require.NoError(t, err)
			got, err := p.ApplyValidatedPatch(parsed, initial)
			require.NoError(t, err)
			encoded, err := json.Marshal(got.Doc)
			require.NoError(t, err)
			var stored string
			require.NoError(t, db.QueryRowContext(ctx, "SELECT doc FROM docs").Scan(&stored))
			assert.JSONEq(t, string(encoded), stored, "SQL and Go agree")
		})
	}
}

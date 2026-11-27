//go:build integration

package corpus

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

type hiddenColumns struct {
	Extra string `json:"extra" db:"extra"`
}

type hiddenColumnRow struct {
	ID string `json:"id" db:"id"`
	*hiddenColumns
}

type hiddenMembers struct {
	X string `json:"x"`
}

type byValueDoc struct {
	hiddenMembers
	Y string `json:"y"`
}

// byValueRow embeds an unexported struct by value, whose promoted fields
// reflection sets, as encoding/json does.
type byValueRow struct {
	ID string `json:"id" db:"id"`
	hiddenColumns
	Doc byValueDoc `json:"doc" db:"doc"`
}

// A row scans the fields an unexported struct embedded by value promotes,
// and leaves a column the resource does not read behind an unexported
// embedded pointer, which reflection cannot allocate.
func rowScannerEmbeddedStructs(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		t.Run("promoted by value", func(t *testing.T) {
			p, db := b.open(t)
			b.load(t, db, sqlTable{name: "by_value", columns: []sqlColumn{
				column[string]("id"), column[string]("extra").required(), column[byValueDoc]("doc").required(),
			}, key: []string{"id"}}, [][]any{{"a", "promoted", `{"x":"member","y":"own"}`}})
			r, err := parse.NewResource[byValueRow](p, "by_value")
			require.NoError(t, err)
			rows := readRows(t, b, r, "by_value")
			want := byValueRow{ID: "a", hiddenColumns: hiddenColumns{Extra: "promoted"}, Doc: byValueDoc{hiddenMembers: hiddenMembers{X: "member"}, Y: "own"}}
			assert.Equal(t, []byValueRow{want}, rows)
		})
		t.Run("an unread column behind a pointer", func(t *testing.T) {
			p, db := b.open(t)
			b.load(t, db, sqlTable{name: "hidden_rows", key: []string{"id"}, columns: []sqlColumn{column[string]("id"), column[string]("extra")}}, [][]any{{"a", "unread"}})
			r, err := parse.NewResource[hiddenColumnRow](p, "hidden_rows", parse.ReadColumns("id"))
			require.NoError(t, err, "a column the resource does not read is never scanned")
			rows := readRows(t, b, r, "hidden_rows")
			assert.Equal(t, []hiddenColumnRow{{ID: "a"}}, rows)
		})
	})
}

type storedSettings struct {
	Theme string `json:"theme"`
}

// storedRow holds every kind of value a column stores apart from integers,
// floats and text: on SQLite as text (timestamps and intervals, and as JSON
// documents and arrays), on PostgreSQL in its native type.
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

// storedTable is storedRow's table, every column but id and every
// nullable.
func storedTable() sqlTable {
	table := sqlTable{name: "stored", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[time.Time]("at"), column[*time.Time]("until"), column[map[string]int]("counts"),
		column[*storedSettings]("settings"), column[[]string]("tags"), column[[]core.Interval]("spans"),
		column[json.RawMessage]("raw"), column[any]("dynamic"), column[core.Interval]("every"),
	}}
	return table
}

// A row scans what each column stores, and NULL leaves its field zero.
func rowScannerStorage(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := b.open(t)
		// SQLite's text keeps nanoseconds, PostgreSQL's timestamptz
		// microseconds.
		nanos := 6000
		if b.engine == SQLite {
			nanos = 6006
		}
		at := time.Date(2026, 1, 2, 3, 4, 5, nanos, time.UTC)
		until := at.Add(time.Hour)
		b.load(t, db, storedTable(), [][]any{
			{"a", at, until, `{"x":1}`, `{"theme":"dark"}`, []string{"p", "q"}, []core.Interval{{Days: 1}}, json.RawMessage(`null`), `[1,"two"]`, core.Interval{Microseconds: 3_600_000_000}},
			{"b", at, nil, nil, nil, nil, nil, nil, nil, core.Interval{Months: 1}},
		})
		r := parse.MustResource[storedRow](p, "stored")
		rows := readRows(t, b, r, "stored")
		require.Len(t, rows, 2)
		// A timestamp is the instant stored, in the location its driver
		// reads it in: pgx reads timestamptz in the local zone.
		for i := range rows {
			rows[i].At = rows[i].At.UTC()
			if rows[i].Until != nil {
				utc := rows[i].Until.UTC()
				rows[i].Until = &utc
			}
		}
		assert.Equal(t, storedRow{
			ID: "a", At: at, Until: &until, Counts: map[string]int{"x": 1}, Settings: &storedSettings{Theme: "dark"},
			Tags: []string{"p", "q"}, Spans: []core.Interval{{Days: 1}}, Raw: json.RawMessage(`null`), Dynamic: []any{float64(1), "two"},
			Every: core.Interval{Microseconds: 3_600_000_000},
		}, rows[0])
		assert.Equal(t, storedRow{ID: "b", At: at, Every: core.Interval{Months: 1}}, rows[1], "NULL leaves a field zero")
	})
}

// A row that cannot hold what a column stores fails the read. A timestamp
// column holding text that is no timestamp is SQLite's alone: PostgreSQL's
// column refuses it, and the Memory engine's Put.
func rowScannerRefusesWhatTheRowCannotHold(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		hour := core.Interval{Microseconds: 3_600_000_000}
		for name, row := range map[string][]any{
			"NULL into a time.Time":          {"a", nil, nil, nil, nil, nil, nil, nil, nil, hour},
			"a member spelt in another case": {"a", at, nil, nil, `{"Theme":"dark"}`, nil, nil, nil, nil, hour},
			"JSON null for a map":            {"a", at, nil, json.RawMessage(`null`), nil, nil, nil, nil, nil, hour},
		} {
			t.Run(name, func(t *testing.T) {
				p, db := b.open(t)
				b.load(t, db, storedTable(), [][]any{row})
				_, err := readAll(t, b, parse.MustResource[storedRow](p, "stored"), "stored")
				assert.Error(t, err)
			})
		}
		t.Run("timestamp text that is no timestamp", func(t *testing.T) {
			if b.engine != SQLite {
				t.Skip("only SQLite's timestamp column holds text that is no timestamp")
			}
			p, db := b.open(t)
			b.create(t, db, storedTable())
			dbtest.Exec(t, db, `INSERT INTO "stored" VALUES ('a', 'yesterday', NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'PT1H')`)
			_, err := readAll(t, b, parse.MustResource[storedRow](p, "stored"), "stored")
			assert.Error(t, err)
		})
	})
}

// readRows is every row of r's table, keyed by id, in key order: on the
// Memory engine, as its lists page them; on a SQL engine, as a SELECT of
// r's columns scans them.
func readRows[T any](t *testing.T, b sqlBackend, r *parse.Resource[T], table string) []T {
	t.Helper()
	rows, err := readAll(t, b, r, table)
	require.NoError(t, err)
	return rows
}

// readAll is readRows with the read's error.
func readAll[T any](t *testing.T, b sqlBackend, r *parse.Resource[T], table string) ([]T, error) {
	t.Helper()
	if b.engine == Memory {
		page, err := r.List(t.Context(), nil, parse.Scope{})
		if err != nil {
			return nil, err
		}
		return page.Items, nil
	}
	return r.Query(t.Context(), parse.Statement{SQL: `SELECT ` + parsetest.JoinColumns(r.Columns()) + ` FROM ` + b.dialect.QuoteIdent(table) + ` ORDER BY "id"`})
}

type countedRow struct {
	ID     string         `json:"id"     db:"id"     parse:"filter,sort"`
	Counts map[string]int `json:"counts" db:"counts"`
}

// A row that fails to scan fails the statement that read it, whose error
// names the table once, then the statement, then the column.
func listNamesAScanFailureOnce(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		p, db := b.open(t)
		b.load(t, db, sqlTable{name: "counted", columns: []sqlColumn{column[string]("id"), column[map[string]int]("counts")}, key: []string{"id"}},
			[][]any{{"a", json.RawMessage(`null`)}})
		r := parse.MustResource[countedRow](p, "counted")
		_, err := r.List(t.Context(), nil, parse.Scope{})
		require.EqualError(t, err, "parse: counted: page: column counts: JSON document decodes to nil map[string]int; use json.RawMessage to preserve JSON null")
	})
}

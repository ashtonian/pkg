//go:build unit

package parse_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

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

func TestRowScanner_SQLiteStorage(t *testing.T) {
	p, db := providertest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE "stored" ("id" TEXT PRIMARY KEY, "at" TEXT NOT NULL, "until" TEXT, "counts" TEXT, "settings" TEXT,
		"tags" TEXT, "spans" TEXT, "raw" TEXT, "dynamic" TEXT, "every" TEXT)`)
	require.NoError(t, err)
	at := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	_, err = db.Exec(`INSERT INTO "stored" VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?), (?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, ?)`,
		"a", core.FormatTimestamp(at), core.FormatTimestamp(at.Add(time.Hour)), `{"x":1}`, `{"theme":"dark"}`, `["p","q"]`, `["P1D"]`, `null`, `[1,"two"]`, "PT1H",
		"b", core.FormatTimestamp(at), "P1M")
	require.NoError(t, err)
	r := parse.MustResource[storedRow](p, "stored")
	rows := parsetest.ResourceRows(t, r, parse.Statement{SQL: `SELECT ` + parsetest.JoinColumns(r.Columns()) + ` FROM "stored" ORDER BY "id"`})
	require.Len(t, rows, 2)
	until := at.Add(time.Hour)
	assert.Equal(t, storedRow{
		ID: "a", At: at, Until: &until, Counts: map[string]int{"x": 1}, Settings: &storedSettings{Theme: "dark"},
		Tags: []string{"p", "q"}, Spans: []core.Interval{{Days: 1}}, Raw: json.RawMessage(`null`), Dynamic: []any{float64(1), "two"},
		Every: core.Interval{Microseconds: 3_600_000_000},
	}, rows[0])
	assert.Equal(t, storedRow{ID: "b", At: at, Every: core.Interval{Months: 1}}, rows[1], "NULL leaves a field zero")
}

func TestRowScanner_RefusesWhatTheRowCannotHold(t *testing.T) {
	p, db := providertest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE "stored" ("id" TEXT PRIMARY KEY, "at" TEXT, "until" TEXT, "counts" TEXT, "settings" TEXT,
		"tags" TEXT, "spans" TEXT, "raw" TEXT, "dynamic" TEXT, "every" TEXT)`)
	require.NoError(t, err)
	r := parse.MustResource[storedRow](p, "stored")
	read := parse.Statement{SQL: `SELECT ` + parsetest.JoinColumns(r.Columns()) + ` FROM "stored"`}
	for name, values := range map[string]string{
		"NULL into a time.Time":               `('a', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'PT1H')`,
		"a member spelt in another case":      `('a', '2026-01-02T03:04:05.000000000Z', NULL, NULL, '{"Theme":"dark"}', NULL, NULL, NULL, NULL, 'PT1H')`,
		"JSON null for a map":                 `('a', '2026-01-02T03:04:05.000000000Z', NULL, 'null', NULL, NULL, NULL, NULL, NULL, 'PT1H')`,
		"timestamp text that is no timestamp": `('a', 'yesterday', NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'PT1H')`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := db.Exec(`DELETE FROM "stored"`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO "stored" VALUES ` + values)
			require.NoError(t, err)
			_, err = r.Query(t.Context(), read)
			assert.Error(t, err)
		})
	}

	_, err = r.RowScanner(parse.ScanJSON("nope"))
	assert.ErrorContains(t, err, `ScanJSON names "nope", which the resource does not read`)
}

//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Every operator × type case executed on SQLite: documents and arrays are
// JSON text, timestamps RFC 3339 text, booleans integers.
func TestFilterMatrix_SQLite(t *testing.T) {
	runFilterMatrix(t, "sqlite", sqliteMatrix(t))
}

func sqliteMatrix(t *testing.T) matrixDB {
	return matrixDB{
		db:      dbtest.SQLite(t),
		dialect: filter.SQLiteDialect{},
		schema: `CREATE TABLE records (id TEXT PRIMARY KEY, name TEXT, count INTEGER, score REAL,
			active INTEGER, at TEXT, ref TEXT, doc TEXT, tags TEXT, loc TEXT)`,
		values: func(r filtertest.Record) []any {
			return []any{r.ID, r.Name, r.Count, r.Score, r.Active, timeText(r), uuidText(r), jsonText(r.Doc), jsonText(r.Tags), jsonText(r.Loc)}
		},
	}
}

// Membership constraints on SQLite, as subqueries and resolved: TEXT for
// strings and UUIDs, INTEGER for integers.
func TestMembershipMatrix_SQLite(t *testing.T) {
	runMembershipMatrix(t, sqliteMatrix(t), map[core.FieldType]string{
		core.FieldTypeString: "TEXT", core.FieldTypeUUID: "TEXT", core.FieldTypeInt: "INTEGER",
	})
}

func timeText(r filtertest.Record) any {
	if r.At == nil {
		return nil
	}
	return core.FormatTimestamp(*r.At)
}

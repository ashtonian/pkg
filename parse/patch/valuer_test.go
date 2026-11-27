//go:build unit

package patch_test

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// amount is a column type in the shape of decimal.Decimal: its JSON form is a
// quoted number and the driver stores Value().
type amount struct{ text string }

func (a *amount) UnmarshalJSON(b []byte) error {
	a.text = strings.Trim(string(b), `"`)
	return nil
}

func (a amount) MarshalJSON() ([]byte, error) { return []byte(`"` + a.text + `"`), nil }

func (a amount) Value() (driver.Value, error) { return a.text, nil }

// A patch to a column whose Go type is a database value binds that value,
// not its JSON form: the quoted "200" a decimal encodes to reached a NUMERIC
// column as the string "\"200\"" (PostgreSQL refused it; SQLite stored it).
func TestPatch_ADatabaseValueColumnBindsTheValue(t *testing.T) {
	type Limit struct {
		ID  string `json:"id"`
		Max amount `json:"max_value" db:"max_value" parse:"patch" patch_ops:"replace"`
	}
	schema, err := patch.GeneratePatchSchema[Limit](core.NewSchemaGenerator())
	require.NoError(t, err)

	parsed, err := patch.ParseMergePatch([]byte(`{"max_value":"200"}`), schema, nil)
	require.NoError(t, err)
	require.Len(t, parsed.Operations, 1)
	assert.Equal(t, amount{text: "200"}, parsed.Operations[0].Value)

	for _, dialect := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		built, err := patch.BuildPatchSQL(parsed, dialect)
		require.NoError(t, err)
		require.Len(t, built.Args, 1, dialect.Name())
		valuer, ok := built.Args[0].(driver.Valuer)
		require.True(t, ok)
		v, err := valuer.Value()
		require.NoError(t, err)
		assert.Equal(t, "200", v, dialect.Name())
	}
}

// A column of an sql.Null type holds NULL as the type's zero value: its path
// accepts null and remove, SQL writes NULL, and Apply stores the zero value,
// so a row cleared in Go reads as one cleared in the database.
// remove is refused, by sentinel, for a type whose column holds no NULL.
func TestGeneratePatchSchema_RemoveNeedsATypeThatHoldsNull(t *testing.T) {
	type unclearable struct {
		ID   string `json:"id"`
		Name string `json:"name" db:"name" parse:"patch" patch_ops:"replace,remove"`
	}
	_, err := patch.GeneratePatchSchema[unclearable](core.NewSchemaGenerator())
	require.ErrorIs(t, err, patch.ErrRemoveNeedsNull)
	require.ErrorContains(t, err, "field Name: patch operation remove requires a type that holds null")
	require.ErrorContains(t, err, "got string")
}

func TestPatch_AnSQLNullColumnClearsToNull(t *testing.T) {
	type Ranked struct {
		ID    string        `json:"id"`
		Score sql.NullInt64 `json:"score" db:"score" parse:"patch" patch_ops:"replace,remove"`
	}
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[Ranked](gen)
	require.NoError(t, err)
	path, ok := schema.Path("/score")
	require.True(t, ok)
	assert.True(t, path.Nullable())

	applier, err := patch.NewApplier[Ranked](gen, nil)
	require.NoError(t, err)
	merge, err := patch.ParseMergePatch([]byte(`{"score":null}`), schema, nil)
	require.NoError(t, err)
	remove, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"remove","path":"/score"}]`))
	require.NoError(t, err)
	for body, parsed := range map[string]*patch.ParsedPatch{"merge null": merge, "remove": remove} {
		for _, dialect := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
			built, err := patch.BuildPatchSQL(parsed, dialect)
			require.NoError(t, err, body)
			assert.Equal(t, `"score" = NULL`, built.SetClause, "%s %s", dialect.Name(), body)
			assert.Empty(t, built.Args, "%s %s", dialect.Name(), body)
		}
		got, err := applier.Apply(parsed, Ranked{ID: "a", Score: sql.NullInt64{Int64: 3, Valid: true}})
		require.NoError(t, err, body)
		assert.Equal(t, sql.NullInt64{}, got.Score, body)
	}
}

// SQLite stores a timestamp as its canonical text.
func TestPatch_NullableWrapperColumnsSQLite(t *testing.T) {
	runNullableWrapperPatches(t, wrapperDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, timestampType: "DATETIME", stamp: func(at sql.NullTime) any {
		if !at.Valid {
			return nil
		}
		return core.FormatTimestamp(at.Time)
	}})
}

// A test reads a nullable wrapper column as the column holds it, its value
// or null, as the replace after it writes it.
func TestPatch_ATestReadsAWrapperColumnsValue(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[rankedVisit](gen)
	require.NoError(t, err)
	applier, err := patch.NewApplier[rankedVisit](gen, nil)
	require.NoError(t, err)
	seen := rankedVisit{ID: 1, Score: sql.NullInt64{Int64: 3, Valid: true}, SeenAt: sql.NullTime{Time: rankedSeen(), Valid: true}}
	unseen := rankedVisit{ID: 1, Score: sql.NullInt64{Int64: 3, Valid: true}}
	rescored := sql.NullInt64{Int64: 8, Valid: true}
	for _, tt := range []struct {
		name   string
		target rankedVisit
		test   string
		holds  bool
	}{
		{"a score that holds", seen, `{"op":"test","path":"/score","value":3}`, true},
		{"a score that does not", seen, `{"op":"test","path":"/score","value":4}`, false},
		{"a time that holds", seen, `{"op":"test","path":"/seen_at","value":"2026-03-04T05:06:07Z"}`, true},
		{"a NULL time", unseen, `{"op":"test","path":"/seen_at","value":null}`, true},
		{"a time that is not NULL", seen, `{"op":"test","path":"/seen_at","value":null}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := patch.NewParser(schema, nil).Parse([]byte(`[` + tt.test + `,{"op":"replace","path":"/score","value":8}]`))
			require.NoError(t, err)
			got, err := applier.Apply(parsed, tt.target)
			if !tt.holds {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want := tt.target
			want.Score = rescored
			assert.Equal(t, want, got)
		})
	}
}

// annotated holds documents of nullable wrappers: notes of sql.NullString,
// which encoding/json writes as its fields even when NULL, and rates of
// decimal.NullDecimal, whose own JSON writes NULL as null.
type annotated struct {
	ID    string                `json:"id"    db:"id"`
	Notes []sql.NullString      `json:"notes" db:"notes" parse:"patch" patch_ops:"replace,array_append"`
	Rates []decimal.NullDecimal `json:"rates" db:"rates" parse:"patch"`
}

// Inside a document, null is what encoding/json writes: an element takes
// null only where its type writes NULL as null, so a patch stores what the
// application's own write of the document stores.
func TestPatch_ADocumentElementTakesNullAsJSONWritesIt(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[annotated](core.NewSchemaGenerator())
	require.NoError(t, err)
	written, err := json.Marshal(annotated{Notes: []sql.NullString{{}}, Rates: []decimal.NullDecimal{{}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"id":"","notes":[{"String":"","Valid":false}],"rates":[null]}`, string(written), "the application's own write")
	for _, tt := range []struct {
		name string
		body string
		want []any
	}{
		{"a NULL decimal is null", `[{"op":"replace","path":"/rates","value":[null,"1.5"]}]`, []any{nil, "1.5"}},
		{"an sql.NullString is its fields", `[{"op":"replace","path":"/notes","value":[{"String":"","Valid":false}]}]`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := patch.NewParser(schema, nil).Parse([]byte(tt.body))
			require.NoError(t, err)
			if tt.want != nil {
				got, err := json.Marshal(parsed.Operations[0].Value)
				require.NoError(t, err)
				want, err := json.Marshal(tt.want)
				require.NoError(t, err)
				assert.JSONEq(t, string(want), string(got))
			}
		})
	}
	for _, body := range []string{
		`[{"op":"replace","path":"/notes","value":[null]}]`,
		`[{"op":"array_append","path":"/notes","value":null}]`,
	} {
		_, err := patch.NewParser(schema, nil).Parse([]byte(body))
		assert.ErrorContains(t, err, "null is not valid for sql.NullString", body)
	}
}

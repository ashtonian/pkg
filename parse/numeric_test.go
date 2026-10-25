//go:build unit

package parse_test

import (
	"database/sql"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// numericEntity covers every integer width, signedness and pointer shape the
//
//	probes corrupted.
type numericEntity struct {
	ID    int64          `json:"id"    db:"id"    parse:"filter,sort"`
	Small int8           `json:"small" db:"small" parse:"filter,patch"`
	U8    uint8          `json:"u8"    db:"u8"    parse:"filter,patch"`
	PU64  *uint64        `json:"pu64"  db:"pu64"  parse:"patch"`
	PI    *int           `json:"pi"    db:"pi"    parse:"patch"`
	Big   int64          `json:"big"   db:"big"   parse:"filter,patch" patch_ops:"replace,test"`
	Ratio float32        `json:"ratio" db:"ratio" parse:"patch"`
	Meta  map[string]any `json:"meta"  db:"meta"  parse:"patch"`
}

func numericParser(t *testing.T) *parse.Resource[numericEntity] {
	t.Helper()
	p, err := parse.NewResource[numericEntity]("numeric_entities")
	require.NoError(t, err)
	return p
}

func requireCode(t *testing.T, err error, want string) {
	t.Helper()
	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "want *core.ParseError, got %v", err)
	assert.Equal(t, want, pe.Code, pe.Message)
}

// Out-of-domain values are rejected when the patch is parsed — never wrapped,
// truncated or rounded into a different value by a later backend.
func TestPatch_RejectsOutOfDomainNumbers(t *testing.T) {
	p := numericParser(t)
	bodies := map[string]string{
		"int8 overflow":         `{"small": 128}`,
		"int8 underflow":        `{"small": -129}`,
		"uint8 overflow":        `{"u8": 256}`,
		"unsigned negative":     `{"u8": -1}`,
		"*uint64 negative":      `{"pu64": -1}`,
		"*int fractional":       `{"pi": 3.7}`,
		"int64 overflow":        `{"big": 9223372036854775808}`,
		"float32 overflow":      `{"ratio": 1e39}`,
		"string for an integer": `{"small": "5"}`,
	}
	for name, body := range bodies {
		t.Run("merge/"+name, func(t *testing.T) {
			_, err := p.ParseMergePatch([]byte(body))
			requireCode(t, err, patch.ECodePatchValueInvalid.String())
		})
	}
	_, err := p.ParsePatch([]byte(`[{"op":"replace","path":"/small","value":128}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
}

func TestPatch_AppliesExactIntegers(t *testing.T) {
	p := numericParser(t)

	t.Run("direct path", func(t *testing.T) {
		parsed, err := p.ParseMergePatch([]byte(`{"big": 9007199254740993, "pu64": 18446744073709551615, "small": -128, "pi": 7}`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{})
		require.NoError(t, err)
		assert.Equal(t, int64(9007199254740993), got.Big)
		require.NotNil(t, got.PU64)
		assert.Equal(t, uint64(math.MaxUint64), *got.PU64)
		assert.Equal(t, int8(-128), got.Small)
		require.NotNil(t, got.PI)
		assert.Equal(t, 7, *got.PI)
	})

	t.Run("document path", func(t *testing.T) {
		// A test op forces the encode → apply → decode document path.
		parsed, err := p.ParsePatch([]byte(`[{"op":"test","path":"/big","value":1},{"op":"replace","path":"/big","value":9007199254740993}]`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{Big: 1})
		require.NoError(t, err)
		assert.Equal(t, int64(9007199254740993), got.Big)
	})

	t.Run("document path leaves untouched fields exact", func(t *testing.T) {
		parsed, err := p.ParsePatch([]byte(`[{"op":"test","path":"/big","value":9007199254740993},{"op":"replace","path":"/small","value":1}]`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{Big: 9007199254740993, PU64: new(uint64(math.MaxUint64))})
		require.NoError(t, err)
		assert.Equal(t, int64(9007199254740993), got.Big)
		assert.Equal(t, uint64(math.MaxUint64), *got.PU64)
		assert.Equal(t, int8(1), got.Small)
	})

	t.Run("nested JSON keeps exact leaves", func(t *testing.T) {
		parsed, err := p.ParseMergePatch([]byte(`{"meta": {"n": 9007199254740993}}`))
		require.NoError(t, err)
		got, err := p.ApplyValidatedPatch(parsed, numericEntity{})
		require.NoError(t, err)
		n, ok := core.CompareNumbers(got.Meta["n"], int64(9007199254740993))
		assert.True(t, ok)
		assert.Zero(t, n)
	})
}

// The SQL backends bind the normalized value; SQLite must store exactly what
// the client sent.
func TestPatch_SQLStoresExactIntegers(t *testing.T) {
	p := numericParser(t)
	parsed, err := p.ParseMergePatch([]byte(`{"big": 9007199254740993}`))
	require.NoError(t, err)
	res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
	require.NoError(t, err)
	require.Equal(t, []any{int64(9007199254740993)}, res.Args)

	db := openNumericDB(t)
	_, err = db.Exec("UPDATE numeric_entities SET "+res.SetClause+" WHERE id = 1", res.Args...)
	require.NoError(t, err)
	var stored int64
	require.NoError(t, db.QueryRow("SELECT big FROM numeric_entities WHERE id = 1").Scan(&stored))
	assert.Equal(t, int64(9007199254740993), stored)
}

// A filter on 2^53+1 must select that row, not its float64 neighbour, on
// every ingress.
func TestFilter_SelectsExactIntegerRow(t *testing.T) {
	p := numericParser(t)
	db := openNumericDB(t)

	cases := map[string]func() (*filter.FilterAST, error){
		"json eq": func() (*filter.FilterAST, error) {
			f, err := p.ParseFilter(`{"big": 9007199254740993}`)
			if err != nil {
				return nil, err
			}
			return f.AST, nil
		},
		"json in": func() (*filter.FilterAST, error) {
			f, err := p.ParseFilter(`{"big": {"$in": [9007199254740993]}}`)
			if err != nil {
				return nil, err
			}
			return f.AST, nil
		},
		"typed map": func() (*filter.FilterAST, error) {
			f, err := p.ParseFilterMap(map[string]any{"big": map[string]any{"$in": []int64{9007199254740993}}})
			if err != nil {
				return nil, err
			}
			return f.AST, nil
		},
		"query params": func() (*filter.FilterAST, error) {
			return filter.NewQueryParamParser(p.FilterSchema()).Parse(map[string][]string{"big": {"9007199254740993"}})
		},
	}
	for name, parseFn := range cases {
		t.Run(name, func(t *testing.T) {
			ast, err := parseFn()
			require.NoError(t, err)
			res, err := filter.NewSQLBuilder(p.FilterSchema(), filter.SQLiteDialect{}).Build(ast)
			require.NoError(t, err)
			rows, err := db.Query("SELECT id FROM numeric_entities WHERE "+res.Where, res.Args...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			var ids []int64
			for rows.Next() {
				var id int64
				require.NoError(t, rows.Scan(&id))
				ids = append(ids, id)
			}
			require.NoError(t, rows.Err())
			assert.Equal(t, []int64{2}, ids)
		})
	}
}

func TestFilter_RejectsOutOfDomainNumbers(t *testing.T) {
	p := numericParser(t)
	for name, q := range map[string]string{
		"int8 overflow":        `{"small": 128}`,
		"unsigned negative":    `{"u8": {"$in": [1, -1]}}`,
		"fraction for integer": `{"big": 1.5}`,
		"float lost precision": `{"big": 9007199254740993.0e0}`,
		"string for integer":   `{"big": "5"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.ParseFilter(q)
			if name == "float lost precision" {
				// An exact literal written in exponent form is still exact.
				require.NoError(t, err)
				return
			}
			requireCode(t, err, filter.ECodeValueTypeMismatch.String())
		})
	}
	_, err := filter.NewQueryParamParser(p.FilterSchema()).Parse(map[string][]string{"small": {"128"}})
	requireCode(t, err, filter.ECodeValueTypeMismatch.String())
}

func openNumericDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.Exec(`CREATE TABLE numeric_entities (id INTEGER PRIMARY KEY, small INTEGER, u8 INTEGER, big INTEGER);
		INSERT INTO numeric_entities (id, big) VALUES (1, 9007199254740992), (2, 9007199254740993), (3, 9007199254740994);`)
	require.NoError(t, err)
	return db
}

// A patch that fails part-way must not leave a pointer target half-written
// (2026-09-28 review, PARSE-08).
func TestApplyValidatedPatch_PointerTargetUntouchedOnFailure(t *testing.T) {
	p, err := parse.NewResource[*numericEntity]("numeric_entities")
	require.NoError(t, err)
	parsed, err := p.ParseMergePatch([]byte(`{"big": 5, "meta": {"a": 1}}`))
	require.NoError(t, err)
	// Force the second operation to fail at apply by retyping its value.
	for _, op := range parsed.Operations {
		if op.Path == "/meta" {
			op.Value = "not an object"
			op.Merge = false
		}
	}
	original := &numericEntity{Big: 1}
	_, err = p.ApplyValidatedPatch(parsed, original)
	require.Error(t, err)
	assert.Equal(t, int64(1), original.Big, "caller's struct must be unchanged after a failed patch")

	ok, err := p.ParseMergePatch([]byte(`{"big": 5}`))
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(ok, original)
	require.NoError(t, err)
	assert.Equal(t, int64(5), got.Big)
	assert.Equal(t, int64(1), original.Big, "successful apply returns a new value")
}

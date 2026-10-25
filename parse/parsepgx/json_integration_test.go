//go:build integration

package parsepgx_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

type JSONFields struct {
	Text  string `json:"text" db:"document_text" parse:"patch"`
	Bytes []byte `json:"bytes" db:"document_bytes" parse:"patch"`
}

type jsonObject struct {
	Name string `json:"name"`
}

type jsonDecoded struct {
	Value string
	Calls int
}

var errStoredJSON = errors.New("stored JSON rejected")

func (v *jsonDecoded) UnmarshalJSON(raw []byte) error {
	v.Calls++
	if err := json.Unmarshal(raw, &v.Value); err != nil {
		return err
	}
	if v.Value == "reject" {
		return errStoredJSON
	}
	return nil
}

type jsonRecord struct {
	JSONFields
	ID      string          `json:"id" db:"id" parse:"sort"`
	Number  int             `json:"number" db:"number"`
	Flag    bool            `json:"flag" db:"flag"`
	Object  jsonObject      `json:"object" db:"object"`
	Keys    map[int]string  `json:"keys" db:"keys"`
	Dynamic any             `json:"dynamic" db:"dynamic"`
	Raw     json.RawMessage `json:"raw" db:"raw"`
	Pointer *string         `json:"pointer" db:"pointer"`
	Decoded jsonDecoded     `json:"decoded" db:"decoded"`
	Native  []string        `json:"native" db:"native"`
	Binary  []byte          `json:"binary" db:"binary"`
	Secret  string          `json:"-" db:"secret"`
	Labels  []string        `json:"labels" db:"-" parse:"patch" relation:"document_labels,document_id,label_id,id"`
}

func jsonResource(opts ...parse.ResourceOption) *parse.Resource[jsonRecord] {
	return parse.MustResource[jsonRecord]("documents", append([]parse.ResourceOption{
		parse.ColumnType("document_text", core.SQLJSONB),
		parse.ColumnType("document_bytes", core.SQLJSON),
		parse.ColumnType("number", core.SQLJSONB),
		parse.ColumnType("flag", core.SQLJSONB),
		parse.ColumnType("pointer", core.SQLJSONB),
	}, opts...)...)
}

func TestJSONStorage_QueryAndUpdateEveryExecMode(t *testing.T) {
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	setup := c.open(t, pgx.QueryExecModeCacheStatement)
	ctx := testContext(t)
	_, err := setup.Exec(ctx, `CREATE TABLE documents (
		id text PRIMARY KEY, document_text jsonb, document_bytes json, number jsonb, flag jsonb,
		object jsonb, keys json, dynamic jsonb, raw jsonb, pointer jsonb, decoded jsonb,
		native text[], binary bytea, secret text);
		CREATE TABLE document_labels (document_id text, label_id text CHECK (label_id <> 'forbidden'), PRIMARY KEY(document_id, label_id));
		INSERT INTO documents VALUES ('full', '"hello"', '"YQ=="', '7', 'true', '{"name":"original"}', '{"1":"one"}',
		'{"value":1}', 'null', '"pointed"', '"custom"', '{native}', '\x61', 'private');
		INSERT INTO documents(id) VALUES ('null')`)
	require.NoError(t, err)
	r := jsonResource()
	want := jsonRecord{
		JSONFields: JSONFields{Text: "hello", Bytes: []byte("a")}, ID: "full", Number: 7, Flag: true,
		Object: jsonObject{Name: "original"}, Keys: map[int]string{1: "one"}, Dynamic: map[string]any{"value": float64(1)},
		Raw: json.RawMessage(`null`), Pointer: new("pointed"), Decoded: jsonDecoded{Value: "custom", Calls: 1},
		Native: []string{"native"}, Binary: []byte("a"), Secret: "private",
	}
	for name, mode := range jsonExecModes() {
		t.Run(name, func(t *testing.T) {
			pool := c.open(t, mode)
			plan, err := r.BuildList(filter.PostgresDialect{}, nil, parse.Scope{})
			require.NoError(t, err)
			page, err := parsepgx.QueryPage(ctx, pool, r, plan)
			require.NoError(t, err)
			require.Len(t, page.Items, 2)
			assert.Equal(t, want, page.Items[0])
			assert.Equal(t, jsonRecord{ID: "null"}, page.Items[1], "SQL NULL resets the typed field")

			for _, body := range []string{`{"text":"hello","bytes":"YQ=="}`, `{"text":"hello","labels":["label"]}`, `{}`} {
				p, err := r.ParseMergePatch([]byte(body))
				require.NoError(t, err)
				update, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("full"), parse.Scope{})
				require.NoError(t, err)
				rows, err := parsepgx.Update(ctx, pool, r, update)
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.Equal(t, want, rows[0])
				applied, err := r.ApplyValidatedPatch(p, want)
				require.NoError(t, err)
				assert.Equal(t, applied.JSONFields, rows[0].JSONFields)
			}

			projected := jsonResource(parse.ReadColumns("id", "document_text", "document_bytes"))
			list, err := projected.BuildList(filter.PostgresDialect{}, nil, parse.Scope{})
			require.NoError(t, err)
			page, err = parsepgx.QueryPage(ctx, pool, projected, list)
			require.NoError(t, err)
			require.Len(t, page.Items, 2)
			assert.Equal(t, jsonRecord{ID: "full", JSONFields: want.JSONFields}, page.Items[0])

			tx, err := pool.Begin(ctx)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, tx.Rollback(testContext(t))) })
			p, err := r.ParseMergePatch([]byte(`{"text":"inside","labels":["transaction"]}`))
			require.NoError(t, err)
			update, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("full"), parse.Scope{})
			require.NoError(t, err)
			rows, err := parsepgx.Update(ctx, tx, r, update)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, "inside", rows[0].Text)
		})
	}
}

type jsonValueRow[T any] struct {
	ID     string   `json:"id" db:"id" parse:"sort"`
	Value  T        `json:"value" db:"value"`
	Name   string   `json:"name" db:"name" parse:"patch"`
	Labels []string `json:"labels" db:"-" parse:"patch" relation:"value_labels,value_id,label_id,id"`
}

func TestJSONStorage_RejectsLossyRowsEveryExecMode(t *testing.T) {
	c := &catalog{dsn: dbtest.PostgresDSN(t)}
	setup := c.open(t, pgx.QueryExecModeCacheStatement)
	_, err := setup.Exec(testContext(t), `CREATE TABLE stored_values (id text PRIMARY KEY, value json, name text);
		CREATE TABLE value_labels (value_id text, label_id text, PRIMARY KEY(value_id,label_id))`)
	require.NoError(t, err)
	for name, mode := range jsonExecModes() {
		t.Run(name, func(t *testing.T) {
			pool := c.open(t, mode)
			t.Run("map spelling", func(t *testing.T) { rejectJSONRow[map[int]int](t, pool, `{"01":1}`, nil) })
			t.Run("map collision", func(t *testing.T) { rejectJSONRow[map[int]int](t, pool, `{"1":1,"01":2}`, nil) })
			t.Run("struct spelling", func(t *testing.T) { rejectJSONRow[jsonObject](t, pool, `{"NAME":"value"}`, nil) })
			t.Run("null interface", func(t *testing.T) { rejectJSONRow[any](t, pool, `null`, nil) })
			t.Run("null map", func(t *testing.T) { rejectJSONRow[map[string]int](t, pool, `null`, nil) })
			t.Run("null pointer", func(t *testing.T) { rejectJSONRow[*string](t, pool, `null`, nil) })
			t.Run("custom error", func(t *testing.T) { rejectJSONRow[jsonDecoded](t, pool, `"reject"`, errStoredJSON) })
		})
	}
}

func rejectJSONRow[T any](t *testing.T, pool *pgxpool.Pool, raw string, cause error) {
	t.Helper()
	ctx := testContext(t)
	_, err := pool.Exec(ctx, `INSERT INTO stored_values VALUES ('one', $1::json, 'original')
		ON CONFLICT(id) DO UPDATE SET value=EXCLUDED.value, name=EXCLUDED.name`, raw)
	require.NoError(t, err)
	r := parse.MustResource[jsonValueRow[T]]("stored_values", parse.ColumnType("value", core.SQLJSON))
	list, err := r.BuildList(filter.PostgresDialect{}, nil, parse.Scope{})
	require.NoError(t, err)
	page, err := parsepgx.QueryPage(ctx, pool, r, list)
	require.Error(t, err)
	assert.Nil(t, page)
	if cause != nil {
		require.ErrorIs(t, err, cause)
	}
	for _, body := range []string{`{"name":"original"}`, `{"name":"changed","labels":["never"]}`} {
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		update, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("one"), parse.Scope{})
		require.NoError(t, err)
		rows, err := parsepgx.Update(ctx, pool, r, update)
		require.Error(t, err)
		assert.Nil(t, rows)
		if cause != nil {
			require.ErrorIs(t, err, cause)
		}
	}
	var name string
	require.NoError(t, pool.QueryRow(ctx, `SELECT name FROM stored_values WHERE id='one'`).Scan(&name))
	assert.Equal(t, "original", name, "relation transaction rolls back on decode failure")
	var labels int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM value_labels`).Scan(&labels))
	assert.Zero(t, labels)
}

func jsonExecModes() map[string]pgx.QueryExecMode {
	return map[string]pgx.QueryExecMode{
		"cache statement": pgx.QueryExecModeCacheStatement,
		"cache describe":  pgx.QueryExecModeCacheDescribe,
		"describe exec":   pgx.QueryExecModeDescribeExec,
		"exec":            pgx.QueryExecModeExec,
		"simple protocol": pgx.QueryExecModeSimpleProtocol,
	}
}

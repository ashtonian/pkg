//go:build integration

package corpus

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/patch"
)

// Engine is a SQL database the corpus runs on.
type Engine string

const (
	// SQLite is an in-memory SQLite database through parsesql.
	SQLite Engine = "sqlite"
	// Postgres is a fresh schema of the PostgreSQL test server through
	// parsepgx.
	Postgres Engine = "postgres"
)

// sqlDialect compiles for a backend and writes a value as its provider
// binds it.
type sqlDialect interface {
	filter.Dialect
	MarshalValue(value any, t core.TypeInfo) (any, error)
}

// sqlBackend is one database every SQL family runs on: the dialect that
// compiles for it, how a test opens a fresh database there, and how a
// table there declares, binds and holds each kind of value, following
// core.TypeOf's storage model. A family's own variants (a collation, the
// PostGIS server, another column type for a kind) are sqlBackendOptions.
type sqlBackend struct {
	engine  Engine
	dialect sqlDialect
	// sqlite opens a fresh SQLite database, and pool a pool on a fresh
	// PostgreSQL schema; each backend sets its engine's.
	sqlite func(t *testing.T) *sql.DB
	pool   func(t *testing.T) *pgxpool.Pool
	// storage replaces the engine's column type for a field type.
	storage map[core.FieldType]string
}

// sqlBackendOption is a family's variant of a backend.
type sqlBackendOption func(*sqlBackend)

// sqlBackendOf is engine's backend with opts applied.
func sqlBackendOf(engine Engine, opts ...sqlBackendOption) sqlBackend {
	var b sqlBackend
	switch engine {
	case SQLite:
		b = sqlBackend{engine: SQLite, dialect: filter.SQLiteDialect{}, sqlite: func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }}
	case Postgres:
		b = sqlBackend{engine: Postgres, dialect: filter.PostgresDialect{}, pool: func(t *testing.T) *pgxpool.Pool { return dbtest.PostgresPool(t) }}
	default:
		panic("corpus: no engine " + string(engine))
	}
	return b.with(opts...)
}

// runOn runs fn on engine's backend with opts applied.
func runOn(t *testing.T, engine Engine, fn func(t *testing.T, b sqlBackend), opts ...sqlBackendOption) {
	t.Helper()
	fn(t, sqlBackendOf(engine, opts...))
}

// open opens a fresh database on b: the provider resources run on there,
// compiling for b's dialect, and a handle for the test's own statements.
func (b sqlBackend) open(t *testing.T) (*parse.Provider, *sql.DB) {
	t.Helper()
	return b.openSettling(t, parse.DefaultSettleTimeout)
}

// openSettling is open with each statement that settles an update bounded
// by settle (parsesql.SettleTimeout, parsepgx.SettleTimeout).
func (b sqlBackend) openSettling(t *testing.T, settle time.Duration) (*parse.Provider, *sql.DB) {
	t.Helper()
	if b.engine == SQLite {
		db := b.sqlite(t)
		return providertest.SQLiteOn(t, db, parsesql.SettleTimeout(settle)), db
	}
	pg, ok := b.dialect.(filter.PostgresDialect)
	require.True(t, ok, "a %s backend compiles for %T", b.engine, b.dialect)
	return providertest.PostgresOn(t, b.pool(t),
		parsepgx.Collation(pg.Collation), parsepgx.ContainmentMaxValues(pg.ContainmentMaxValues), parsepgx.SettleTimeout(settle))
}

// with is b with opts applied.
func (b sqlBackend) with(opts ...sqlBackendOption) sqlBackend {
	b.storage = maps.Clone(b.storage)
	for _, opt := range opts {
		opt(&b)
	}
	return b
}

// on applies opts to engine's backend alone.
func on(engine Engine, opts ...sqlBackendOption) sqlBackendOption {
	return func(b *sqlBackend) {
		if b.engine != engine {
			return
		}
		for _, opt := range opts {
			opt(b)
		}
	}
}

// storing declares the columns holding values of ft as columnType.
func storing(ft core.FieldType, columnType string) sqlBackendOption {
	return func(b *sqlBackend) {
		if b.storage == nil {
			b.storage = map[core.FieldType]string{}
		}
		b.storage[ft] = columnType
	}
}

// onPostGIS opens PostgreSQL on the PARSE_POSTGIS_DSN server, PostGIS
// enabled.
func onPostGIS() sqlBackendOption {
	return on(Postgres, func(b *sqlBackend) {
		b.pool = func(t *testing.T) *pgxpool.Pool { return dbtest.PostGISPool(t) }
	})
}

// collation compiles PostgreSQL's code-point comparisons under c and
// declares its text columns with c.
func collation(c filter.PostgresCollation) sqlBackendOption {
	return on(Postgres, func(b *sqlBackend) {
		b.dialect = filter.PostgresDialect{Collation: c}
		storing(core.FieldTypeString, "TEXT COLLATE "+c.String())(b)
	})
}

// sessionParams opens PostgreSQL connections that start with params, runtime
// parameters such as TimeZone, set as startup parameters so every
// connection the pool opens has them.
func sessionParams(params map[string]string) sqlBackendOption {
	return on(Postgres, func(b *sqlBackend) {
		b.pool = func(t *testing.T) *pgxpool.Pool {
			config := dbtest.PostgresPoolConfig(t)
			maps.Copy(config.ConnConfig.RuntimeParams, params)
			pool, err := pgxpool.NewWithConfig(dbtest.Context(t), config)
			require.NoError(t, err)
			t.Cleanup(pool.Close)
			return pool
		}
	})
}

// hooked opens SQLite through the driver that reports each statement to the
// hook dbtest.OnStatement puts in its context.
func hooked() sqlBackendOption {
	return on(SQLite, func(b *sqlBackend) {
		b.sqlite = func(t *testing.T) *sql.DB { return dbtest.SQLiteHooked(t) }
	})
}

// columnType is the column type b declares a column of kind with: SQLite
// holds integers and booleans as INTEGER, floats as REAL and every other
// value as text (decimals exactly, timestamps as their canonical text,
// documents and arrays as JSON); PostgreSQL holds each in its native type.
func (b sqlBackend) columnType(kind core.TypeInfo) string {
	declared, ok := b.storage[kind.Type]
	if ok {
		return declared
	}
	if b.engine == SQLite {
		switch kind.Type {
		case core.FieldTypeInt, core.FieldTypeBool:
			return "INTEGER"
		case core.FieldTypeFloat:
			return "REAL"
		}
		return "TEXT"
	}
	switch kind.Type {
	case core.FieldTypeArray:
		return b.columnType(core.TypeInfo{Type: kind.Elem, NumberKind: kind.NumberKind}) + "[]"
	case core.FieldTypeJSON, core.FieldTypeObject, core.FieldTypeGeo:
		return "JSONB"
	case core.FieldTypeInt:
		return "BIGINT"
	case core.FieldTypeFloat:
		if kind.NumberKind == reflect.Float32 {
			return "REAL"
		}
		return "DOUBLE PRECISION"
	case core.FieldTypeBool:
		return "BOOLEAN"
	case core.FieldTypeDecimal:
		return "NUMERIC"
	case core.FieldTypeTimestamp:
		return "TIMESTAMPTZ"
	case core.FieldTypeString:
		return "TEXT"
	}
	// date, time, interval, inet, cidr, macaddr, uuid: PostgreSQL's type
	// of the name.
	return strings.ToUpper(string(kind.Type))
}

// clock is the SQL expression for the database's current time, as a
// timestamp column on b holds it: on SQLite parse's canonical text, UTC.
func (b sqlBackend) clock() string {
	if b.engine == SQLite {
		return `strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`
	}
	return "now()"
}

// arg is v as a column of kind on b binds it: NULL for nil, a document's
// JSON text (raw JSON and strings as written, so a fixture keeps its key
// order and spacing), a decimal as its text at its own scale (a decimal's
// driver value drops trailing zeros), an array on PostgreSQL as the slice
// itself (a fixture's NULL elements included, which the executor's typed
// slices refuse), and anything else as the dialect's executor writes it: a
// timestamp as SQLite's canonical text, an array as JSON text on SQLite.
func (b sqlBackend) arg(t testing.TB, kind core.TypeInfo, v any) any {
	t.Helper()
	v, err := core.Deref(v)
	require.NoError(t, err)
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.IsNil() {
		return nil
	}
	if core.IsDocument(kind.Type) || kind.Type == core.FieldTypeGeo {
		return documentText(t, v)
	}
	if kind.Type == core.FieldTypeArray && b.engine == Postgres {
		return v
	}
	d, isDecimal := v.(interface {
		StringFixed(places int32) string
		Exponent() int32
	})
	if isDecimal && kind.Type == core.FieldTypeDecimal {
		return d.StringFixed(max(0, -d.Exponent()))
	}
	out, err := b.dialect.MarshalValue(v, kind)
	require.NoError(t, err, "a %s value %v", kind.Type, v)
	return out
}

// documentText is a document's JSON text: raw JSON as written, anything
// else as encoding/json writes it.
func documentText(t testing.TB, v any) string {
	t.Helper()
	switch doc := v.(type) {
	case json.RawMessage:
		return string(doc)
	case string:
		return doc
	}
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// sqlColumn is a column of a fixture table and the kind of value it holds,
// from which a backend declares it, binds its values and reads them.
type sqlColumn struct {
	name    string
	kind    core.TypeInfo
	notNull bool
	// as replaces the column type on the engines it names: a fixture that
	// holds this column's kind otherwise than its other columns.
	as map[Engine]string
}

// column is a column holding values of Go type T, classified by
// core.TypeOf.
func column[T any](name string) sqlColumn {
	c := sqlColumn{name: name, kind: core.TypeOf(reflect.TypeFor[T]())}
	return c
}

// holding is c declared as columnType on engine.
func (c sqlColumn) holding(engine Engine, columnType string) sqlColumn {
	c.as = maps.Clone(c.as)
	if c.as == nil {
		c.as = map[Engine]string{}
	}
	c.as[engine] = columnType
	return c
}

// required is c declared NOT NULL.
func (c sqlColumn) required() sqlColumn {
	c.notNull = true
	return c
}

// sqlTable is a fixture table: its name, its columns and its primary key's
// columns, none for a table without one.
type sqlTable struct {
	name    string
	columns []sqlColumn
	key     []string
}

// holding is table with its column name declared as columnType on engine.
func (table sqlTable) holding(name string, engine Engine, columnType string) sqlTable {
	i := slices.IndexFunc(table.columns, func(c sqlColumn) bool { return c.name == name })
	if i < 0 {
		panic("table " + table.name + " has no column " + name)
	}
	table.columns = slices.Clone(table.columns)
	table.columns[i] = table.columns[i].holding(engine, columnType)
	return table
}

// schemaTable is the table schema's fields read: a column per field that
// reads a column of its own, keyed by schema's primary key or by id. A
// document declared held as json or text is a JSON or TEXT column on
// PostgreSQL.
func schemaTable(schema *filter.ResourceSchema) sqlTable {
	table := sqlTable{name: schema.Name, key: schema.PrimaryKey}
	if len(table.key) == 0 {
		table.key = []string{"id"}
	}
	seen := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(schema.Fields)) {
		f := schema.Fields[name]
		if f.Path != nil || f.Relation != nil || f.Expr != "" || f.Column == "" || seen[f.Column] {
			continue
		}
		seen[f.Column] = true
		c := sqlColumn{name: f.Column, kind: core.TypeInfo{Type: f.DataType, Elem: f.Elem, NumberKind: f.NumberKind}}
		switch f.Storage {
		case filter.StorageJSON:
			c = c.holding(Postgres, "JSON")
		case filter.StorageText:
			c = c.holding(Postgres, "TEXT")
		}
		table.columns = append(table.columns, c)
	}
	return table
}

// recordRows are records as rows of table: a row per record, its value per
// column the record's field of that db name.
func recordRows[T any](t testing.TB, table sqlTable, records []T) [][]any {
	t.Helper()
	fields := map[string]int{}
	rt := reflect.TypeFor[T]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("db"), ",")
		fields[name] = i
	}
	rows := make([][]any, len(records))
	for r, record := range records {
		rv := reflect.ValueOf(record)
		row := make([]any, len(table.columns))
		for i, c := range table.columns {
			index, ok := fields[c.name]
			require.True(t, ok, "%s has no field for column %s", rt, c.name)
			row[i] = rv.Field(index).Interface()
		}
		rows[r] = row
	}
	return rows
}

// declared is c's column type on b.
func (b sqlBackend) declared(c sqlColumn) string {
	declared, ok := c.as[b.engine]
	if ok {
		return declared
	}
	return b.columnType(c.kind)
}

// placeholder is the nth parameter's placeholder in a value of c: on
// PostgreSQL a document's casts to its json or jsonb column.
func (b sqlBackend) placeholder(n int, c sqlColumn) string {
	ph := b.dialect.Placeholder(n)
	declared := b.declared(c)
	if b.engine == Postgres && (declared == "JSONB" || declared == "JSON") {
		return ph + "::" + strings.ToLower(declared)
	}
	return ph
}

// create creates tables in db.
func (b sqlBackend) create(t testing.TB, db *sql.DB, tables ...sqlTable) {
	t.Helper()
	for _, table := range tables {
		defs := make([]string, 0, len(table.columns)+1)
		for _, c := range table.columns {
			def := c.name + " " + b.declared(c)
			if c.notNull {
				def += " NOT NULL"
			}
			defs = append(defs, def)
		}
		if len(table.key) > 0 {
			defs = append(defs, "PRIMARY KEY ("+strings.Join(table.key, ", ")+")")
		}
		dbtest.Exec(t, db, "CREATE TABLE "+table.name+" ("+strings.Join(defs, ", ")+")")
	}
}

// insert writes rows into table, each a value per column in table's order.
func (b sqlBackend) insert(t testing.TB, db *sql.DB, table sqlTable, rows ...[]any) {
	t.Helper()
	names := make([]string, len(table.columns))
	phs := make([]string, len(table.columns))
	for i, c := range table.columns {
		names[i] = c.name
		phs[i] = b.placeholder(i+1, c)
	}
	insert := "INSERT INTO " + table.name + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	for _, row := range rows {
		require.Len(t, row, len(table.columns), "a row of %s", table.name)
		args := make([]any, len(row))
		for i, v := range row {
			args[i] = b.arg(t, table.columns[i].kind, v)
		}
		_, err := db.ExecContext(t.Context(), insert, args...)
		require.NoError(t, err, "%s %v", insert, row)
	}
}

// filterIDs is a filtertest backend over schema's table in db: the ids of
// the rows each filter's WHERE selects there.
func (b sqlBackend) filterIDs(db *sql.DB, schema *filter.ResourceSchema) filtertest.Backend {
	return func(t *testing.T, f *filter.ParsedFilter) ([]string, error) {
		res, err := filter.NewSQLBuilder(schema, b.dialect).Build(f.AST())
		if err != nil {
			return nil, err
		}
		return dbtest.QueryRows[string](t, db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args...), nil
	}
}

// rowIDs is each row's first column, the id a statement selects first,
// which must be text.
func rowIDs(t testing.TB, rows [][]any) []string {
	t.Helper()
	return parsetest.ItemIDs(rows, func(row []any) string {
		id, ok := row[0].(string)
		require.True(t, ok, "an id is text: %T %v", row[0], row[0])
		return id
	})
}

// load creates table in db and writes rows into it.
func (b sqlBackend) load(t testing.TB, db *sql.DB, table sqlTable, rows [][]any) {
	t.Helper()
	b.create(t, db, table)
	b.insert(t, db, table, rows...)
}

// sortedIDs is parsetest.ItemIDs sorted: the rows a statement returned, compared as a
// set whatever order they came back in.
func sortedIDs[T any, K cmp.Ordered](items []T, id func(T) K) []K {
	out := parsetest.ItemIDs(items, id)
	slices.Sort(out)
	return out
}

// listPage lists req under s on r's provider (Resource.List).
func listPage[T any](t testing.TB, r *parse.Resource[T], req *parse.ListRequest, s parse.Scope) *filter.Page[T] {
	t.Helper()
	page, err := r.List(t.Context(), req, s)
	require.NoError(t, err)
	return page
}

// applyUpdate applies p to target under s on r's provider (Resource.Update),
// or with inTx in a transaction it rolls back after the call, the update
// under a savepoint of it (Resource.In).
func applyUpdate[T any](t testing.TB, r *parse.Resource[T], inTx bool, p *patch.ParsedPatch, target parse.Target, s parse.Scope, opts ...parse.UpdateOption) (parse.UpdateResult[T], error) {
	t.Helper()
	if !inTx {
		return r.Update(t.Context(), p, target, s, opts...)
	}
	tx, err := r.Provider().Begin(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
	scoped, err := r.In(tx)
	require.NoError(t, err)
	return scoped.Update(t.Context(), p, target, s, opts...)
}

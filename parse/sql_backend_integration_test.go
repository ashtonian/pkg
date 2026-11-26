//go:build integration

package parse_test

import (
	"database/sql"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// sqlEngine is a database parse's SQL runs on in the integration suites.
type sqlEngine string

const (
	sqliteEngine   sqlEngine = "sqlite"
	postgresEngine sqlEngine = "postgres"
)

// sqlDialect compiles for a backend and writes a value as its executor
// binds it.
type sqlDialect interface {
	parse.Dialect
	MarshalValue(value any, t core.TypeInfo) (any, error)
}

// sqlBackend is one database every SQL family runs on: the dialect that
// compiles for it, how a test opens a fresh database there, and how a
// table there declares, binds and holds each kind of value, following
// core.TypeOf's storage model. A family's own variants (a collation, the
// PostGIS server, another column type for a kind) are sqlBackendOptions.
type sqlBackend struct {
	engine  sqlEngine
	dialect sqlDialect
	open    func(t *testing.T) *sql.DB
	// storage replaces the engine's column type for a field type.
	storage map[core.FieldType]string
}

// sqlBackendOption is a family's variant of a backend.
type sqlBackendOption func(*sqlBackend)

// sqlBackends are SQLite and PostgreSQL, each with opts applied.
func sqlBackends(opts ...sqlBackendOption) []sqlBackend {
	backends := []sqlBackend{
		{engine: sqliteEngine, dialect: filter.SQLiteDialect{}, open: func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }},
		{engine: postgresEngine, dialect: filter.PostgresDialect{}, open: func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }},
	}
	for i := range backends {
		backends[i] = backends[i].with(opts...)
	}
	return backends
}

// sqlBackendOf is engine's backend with opts applied, for a test of what
// one engine alone does.
func sqlBackendOf(engine sqlEngine, opts ...sqlBackendOption) sqlBackend {
	for _, b := range sqlBackends(opts...) {
		if b.engine == engine {
			return b
		}
	}
	panic("no backend " + string(engine))
}

// runSQLBackends runs fn on each of sqlBackends(opts...), a subtest named
// for its engine.
func runSQLBackends(t *testing.T, fn func(t *testing.T, b sqlBackend), opts ...sqlBackendOption) {
	t.Helper()
	for _, b := range sqlBackends(opts...) {
		t.Run(string(b.engine), func(t *testing.T) { fn(t, b) })
	}
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
func on(engine sqlEngine, opts ...sqlBackendOption) sqlBackendOption {
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
	return on(postgresEngine, func(b *sqlBackend) {
		b.open = func(t *testing.T) *sql.DB { return dbtest.PostGIS(t) }
	})
}

// collation compiles PostgreSQL's code-point comparisons under c and
// declares its text columns with c.
func collation(c filter.PostgresCollation) sqlBackendOption {
	return on(postgresEngine, func(b *sqlBackend) {
		b.dialect = filter.PostgresDialect{Collation: c}
		storing(core.FieldTypeString, "TEXT COLLATE "+c.String())(b)
	})
}

// sessionParams opens PostgreSQL connections that start with params, runtime
// parameters such as TimeZone, set as startup parameters so every
// connection the pool opens has them.
func sessionParams(params map[string]string) sqlBackendOption {
	return on(postgresEngine, func(b *sqlBackend) {
		b.open = func(t *testing.T) *sql.DB {
			config := dbtest.PostgresConfig(t)
			maps.Copy(config.RuntimeParams, params)
			db := stdlib.OpenDB(*config)
			t.Cleanup(func() { _ = db.Close() })
			return db
		}
	})
}

// hooked opens SQLite through the driver that reports each statement to the
// hook dbtest.OnStatement puts in its context.
func hooked() sqlBackendOption {
	return on(sqliteEngine, func(b *sqlBackend) {
		b.open = func(t *testing.T) *sql.DB { return dbtest.SQLiteHooked(t) }
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
	if b.engine == sqliteEngine {
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
	if b.engine == sqliteEngine {
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
	v = core.Deref(v)
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
	if kind.Type == core.FieldTypeArray && b.engine == postgresEngine {
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
	as map[sqlEngine]string
}

// column is a column holding values of Go type T, classified by
// core.TypeOf.
func column[T any](name string) sqlColumn {
	c := sqlColumn{name: name, kind: core.TypeOf(reflect.TypeFor[T]())}
	return c
}

// holding is c declared as columnType on engine.
func (c sqlColumn) holding(engine sqlEngine, columnType string) sqlColumn {
	c.as = maps.Clone(c.as)
	if c.as == nil {
		c.as = map[sqlEngine]string{}
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
func (table sqlTable) holding(name string, engine sqlEngine, columnType string) sqlTable {
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
			c = c.holding(postgresEngine, "JSON")
		case filter.StorageText:
			c = c.holding(postgresEngine, "TEXT")
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
	if b.engine == postgresEngine && (declared == "JSONB" || declared == "JSON") {
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
		execStatements(t, db, "CREATE TABLE "+table.name+" ("+strings.Join(defs, ", ")+")")
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
		return queryRows[string](t, db, "SELECT id FROM "+schema.Name+" WHERE "+res.Where, res.Args...), nil
	}
}

// rowIDs is each row's first column, the id a statement selects first,
// which must be text.
func rowIDs(t testing.TB, rows [][]any) []string {
	t.Helper()
	return itemIDs(rows, func(row []any) string {
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

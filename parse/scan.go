package parse

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"database/sql"

	"github.com/ashtonian/pkg/jsonvalue"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/internal/jsonkeys"
)

// RowScanner reads the rows of one statement over the resource into T: a
// list's page, an update's RETURNING, Current or Reread, or a repository's
// own read of Columns. Every such statement reads ColumnNames in order, so
// the scanner holds one rule per column, decided once when it is built, and
// a row pays only for its values. Executors (parsepgx, parsesql) build one
// per statement; a caller running a statement itself does the same.
//
// A column lands in the field the resource compiled it from: the column
// tag (core.WithColumnTag, default db), else the column convention over the
// field's JSON name, with embedded structs promoted as encoding/json
// promotes them and nil embedded pointers allocated. Fields of columns the
// resource does not read (ReadColumns) stay zero.
//
// A JSON document is decoded into a fresh value, with presence and the
// member spelling encoding/json writes for the field's type checked, before
// it reaches the row; NULL leaves the field zero. That holds for a column
// declared JSON or JSONB (ColumnType), for the columns ScanJSON names, and
// on SQLite, which stores them as JSON text, for every document and array
// field (core.TypeOf) whose type is not an sql.Scanner. A timestamp on
// SQLite is its text (core.FormatTimestamp) and is parsed. The driver scans
// every other column into its field.
type RowScanner[T any] struct {
	table   string
	columns []rowColumn
}

// rowColumn is one column's destination rule.
type rowColumn struct {
	name   string
	field  *core.Field
	decode rowDecode
}

type rowDecode uint8

const (
	// decodeDriver hands the driver the field itself.
	decodeDriver rowDecode = iota
	// decodeJSON decodes a stored document into the field.
	decodeJSON
	// decodeTimestamp parses SQLite's timestamp text into the field.
	decodeTimestamp
)

// ScanOption adjusts a RowScanner to what the driver learns from the
// statement's result.
type ScanOption func(*scanConfig)

type scanConfig struct {
	json []string
}

// ScanJSON decodes the named columns as stored JSON documents, as a
// declared JSON column is. It is for a driver that learns from the result
// which columns hold JSON where the resource declares none: parsepgx names
// the columns PostgreSQL reports as json or jsonb.
func ScanJSON(columns ...string) ScanOption {
	return func(c *scanConfig) { c.json = append(c.json, columns...) }
}

var scannerType = reflect.TypeFor[sql.Scanner]()

// RowScanner builds the scanner for rows of a statement r compiled for
// dialect d. A ScanJSON column the resource does not read is an error.
func (r *Resource[T]) RowScanner(d Dialect, opts ...ScanOption) (*RowScanner[T], error) {
	if d == nil {
		return nil, fmt.Errorf("parse: %s: row scanner needs a dialect", r.table)
	}
	var cfg scanConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	for _, name := range cfg.json {
		if !slices.Contains(r.read, name) {
			return nil, fmt.Errorf("parse: %s: ScanJSON names %q, which the resource does not read", r.table, name)
		}
	}
	sqlite := dialect.KindOf(d) == dialect.KindSQLite
	columns := make([]rowColumn, len(r.read))
	for i, name := range r.read {
		f := r.columnFields[name]
		storage, declared := r.sqlTypes[name]
		columns[i] = rowColumn{name: name, field: f, decode: rowDecodeOf(f.Type, declared && storage.IsJSON() || slices.Contains(cfg.json, name), sqlite)}
	}
	return &RowScanner[T]{table: r.table, columns: columns}, nil
}

// rowDecodeOf is the rule for a field of type t: json when the column holds
// JSON by declaration or by the result's own type; otherwise what SQLite
// stores as text and the driver cannot scan into t.
func rowDecodeOf(t reflect.Type, json, sqlite bool) rowDecode {
	switch {
	case json:
		return decodeJSON
	case !sqlite || scans(t):
		return decodeDriver
	}
	switch info := core.TypeOf(t); {
	case info.Type == core.FieldTypeTimestamp:
		return decodeTimestamp
	case core.IsDocument(info.Type) || info.Type == core.FieldTypeArray:
		return decodeJSON
	}
	return decodeDriver
}

// Scan reads one row. scan is the driver's row scan (sql.Rows.Scan,
// pgx.Rows.Scan), called once with one destination per column; its error
// is returned as it is, as drivers name the column that failed.
func (s *RowScanner[T]) Scan(scan func(dest ...any) error) (T, error) {
	var item T
	v := reflect.ValueOf(&item).Elem()
	for v.Kind() == reflect.Pointer {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	dest := make([]any, len(s.columns))
	for i, c := range s.columns {
		fv, err := c.field.Settable(v)
		if err != nil {
			var zero T
			return zero, fmt.Errorf("parse: %s: column %s: %w", s.table, c.name, err)
		}
		switch c.decode {
		case decodeJSON:
			dest[i] = jsonColumn{target: fv}
		case decodeTimestamp:
			dest[i] = timestampColumn{target: fv}
		default:
			dest[i] = fv.Addr().Interface()
		}
	}
	if err := scan(dest...); err != nil {
		var zero T
		return zero, err
	}
	return item, nil
}

// jsonColumn decodes a stored document into target, a settable field.
type jsonColumn struct{ target reflect.Value }

func (c jsonColumn) Scan(src any) error {
	if src == nil {
		c.target.SetZero()
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		return fmt.Errorf("JSON scan source must be string or []byte, got %T", src)
	}
	// Presence and member spelling are checked on a fresh value, so a
	// document that fails either never reaches the row.
	fresh := reflect.New(c.target.Type())
	if err := jsonvalue.UnmarshalPresent(raw, fresh.Interface()); err != nil {
		return err
	}
	if err := jsonkeys.Validate(raw, fresh.Interface()); err != nil {
		return err
	}
	c.target.Set(fresh.Elem())
	return nil
}

// timestampColumn parses SQLite's timestamp text into target, a time.Time
// or *time.Time field. A driver that reads the column's declared type as a
// timestamp hands over a time.Time.
type timestampColumn struct{ target reflect.Value }

func (c timestampColumn) Scan(src any) error {
	if src == nil {
		if c.target.Kind() != reflect.Pointer {
			return fmt.Errorf("NULL into %s", c.target.Type())
		}
		c.target.SetZero()
		return nil
	}
	var text string
	switch v := src.(type) {
	case time.Time:
		c.set(v)
		return nil
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("timestamp scan source must be text or time.Time, got %T", src)
	}
	at, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return fmt.Errorf("timestamp text: %w", err)
	}
	c.set(at)
	return nil
}

func (c timestampColumn) set(at time.Time) {
	target := c.target
	for target.Kind() == reflect.Pointer {
		target.Set(reflect.New(target.Type().Elem()))
		target = target.Elem()
	}
	target.Set(reflect.ValueOf(at))
}

// scans reports whether t, pointers stripped, is an sql.Scanner, which
// decodes the driver's value itself; the driver allocates the pointers.
func scans(t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return reflect.PointerTo(t).Implements(scannerType)
}

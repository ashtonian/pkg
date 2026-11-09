// Package parsesqlite registers the SQL functions parse's SQLite dialect
// calls, with the modernc.org/sqlite driver. SQLite has no exact decimal
// type: a NUMERIC column or a json_extract result holds a double, so
// filter.SQLiteDialect compares, orders and facets decimals through two
// deterministic functions instead.
//
//   - parse_decimal(x) is the canonical decimal text (core.DecimalText) of
//     x, or NULL: equality, sets and facet keys compare it.
//   - parse_decimal_key(x) is a BLOB that orders bytewise as the decimal x,
//     or NULL: ranges, ORDER BY and keyset seeks compare it.
//
// x is a decimal column's value (an INTEGER, a REAL, or TEXT holding a JSON
// number literal) or a document member as SQLite's `->` operator returns
// it: a JSON number literal as written, or a JSON string literal holding a
// decimal in plain notation, as filter.PathValue reads a decimal member.
// Anything else is NULL, so a filter treats it as missing.
//
// Call Register before the database opens its first connection. A statement
// on a connection without the functions fails with SQLite's "no such
// function: parse_decimal" rather than comparing a rounded double.
package parsesqlite

import (
	"database/sql/driver"
	"fmt"
	"sync"

	"modernc.org/sqlite"

	"github.com/ashtonian/pkg/parse/internal/decimals"
)

// The functions' SQL names, as filter.SQLiteDialect calls them.
const (
	DecimalFunc    = decimals.SQLiteValueFunc
	DecimalKeyFunc = decimals.SQLiteKeyFunc
)

// Register registers parse_decimal and parse_decimal_key with modernc.org/sqlite,
// for every connection the "sqlite" driver opens afterwards; connections
// already open lack them. It registers once per process: later calls, from
// any goroutine or package, return the first call's result.
func Register() error { return register() }

var register = sync.OnceValue(func() error {
	for name, fn := range map[string]func(driver.Value) (driver.Value, error){
		DecimalFunc:    Decimal,
		DecimalKeyFunc: DecimalKey,
	} {
		err := sqlite.RegisterDeterministicScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return fn(args[0])
		})
		if err != nil {
			return fmt.Errorf("parsesqlite: register %s: %w", name, err)
		}
	}
	return nil
})

// Decimal is parse_decimal for one SQLite value: its canonical decimal
// text, or nil (SQL NULL) when it holds no decimal. It is exported for a
// SQLite driver other than modernc.org/sqlite to register under
// DecimalFunc.
func Decimal(v driver.Value) (driver.Value, error) {
	text, ok := decimals.Value(v)
	if !ok {
		return nil, nil
	}
	return text, nil
}

// DecimalKey is parse_decimal_key for one SQLite value: a BLOB whose bytes
// order as the decimal does, equal for equal decimals, or nil (SQL NULL)
// when it holds no decimal. It is exported for a SQLite driver other than
// modernc.org/sqlite to register under DecimalKeyFunc.
func DecimalKey(v driver.Value) (driver.Value, error) {
	text, ok := decimals.Value(v)
	if !ok {
		return nil, nil
	}
	return decimals.Key(text), nil
}

// Package parsesql runs parse statements on SQLite through database/sql,
// with any SQLite driver: Query reads a statement's rows into the
// resource's type through its row contract (parse.RowScanner), which
// decodes the JSON text SQLite stores documents and arrays in and parses
// its timestamp text. The SQL functions SQLite statements call are
// registered by parsesqlite. PostgreSQL runs through parsepgx, whose codecs
// scan PostgreSQL's native arrays and documents by their result types.
package parsesql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Queryer runs a query: *sql.DB, *sql.Conn and *sql.Tx have it.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Query runs st, a statement over r compiled for SQLite that reads
// r.Columns (a repository's own read of the table, or a plan's page,
// Current or Reread), and returns its rows scanned through r's row
// contract. A row that fails to scan yields no rows at all.
func Query[T any](ctx context.Context, db Queryer, r *parse.Resource[T], st parse.Statement) ([]T, error) {
	rows, err := query(ctx, db, r, st)
	if err != nil {
		return nil, fmt.Errorf("parsesql: %s: %w", r.Table(), err)
	}
	return rows, nil
}

func query[T any](ctx context.Context, db Queryer, r *parse.Resource[T], st parse.Statement) (_ []T, err error) {
	scanner, err := r.RowScanner(filter.SQLiteDialect{})
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, st.SQL, st.Args...)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []T
	for rows.Next() {
		item, err := scanner.Scan(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

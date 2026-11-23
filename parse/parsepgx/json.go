package parsepgx

import (
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// collect scans rows, the result of one of r's statements, through r's row
// contract and closes them. The scanner is built from the first row's field
// descriptions, which every exec mode has by then: a column PostgreSQL
// reports as json or jsonb is decoded as a stored document whether or not
// the resource declares its type. A row that fails yields no rows at all.
func collect[T any](rows pgx.Rows, r *parse.Resource[T]) ([]T, error) {
	defer rows.Close()
	var (
		scanner *parse.RowScanner[T]
		out     []T
	)
	for rows.Next() {
		if scanner == nil {
			var err error
			if scanner, err = r.RowScanner(filter.PostgresDialect{}, parse.ScanJSON(jsonColumns(rows.FieldDescriptions())...)); err != nil {
				return nil, err
			}
		}
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

func jsonColumns(fields []pgconn.FieldDescription) []string {
	var names []string
	for _, f := range fields {
		if f.DataTypeOID == pgtype.JSONOID || f.DataTypeOID == pgtype.JSONBOID {
			names = append(names, f.Name)
		}
	}
	return names
}

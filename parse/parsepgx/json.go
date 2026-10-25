package parsepgx

import (
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ashtonian/pkg/jsonvalue"
	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

func rowToResource[T any](resource *parse.Resource[T]) pgx.RowToFunc[T] {
	return func(row pgx.CollectableRow) (T, error) {
		return pgx.RowToStructByNameLax[T](resourceRow[T]{CollectableRow: row, resource: resource})
	}
}

type resourceRow[T any] struct {
	pgx.CollectableRow
	resource *parse.Resource[T]
}

func (r resourceRow[T]) Scan(dest ...any) error {
	for i, field := range r.FieldDescriptions() {
		storage, _ := r.resource.ColumnSQLType(field.Name)
		// Result OIDs also cover JSON fields inferred without a ColumnType option.
		if storage.IsJSON() || field.DataTypeOID == pgtype.JSONOID || field.DataTypeOID == pgtype.JSONBOID {
			dest[i] = jsonScanner{target: dest[i]}
		}
	}
	return r.CollectableRow.Scan(dest...)
}

type jsonScanner struct{ target any }

func (s jsonScanner) Scan(source any) error {
	target := reflect.ValueOf(s.target)
	if !target.IsValid() || target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("JSON scan target must be a non-nil pointer, got %T", s.target)
	}
	if source == nil {
		target.Elem().SetZero()
		return nil
	}
	var raw []byte
	switch value := source.(type) {
	case string:
		raw = []byte(value)
	case []byte:
		raw = value
	default:
		return fmt.Errorf("JSON scan source must be string or []byte, got %T", source)
	}
	// Key validation must succeed before the decoded value reaches the row.
	fresh := reflect.New(target.Elem().Type())
	if err := jsonvalue.UnmarshalPresent(raw, fresh.Interface()); err != nil {
		return err
	}
	if err := core.ValidateJSONKeyRepresentation(raw, fresh.Interface()); err != nil {
		return err
	}
	target.Elem().Set(fresh.Elem())
	return nil
}

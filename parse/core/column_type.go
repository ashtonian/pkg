package core

import (
	"fmt"
	"maps"
)

// WithColumnTypes supplies physical storage declarations keyed by column name.
// The generator owns a copy of the map.
func WithColumnTypes(types map[string]SQLType) GeneratorOption {
	return func(o *GeneratorOptions) { o.ColumnTypes = maps.Clone(types) }
}

// FieldType combines a field's Go representation with its declared storage.
// JSON scalar and scalar-slice fields use document operators, while typed
// objects retain their member schema and native arrays retain array semantics.
func (g *SchemaGenerator) FieldType(field *Field) (TypeInfo, error) {
	info := TypeOf(field.Type)
	if len(g.Opts.ColumnTypes) == 0 || field.Tags.Relation != "" {
		return info, nil
	}
	column, ok, err := g.FieldColumn(field)
	if err != nil {
		return TypeInfo{}, err
	}
	if !ok {
		return info, nil
	}
	storage, declared := g.Opts.ColumnTypes[column]
	if !declared {
		return info, nil
	}
	if !storage.Valid() {
		return TypeInfo{}, fmt.Errorf("column %q declares type %q, which has no read rule", column, storage)
	}
	if storage.IsJSON() && !IsDocument(info.Type) {
		return TypeInfo{Type: FieldTypeJSON, SQLType: storage}, nil
	}
	info.SQLType = storage
	return info, nil
}

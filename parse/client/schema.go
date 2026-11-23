package client

import (
	"encoding/json"
	"fmt"
)

// SchemaVersion is the schema document format this client reads
// (parse.SchemaVersion). The server raises it when a member is removed,
// renamed or redefined; adding a member keeps it.
const SchemaVersion = 1

// SchemaVersionError is a schema document in a format version this client
// does not read. Decoding a Schema, alone or in a SchemaList, refuses it.
type SchemaVersionError struct {
	Resource string
	Version  int
}

func (e *SchemaVersionError) Error() string {
	return fmt.Sprintf("client: the schema document of %q is version %d; this client reads version %d", e.Resource, e.Version, SchemaVersion)
}

// Schema is a resource's schema document, what parse's schema route serves:
// the names a request may filter, sort, search and patch, and the values
// each takes. Types and operators are the server's names for them
// ("decimal", "has_key", "array_append").
type Schema struct {
	Version  int    `json:"version"`
	Resource string `json:"resource"`
	// Key are the key columns, in key order: an item path has a segment per
	// column (ItemPath). Absent for a schema registered without a key.
	Key    []string      `json:"key,omitempty"`
	Filter *FilterSchema `json:"filter,omitempty"`
	Patch  *PatchSchema  `json:"patch,omitempty"`
	Sort   *SortSchema   `json:"sort,omitempty"`
	Search *SearchSchema `json:"search,omitempty"`
}

// UnmarshalJSON decodes a schema document, refusing a version other than
// SchemaVersion with a *SchemaVersionError.
func (s *Schema) UnmarshalJSON(data []byte) error {
	type document Schema
	var doc document
	err := json.Unmarshal(data, &doc)
	if err != nil {
		return err
	}
	if doc.Version != SchemaVersion {
		refused := &SchemaVersionError{Resource: doc.Resource, Version: doc.Version}
		return refused
	}
	*s = Schema(doc)
	return nil
}

// SchemaList is the schema list route's answer: the document of every
// resource the server registers.
type SchemaList struct {
	Schemas []Schema `json:"schemas"`
}

// FilterSchema is the names a request filter may name: the filter fields
// and the key.
type FilterSchema struct {
	Fields map[string]FilterFieldSchema `json:"fields"`
}

// FilterFieldSchema is one filterable field, or an element path of one.
type FilterFieldSchema struct {
	Name        string `json:"name"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	DataType    string `json:"data_type"`
	// Wire is "string" for a bool, integer or float field whose operands
	// are JSON strings holding the value (Quoted); empty, operands take the
	// data type's own JSON form.
	Wire string `json:"wire,omitempty"`
	// Operators are the operators the server takes on the field, without
	// their $.
	Operators  []string         `json:"operators"`
	Indexed    bool             `json:"indexed,omitempty"`
	Immutable  bool             `json:"immutable,omitempty"`
	EnumValues []string         `json:"enum_values,omitempty"`
	Facetable  bool             `json:"facetable,omitempty"`
	Searchable bool             `json:"searchable,omitempty"`
	Includable bool             `json:"includable,omitempty"`
	Group      string           `json:"group,omitempty"`
	Order      int              `json:"order,omitempty"`
	GeoConfig  *GeoConfigSchema `json:"geo_config,omitempty"`
	// Relation is a relation field's kind and target resource.
	Relation *RelationSchema `json:"relation,omitempty"`
	// Elements are a JSON array's element paths, which Some and None
	// filter on one element.
	Elements map[string]FilterFieldSchema `json:"elements,omitempty"`
	// Elem is an array's element type.
	Elem string `json:"elem,omitempty"`
	// Element is a scalar array's element, which Some and None conditions
	// compare with its operators.
	Element *FilterFieldSchema `json:"element,omitempty"`
}

// RelationSchema is a relation field's kind (many_to_many, has_many or
// belongs_to) and the target resource whose schema its Some, None, Is and
// IsNot filters use; no target for a pair table compared by key alone.
type RelationSchema struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

// GeoConfigSchema is a geographic field's storage and spatial reference.
type GeoConfigSchema struct {
	StorageType string `json:"storage_type"`
	SRID        int    `json:"srid"`
}

// PatchSchema is the paths a patch may write, by JSON Pointer, and its
// bounds.
type PatchSchema struct {
	Paths         map[string]PatchPathSchema `json:"paths"`
	MaxOperations int                        `json:"max_operations"`
	// MaxRelationValues is the most keys one operation on a relation path
	// names, repeats counted once; 0 is unlimited.
	MaxRelationValues int `json:"max_relation_values"`
}

// PatchPathSchema is one patchable path: what the server checks a value
// against.
type PatchPathSchema struct {
	AllowedOps []string `json:"allowed_ops"`
	DataType   string   `json:"data_type"`
	// Wire is "string" for a path whose value is a JSON string holding a
	// bool, integer or float (Quoted).
	Wire string `json:"wire,omitempty"`
	// Nullable reports that the path takes null (remove, or clear).
	Nullable   bool     `json:"nullable,omitempty"`
	EnumValues []string `json:"enum_values,omitempty"`
	// MinLength and MaxLength bound a string path, or each element of an
	// array path, in Unicode code points.
	MinLength *int `json:"min_length,omitempty"`
	MaxLength *int `json:"max_length,omitempty"`
	// Minimum and Maximum bound a number path, or each element of an array
	// path, inclusively, as exact decimal text.
	Minimum string `json:"minimum,omitempty"`
	Maximum string `json:"maximum,omitempty"`
	// MinItems and MaxItems bound an array path's element count.
	MinItems *int `json:"min_items,omitempty"`
	MaxItems *int `json:"max_items,omitempty"`
	// Elem is an array path's element type.
	Elem string `json:"elem,omitempty"`
	// NumberKind is the Go kind of a number path or of its elements (int8
	// … int64, uint8 … uint64, float32, float64).
	NumberKind string `json:"number_kind,omitempty"`
	// Relation reports a relation's membership path, whose value is a list
	// of target keys.
	Relation bool `json:"relation,omitempty"`
	// Key are the target key's columns, in key order, of a membership path
	// whose target key has several: each key is then a JSON array of a
	// value per column.
	Key []RelationKeySchema `json:"key,omitempty"`
}

// RelationKeySchema is one target key column of a membership path: the type
// and wire form of its value in each key.
type RelationKeySchema struct {
	DataType   string `json:"data_type"`
	Wire       string `json:"wire,omitempty"`
	NumberKind string `json:"number_kind,omitempty"`
}

// SortSchema is the fields a sort may name and its bounds.
type SortSchema struct {
	Fields       map[string]SortFieldSchema `json:"fields"`
	DefaultField string                     `json:"default_field,omitempty"`
	// DefaultDir is "asc" or "desc".
	DefaultDir string `json:"default_dir,omitempty"`
	MaxFields  int    `json:"max_fields"`
}

// SortFieldSchema is one sortable field.
type SortFieldSchema struct {
	Indexed bool `json:"indexed,omitempty"`
}

// SearchSchema is the resource's free-text search.
type SearchSchema struct {
	Enabled bool     `json:"enabled"`
	Fields  []string `json:"fields"`
	// Param is the query parameter a GET list takes the search in. The
	// server reads "search" too, which ListParams sends.
	Param string `json:"param"`
	// Member is the list body member a POST list takes the search in.
	Member string `json:"member"`
}

//go:build unit

package core_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A tag with several bad parts reports the first in the tag's order,
// every time.
func TestParseRelationTag_ReportsTheFirstBadPart(t *testing.T) {
	for range 50 {
		_, err := core.ParseRelationTag("bad junction,bad source,bad target,bad key")
		require.Error(t, err)
		assert.EqualError(t, err, `relation tag junction "bad junction" is not a SQL identifier`)
	}
	_, err := core.ParseRelationTag("widget_tags,widget_id,bad target,bad key")
	assert.EqualError(t, err, `relation tag target column "bad target" is not a SQL identifier`)
}

// A part joins a key of several columns with `+`, in key order; the source
// columns pair with the owner key by position.
func TestParseRelationTag_CompositeKeys(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tag     string
		want    *core.RelationSchema
		wantErr string
	}{
		{
			name: "one column",
			tag:  "widget_tags,widget_id,tag_id",
			want: &core.RelationSchema{JoinTable: "widget_tags", SourceColumns: []string{"widget_id"}, TargetColumns: []string{"tag_id"}, OwnerKey: []string{"id"}},
		},
		{
			name: "composite owner key",
			tag:  "stock_tags,warehouse_id+sku,tag_id,warehouse_id+sku,tenant_id",
			want: &core.RelationSchema{
				JoinTable: "stock_tags", SourceColumns: []string{"warehouse_id", "sku"}, TargetColumns: []string{"tag_id"},
				OwnerKey: []string{"warehouse_id", "sku"}, TenantColumn: "tenant_id",
			},
		},
		{name: "source without its key", tag: "stock_tags,warehouse_id+sku,tag_id", wantErr: "do not pair with primary key id: 2 and 1 columns"},
		{name: "key without its source", tag: "stock_tags,stock_id,tag_id,warehouse_id+sku", wantErr: "do not pair with primary key warehouse_id+sku: 1 and 2 columns"},
		{
			name: "composite target key",
			tag:  "stock_tags,tag_id,warehouse_id+sku",
			want: &core.RelationSchema{JoinTable: "stock_tags", SourceColumns: []string{"tag_id"}, TargetColumns: []string{"warehouse_id", "sku"}, OwnerKey: []string{"id"}},
		},
		{name: "target column twice", tag: "stock_tags,tag_id,sku+sku", wantErr: `target columns name "sku" twice`},
		{name: "empty column", tag: "stock_tags,warehouse_id+,tag_id,warehouse_id+sku", wantErr: `source column "" is not a SQL identifier`},
		{name: "column twice", tag: "stock_tags,warehouse_id+warehouse_id,tag_id,warehouse_id+sku", wantErr: `source columns name "warehouse_id" twice`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ParseRelationTag(tt.tag)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Check refuses column lists that cannot pair by position, and columns
// typed by no scalar.
func TestRelationSchema_Check(t *testing.T) {
	one := []core.TypeInfo{{Type: core.FieldTypeString}}
	two := []core.TypeInfo{{Type: core.FieldTypeString}, {Type: core.FieldTypeTimestamp}}
	for _, tt := range []struct {
		name    string
		rel     core.RelationSchema
		wantErr string
	}{
		{name: "default owner key", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t"}, SourceTypes: one, TargetTypes: one}},
		{
			name: "composite through a target",
			rel: core.RelationSchema{
				JoinTable: "j", SourceColumns: []string{"a", "b"}, TargetColumns: []string{"x", "y"}, OwnerKey: []string{"k1", "k2"},
				TargetTable: "t", TargetKeys: []string{"x", "y"}, SourceTypes: two, TargetTypes: two,
			},
		},
		{
			name: "decimal and float keys",
			rel: core.RelationSchema{
				JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t"},
				SourceTypes: []core.TypeInfo{{Type: core.FieldTypeDecimal}}, TargetTypes: []core.TypeInfo{{Type: core.FieldTypeFloat, NumberKind: reflect.Float64}},
			},
		},
		{
			name:    "untyped",
			rel:     core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t"}},
			wantErr: "relation j types 0 of its 1 source columns (s)",
		},
		{
			name:    "a type short",
			rel:     core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"x", "y"}, SourceTypes: one, TargetTypes: one},
			wantErr: "relation j types 1 of its 2 target columns (x, y)",
		},
		{
			name: "a document key",
			rel: core.RelationSchema{
				JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t"},
				SourceTypes: one, TargetTypes: []core.TypeInfo{{Type: core.FieldTypeJSON}},
			},
			wantErr: "relation j: target column t is json; a relation correlates on scalar keys",
		},
		{name: "no target column", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}}, wantErr: "names no source or no target column"},
		{
			name:    "source longer than the owner key",
			rel:     core.RelationSchema{JoinTable: "j", SourceColumns: []string{"a", "b"}, TargetColumns: []string{"t"}},
			wantErr: "source columns (a, b) do not pair with the owner key (id)",
		},
		{
			name:    "target key shorter than the target columns",
			rel:     core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"x", "y"}, TargetTable: "t", TargetKeys: []string{"x"}},
			wantErr: "target columns (x, y) do not pair with t's key (x)",
		},
		{name: "column twice", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t", "t"}}, wantErr: `target columns name "t" twice`},
		{name: "no identifier", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s;"}, TargetColumns: []string{"t"}}, wantErr: "is not a SQL identifier"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rel.Check()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// RelationTag types a tag's source columns by the fields holding the owner
// key and its target column by the field's element, any scalar, or a
// target key of several columns by the element struct's fields; a document
// key, an owner key the struct does not hold, a target column the element
// does not hold, an element of several columns that is no struct and a
// field that is no slice are refused.
func TestSchemaGenerator_RelationTag(t *testing.T) {
	type readingKey struct {
		Device string    `db:"device_id"`
		At     time.Time `db:"at"`
		Note   string    `db:"note"`
	}
	type readings struct {
		DeviceID string           `json:"device_id" db:"device_id"`
		At       time.Time        `json:"at" db:"at"`
		Price    float32          `json:"price" db:"price"`
		Tags     []string         `json:"-" db:"-" relation:"reading_tags,device_id+at,tag_id,device_id+at"`
		Prices   []float32        `json:"-" db:"-" relation:"price_lists,price,list_price,price"`
		Docs     []map[string]any `json:"-" db:"-" relation:"reading_docs,device_id,doc,device_id"`
		Orphans  []string         `json:"-" db:"-" relation:"orphans,orphan_id,tag_id"`
		Flat     string           `json:"-" db:"-" relation:"flats,device_id,tag_id,device_id"`
		Readings []readingKey     `json:"-" db:"-" relation:"tag_readings,price_id,device_id+at,price"`
		Missing  []readingKey     `json:"-" db:"-" relation:"tag_readings,price_id,device_id+taken,price"`
		Untyped  []string         `json:"-" db:"-" relation:"tag_readings,price_id,device_id+at,price"`
		Instants []time.Time      `json:"-" db:"-" relation:"tag_readings,price_id,device_id+at,price"`
	}
	g := core.NewSchemaGenerator()
	fields, err := g.StructFields(reflect.TypeFor[readings]())
	require.NoError(t, err)
	for _, tt := range []struct {
		field   string
		source  []core.TypeInfo
		target  []core.TypeInfo
		wantErr string
	}{
		{
			field:  "Tags",
			source: []core.TypeInfo{{Type: core.FieldTypeString}, {Type: core.FieldTypeTimestamp}},
			target: []core.TypeInfo{{Type: core.FieldTypeString}},
		},
		{
			field:  "Prices",
			source: []core.TypeInfo{{Type: core.FieldTypeFloat, NumberKind: reflect.Float32}},
			target: []core.TypeInfo{{Type: core.FieldTypeFloat, NumberKind: reflect.Float32}},
		},
		{field: "Docs", wantErr: "target column doc is json; a relation correlates on scalar keys"},
		{field: "Orphans", wantErr: "relation tag primary key id is no column of core_test.readings"},
		{field: "Flat", wantErr: "a relation's field is a slice of target keys"},
		{
			field:  "Readings",
			source: []core.TypeInfo{{Type: core.FieldTypeFloat, NumberKind: reflect.Float32}},
			target: []core.TypeInfo{{Type: core.FieldTypeString}, {Type: core.FieldTypeTimestamp}},
		},
		{field: "Missing", wantErr: "relation tag target column taken is no column of core_test.readingKey"},
		{field: "Untyped", wantErr: "target key device_id+at has 2 columns; the field holds string, not a struct holding a field per column"},
		{field: "Instants", wantErr: "the field holds time.Time, not a struct"},
	} {
		t.Run(tt.field, func(t *testing.T) {
			i := slices.IndexFunc(fields.Fields, func(f *core.Field) bool { return f.GoName == tt.field })
			require.GreaterOrEqual(t, i, 0)
			rel, err := g.RelationTag(fields, fields.Fields[i])
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.source, rel.SourceTypes)
			assert.Equal(t, tt.target, rel.TargetTypes)
		})
	}
}

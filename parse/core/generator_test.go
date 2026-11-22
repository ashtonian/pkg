//go:build unit

package core_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
)

func TestSchemaGenerator_ColumnName(t *testing.T) {
	tests := []struct {
		name  string
		opts  []core.GeneratorOption
		field string
		dbTag string
		want  string
	}{
		{"default snake", nil, "deviceType", "", "device_type"},
		{"db tag override", nil, "deviceType", "custom_col", "custom_col"},
		{"db tag dash ignored", nil, "deviceType", "-", "device_type"},
		{"camel convention", []core.GeneratorOption{core.WithColumnConvention(core.CamelCase)}, "DeviceType", "", "deviceType"},
		{"db tag beats convention", []core.GeneratorOption{core.WithColumnConvention(core.CamelCase)}, "DeviceType", "my_col", "my_col"},
		{"db tag options stripped", nil, "id", "id,pk", "id"},
		{"non-identifier column rejected", nil, "x", "x; DROP TABLE t", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := core.NewSchemaGenerator(tt.opts...)
			got, err := gen.ColumnName(tt.field, tt.dbTag)
			if tt.want == "" {
				if err == nil {
					t.Errorf("expected an identifier error, got %q", got)
				}
				_, err = gen.MappedExpr("items", tt.field, core.FieldTypeString, tt.dbTag)
				if err == nil {
					t.Error("MappedExpr without a mapper accepted a column that is no identifier")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			expr, err := gen.MappedExpr("items", tt.field, core.FieldTypeString, tt.dbTag)
			if err != nil || expr != "" {
				t.Errorf("MappedExpr without a mapper = %q, %v; want the column itself", expr, err)
			}
		})
	}
}

type quotingMapper struct{}

func (quotingMapper) MapColumn(table, column string, _ core.FieldType) string {
	return `lower("` + table + `"."` + column + `")`
}

func TestSchemaGenerator_MappedExpr(t *testing.T) {
	gen := core.NewSchemaGenerator(core.WithColumnMapper(quotingMapper{}))
	got, err := gen.MappedExpr("items", "displayName", core.FieldTypeString, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := `lower("items"."display_name")`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSchemaGenerator_TableName(t *testing.T) {
	tests := []struct {
		name string
		opts []core.GeneratorOption
		want string
	}{
		{"default plural snake", nil, "mock_types"},
		{"explicit override", []core.GeneratorOption{core.WithSQLTableName("custom")}, "custom"},
		{"singular convention", []core.GeneratorOption{core.WithTableConvention(core.Singular)}, "mock_type"},
		{"non-identifier override rejected", []core.GeneratorOption{core.WithSQLTableName("items where 1=1")}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := core.NewSchemaGenerator(tt.opts...)
			got, err := gen.TableName(mockTypeName{})
			if tt.want == "" {
				if err == nil {
					t.Errorf("expected an identifier error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type mockTypeName struct{}

func (mockTypeName) Name() string { return "MockType" }

func TestSchemaGenerator_FieldNameTag(t *testing.T) {
	gen := core.NewSchemaGenerator()
	if gen.FieldNameTag() != "json" {
		t.Errorf("default should be json, got %q", gen.FieldNameTag())
	}

	gen2 := core.NewSchemaGenerator(core.WithFieldNameTag("bson"))
	if gen2.FieldNameTag() != "bson" {
		t.Errorf("expected bson, got %q", gen2.FieldNameTag())
	}
}

func TestSchemaGenerator_ColumnTagKey(t *testing.T) {
	gen := core.NewSchemaGenerator()
	if gen.ColumnTagKey() != "db" {
		t.Errorf("default should be db, got %q", gen.ColumnTagKey())
	}

	gen2 := core.NewSchemaGenerator(core.WithColumnTag("sql"))
	if gen2.ColumnTagKey() != "sql" {
		t.Errorf("expected sql, got %q", gen2.ColumnTagKey())
	}
}

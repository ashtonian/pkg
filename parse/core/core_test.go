//go:build unit

package core_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
)

func TestToSnakeCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"DeviceType", "device_type"},
		{"ID", "id"},
		{"HTTPStatus", "http_status"},
		{"createdAt", "created_at"},
		{"already_snake", "already_snake"},
		{"JSONData", "json_data"},
		{"", ""},
	}
	for _, tt := range tests {
		got := core.ToSnakeCase(tt.in)
		if got != tt.want {
			t.Errorf("ToSnakeCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestColumnConventions(t *testing.T) {
	tests := []struct {
		name string
		conv core.NamingConvention
		in   string
		want string
	}{
		{"snake", core.SnakeCase, "DeviceType", "device_type"},
		{"camel", core.CamelCase, "DeviceType", "deviceType"},
		{"pascal", core.PascalCase, "DeviceType", "DeviceType"},
		{"lower", core.LowerCase, "DeviceType", "devicetype"},
	}
	for _, tt := range tests {
		got := tt.conv(tt.in)
		if got != tt.want {
			t.Errorf("%s(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestTableConventions(t *testing.T) {
	tests := []struct {
		name string
		conv core.TableConvention
		in   string
		want string
	}{
		{"plural_snake", core.PluralSnake, "Device", "devices"},
		{"singular", core.Singular, "Device", "device"},
		{"plural_camel", core.PluralCamel, "Device", "devices"},
	}
	for _, tt := range tests {
		got := tt.conv(tt.in)
		if got != tt.want {
			t.Errorf("%s(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestSchemaGenerator_ColumnExpr(t *testing.T) {
	tests := []struct {
		name  string
		opts  []core.GeneratorOption
		field string
		dbTag string
		want  string
	}{
		{"default snake", nil, "deviceType", "", "items.device_type"},
		{"db tag override", nil, "deviceType", "custom_col", "items.custom_col"},
		{"db tag dash ignored", nil, "deviceType", "-", "items.device_type"},
		{"camel convention", []core.GeneratorOption{core.WithColumnConvention(core.CamelCase)}, "DeviceType", "", "items.deviceType"},
		{"db tag beats convention", []core.GeneratorOption{core.WithColumnConvention(core.CamelCase)}, "DeviceType", "my_col", "items.my_col"},
		{"db tag options stripped", nil, "id", "id,pk", "items.id"},
		{"non-identifier column rejected", nil, "x", "x; DROP TABLE t", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen := core.NewSchemaGenerator(tt.opts...)
			got, err := gen.ColumnExpr("items", tt.field, core.FieldTypeString, tt.dbTag)
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

func TestPluralize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"device", "devices"},
		{"policy", "policies"},
		{"inventory", "inventories"},
		{"status", "statuses"},
		{"address", "addresses"},
		{"fox", "foxes"},
		{"match", "matches"},
		{"wish", "wishes"},
		{"buzz", "buzzes"},
		{"key", "keys"}, // vowel before y → just add s
		{"day", "days"}, // vowel before y → just add s
		{"boy", "boys"}, // vowel before y → just add s
		{"category", "categories"},
		{"entity", "entities"},
		{"", ""},
	}
	for _, tt := range tests {
		got := core.Pluralize(tt.in)
		if got != tt.want {
			t.Errorf("Pluralize(%q) = %q, want %q", tt.in, got, tt.want)
		}
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

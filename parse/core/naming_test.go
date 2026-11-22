//go:build unit

package core_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/core"
)

func TestSnakeCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"DeviceType", "device_type"},
		{"ID", "id"},
		{"HTTPStatus", "http_status"},
		{"createdAt", "created_at"},
		{"already_snake", "already_snake"},
		{"JSONData", "json_data"},
		{"userIDs", "user_ids"},
		{"IDs", "ids"},
		{"userIDsByTenant", "user_ids_by_tenant"},
		{"APIService", "api_service"},
		{"", ""},
	}
	for _, tt := range tests {
		got := core.SnakeCase(tt.in)
		if got != tt.want {
			t.Errorf("SnakeCase(%q) = %q, want %q", tt.in, got, tt.want)
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
		{"Analysis", "Analyses"},
		{"basis", "bases"},
		{"analyses", "analyses"},
		{"Quiz", "Quizzes"},
		{"fez", "fezzes"},
		{"quizzes", "quizzes"},
		{"topaz", "topazes"},
		{"waltz", "waltzes"},
		// Irregular nouns take the regular rule; name their tables.
		{"Leaf", "Leafs"},
		{"Person", "Persons"},
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

//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// docThing has a key with no parse tag, a tenant and a soft-delete column,
// an untagged and a hidden field, and relations of both directions.
type docThing struct {
	ID        string     `json:"id"         db:"id"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Name      string     `json:"name"       db:"name"     parse:"filter,sort"`
	Labels    []string   `json:"labels"     db:"labels"   parse:"filter"`
	Rank      int        `json:"rank"       db:"rank"`
	Secret    string     `json:"-"          db:"secret"`
	OwnerID   string     `json:"owner_id"   db:"owner_id"`
}

type docOwner struct {
	Key  int64  `json:"key"  db:"key"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type docPart struct {
	ID      string `json:"id"       db:"id"       parse:"filter"`
	ThingID string `json:"thing_id" db:"thing_id"`
	State   string `json:"state"    db:"state"    parse:"filter"`
}

// The schema document's filter section lists exactly the names a request
// filter may name, and for each exactly the operators the parser takes on
// it, on every resource the registry serves: the key included, untagged or
// not, and the tenant and soft-delete columns, untagged fields and hidden
// fields left out.
func TestSchemaDocument_ListsWhatTheParserAccepts(t *testing.T) {
	owners := parse.MustResource[docOwner]("doc_owners", parse.PrimaryKey("key"))
	parts := parse.MustResource[docPart]("doc_parts")
	things := parse.MustResource[docThing]("doc_things", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	things.MustRelate("owner", parse.BelongsTo(owners, "owner_id", parse.Indexed()))
	things.MustRelate("parts", parse.HasMany(parts, "thing_id", parse.Indexed()))
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, things))
	require.NoError(t, parse.RegisterResource(reg, owners))
	require.NoError(t, parse.RegisterResource(reg, parts))

	parsers := map[string]func(string) error{
		"doc_things": func(doc string) error { _, err := things.ParseFilter(doc); return err },
		"doc_owners": func(doc string) error { _, err := owners.ParseFilter(doc); return err },
		"doc_parts":  func(doc string) error { _, err := parts.ParseFilter(doc); return err },
	}
	// Every name any of the resources' fields has, as JSON or as a column.
	candidates := []string{
		"id", "tenant_id", "deleted_at", "name", "labels", "rank", "secret", "owner_id", "owner", "parts",
		"key", "thing_id", "state", "Secret", "missing",
	}
	operators := append(filter.Operators(), filter.OpIs, filter.OpIsNot)
	require.ElementsMatch(t, slices.Collect(maps.Keys(parsers)), reg.ResourceNames())

	for _, resource := range reg.ResourceNames() {
		parseFilter := parsers[resource]
		listed := reg.SchemaFor(resource).Filter.Fields
		for _, name := range candidates {
			code := filterCode(t, parseFilter, name, filter.OpExists)
			_, documented := listed[name]
			assert.Equal(t, documented, code != filter.ECodeUnknownField.String(),
				"%s: the document lists %q: %v; the parser answers %q", resource, name, documented, code)
		}
		for name, field := range listed {
			for _, op := range operators {
				code := filterCode(t, parseFilter, name, op)
				assert.Equal(t, slices.Contains(field.Operators, op), code != filter.ECodeInvalidOperator.String(),
					"%s: %q lists $%s: %v; the parser answers %q", resource, name, op, slices.Contains(field.Operators, op), code)
			}
		}
	}
	key := reg.SchemaFor("doc_things").Filter.Fields["id"]
	require.NotNil(t, key, "the untagged key is listed")
	assert.Equal(t, core.FieldTypeString, key.DataType)
	assert.Equal(t, core.FieldTypeInt, reg.SchemaFor("doc_owners").Filter.Fields["key"].DataType, "a key of its own name and type")
}

// filterCode is the code parse answers {name: {$op: operand}} with, "" when
// it accepts it. The operand need not suit the operator: whether the
// parser takes the operator on the field is decided first.
func filterCode(t *testing.T, parseFilter func(string) error, name string, op filter.FilterOperator) string {
	t.Helper()
	var operand any = "a"
	switch op {
	case filter.OpIn, filter.OpNin:
		operand = []any{"a"}
	case filter.OpNull, filter.OpNotNull, filter.OpExists:
		operand = true
	case filter.OpSome, filter.OpNone, filter.OpIs, filter.OpIsNot:
		operand = map[string]any{}
	}
	doc, err := json.Marshal(map[string]any{name: map[string]any{"$" + string(op): operand}})
	require.NoError(t, err)
	err = parseFilter(string(doc))
	if err == nil {
		return ""
	}
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%s $%s: %v is no ParseError", name, op, err)
	return pe.Code
}

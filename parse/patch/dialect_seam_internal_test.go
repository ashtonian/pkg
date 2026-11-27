//go:build unit

package patch

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// providerDialect is a SQL provider whose write SQL differs from every
// built-in dialect's: each method marks what it renders. It implements
// RelationDialect over the internal dialect kind, as a new provider in the
// module does, so a patch compiled for it is written through those methods
// alone. It cannot write a member named "nope", test one named
// "untestable" or compare a decimal, and refuses them as a provider does.
type providerDialect struct{ dialect.SQLite }

func refused(what string) error {
	err := core.NewError(core.CodeOpNotSQLCompilable, "the provider cannot "+what)
	return err
}

func refusesMember(member []string, name string) error {
	if slices.Contains(member, name) {
		return refused("reach member " + name)
	}
	return nil
}

func (providerDialect) MarshalValue(value any, _ core.TypeInfo) (any, error) { return value, nil }

func (providerDialect) ChangedCondition(column, expr string, t core.TypeInfo) (string, error) {
	if t.Type == core.FieldTypeDecimal {
		return "", refused("compare a decimal")
	}
	return "changed(" + column + ", " + expr + ", " + string(t.Type) + ")", nil
}

func (providerDialect) ChangedDocumentCondition(column, doc string) (string, error) {
	return "doc_changed(" + column + ", " + doc + ")", nil
}

func (providerDialect) MergePatch(doc string, p map[string]any, bind func(any) string) (string, error) {
	text, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return "merge(" + doc + ", " + bind(string(text)) + ")", nil
}

func (providerDialect) ArrayAppend(array string, element any, bind func(any) string) (string, error) {
	return "append(" + array + ", " + bind(element) + ")", nil
}

func (providerDialect) MemberDocument(expr string) (string, error) { return "doc(" + expr + ")", nil }

func (providerDialect) SetMember(doc string, member []string, value json.RawMessage, bind func(any) string) (string, error) {
	err := refusesMember(member, "nope")
	if err != nil {
		return "", err
	}
	return "set(" + doc + ", " + bind(strings.Join(member, ".")) + ", " + bind(string(value)) + ")", nil
}

func (providerDialect) RemoveMember(doc string, member []string, bind func(any) string) (string, error) {
	err := refusesMember(member, "nope")
	if err != nil {
		return "", err
	}
	return "remove(" + doc + ", " + bind(strings.Join(member, ".")) + ")", nil
}

func (providerDialect) HasMember(doc string, member []string, bind func(any) string) (string, error) {
	err := refusesMember(member, "untestable")
	if err != nil {
		return "", err
	}
	return "has(" + doc + ", " + bind(strings.Join(member, ".")) + ")", nil
}

func (providerDialect) IsObject(doc string, member []string, bind func(any) string) (string, error) {
	err := refusesMember(member, "untestable")
	if err != nil {
		return "", err
	}
	return "object(" + doc + ", " + bind(strings.Join(member, ".")) + ")", nil
}

func (providerDialect) KeyIn(_, alias string, columns []string, _ []core.TypeInfo, keys []core.Key, member bool, bind func(any) string) (string, error) {
	in := "in"
	if !member {
		in = "not_in"
	}
	return in + "(" + alias + "." + strings.Join(columns, "+") + ", " + bind(len(keys)) + ")", nil
}

func (providerDialect) KeyOperand(expr string, _ core.TypeInfo) string { return expr }

func (providerDialect) RelationRows(_ *core.RelationSchema, owners, values []core.Key, tenant *core.PairTenant, bind func(any) string) (core.PairRows, error) {
	rows := core.PairRows{Sources: []string{"o.k"}, Targets: []string{"v.k"}, From: " FROM pairs(" + bind(len(owners)) + ", " + bind(len(values)) + ")", Where: []string{"rows_ok"}}
	if tenant != nil {
		rows.Tenant = "t." + bind(tenant.Value)
	}
	return rows, nil
}

// seamSchema declares a json column raw, a native array tags, and a
// document doc whose members a patch writes at any depth.
func seamSchema(t *testing.T) *PatchSchema {
	t.Helper()
	ops := []PatchOp{PatchOpAdd, PatchOpReplace, PatchOpRemove}
	s, err := NewPatchSchema("things", map[string]*PathSchema{
		"/raw":    {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLType: core.SQLJSON, SQLColumn: "raw"},
		"/amount": {AllowedOps: ops, DataType: core.FieldTypeDecimal, SQLColumn: "amount"},
		"/tags":   {AllowedOps: append(slices.Clone(ops), PatchOpArrayAppend), DataType: core.FieldTypeArray, SQLColumn: "tags", GoType: reflect.TypeFor[[]string]()},
		"/doc":    {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLType: core.SQLJSONB, SQLColumn: "doc"},
		"/doc/**": {AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: "doc"},
	})
	require.NoError(t, err)
	return s
}

// TestBuildPatchSQL_WritesThroughTheDialect compiles each piece of a
// patch's dialect-specific SQL for a provider of its own: the statement is
// what the provider's methods render, so a new SQL provider is one type.
func TestBuildPatchSQL_WritesThroughTheDialect(t *testing.T) {
	for _, tc := range []struct {
		name              string
		body              string
		set, pre, changed string
		args              []any
	}{
		{name: "a merge into a json column", body: `{"raw":{"a":1}}`,
			set:     `"raw" = merge("raw", ?1)`,
			changed: `(changed("raw", merge("raw", ?1), json))`,
			args:    []any{`{"a":1}`}},
		{name: "an append", body: `[{"op":"array_append","path":"/tags","value":"x"}]`,
			set:     `"tags" = append("tags", ?1)`,
			changed: `(changed("tags", append("tags", ?1), array))`,
			args:    []any{"x"}},
		{name: "a member added", body: `[{"op":"add","path":"/doc/b/c","value":1}]`,
			set:     `"doc" = set(doc("doc"), ?1, ?2)`,
			pre:     `(object(doc("doc"), ?3))`,
			changed: `(doc_changed("doc", set(doc("doc"), ?1, ?2)))`,
			args:    []any{"b.c", "1", "b"}},
		{name: "a member replaced and another removed", body: `[{"op":"replace","path":"/doc/a","value":2},{"op":"remove","path":"/doc/b"}]`,
			set:     `"doc" = remove(set(doc("doc"), ?1, ?2), ?3)`,
			pre:     `(has(doc("doc"), ?4) AND has(doc("doc"), ?5))`,
			changed: `(doc_changed("doc", remove(set(doc("doc"), ?1, ?2), ?3)))`,
			args:    []any{"a", "2", "b", "a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var parsed *ParsedPatch
			var err error
			if strings.HasPrefix(tc.body, "[") {
				parsed, err = NewParser(seamSchema(t), nil).Parse([]byte(tc.body))
			} else {
				parsed, err = ParseMergePatch([]byte(tc.body), seamSchema(t), nil)
			}
			require.NoError(t, err)
			res, err := BuildPatchSQL(parsed, providerDialect{}, WithChangePredicate())
			require.NoError(t, err)
			assert.Equal(t, tc.set, res.SetClause)
			assert.Equal(t, tc.pre, res.Precondition)
			assert.Equal(t, tc.changed, res.ChangePredicate)
			assert.Equal(t, tc.args, res.Args)
		})
	}
}

// TestBuildPatchSQL_ReportsTheDialectsRefusal: what a provider refuses to
// write, test or compare is the operation's E2070, naming its path.
func TestBuildPatchSQL_ReportsTheDialectsRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, body, path string
	}{
		{"a member set", `[{"op":"add","path":"/doc/nope","value":1}]`, "/doc/nope"},
		{"a member removed", `[{"op":"remove","path":"/doc/nope"}]`, "/doc/nope"},
		{"a member tested", `[{"op":"replace","path":"/doc/untestable","value":1}]`, "/doc/untestable"},
		{"a member's parent tested", `[{"op":"add","path":"/doc/untestable/a","value":1}]`, "/doc/untestable/a"},
		{"a column compared", `[{"op":"replace","path":"/amount","value":"1.5"}]`, "/amount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := NewParser(seamSchema(t), nil).Parse([]byte(tc.body))
			require.NoError(t, err)
			_, err = BuildPatchSQL(parsed, providerDialect{}, WithChangePredicate())
			require.ErrorIs(t, err, core.CodeOpNotSQLCompilable)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.path, pe.Path)
			assert.Contains(t, pe.Message, "the provider cannot")
		})
	}
}

// TestRenderPreconditions_RefusesNoMember: a precondition is on a member
// of a document, never on the document whole.
func TestRenderPreconditions_RefusesNoMember(t *testing.T) {
	_, _, err := RenderPreconditions(providerDialect{}, []Precondition{{Op: 0, Path: "/doc", Column: "doc", Exists: true}}, func(any) string { return "?1" })
	require.ErrorContains(t, err, "names no member")
}

// TestRenderRelationOp_InsertsThroughTheDialect renders a membership
// change's statements from the provider's methods: an insert of pairs
// without a target table from the rows its RelationRows reads, and a
// replace's delete of the pairs it drops from its KeyIn.
func TestRenderRelationOp_InsertsThroughTheDialect(t *testing.T) {
	rel := &core.RelationSchema{JoinTable: "widget_tags", SourceColumns: []string{"widget_id"}, TargetColumns: []string{"tag_id"},
		SourceTypes: []core.TypeInfo{{Type: core.FieldTypeInt, NumberKind: reflect.Int}}, TargetTypes: []core.TypeInfo{{Type: core.FieldTypeUUID}},
		TenantColumn: "tenant_id"}
	const tenant = "00000000-0000-0000-0000-000000000101"
	const tag = "00000000-0000-0000-0000-00000000000c"
	opts := RenderRelationOpOptions{TenantValue: tenant}

	stmts, err := RenderRelationOp(RelationPatchOp{Field: "tags", Schema: rel, Verb: RelationVerbAdd, Values: []any{tag}}, providerDialect{}, []core.Key{{1}, {2}}, opts)
	require.NoError(t, err)
	require.Len(t, stmts, 1)
	assert.Equal(t, `INSERT INTO "widget_tags" ("widget_id", "tag_id", "tenant_id") SELECT o.k, v.k, t.?3 FROM pairs(?1, ?2) WHERE rows_ok ON CONFLICT DO NOTHING`, stmts[0].SQL)
	assert.Equal(t, []any{2, 1, tenant}, stmts[0].Args)

	stmts, err = RenderRelationOp(RelationPatchOp{Field: "tags", Schema: rel, Verb: RelationVerbReplace, Values: []any{tag}}, providerDialect{}, []core.Key{{1}}, opts)
	require.NoError(t, err)
	require.Len(t, stmts, 2)
	assert.Equal(t, `DELETE FROM "widget_tags" WHERE in(widget_tags.widget_id, ?1) AND "widget_tags"."tenant_id" = ?2 AND not_in(widget_tags.tag_id, ?3)`, stmts[0].SQL)
	assert.Equal(t, []any{1, tenant, 1}, stmts[0].Args)
}

//go:build unit

package patch_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// providerDialect is a SQL provider whose write SQL differs from every
// built-in dialect's: each method marks what it renders. A patch compiled
// for it must be written through those methods alone.
type providerDialect struct{ filter.SQLiteDialect }

func (providerDialect) ChangedCondition(column, expr string, t core.TypeInfo) string {
	return "changed(" + column + ", " + expr + ", " + string(t.Type) + ")"
}

func (providerDialect) MergePatch(doc string, storage core.SQLType, p map[string]any, bind func(any) string) (string, error) {
	text, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return "merge(" + doc + ", " + string(storage) + ", " + bind(string(text)) + ")", nil
}

func (providerDialect) ArrayAppend(array string, element any, bind func(any) string) (string, error) {
	return "append(" + array + ", " + bind(element) + ")", nil
}

func (providerDialect) MemberDocument(expr string) string { return "doc(" + expr + ")" }

func (providerDialect) SetMember(doc string, member []string, value json.RawMessage, bind func(any) string) string {
	return "set(" + doc + ", " + bind(strings.Join(member, ".")) + ", " + bind(string(value)) + ")"
}

func (providerDialect) RemoveMember(doc string, member []string, bind func(any) string) string {
	return "remove(" + doc + ", " + bind(strings.Join(member, ".")) + ")"
}

func (providerDialect) HasMember(doc string, member []string, bind func(any) string) string {
	return "has(" + doc + ", " + bind(strings.Join(member, ".")) + ")"
}

func (providerDialect) IsObject(doc string, member []string, bind func(any) string) string {
	return "object(" + doc + ", " + bind(strings.Join(member, ".")) + ")"
}

func (providerDialect) RelationRows(rel *core.RelationSchema, owners, values []core.Key, tenant *core.PairTenant, bind func(any) string) (core.PairRows, error) {
	rows := core.PairRows{Sources: []string{"o.k"}, Targets: []string{"v.k"}, From: " FROM pairs(" + bind(len(owners)) + ", " + bind(len(values)) + ")", Where: []string{"rows_ok"}}
	if tenant != nil {
		rows.Tenant = "t." + bind(tenant.Value)
	}
	return rows, nil
}

// TestBuildPatchSQL_WritesThroughTheDialect compiles each piece of a
// patch's dialect-specific SQL for a provider of its own: the statement is
// what the provider's methods render, so a new SQL provider is one type.
func TestBuildPatchSQL_WritesThroughTheDialect(t *testing.T) {
	members := func(t *testing.T, body string) *patch.ParsedPatch {
		t.Helper()
		parsed, err := patch.NewParser(memberSchema(core.SQLJSONB), nil).Parse([]byte(body))
		require.NoError(t, err)
		return parsed
	}
	for _, tc := range []struct {
		name              string
		patch             func(t *testing.T) *patch.ParsedPatch
		set, pre, changed string
		args              []any
	}{
		{name: "a merge into a json column", patch: func(t *testing.T) *patch.ParsedPatch { return patchOf(t, `{"raw":{"a":1}}`) },
			set:     `"raw" = merge("raw", json, ?1)`,
			changed: `(changed("raw", merge("raw", json, ?1), json))`,
			args:    []any{`{"a":1}`}},
		{name: "an append", patch: func(t *testing.T) *patch.ParsedPatch {
			return patchOf(t, `[{"op":"array_append","path":"/tags","value":"x"}]`)
		},
			set:     `"tags" = append("tags", ?1)`,
			changed: `(changed("tags", append("tags", ?1), array))`,
			args:    []any{"x"}},
		{name: "a member added", patch: func(t *testing.T) *patch.ParsedPatch { return members(t, `[{"op":"add","path":"/doc/b/c","value":1}]`) },
			set:     `"doc" = set(doc("doc"), ?1, ?2)`,
			pre:     `(object(doc("doc"), ?3))`,
			changed: `(changed("doc", set(doc("doc"), ?1, ?2), json))`,
			args:    []any{"b.c", "1", "b"}},
		{name: "a member replaced and another removed", patch: func(t *testing.T) *patch.ParsedPatch {
			return members(t, `[{"op":"replace","path":"/doc/a","value":2},{"op":"remove","path":"/doc/b"}]`)
		},
			set:     `"doc" = remove(set(doc("doc"), ?1, ?2), ?3)`,
			pre:     `(has(doc("doc"), ?4) AND has(doc("doc"), ?5))`,
			changed: `(changed("doc", remove(set(doc("doc"), ?1, ?2), ?3), json))`,
			args:    []any{"a", "2", "b", "a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := patch.BuildPatchSQL(tc.patch(t), providerDialect{}, patch.WithChangePredicate())
			require.NoError(t, err)
			assert.Equal(t, tc.set, res.SetClause)
			assert.Equal(t, tc.pre, res.Precondition)
			assert.Equal(t, tc.changed, res.ChangePredicate)
			assert.Equal(t, tc.args, res.Args)
		})
	}
}

// TestRenderPreconditions_RefusesNoMember: a precondition is on a member
// of a document, never on the document whole.
func TestRenderPreconditions_RefusesNoMember(t *testing.T) {
	_, _, err := patch.RenderPreconditions(filter.SQLiteDialect{}, []patch.Precondition{{Op: 0, Path: "/doc", Column: "doc", Exists: true}}, func(any) string { return "?1" })
	require.ErrorContains(t, err, "names no member")
}

// TestRenderRelationOp_InsertsThroughTheDialect renders an insert of pairs
// without a target table from the rows the provider's RelationRows reads.
func TestRenderRelationOp_InsertsThroughTheDialect(t *testing.T) {
	op, opts := relationOp(true, false, patch.RelationVerbAdd, []any{tagC})
	stmts, err := patch.RenderRelationOp(op, providerDialect{}, []core.Key{{1}, {2}}, opts)
	require.NoError(t, err)
	require.Len(t, stmts, 1)
	assert.Equal(t, `INSERT INTO "widget_tags" ("widget_id", "tag_id", "tenant_id") SELECT o.k, v.k, t.?3 FROM pairs(?1, ?2) WHERE rows_ok ON CONFLICT DO NOTHING`, stmts[0].SQL)
	assert.Equal(t, []any{2, 1, tenant1}, stmts[0].Args)
}

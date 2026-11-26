//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// writesSchema has a document /doc whose members are patchable, a second
// document /other to move or copy from, and a native array /tags.
func writesSchema() *patch.PatchSchema {
	transfer := []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy}
	return newSchema("items", 20, map[string]*patch.PathSchema{
		"/doc":    {AllowedOps: transfer, DataType: core.FieldTypeJSON, SQLColumn: "doc"},
		"/doc/**": {AllowedOps: transfer, DataType: core.FieldTypeAny},
		"/other":  {AllowedOps: transfer, DataType: core.FieldTypeJSON, SQLColumn: "other"},
		"/tags":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpArrayAppend}, DataType: core.FieldTypeArray, SQLColumn: "tags"},
	})
}

func mustPolicy(t *testing.T, opts ...patch.PolicyOption) *patch.PatchPolicyConstraints {
	t.Helper()
	pc, err := patch.NewPatchPolicyConstraints(opts...)
	require.NoError(t, err)
	return pc
}

func parseWrites(t *testing.T, merge bool, body string) *patch.ParsedPatch {
	t.Helper()
	var (
		parsed *patch.ParsedPatch
		err    error
	)
	if merge {
		parsed, err = patch.ParseMergePatch([]byte(body), writesSchema(), nil)
	} else {
		parsed, err = patch.NewParser(writesSchema(), nil).Parse([]byte(body))
	}
	require.NoError(t, err)
	return parsed
}

// Policy judges what an operation writes: an operation whose path is an
// ancestor of a forbidden or constrained path writes it whole (or, as a
// merge, writes the members it names); one at the path writes it; one below
// it writes inside it, which a pattern for the path alone does not forbid.
func TestPolicy_JudgesWhatAnOperationWrites(t *testing.T) {
	forbidA := mustPolicy(t, patch.ForbidPaths("/doc/a"))
	forbidTree := mustPolicy(t, patch.ForbidPaths("/doc/a/**"))
	constrainA := mustPolicy(t, patch.ConstrainValues("/doc/a", patch.ValueConstraint{AllowedValues: []any{"ok"}}))
	const (
		accepted  = ""
		forbidden = "E2084"
		refused   = "E2085"
	)
	for _, tc := range []struct {
		name   string
		merge  bool
		body   string
		policy *patch.PatchPolicyConstraints
		want   string
	}{
		// A forbidden pattern, from an ancestor.
		{"forbidden: merge naming it", true, `{"doc":{"a":1}}`, forbidA, forbidden},
		{"forbidden: merge naming it null", true, `{"doc":{"a":null}}`, forbidA, forbidden},
		{"forbidden: merge naming a sibling", true, `{"doc":{"z":1}}`, forbidA, accepted},
		{"forbidden: replace ancestor", false, `[{"op":"replace","path":"/doc","value":{"z":1}}]`, forbidA, forbidden},
		{"forbidden: add ancestor", false, `[{"op":"add","path":"/doc","value":{}}]`, forbidA, forbidden},
		{"forbidden: remove ancestor", false, `[{"op":"remove","path":"/doc"}]`, forbidA, forbidden},
		{"forbidden: move into ancestor", false, `[{"op":"move","from":"/other","path":"/doc"}]`, forbidA, forbidden},
		{"forbidden: move from ancestor", false, `[{"op":"move","from":"/doc","path":"/other"}]`, forbidA, forbidden},
		{"forbidden: copy into ancestor", false, `[{"op":"copy","from":"/other","path":"/doc"}]`, forbidA, forbidden},
		{"forbidden: copy from ancestor", false, `[{"op":"copy","from":"/doc","path":"/other"}]`, forbidA, forbidden},
		// A forbidden pattern, at the path.
		{"forbidden: merge naming it as an object", true, `{"doc":{"a":{"b":1}}}`, forbidA, forbidden},
		{"forbidden: replace it", false, `[{"op":"replace","path":"/doc/a","value":1}]`, forbidA, forbidden},
		{"forbidden: add it", false, `[{"op":"add","path":"/doc/a","value":1}]`, forbidA, forbidden},
		{"forbidden: remove it", false, `[{"op":"remove","path":"/doc/a"}]`, forbidA, forbidden},
		{"forbidden: move into it", false, `[{"op":"move","from":"/doc/z","path":"/doc/a"}]`, forbidA, forbidden},
		{"forbidden: copy into it", false, `[{"op":"copy","from":"/doc/z","path":"/doc/a"}]`, forbidA, forbidden},
		{"forbidden: copy from it", false, `[{"op":"copy","from":"/doc/a","path":"/doc/z"}]`, forbidA, forbidden},
		// A forbidden pattern, from a descendant: the path alone leaves
		// what is inside it writable, its subtree does not.
		{"forbidden: merge below it", true, `{"doc":{"a":{"b":1}}}`, mustPolicy(t, patch.ForbidPaths("/doc/a/b")), forbidden},
		{"forbidden: replace below the path", false, `[{"op":"replace","path":"/doc/a/b","value":1}]`, forbidA, accepted},
		{"forbidden: replace below the subtree", false, `[{"op":"replace","path":"/doc/a/b","value":1}]`, forbidTree, forbidden},
		{"forbidden: add below the subtree", false, `[{"op":"add","path":"/doc/a/b","value":1}]`, forbidTree, forbidden},
		{"forbidden: remove below the subtree", false, `[{"op":"remove","path":"/doc/a/b"}]`, forbidTree, forbidden},
		{"forbidden: move below the subtree", false, `[{"op":"move","from":"/doc/z","path":"/doc/a/b"}]`, forbidTree, forbidden},
		{"forbidden: copy below the subtree", false, `[{"op":"copy","from":"/doc/z","path":"/doc/a/b"}]`, forbidTree, forbidden},
		// A value constraint, from an ancestor: a whole value's absent member
		// is null, a merge's untouched.
		{"value: merge setting it", true, `{"doc":{"a":"bad"}}`, constrainA, refused},
		{"value: merge setting it allowed", true, `{"doc":{"a":"ok"}}`, constrainA, accepted},
		{"value: merge leaving it", true, `{"doc":{"z":1}}`, constrainA, accepted},
		{"value: merge clearing it", true, `{"doc":{"a":null}}`, constrainA, refused},
		{"value: replace ancestor setting it", false, `[{"op":"replace","path":"/doc","value":{"a":"bad"}}]`, constrainA, refused},
		{"value: replace ancestor allowed", false, `[{"op":"replace","path":"/doc","value":{"a":"ok"}}]`, constrainA, accepted},
		{"value: replace ancestor omitting it", false, `[{"op":"replace","path":"/doc","value":{"z":1}}]`, constrainA, refused},
		{"value: add ancestor setting it", false, `[{"op":"add","path":"/doc","value":{"a":"bad"}}]`, constrainA, refused},
		{"value: remove ancestor", false, `[{"op":"remove","path":"/doc"}]`, constrainA, refused},
		{"value: move into ancestor", false, `[{"op":"move","from":"/other","path":"/doc"}]`, constrainA, refused},
		{"value: move from ancestor", false, `[{"op":"move","from":"/doc","path":"/other"}]`, constrainA, refused},
		{"value: copy into ancestor", false, `[{"op":"copy","from":"/other","path":"/doc"}]`, constrainA, refused},
		{"value: copy from ancestor", false, `[{"op":"copy","from":"/doc","path":"/other"}]`, constrainA, accepted},
		// A value constraint, at the path.
		{"value: merge into it", true, `{"doc":{"a":{"b":1}}}`, constrainA, refused},
		{"value: replace it", false, `[{"op":"replace","path":"/doc/a","value":"bad"}]`, constrainA, refused},
		{"value: replace it allowed", false, `[{"op":"replace","path":"/doc/a","value":"ok"}]`, constrainA, accepted},
		{"value: add it", false, `[{"op":"add","path":"/doc/a","value":"bad"}]`, constrainA, refused},
		{"value: remove it", false, `[{"op":"remove","path":"/doc/a"}]`, constrainA, refused},
		{"value: move into it", false, `[{"op":"move","from":"/doc/z","path":"/doc/a"}]`, constrainA, refused},
		{"value: copy into it", false, `[{"op":"copy","from":"/doc/z","path":"/doc/a"}]`, constrainA, refused},
		// A value constraint, from a descendant: it judges its own path.
		{"value: merge below it", true, `{"doc":{"a":{"b":"bad"}}}`, mustPolicy(t, patch.ConstrainValues("/doc/a/b", patch.ValueConstraint{AllowedValues: []any{"ok"}})), refused},
		{"value: replace below it", false, `[{"op":"replace","path":"/doc/a/b","value":"bad"}]`, constrainA, accepted},
		{"value: remove below it", false, `[{"op":"remove","path":"/doc/a/b"}]`, constrainA, accepted},
		{"value: move below it", false, `[{"op":"move","from":"/doc/z","path":"/doc/a/b"}]`, constrainA, accepted},
		{"value: copy below it", false, `[{"op":"copy","from":"/doc/z","path":"/doc/a/b"}]`, constrainA, accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := patch.ApplyPatchPolicyConstraints(parseWrites(t, tc.merge, tc.body), tc.policy, nil)
			if tc.want == accepted {
				assert.NoError(t, err)
				return
			}
			requireCode(t, err, tc.want)
		})
	}
}

// A constraint on an array path judges each element, however the array is
// written; a value its pattern or bounds cannot judge is refused.
func TestPolicy_ArrayElementsAndUnjudgeableValues(t *testing.T) {
	pc := mustPolicy(t,
		patch.ConstrainValues("/tags", patch.ValueConstraint{Pattern: "^[a-z]+$", DeniedValues: []any{"admin"}}),
		patch.ConstrainValues("/doc/n", patch.ValueConstraint{Min: new(1.0)}),
		patch.ConstrainValues("/doc/tag", patch.ValueConstraint{Pattern: "^[a-z]+$"}),
	)
	for _, tc := range []struct {
		name  string
		merge bool
		body  string
		want  string
	}{
		{"append denied", false, `[{"op":"array_append","path":"/tags","value":"admin"}]`, "E2085"},
		{"replace denied element", false, `[{"op":"replace","path":"/tags","value":["ok","admin"]}]`, "E2085"},
		{"replace unmatched element", false, `[{"op":"replace","path":"/tags","value":["ok","BAD!"]}]`, "E2085"},
		{"merge denied element", true, `{"tags":["admin"]}`, "E2085"},
		{"replace allowed elements", false, `[{"op":"replace","path":"/tags","value":["ok","fine"]}]`, ""},
		{"clear", false, `[{"op":"remove","path":"/tags"}]`, ""},
		{"bounds on an object", true, `{"doc":{"n":{"x":1}}}`, "E2085"},
		{"bounds on text", true, `{"doc":{"n":"5"}}`, "E2085"},
		{"bounds on a number", true, `{"doc":{"n":5}}`, ""},
		{"pattern on an array", true, `{"doc":{"tag":["a"]}}`, "E2085"},
		{"pattern on a number", false, `[{"op":"replace","path":"/doc","value":{"tag":1}}]`, "E2085"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := patch.ApplyPatchPolicyConstraints(parseWrites(t, tc.merge, tc.body), pc, nil)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			requireCode(t, err, tc.want)
		})
	}
}

// A constraint on an array path judges each element whichever operation
// writes it: an element added or replaced by its own path included, and a
// declared array inside a document written whole with its document. A write
// it cannot judge as an element — a move or copy into one, a write inside
// one — is refused; removing an element writes nothing.
func TestPolicy_ArrayConstraintJudgesElementWrites(t *testing.T) {
	transfer := []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy}
	schema := newSchema("items", 20, map[string]*patch.PathSchema{
		"/tags":        {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpArrayAppend}, DataType: core.FieldTypeArray, SQLColumn: "tags"},
		"/tags/*":      {AllowedOps: transfer, DataType: core.FieldTypeAny},
		"/tags/*/**":   {AllowedOps: transfer, DataType: core.FieldTypeAny},
		"/doc":         {AllowedOps: transfer, DataType: core.FieldTypeJSON, SQLColumn: "doc"},
		"/doc/**":      {AllowedOps: transfer, DataType: core.FieldTypeAny},
		"/doc/roles":   {AllowedOps: transfer, DataType: core.FieldTypeArray},
		"/doc/roles/*": {AllowedOps: transfer, DataType: core.FieldTypeAny},
		"/other":       {AllowedOps: transfer, DataType: core.FieldTypeJSON, SQLColumn: "other"},
	})
	deny := patch.ValueConstraint{DeniedValues: []any{"admin", nil}}
	pc := mustPolicy(t, patch.ConstrainValues("/tags", deny), patch.ConstrainValues("/doc/roles", deny))
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"add appended element", `[{"op":"add","path":"/tags/-","value":"admin"}]`, "E2085"},
		{"add element at an index", `[{"op":"add","path":"/tags/0","value":"admin"}]`, "E2085"},
		{"replace element", `[{"op":"replace","path":"/tags/0","value":"admin"}]`, "E2085"},
		{"replace element with null", `[{"op":"replace","path":"/tags/0","value":null}]`, "E2085"},
		{"add allowed element", `[{"op":"add","path":"/tags/-","value":"ok"}]`, ""},
		{"remove element", `[{"op":"remove","path":"/tags/0"}]`, ""},
		{"move element out", `[{"op":"move","from":"/tags/0","path":"/other"}]`, ""},
		{"copy into element", `[{"op":"copy","from":"/other","path":"/tags/-"}]`, "E2085"},
		{"move into element", `[{"op":"move","from":"/other","path":"/tags/0"}]`, "E2085"},
		{"write inside element", `[{"op":"add","path":"/tags/0/name","value":"x"}]`, "E2085"},
		{"remove inside element", `[{"op":"remove","path":"/tags/0/name"}]`, "E2085"},
		{"declared array in a document set whole", `[{"op":"replace","path":"/doc","value":{"roles":["ok","admin"]}}]`, "E2085"},
		{"declared array member set whole", `[{"op":"replace","path":"/doc/roles","value":["admin"]}]`, "E2085"},
		{"declared array element", `[{"op":"add","path":"/doc/roles/-","value":"admin"}]`, "E2085"},
		{"declared array allowed", `[{"op":"replace","path":"/doc","value":{"roles":["ok"]}}]`, ""},
		{"declared array absent", `[{"op":"replace","path":"/doc","value":{}}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := patch.NewParser(schema, nil).Parse([]byte(tc.body))
			require.NoError(t, err)
			err = patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			requireCode(t, err, tc.want)
		})
	}
}

// An operation carries only what it writes: remove, move and copy no value,
// whatever a client attached, and the paths it writes are the affected ones.
func TestOperations_CarryWhatTheyWrite(t *testing.T) {
	schema := newSchema("items", 0, map[string]*patch.PathSchema{
		"/a": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy, patch.PatchOpTest}, DataType: core.FieldTypeString, SQLColumn: "a"},
		"/b": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy}, DataType: core.FieldTypeString, SQLColumn: "b"},
	})
	parser := patch.NewParser(schema, nil)
	for _, body := range []string{
		`[{"op":"remove","path":"/a","value":"kept?"}]`,
		`[{"op":"move","from":"/a","path":"/b","value":"kept?"}]`,
		`[{"op":"copy","from":"/a","path":"/b","value":"kept?"}]`,
	} {
		parsed, err := parser.Parse([]byte(body))
		require.NoError(t, err, body)
		assert.Nil(t, parsed.Operations[0].Value, body)
	}

	parsed, err := parser.Parse([]byte(`[{"op":"move","from":"/a","path":"/b"}]`))
	require.NoError(t, err)
	assert.Equal(t, []string{"/a", "/b"}, parsed.AffectedPaths)
	assert.Equal(t, []string{"/a", "/b"}, parsed.AuditInfo()["affected_paths"])

	parsed, err = parser.Parse([]byte(`[{"op":"test","path":"/a","value":"x"},{"op":"replace","path":"/b","value":"y"}]`))
	require.NoError(t, err)
	assert.Equal(t, []string{"/b"}, parsed.AffectedPaths, "a test writes nothing")
}

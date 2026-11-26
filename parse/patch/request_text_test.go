//go:build unit

package patch_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

func requestTextSchema() *patch.PatchSchema {
	ops := []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpCopy, patch.PatchOpMove, patch.PatchOpArrayAppend}
	return newSchema("items", 20, map[string]*patch.PathSchema{
		"/name":   {AllowedOps: ops, DataType: core.FieldTypeString, GoType: reflect.TypeFor[string](), SQLColumn: "name"},
		"/doc":    {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLColumn: "doc"},
		"/doc/**": {AllowedOps: ops, DataType: core.FieldTypeAny},
		"/tags":   {AllowedOps: ops, DataType: core.FieldTypeArray, GoType: reflect.TypeFor[[]string](), SQLColumn: "tags"},
	})
}

// A patch's text — every string of its values, a document's member names,
// its paths — is valid UTF-8 without U+0000, whichever way it arrives: a
// JSON body (whose decoder refuses invalid UTF-8 itself, and reads \u0000 as
// the character), Go values, or a merge patch. PostgreSQL refuses either in
// a text or jsonb parameter, so the patch is refused at parse instead.
func TestParse_RefusesInvalidText(t *testing.T) {
	refused := func(t *testing.T, err error, code string) {
		t.Helper()
		requireCode(t, err, code)
		assert.True(t, errors.Is(err, core.ErrInvalidText), "%v does not wrap ErrInvalidText", err)
	}
	for _, bad := range []struct{ name, text string }{{"U+0000", "\x00"}, {"invalid UTF-8", "\xff"}} {
		t.Run(bad.name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				op   patch.RawPatchOp
				code string
			}{
				{"a field's value", patch.RawPatchOp{Op: "replace", Path: "/name", Value: "a" + bad.text, HasValue: true}, "E2077"},
				{"a document's member", patch.RawPatchOp{Op: "replace", Path: "/doc", Value: map[string]any{"k": "v" + bad.text}, HasValue: true}, "E2077"},
				{"a document's member name", patch.RawPatchOp{Op: "replace", Path: "/doc", Value: map[string]any{"k" + bad.text: "v"}, HasValue: true}, "E2077"},
				{"a list's element", patch.RawPatchOp{Op: "replace", Path: "/tags", Value: []any{"a", "b" + bad.text}, HasValue: true}, "E2077"},
				{"an appended element", patch.RawPatchOp{Op: "array_append", Path: "/tags", Value: "b" + bad.text, HasValue: true}, "E2077"},
				{"a member's path", patch.RawPatchOp{Op: "add", Path: "/doc/a" + bad.text, Value: 1, HasValue: true}, patch.ECodeInvalidPatchOp.String()},
				{"a copy's source", patch.RawPatchOp{Op: "copy", From: "/doc/a" + bad.text, Path: "/doc/b"}, patch.ECodeInvalidPatchOp.String()},
			} {
				t.Run(tc.name, func(t *testing.T) {
					_, err := patch.NewParser(requestTextSchema(), nil).ParseOps([]patch.RawPatchOp{tc.op})
					refused(t, err, tc.code)
				})
			}
			t.Run("merge patch", func(t *testing.T) {
				for _, body := range []map[string]any{
					{"name": "a" + bad.text},
					{"doc": map[string]any{"k": map[string]any{"k" + bad.text: "v"}}},
					{"tags": []any{"b" + bad.text}},
				} {
					_, err := patch.ParseMergePatchMap(body, requestTextSchema(), nil)
					refused(t, err, "E2077")
				}
				_, err := patch.ParseMergePatchMap(map[string]any{"na" + bad.text + "me": "x"}, requestTextSchema(), nil)
				refused(t, err, patch.ECodePatchFieldNotFound.String())
			})
		})
	}

	t.Run("a JSON body's \\u0000", func(t *testing.T) {
		for _, body := range []string{
			`[{"op":"replace","path":"/name","value":"a\u0000b"}]`,
			`[{"op":"replace","path":"/doc","value":{"k\u0000":"v"}}]`,
			`[{"op":"add","path":"/doc/a\u0000b","value":1}]`,
		} {
			_, err := patch.NewParser(requestTextSchema(), nil).Parse([]byte(body))
			require.Error(t, err, body)
			assert.True(t, errors.Is(err, core.ErrInvalidText), body)
		}
		_, err := patch.ParseMergePatch([]byte(`{"doc":{"k":"v\u0000"}}`), requestTextSchema(), nil)
		refused(t, err, "E2077")
	})
	t.Run("valid text", func(t *testing.T) {
		_, err := patch.NewParser(requestTextSchema(), nil).ParseOps([]patch.RawPatchOp{
			{Op: "replace", Path: "/name", Value: "café \U0001F600 \x01", HasValue: true},
			{Op: "add", Path: "/doc/é", Value: map[string]any{"�": "v"}, HasValue: true},
		})
		require.NoError(t, err)
	})
}

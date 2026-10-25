//go:build unit

package parse_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
	"github.com/stretchr/testify/require"
)

type transferRow struct {
	ID     string  `json:"id,omitempty" db:"id"`
	Source *string `json:"source" parse:"patch" patch_ops:"replace,remove,copy,move"`
	Status string  `json:"status" parse:"patch" patch_ops:"replace,copy,move" enum:"active,offline"`
}

func TestPatchTransferDestinationValidation(t *testing.T) {
	p, err := parse.NewResource[transferRow]("transfer_rows")
	require.NoError(t, err)
	for _, verb := range []string{"copy", "move"} {
		for _, tc := range []struct {
			name      string
			source    *string
			wantError bool
		}{
			{"enum", new("invalid"), true}, {"null", nil, true}, {"valid", new("active"), false},
		} {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				target := transferRow{Source: tc.source, Status: "offline"}
				parsed, err := p.ParsePatch([]byte(fmt.Sprintf(`[{"op":%q,"from":"/source","path":"/status"}]`, verb)))
				require.NoError(t, err)
				got, err := p.ApplyValidatedPatch(parsed, target)
				if tc.wantError {
					var pe *core.ParseError
					require.ErrorAs(t, err, &pe)
					require.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
					require.Equal(t, "/status", pe.Path)
					require.Equal(t, 0, *pe.Index)
				} else {
					require.NoError(t, err)
					require.Equal(t, "active", got.Status)
					if verb == "move" {
						require.Nil(t, got.Source)
					} else {
						require.Equal(t, tc.source, got.Source)
					}
				}
				require.Equal(t, "offline", target.Status)
				require.Equal(t, tc.source, target.Source)
			})
		}
	}
}

func TestPatchTransferUsesCurrentValueAndCustomValidator(t *testing.T) {
	p, err := parse.NewResource[transferRow]("transfer_rows")
	require.NoError(t, err)
	sentinel := errors.New("destination validator")
	p.PatchSchema().AllowedPaths["/status"].Validators = append(p.PatchSchema().AllowedPaths["/status"].Validators, func(value any) error {
		if value == "offline" {
			return sentinel
		}
		return nil
	})
	for _, tc := range []struct {
		value string
		fail  bool
	}{{"offline", true}, {"active", false}} {
		target := transferRow{Source: new("invalid"), Status: "offline"}
		body := fmt.Sprintf(`[{"op":"replace","path":"/source","value":%q},{"op":"copy","from":"/source","path":"/status"}]`, tc.value)
		got, applyErr := p.ApplyPatch([]byte(body), target)
		if tc.fail {
			require.ErrorIs(t, applyErr, sentinel)
		} else {
			require.NoError(t, applyErr)
			require.Equal(t, "active", got.Status)
		}
		require.Equal(t, "invalid", *target.Source)
	}
	// A policy-constrained destination remains rejected before document execution.
	_, err = p.ParsePatch([]byte(`[{"op":"copy","from":"/source","path":"/status"}]`), parse.WithPatchPolicy(&patch.PatchPolicyConstraints{ValueConstraints: map[string]*patch.ValueConstraint{"/status": {AllowedValues: []any{"active"}}}}, nil))
	require.Error(t, err)
}

type emptyKeyRow struct {
	ID   string         `json:"id,omitempty" db:"id"`
	Meta map[string]any `json:"meta" parse:"patch"`
}

func emptyKeyParser(t *testing.T, opts ...core.Option) *parse.Resource[emptyKeyRow] {
	t.Helper()
	p, err := parse.NewResource[emptyKeyRow]("empty_key_rows", parse.ParserOptions(opts...))
	require.NoError(t, err)
	p.PatchSchema().AllowedPaths["/meta/**"] = &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpCopy, patch.PatchOpMove, patch.PatchOpTest}}
	return p
}

func TestPatchEmptyObjectKeys(t *testing.T) {
	p := emptyKeyParser(t)
	target := emptyKeyRow{Meta: map[string]any{"": map[string]any{"x": "old"}, "\x00": "collision", "array": []any{map[string]any{"": map[string]any{"z": "old"}}}}}
	body := `[
 {"op":"replace","path":"/meta//x","value":"new"},
 {"op":"copy","from":"/meta//x","path":"/meta/copied"},
 {"op":"move","from":"/meta/copied","path":"/meta//moved"},
 {"op":"remove","path":"/meta//x"},
 {"op":"add","path":"/meta/array/0//z","value":"new"},
 {"op":"add","path":"/meta//payload","value":{"":{"a/b":"ok"},"\u0000":"keep"}},
 {"op":"test","path":"/meta//moved","value":"new"},
 {"op":"test","path":"/meta/array/0//z","value":"new"},
 {"op":"test","path":"/meta//payload//a~1b","value":"ok"}
 ]`
	got, err := p.ApplyPatch([]byte(body), target)
	require.NoError(t, err)
	require.Equal(t, "collision", got.Meta["\x00"])
	nested, ok := got.Meta[""].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "new", nested["moved"])
	require.NotContains(t, nested, "x")
	payload, payloadOK := nested["payload"].(map[string]any)
	require.True(t, payloadOK)
	require.Equal(t, "keep", payload["\x00"])
	original, originalOK := target.Meta[""].(map[string]any)
	require.True(t, originalOK)
	require.Equal(t, "old", original["x"])
	_, err = p.ApplyPatch([]byte(`[{"op":"replace","path":"/meta//missing","value":1}]`), target)
	require.Error(t, err)
	_, err = p.ApplyPatch([]byte(`[{"op":"add","path":"/meta/missing//x","value":1}]`), target)
	require.Error(t, err)
}

func TestPatchCopyBudgetCountsOverwritesAcrossRuns(t *testing.T) {
	for _, source := range []string{"/meta/source", "/meta//source"} {
		t.Run(source, func(t *testing.T) {
			p := emptyKeyParser(t, core.WithMaxPatchCopyBytes(10))
			target := emptyKeyRow{Meta: map[string]any{"source": "abcd", "": map[string]any{"source": "abcd"}, "dest": "existing"}}
			// Each copy costs six encoded bytes even when replacing a longer value.
			body := fmt.Sprintf(`[{"op":"copy","from":%q,"path":"/meta/dest"},{"op":"test","path":"/meta/dest","value":"abcd"},{"op":"copy","from":%q,"path":"/meta/dest"}]`, source, source)
			_, err := p.ApplyPatch([]byte(body), target)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			require.Equal(t, 2, *pe.Index)
			oneCopy := []byte(fmt.Sprintf(`[{"op":"copy","from":%q,"path":"/meta/dest"}]`, source))
			got, err := emptyKeyParser(t, core.WithMaxPatchCopyBytes(6)).ApplyPatch(oneCopy, target)
			require.NoError(t, err)
			require.Equal(t, "abcd", got.Meta["dest"])
			require.Equal(t, "existing", target.Meta["dest"])
			_, err = emptyKeyParser(t, core.WithMaxPatchCopyBytes(5)).ApplyPatch(oneCopy, target)
			require.Error(t, err)
		})
	}
}

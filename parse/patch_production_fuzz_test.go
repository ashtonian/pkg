//go:build unit

package parse_test

import (
	"encoding/json"
	"testing"
	"unicode/utf8"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
	"github.com/stretchr/testify/require"
)

// Accepted requests may still fail against current data (for example a missing
// nested path). Neither outcome may mutate the caller or erase hidden state.
func FuzzPatchApplyPreservesTarget(f *testing.F) {
	for _, seed := range []string{
		`null`, `[]`, `{}`, `[{"op":"replace","path":"/optional","value":null}]`,
		`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`,
		`{"optional":null}`, `{"optional":"new"}`, `[{"op":"remove","path":"/optional"}]`,
	} {
		f.Add(seed)
	}
	p, err := parse.NewResource[*productionPatchRow]("production_patch_rows")
	require.NoError(f, err)
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 4096 {
			t.Skip()
		}
		for _, merge := range []bool{false, true} {
			target := &productionPatchRow{Name: "old", Optional: new("present"), Secret: true, private: "keep"}
			var parsed *patch.ParsedPatch
			var err error
			if merge {
				parsed, err = p.ParseMergePatch([]byte(body))
			} else {
				parsed, err = p.ParsePatch([]byte(body))
			}
			if err != nil {
				continue
			}
			got, err := p.ApplyValidatedPatch(parsed, target)
			require.Equal(t, "old", target.Name)
			require.Equal(t, "present", *target.Optional)
			require.True(t, target.Secret)
			require.Equal(t, "keep", target.private)
			if err == nil {
				require.NotNil(t, got)
				require.True(t, got.Secret)
				require.Equal(t, "keep", got.private)
			}
		}
	})
}

func FuzzEmptyKeyPatchPaths(f *testing.F) {
	f.Add("", "value")
	f.Add("a/b~c", "value")
	f.Add("\x00", "\x00")
	f.Fuzz(func(t *testing.T, key, value string) {
		if len(key)+len(value) > 4096 || !utf8.ValidString(key) || !utf8.ValidString(value) {
			t.Skip()
		}
		p := emptyKeyParser(t)
		target := emptyKeyRow{Meta: map[string]any{"": map[string]any{key: "old"}, "\x00": "collision"}}
		path := core.FormatPointer("meta", "", key)
		body, err := json.Marshal([]map[string]any{
			{"op": "replace", "path": path, "value": value},
			{"op": "copy", "from": path, "path": "/meta/destination"},
			{"op": "test", "path": "/meta/destination", "value": value},
		})
		require.NoError(t, err)
		got, err := p.ApplyPatch(body, target)
		require.NoError(t, err)
		require.Equal(t, value, got.Meta["destination"])
		require.Equal(t, "collision", got.Meta["\x00"])
		original, ok := target.Meta[""].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "old", original[key])
	})
}

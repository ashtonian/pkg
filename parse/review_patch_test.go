//go:build unit

package parse_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// PARSE-16: a patch body over MaxInputSize is E2063, the code the error table
// documents for every oversized body, filter or patch.
func TestPatch_OversizedBodyIsInputTooLarge(t *testing.T) {
	cfg := core.DefaultConfig()
	cfg.MaxInputSize = 16
	schema := &patch.PatchSchema{AllowedPaths: map[string]*patch.PathSchema{"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}}}}
	body := []byte(`{"name":"` + strings.Repeat("x", 32) + `"}`)

	_, err := patch.ParseMergePatch(body, schema, cfg)
	requireCode(t, err, patch.ECodeInputTooLarge.String())

	_, err = patch.NewParser(schema, cfg).Parse([]byte(`[{"op":"replace","path":"/name","value":"` + strings.Repeat("x", 32) + `"}]`))
	requireCode(t, err, patch.ECodeInputTooLarge.String())
}

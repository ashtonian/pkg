//go:build unit || integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// gadgetAttrs has a JSON document whose members a JSON Patch may write at
// any depth.
type gadgetAttrs struct {
	ID    int64          `json:"id"    db:"id"`
	Attrs map[string]any `json:"attrs" db:"attrs" parse:"patch"`
}

func gadgetAttrsResource(t *testing.T) *parse.Resource[gadgetAttrs] {
	t.Helper()
	r, err := parse.NewResource[gadgetAttrs]("gadget_attrs", parse.CustomizeSchemas(func(s parse.Schemas) error {
		return s.Patch.AddPath("/attrs/**", &patch.PathSchema{
			AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove},
			DataType:   core.FieldTypeAny, SQLColumn: "attrs",
		})
	}))
	require.NoError(t, err)
	return r
}

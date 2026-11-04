//go:build unit

package patch_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type operationBoundRow struct {
	Name string `json:"name" db:"name" parse:"patch" maxlen:"1"`
}

func TestGeneratedSchemaOperationLimit(t *testing.T) {
	t.Parallel()
	for name, generate := range map[string]func(*core.SchemaGenerator) (*patch.PatchSchema, error){
		"client": patch.GeneratePatchSchema[operationBoundRow],
		"server": patch.GenerateServerPatchSchema[operationBoundRow],
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := generate(core.NewSchemaGenerator())
			require.NoError(t, err)
			require.Equal(t, 50, schema.MaxOperations)
			require.Equal(t, patch.DefaultMaxOperations, schema.MaxOperations)
			operation := `{"op":"replace","path":"/name","value":"x"}`
			body := []byte("[" + strings.Repeat(operation+",", patch.DefaultMaxOperations-1) + operation + "]")
			parsed, err := patch.NewParser(schema, nil).Parse(body)
			require.NoError(t, err)
			require.Len(t, parsed.Operations, patch.DefaultMaxOperations)

			over := []byte("[" + strings.Repeat(operation+",", patch.DefaultMaxOperations) + operation + "]")
			_, err = patch.NewParser(schema, nil).Parse(over)
			requireCode(t, err, patch.ECodeTooManyOps.String())

			cfg := core.DefaultConfig()
			cfg.MaxInputSize = len(body) - 1
			_, err = patch.NewParser(schema, cfg).Parse(body)
			requireCode(t, err, patch.ECodeInputTooLarge.String())
			_, err = patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"xx"}]`))
			requireCode(t, err, patch.ECodePatchValueInvalid.String())
		})
	}
}

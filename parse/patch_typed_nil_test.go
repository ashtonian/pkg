//go:build unit

package parse_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type nullablePatchRow struct {
	ID       string       `json:"id" db:"id"`
	At       *time.Time   `json:"at" db:"at" parse:"patch"`
	Required time.Time    `json:"required" db:"required" parse:"patch"`
	Times    []*time.Time `json:"times" db:"times" parse:"patch" patch_ops:"replace,array_append"`
	Values   []time.Time  `json:"values" db:"values" parse:"patch" patch_ops:"replace,array_append"`
}

func TestValidateOperations_TypedNilTimestamp(t *testing.T) {
	p, err := parse.NewResource[nullablePatchRow]("nullable_patch_rows")
	require.NoError(t, err)
	at := time.Date(2026, 10, 25, 1, 2, 3, 123456789, time.UTC)
	var absent *time.Time
	var boxed any = absent
	for name, value := range map[string]any{
		"nil pointer":            absent,
		"pointer to nil pointer": &absent,
		"pointer to interface":   &boxed,
	} {
		t.Run(name, func(t *testing.T) {
			built, parseErr := p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: value}})
			require.NoError(t, parseErr)
			wire, parseErr := p.ParsePatch([]byte(`[{"op":"replace","path":"/at","value":null}]`))
			require.NoError(t, parseErr)
			assert.Nil(t, built.Operations[0].Value)
			assert.Equal(t, wire.EstimatedCost, built.EstimatedCost)
			assert.Equal(t, wire.AffectedPaths, built.AffectedPaths)
			for _, dialect := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
				builtSQL, sqlErr := patch.BuildPatchSQL(built, dialect)
				require.NoError(t, sqlErr)
				wireSQL, sqlErr := patch.BuildPatchSQL(wire, dialect)
				require.NoError(t, sqlErr)
				assert.Equal(t, wireSQL, builtSQL)
				assert.Contains(t, builtSQL.SetClause, "at = NULL")
			}
			got, applyErr := p.ApplyValidatedPatch(built, nullablePatchRow{At: &at, Required: at})
			require.NoError(t, applyErr)
			assert.Nil(t, got.At)
			assert.Equal(t, at, got.Required)
		})
	}

	nonnull, err := p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: &at}})
	require.NoError(t, err)
	got, err := p.ApplyValidatedPatch(nonnull, nullablePatchRow{})
	require.NoError(t, err)
	require.NotNil(t, got.At)
	assert.Equal(t, at, *got.At)

	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/required", Value: absent}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
	assert.Contains(t, pe.Message, "not nullable")

	server, err := parse.NewResource[sensor]("sensors")
	require.NoError(t, err)
	cleared, err := server.ValidateServerOperations([]patch.RawPatchOp{{Op: "replace", Path: "/rotated", Value: absent}})
	require.NoError(t, err)
	assert.Nil(t, cleared.Operations[0].Value)
}

func TestValidateOperations_TypedNilArrayElement(t *testing.T) {
	p, err := parse.NewResource[nullablePatchRow]("nullable_patch_rows")
	require.NoError(t, err)
	var absent *time.Time
	for _, op := range []string{"replace", "array_append"} {
		t.Run(op, func(t *testing.T) {
			var value any = absent
			if op == "replace" {
				value = []*time.Time{absent}
			}
			built, parseErr := p.ValidateOperations([]patch.RawPatchOp{{Op: op, Path: "/times", Value: value}})
			require.NoError(t, parseErr)
			got, applyErr := p.ApplyValidatedPatch(built, nullablePatchRow{})
			require.NoError(t, applyErr)
			assert.Equal(t, []*time.Time{nil}, got.Times)

			_, parseErr = p.ValidateOperations([]patch.RawPatchOp{{Op: op, Path: "/values", Value: value}})
			require.Error(t, parseErr, "non-nullable array elements reject typed nil")
		})
	}
}

func TestValidateOperations_CyclicPointerIsNotNull(t *testing.T) {
	p, err := parse.NewResource[nullablePatchRow]("nullable_patch_rows")
	require.NoError(t, err)
	var cycle any
	cycle = &cycle
	_, err = p.ValidateOperations([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: cycle}})
	var pe *core.ParseError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
}

func TestValidateOperations_TypedNilValidatorValue(t *testing.T) {
	for name, goType := range map[string]reflect.Type{
		"time":    reflect.TypeFor[*time.Time](),
		"string":  reflect.TypeFor[*string](),
		"integer": reflect.TypeFor[*int64](),
		"boolean": reflect.TypeFor[*bool](),
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			p := patch.NewParser(&patch.PatchSchema{AllowedPaths: map[string]*patch.PathSchema{
				"/value": {
					AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, GoType: goType,
					Validators: []core.Validator{func(value any) error {
						calls++
						assert.Nil(t, value, "validators receive canonical null")
						return nil
					}},
				},
			}}, nil)
			_, err := p.ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/value", Value: reflect.Zero(goType).Interface()}})
			require.NoError(t, err)
			assert.Equal(t, 1, calls)
		})
	}
}

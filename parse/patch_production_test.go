//go:build unit

package parse_test

import (
	"errors"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/stretchr/testify/require"
)

type productionPatchRow struct {
	ID       string  `json:"id,omitempty" db:"id"`
	Name     string  `json:"name" parse:"patch" patch_ops:"replace,test"`
	Optional *string `json:"optional,omitempty" parse:"patch" patch_ops:"replace,remove"`
	Secret   bool    `json:"-" db:"secret" parse:"server"`
	private  string
}

func TestDocumentPatchPreservesHiddenState(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow]("production_patch_rows")
	require.NoError(t, err)
	original := productionPatchRow{Name: "old", Secret: true, private: "keep"}
	got, err := p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`), original)
	require.NoError(t, err)
	require.True(t, got.Secret)
	require.Equal(t, "keep", got.private)
	require.Equal(t, "new", got.Name)
	require.Equal(t, "old", original.Name)
}

func TestDocumentPatchOmittedFieldMatchesDirect(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow]("production_patch_rows")
	require.NoError(t, err)
	for _, operation := range []string{`{"op":"replace","path":"/optional","value":"new"}`, `{"op":"remove","path":"/optional"}`} {
		direct, err := p.ApplyPatch([]byte("["+operation+"]"), productionPatchRow{Name: "old"})
		require.NoError(t, err)
		fallback, err := p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},`+operation+`]`), productionPatchRow{Name: "old"})
		require.NoError(t, err)
		require.Equal(t, direct, fallback)
	}
}

func TestPatchRejectsNullDocument(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow]("production_patch_rows")
	require.NoError(t, err)
	_, err = p.ParsePatch([]byte(`null`))
	require.Error(t, err)
	_, err = p.ParseMergePatch([]byte(`null`))
	require.Error(t, err)
}

func TestMergeNullRunsDestinationValidators(t *testing.T) {
	p, err := parse.NewResource[productionPatchRow]("production_patch_rows")
	require.NoError(t, err)
	rejected := errors.New("cannot clear")
	p.PatchSchema().AllowedPaths["/optional"].Validators = []core.Validator{func(value any) error {
		if value == nil {
			return rejected
		}
		return nil
	}}
	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":"/optional","value":null}]`))
	require.ErrorIs(t, err, rejected)
	_, err = p.ParseMergePatch([]byte(`{"optional":null}`))
	require.ErrorIs(t, err, rejected)
}

func TestPatchNullableArrayElements(t *testing.T) {
	type row struct {
		ID       string `json:"id,omitempty" db:"id"`
		Numbers  []int  `json:"numbers" parse:"patch" patch_ops:"replace,array_append"`
		Optional []*int `json:"optional" parse:"patch" patch_ops:"replace,array_append"`
	}
	p, err := parse.NewResource[row]("rows")
	require.NoError(t, err)
	_, err = p.ParsePatch([]byte(`[{"op":"array_append","path":"/numbers","value":null}]`))
	require.Error(t, err)
	for _, body := range []string{`[{"op":"replace","path":"/optional","value":[null,1]}]`, `[{"op":"array_append","path":"/optional","value":null},{"op":"array_append","path":"/optional","value":1}]`} {
		got, err := p.ApplyPatch([]byte(body), row{})
		require.NoError(t, err)
		require.Len(t, got.Optional, 2)
		require.Nil(t, got.Optional[0])
		require.Equal(t, 1, *got.Optional[1])
	}
}

func TestDocumentPatchNilTarget(t *testing.T) {
	p, err := parse.NewResource[*productionPatchRow]("production_patch_rows")
	require.NoError(t, err)
	_, err = p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"}]`), nil)
	require.Error(t, err)
	maps, err := parse.NewResource[*emptyKeyRow]("empty_key_rows")
	require.NoError(t, err)
	parsed, err := maps.ParseMergePatch([]byte(`{"meta":{"x":1}}`))
	require.NoError(t, err)
	_, err = maps.ApplyValidatedPatch(parsed, nil)
	require.Error(t, err)
}

func TestDocumentPatchPreservesUnrelatedNestedState(t *testing.T) {
	type details struct {
		Visible string `json:"visible"`
		Hidden  string `json:"-"`
		private string
	}
	type row struct {
		ID      string  `json:"id,omitempty" db:"id"`
		Name    string  `json:"name" parse:"patch" patch_ops:"replace,test"`
		Details details `json:"details"`
	}
	p, err := parse.NewResource[row]("rows")
	require.NoError(t, err)
	target := row{Name: "old", Details: details{Visible: "visible", Hidden: "hidden", private: "private"}}
	got, err := p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`), target)
	require.NoError(t, err)
	require.Equal(t, target.Details, got.Details)
	require.Equal(t, "new", got.Name)
}

func TestDocumentPatchRequiresMatchingFieldCodec(t *testing.T) {
	type row struct {
		ID   string `json:"id,omitempty" db:"id"`
		Name string `bson:"name" json:"-" parse:"patch" patch_ops:"replace,test"`
	}
	p, err := parse.NewResource[row]("rows", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	require.NoError(t, err)
	got, err := p.ApplyPatch([]byte(`[{"op":"replace","path":"/name","value":"new"}]`), row{Name: "old"})
	require.NoError(t, err)
	require.Equal(t, "new", got.Name)
	_, err = p.ApplyPatch([]byte(`[{"op":"test","path":"/name","value":"old"},{"op":"replace","path":"/name","value":"new"}]`), row{Name: "old"})
	require.ErrorContains(t, err, "configure a matching codec")
}

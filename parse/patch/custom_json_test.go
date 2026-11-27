//go:build unit

package patch_test

import (
	"encoding/json/jsontext"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/patch"
)

// codedV1 and codedV2 write themselves as one string: through
// encoding/json's methods, and through encoding/json/v2's, which
// encoding/json honours too.
type (
	codedV1 struct{ A int }
	codedV2 struct{ A int }
)

func (codedV1) MarshalJSON() ([]byte, error) { return []byte(`"coded"`), nil }
func (*codedV1) UnmarshalJSON([]byte) error  { return nil }

func (codedV2) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteToken(jsontext.String("coded"))
}

func (*codedV2) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	_, err := dec.ReadValue()
	return err
}

// A document of a type that writes itself is no object of its Go fields,
// whichever encoding/json methods it writes itself through: an object merge
// into it is refused, not merged into fields the stored document lacks.
func TestMergePatch_ATypeThatWritesItselfIsNoObject(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[codedV1](), reflect.TypeFor[codedV2]()} {
		t.Run(typ.Name(), func(t *testing.T) {
			_, err := patch.ParseMergePatchMap(map[string]any{"value": map[string]any{"A": 3}}, declaredJSONSchema(typ), nil)
			require.ErrorContains(t, err, "object merge for custom JSON type patch_test."+typ.Name()+" is not supported")
		})
	}
}

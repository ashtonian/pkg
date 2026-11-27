//go:build unit

package core

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/internal/shapes"
)

// taggedPlain is an unexported struct a tag names, which encoding/json
// writes as a member. taggedStampA and taggedStampB have methods
// encoding/json cannot call through an unexported field, so it drops them;
// their methods collide, so the struct embedding both inherits neither.
type taggedPlain struct{ P int }

type taggedStampA struct{ At int64 }

func (taggedStampA) MarshalJSON() ([]byte, error) { return []byte(`0`), nil }

type taggedStampB struct{ At int64 }

func (taggedStampB) MarshalJSON() ([]byte, error) { return []byte(`0`), nil }

type taggedUnexported struct {
	taggedPlain  `json:"plain"`
	taggedStampA `json:"a"`
	taggedStampB `json:"b"`
	N            int `json:"n"`
}

// tieLeft and tieRight give the struct embedding both an ID at one depth,
// which neither keeps.
type tieLeft struct{ ID, L int }

type tieRight struct{ ID, R int }

type tieDoc struct {
	tieLeft
	tieRight
}

// JSONFieldsOf and the document shapes stored documents are checked
// against (shapes.Document) are two engines over one naming; they must
// name the same members, the ones encoding/json writes, for an embedded
// self-coding type, a tagged unexported struct with and without methods,
// and a tie.
func TestJSONFieldsOf_NamesTheMembersDocumentShapesRead(t *testing.T) {
	for _, value := range []any{docCollide{}, taggedUnexported{}, tieDoc{}} {
		typ := reflect.TypeOf(value)
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		var members map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &members))
		written := slices.Sorted(maps.Keys(members))

		fields, err := JSONFieldsOf(typ)
		require.NoError(t, err)
		var listed []string
		for f := range fields.Fields() {
			listed = append(listed, f.External())
		}
		slices.Sort(listed)

		shape, err := shapes.Document(typ)
		require.NoError(t, err)
		var read []string
		for _, f := range shape.Fields() {
			read = append(read, f.Name())
		}
		slices.Sort(read)

		assert.Equal(t, written, listed, "%s: JSONFieldsOf", typ)
		assert.Equal(t, written, read, "%s: shapes.Document", typ)
	}
}

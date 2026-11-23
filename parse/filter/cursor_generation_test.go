//go:build unit

package filter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/cursor"
)

// extendedCursorField is CursorField with a member it does not declare, as
// another release's token could carry.
type extendedCursorField struct {
	Name      string        `msgpack:"n"`
	Value     any           `msgpack:"v"`
	Direction SortDirection `msgpack:"d"`
	Collation string        `msgpack:"c"`
}

type extendedPageToken struct {
	Fields  []extendedCursorField `msgpack:"f"`
	Reverse bool                  `msgpack:"r,omitempty"`
	Limit   int                   `msgpack:"l"`
}

// A cursor of another generation is an unsupported version whatever its
// payload holds; a cursor of this generation holding a member PageToken or
// CursorField does not declare is malformed. Both are E2082.
func TestDecodeCursorReportsGenerationBeforeMembers(t *testing.T) {
	declared := PageToken{Fields: []CursorField{{Name: "id", Value: "a", Direction: SortAsc}}}
	extended := extendedPageToken{
		Fields: []extendedCursorField{{Name: "id", Value: "a", Direction: SortAsc, Collation: "C"}},
		Limit:  25,
	}

	tests := []struct {
		name       string
		payload    any
		generation uint32
		message    string
		cause      error
	}{
		{"a newer generation with undefined members", extended, cursorGeneration + 1, "unsupported cursor version", cursor.ErrCursorGeneration},
		{"an older generation with undefined members", extended, cursorGeneration - 1, "unsupported cursor version", cursor.ErrCursorGeneration},
		{"a newer generation with the declared members", declared, cursorGeneration + 1, "unsupported cursor version", cursor.ErrCursorGeneration},
		{"this generation with undefined members", extended, cursorGeneration, "invalid cursor data", cursor.ErrInvalidCursor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, err := cursor.Encode(tt.payload, cursor.WithGeneration(tt.generation))
			require.NoError(t, err)

			_, err = DecodeCursor(tok)
			var pe *core.ParseError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, ECodeInvalidCursor.String(), pe.Code)
			assert.Equal(t, tt.message, pe.Message)
			assert.ErrorIs(t, err, tt.cause)
		})
	}
}

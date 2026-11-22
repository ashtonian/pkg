//go:build unit

package filter_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A cursor field that names its value twice panicked the MessagePack decoder;
// it is a malformed cursor like any other.
func TestDecodeCursorRefusesRepeatedMember(t *testing.T) {
	for _, tok := range []string{
		"gqFnA6FwgaFmkYShbqJpZKF2oWGhdqFioWSjYXNj", // {g:3, p:{f:[{n:"id", v:"a", v:"b", d:"asc"}]}}
		"gqFnA6FwgaFmkYShbqJpZKF2AaF2AqFko2FzYw",   // the same with v:1, v:2
	} {
		var err error
		require.NotPanics(t, func() { _, err = filter.DecodeCursor(tok) }, tok)
		var pe *core.ParseError
		require.True(t, errors.As(err, &pe), "%s: %v", tok, err)
		assert.Equal(t, core.CodeInvalidCursor.String(), pe.Code, tok)
	}
}

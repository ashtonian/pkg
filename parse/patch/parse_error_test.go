//go:build unit || concurrency

package patch_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// requireCode asserts err is a *core.ParseError carrying code.
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "error %v is not a ParseError", err)
	assert.Equal(t, code, pe.Code, pe.Message)
}

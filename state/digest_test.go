//go:build unit

package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStateDigestRetainsNumericIdentityAcrossDocumentFormatting(t *testing.T) {
	one, err := JSONDigest([]byte(`{"size":9007199254740993,"identity":"one"}`))
	require.NoError(t, err)
	two, err := JSONDigest([]byte("{\n  \"identity\": \"one\",\n  \"size\": 9007199254740993\n}\n"))
	require.NoError(t, err)
	require.Equal(t, one, two)
	changed, err := JSONDigest([]byte(`{"size":9007199254740992,"identity":"one"}`))
	require.NoError(t, err)
	require.NotEqual(t, one, changed)
	for _, invalid := range [][]byte{nil, []byte(`{} {}`), []byte(`{"field":`), {'"', 0xff, '"'}} {
		_, err := JSONDigest(invalid)
		require.Error(t, err)
	}
}

//go:build unit

package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func TestParseEnumTag(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want []string
	}{
		{"a,b", []string{"a", "b"}},
		{"a, b ,c", []string{"a", "b", "c"}},
		{"'net 30, eom','b'", []string{"net 30, eom", "b"}},
		{"' padded', 'trailing '", []string{" padded", "trailing "}},
		{"'it''s',''''", []string{"it's", "'"}},
		{"a, 'b, c' ,d", []string{"a", "b, c", "d"}},
		{"'',a", []string{"", "a"}},
		{"'a,b'", []string{"a,b"}},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			got, err := core.ParseEnumTag(tc.tag)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
	for _, tag := range []string{"'a", "'a''", "'a'b", "'a' b", "a,,b", "a,", ",a", " "} {
		t.Run("refuses "+tag, func(t *testing.T) {
			_, err := core.ParseEnumTag(tag)
			assert.Error(t, err)
		})
	}
}

//go:build unit

package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePointer(t *testing.T) {
	tests := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "", want: nil},
		{in: "/", want: []string{""}},
		{in: "/a/b", want: []string{"a", "b"}},
		{in: "/a~1b", want: []string{"a/b"}},
		{in: "/m~0n", want: []string{"m~n"}},
		{in: "/~01", want: []string{"~1"}}, // ~0 then literal 1, not ~01 → /
		{in: "a/b", wantErr: true},
		{in: "/a~2b", wantErr: true},
		{in: "/a~", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParsePointer(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatPointerRoundTrips(t *testing.T) {
	for _, tokens := range [][]string{{"a/b"}, {"m~n", "x"}, {"~1"}, {"plain"}} {
		got, err := ParsePointer(FormatPointer(tokens...))
		require.NoError(t, err)
		assert.Equal(t, tokens, got)
	}
}

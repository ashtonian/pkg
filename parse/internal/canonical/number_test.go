//go:build unit

package canonical

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A number's form is one spelling of its value and display scale, never
// longer than the literal's parts, and exact whatever its exponent.
func TestNumberForm(t *testing.T) {
	big := "1" + strings.Repeat("0", 20) // 10^20, past int64 arithmetic
	for _, tc := range []struct{ literal, want string }{
		{"0", "0"},
		{"-0", "0"},
		{"0.0", "0e-1"},
		{"-0.00e1", "0e-1"},
		{"0e-1001", "0e-1001"},
		{"0e1001", "0"},
		{"100", "1e2"},
		{"1e2", "1e2"},
		{"10e1", "1e2"},
		{"1E+2", "1e2"},
		{"1e002", "1e2"},
		{"-100", "-1e2"},
		{"1", "1"},
		{"1.0", "10e-1"},
		{"1.0e1", "1e1"},
		{"1.00e1", "100e-1"},
		{"15.0", "150e-1"},
		{"1.50e1", "150e-1"},
		{"-1.50", "-150e-2"},
		{"0.001", "1e-3"},
		{"1E-3", "1e-3"},
		{"0.0010", "10e-4"},
		{"0.1e-999", "1e-1000"},
		{"1" + strings.Repeat("0", 1001), "1e1001"},
		{"1" + strings.Repeat("0", 1001) + ".0", "1" + strings.Repeat("0", 1002) + "e-1"},
		{"1e100000", "1e100000"},
		{"1e" + big, "1e" + big},
		{"0.001e" + big, "1e" + strings.Repeat("9", 18) + "97"},
		{"1000e-" + big, "1000e-" + big},
		{"1e-" + big, "1e-" + big},
		{"1.0e-" + big, "10e-1" + strings.Repeat("0", 19) + "1"},
		{"1.000e1" + strings.Repeat("0", 17), "1e1" + strings.Repeat("0", 17)},
		{"1.001e1" + strings.Repeat("0", 17), "1001e99999999999999997"},
		{"-12.5e-" + strings.Repeat("9", 25), "-125e-1" + strings.Repeat("0", 25)},
	} {
		t.Run(tc.literal[:min(len(tc.literal), 40)], func(t *testing.T) {
			got := numberForm(json.Number(tc.literal))
			assert.Equal(t, tc.want, string(got))
			assert.Equal(t, got, numberForm(got), "the form of a form is itself")
			assert.True(t, json.Valid([]byte(got)), "%s is a JSON number", got)
		})
	}
}

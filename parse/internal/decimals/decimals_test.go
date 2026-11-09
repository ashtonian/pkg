//go:build unit

package decimals

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

func TestMember(t *testing.T) {
	tests := []struct {
		name   string
		member any
		want   string
		ok     bool
	}{
		{"number", json.Number("1.500e2"), "150", true},
		{"number past double precision", json.Number("9007199254740993.5"), "9007199254740993.5", true},
		{"Go number", 2.5, "2.5", true},
		{"plain string", "-12.50", "-12.5", true},
		{"string with an exponent", "1e3", "", false},
		{"string with a leading zero", "012", "", false},
		{"string past the length bound", strings.Repeat("9", MaxMemberLength+1), "", false},
		{"number past the digit bound", json.Number("1e1000"), "", false},
		{"bool", true, "", false},
		{"null", nil, "", false},
		{"object", map[string]any{}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Member(tt.member)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValue(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
		ok    bool
	}{
		{"integer", int64(-9223372036854775808), "-9223372036854775808", true},
		{"real", 0.1, "0.1", true},
		{"decimal text", "12345678901234567890.123456789012345678", "12345678901234567890.123456789012345678", true},
		{"text with an exponent and a scale", "1.500e2", "150", true},
		{"negative zero", "-0.0", "0", true},
		{"blob text", []byte("10.50"), "10.5", true},
		{"a member's JSON string", `"9.50"`, "9.5", true},
		{"a member's JSON string with an exponent", `"1e3"`, "", false},
		{"a malformed JSON string", `"9.5`, "", false},
		{"JSON null", "null", "", false},
		{"JSON true", "true", "", false},
		{"JSON object", `{"a":1}`, "", false},
		{"text that is no number", "12 apples", "", false},
		{"SQL NULL", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Value(tt.value)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// The key orders as the decimals do, and equal keys are equal decimals:
// over boundary values and generated literals of every sign, scale and
// magnitude, sorting by key gives the order core.CompareDecimals gives.
func TestKeyOrdersAsTheDecimals(t *testing.T) {
	widest := strings.Repeat("9", core.MaxDecimalDigits)
	texts := []string{
		"0", "1", "-1", "0.5", "0.51", "-0.5", "-0.51", "10", "9.99", "100", "0.001", "-0.001",
		"9007199254740992", "9007199254740993", "9223372036854775807", "9223372036854775808",
		"-9223372036854775808", "-9223372036854775809",
		"12345678901234567890.123456789012345678", "12345678901234567890.123456789012345679",
		widest, "-" + widest, "0." + strings.Repeat("0", core.MaxDecimalDigits-3) + "1",
	}
	r := rand.New(rand.NewPCG(144, 2026))
	for range 5000 {
		texts = append(texts, randomDecimal(t, r))
	}
	for i, text := range texts {
		canonical, err := core.DecimalText(text)
		require.NoError(t, err, text)
		texts[i] = canonical
	}

	byKey := slices.Clone(texts)
	slices.SortStableFunc(byKey, func(a, b string) int { return bytes.Compare(Key(a), Key(b)) })
	byValue := slices.Clone(texts)
	slices.SortStableFunc(byValue, func(a, b string) int {
		c, ok := core.CompareDecimals(a, b)
		require.True(t, ok)
		return c
	})
	require.Equal(t, len(byValue), len(byKey))
	for i := range byKey {
		c, _ := core.CompareDecimals(byKey[i], byValue[i])
		require.Zero(t, c, "position %d: %s by key, %s by value", i, byKey[i], byValue[i])
	}
	for i := 1; i < len(byKey); i++ {
		c, _ := core.CompareDecimals(byKey[i-1], byKey[i])
		assert.Equal(t, c == 0, bytes.Equal(Key(byKey[i-1]), Key(byKey[i])), "%s and %s", byKey[i-1], byKey[i])
	}
}

// randomDecimal is a JSON number literal: a sign, up to 40 digits with
// leading and trailing zeros, a fraction and an exponent, each sometimes.
func randomDecimal(t *testing.T, r *rand.Rand) string {
	t.Helper()
	digits := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteByte(byte('0' + r.IntN(10)))
		}
		return b.String()
	}
	var b strings.Builder
	if r.IntN(2) == 0 {
		b.WriteByte('-')
	}
	intPart := strings.TrimLeft(digits(1+r.IntN(20)), "0")
	if intPart == "" {
		intPart = "0"
	}
	b.WriteString(intPart)
	if r.IntN(3) > 0 {
		b.WriteString("." + digits(1+r.IntN(20)))
	}
	if r.IntN(4) == 0 {
		b.WriteString("e")
		if r.IntN(2) == 0 {
			b.WriteByte('-')
		}
		b.WriteString(strconv.Itoa(r.IntN(40)))
	}
	return b.String()
}

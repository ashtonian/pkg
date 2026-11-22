package gowalker_test

import (
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

func TestMisspeltTagKey(t *testing.T) {
	known := []string{"default", "validate", "env", "envref", "filter_ops", "minitems", "maxitems"}
	for _, tc := range []struct {
		written string
		known   []string
		meant   string
	}{
		{"defualt", known, "default"},  // swapped
		{"defaul", known, "default"},   // deleted
		{"defaults", known, "default"}, // inserted
		{"dafault", known, "default"},  // replaced
		{"vaildate", known, "validate"},
		{"Default", known, "default"},
		{"DEFAULT", known, "default"},
		{"env_ref", known, "envref"},
		{"env-Ref", known, "envref"},
		{"filterOps", known, "filter_ops"},
		{"filter-ops", known, "filter_ops"},
		{"filter_op", known, "filter_ops"},
		{"minItems", known, "minitems"},
		// Equal under folding beats one edit, wherever the keys are listed.
		{"ABC", []string{"abd", "abc_"}, "abc_"},
		{"default", known, ""},
		{"filter_ops", known, ""},
		{"json", known, ""},
		{"yaml", known, ""},
		{"dfeualt", known, ""}, // two edits
		{"cba", []string{"abc"}, ""},
		{"badc", []string{"abcd"}, ""},
		{"a", []string{"abc"}, ""},
		{"x", nil, ""},
	} {
		meant, ok := gowalker.MisspeltTagKey(tc.written, tc.known)
		if meant != tc.meant || ok != (tc.meant != "") {
			t.Errorf("MisspeltTagKey(%q, %q) = %q, %v; want %q", tc.written, tc.known, meant, ok, tc.meant)
		}
	}
}

// One edit is symmetric, at either end and in the middle.
func TestMisspeltTagKey_OneEdit(t *testing.T) {
	for _, pair := range [][2]string{
		{"abc", "abd"}, {"abc", "xbc"}, {"abc", "ab"}, {"abc", "bc"}, {"abc", "abcd"},
		{"abc", "bac"}, {"abc", "acb"}, {"ab", "ba"}, {"", "a"},
	} {
		for _, p := range [][2]string{pair, {pair[1], pair[0]}} {
			if meant, ok := gowalker.MisspeltTagKey(p[0], []string{p[1]}); !ok || meant != p[1] {
				t.Errorf("%q is not one edit from %q", p[0], p[1])
			}
		}
	}
}

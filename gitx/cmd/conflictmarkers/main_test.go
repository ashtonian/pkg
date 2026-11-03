package main

import "testing"

func TestScope(t *testing.T) {
	read := scope([]string{"records/", "vendor/"}, []string{".md"})
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"docs/guide.md", true},
		{"scripts/run.sh", true},
		{"records/LEDGER.md", true},
		{"records/run-1/README.md", true},
		{"records/run-1/sut.log", false},
		{"vendor/x/y.go", false},
		{"records.md", true}, // not under the prefix
	} {
		if got := read(tc.path); got != tc.want {
			t.Errorf("read(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if !scope(nil, nil)("anything") {
		t.Error("with nothing skipped, a path is not read")
	}
	if scope([]string{"records/"}, nil)("records/a.md") {
		t.Error("with nothing kept, a file under a skipped prefix is read")
	}
}

//go:build unit

package filter_test

import (
	"reflect"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
)

func buildRaw(t *testing.T, rawSQL string, args ...any) *filter.SQLResult {
	t.Helper()
	b := filter.NewSQLBuilder(nil, filter.PostgresDialect{})
	res, err := b.Build(&filter.FilterAST{Root: &filter.FilterNode{
		Op:      filter.OpRawSQL,
		RawSQL:  rawSQL,
		RawArgs: args,
	}})
	if err != nil {
		t.Fatalf("Build(%q): %v", rawSQL, err)
	}
	return res
}

func argsEqual(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func TestBuildRawSQL_Renumber(t *testing.T) {
	tests := []struct {
		name      string
		rawSQL    string
		args      []any
		wantWhere string
		wantArgs  []any
	}{
		{"ordered", "$1 AND $2", []any{"a", "b"}, "$1 AND $2", []any{"a", "b"}},
		// $10 must survive the renumber: the old substring replace corrupted it
		// because "$1" matches inside "$10".
		{
			"multi-digit list",
			"x IN ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
			[]any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			"x IN ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)",
			[]any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		// A lone "$11" must map to RawArgs[10], not be split into "$1"+"1".
		{
			"single multi-digit",
			"v = $11",
			[]any{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "eleven"},
			"v = $1",
			[]any{"eleven"},
		},
		// Renumber follows left-to-right appearance order.
		{"out of order", "$2 = $1", []any{"first", "second"}, "$1 = $2", []any{"second", "first"}},
		// Each occurrence of a repeated placeholder gets its own slot + arg, so
		// the result is correct for positional (?) dialects too.
		{"repeated", "$1 OR $1", []any{"x", "y"}, "$1 OR $2", []any{"x", "x"}},
		{"empty", "", nil, "1=1", []any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := buildRaw(t, tt.rawSQL, tt.args...)
			if res.Where != tt.wantWhere {
				t.Errorf("Where = %q, want %q", res.Where, tt.wantWhere)
			}
			if !argsEqual(res.Args, tt.wantArgs) {
				t.Errorf("Args = %#v, want %#v", res.Args, tt.wantArgs)
			}
		})
	}
}

func TestBuildRawSQL_PlaceholderOutOfRange(t *testing.T) {
	b := filter.NewSQLBuilder(nil, filter.PostgresDialect{})
	_, err := b.Build(&filter.FilterAST{Root: &filter.FilterNode{
		Op:      filter.OpRawSQL,
		RawSQL:  "x = $5",
		RawArgs: []any{"only-one"},
	}})
	if err == nil {
		t.Fatal("expected an error for out-of-range placeholder $5 with 1 arg, got nil")
	}
}

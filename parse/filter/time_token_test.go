//go:build unit

package filter_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestResolveRelativeTime(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in      string
		want    time.Time
		isToken bool
		wantErr bool
	}{
		{in: "now", want: now, isToken: true},
		{in: "now-1h", want: now.Add(-time.Hour), isToken: true},
		{in: "now-24h", want: now.Add(-24 * time.Hour), isToken: true},
		{in: "now-7d", want: now.Add(-7 * 24 * time.Hour), isToken: true},
		{in: "now+5m", want: now.Add(5 * time.Minute), isToken: true},
		{in: "now-1M", want: now.Add(-30 * 24 * time.Hour), isToken: true},
		// Offsets beyond time.Duration's ±292 years are token-shaped but
		// unresolvable — an error, never a wrapped date or a literal.
		{in: "now+10000000000s", isToken: true, wantErr: true},
		{in: "now-99999999999999999999s", isToken: true, wantErr: true},
		{in: "", isToken: false},
		{in: "yesterday", isToken: false},
		{in: "now-", isToken: false},
		{in: "now-1", isToken: false},
		{in: "now-7q", isToken: false},
		{in: "2026-01-01T00:00:00Z", isToken: false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, isToken, err := filter.ResolveRelativeTime(c.in, now)
			if isToken != c.isToken {
				t.Fatalf("isToken = %v, want %v", isToken, c.isToken)
			}
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if c.isToken && !c.wantErr && !got.Equal(c.want) {
				t.Errorf("resolved %v, want %v", got, c.want)
			}
		})
	}
}

func TestFilterParser_ResolvesRelativeTokens(t *testing.T) {
	schema := &filter.ResourceSchema{
		Name: "events",
		Fields: map[string]*filter.FieldSchema{
			"created_at": {
				ExternalName:     "created_at",
				SQLExpr:          "events.created_at",
				DataType:         core.FieldTypeTimestamp,
				AllowedOperators: []filter.FilterOperator{filter.OpGte, filter.OpLte, filter.OpEq},
			},
		},
	}

	frozen := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cfg := core.DefaultConfig()
	cfg.Clock = func() time.Time { return frozen }
	p := filter.NewFilterParser(schema, cfg)

	t.Run("gte token resolves to absolute timestamp in AST", func(t *testing.T) {
		ast, err := p.Parse(`{"created_at": {"$gte": "now-7d"}}`)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		// The AST holds the resolved canonical string; the SQL builder sees an
		// absolute value and never has to know about clocks.
		expected := core.FormatTimestamp(frozen.Add(-7 * 24 * time.Hour))
		got := findValue(t, ast.Root)
		if got != expected {
			t.Errorf("created_at >= got %v, want %v", got, expected)
		}
	})

	t.Run("eq token resolves at parse time", func(t *testing.T) {
		ast, err := p.Parse(`{"created_at": "now"}`)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		expected := core.FormatTimestamp(frozen)
		got := findValue(t, ast.Root)
		if got != expected {
			t.Errorf("created_at = got %v, want %v", got, expected)
		}
	})

	t.Run("rfc3339 string normalizes to the canonical form", func(t *testing.T) {
		ast, err := p.Parse(`{"created_at": {"$gte": "2026-04-01T02:00:00+02:00"}}`)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		got := findValue(t, ast.Root)
		if got != "2026-04-01T00:00:00.000000000Z" {
			t.Errorf("got %v, want the fixed-width UTC form", got)
		}
	})

	t.Run("in members resolve like scalars", func(t *testing.T) {
		_, err := p.Parse(`{"created_at": {"$in": ["now-7d", "2026-04-01T02:00:00+02:00"]}}`)
		if err == nil {
			t.Fatal("created_at does not allow $in; expected an operator error")
		}
		wide := *schema.Fields["created_at"]
		wide.AllowedOperators = append(wide.AllowedOperators, filter.OpIn)
		wp := filter.NewFilterParser(&filter.ResourceSchema{Name: "events", Fields: map[string]*filter.FieldSchema{"created_at": &wide}}, cfg)
		ast, err := wp.Parse(`{"created_at": {"$in": ["now-7d", "2026-04-01T02:00:00+02:00"]}}`)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		want := []any{core.FormatTimestamp(frozen.Add(-7 * 24 * time.Hour)), "2026-04-01T00:00:00.000000000Z"}
		got, ok := ast.Root.Value.([]any)
		if !ok {
			t.Fatalf("$in value = %T, want []any", ast.Root.Value)
		}
		if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("$in members = %v, want %v", got, want)
		}
	})

	t.Run("overflowing token rejected, never passed through", func(t *testing.T) {
		_, err := p.Parse(`{"created_at": {"$gte": "now+99999999999999999999s"}}`)
		var pe *core.ParseError
		if !errors.As(err, &pe) || pe.Code != filter.ECodeValueTypeMismatch.String() {
			t.Fatalf("err = %v, want %s", err, filter.ECodeValueTypeMismatch.String())
		}
	})

	t.Run("invalid token rejected with helpful error", func(t *testing.T) {
		_, err := p.Parse(`{"created_at": "yesterday"}`)
		if err == nil {
			t.Fatal("expected error for non-token non-RFC3339 string")
		}
		if !strings.Contains(err.Error(), "RFC 3339 or a relative token") {
			t.Errorf("error message should mention relative token; got: %v", err)
		}
	})
}

func findValue(t *testing.T, n *filter.FilterNode) string {
	t.Helper()
	if n == nil {
		t.Fatal("nil filter node")
	}
	if n.Value != nil {
		s, ok := n.Value.(string)
		if !ok {
			t.Fatalf("expected string value, got %T", n.Value)
		}
		return s
	}
	for _, c := range n.Children {
		if v := findValue(t, c); v != "" {
			return v
		}
	}
	return ""
}

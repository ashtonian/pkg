package path_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/gowalker/path"
)

func TestTextPaths(t *testing.T) {
	for _, text := range []string{"", "Field", "Servers[\"primary.eu\"].Ports[0]", "[\"key\"].Name", "[0][1]", "Δείγμα.名前", "M[\"quote\\\"and\\\\slash\"]", "[\"\"]"} {
		t.Run(text, func(t *testing.T) {
			p, err := path.Parse(text)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := p.MarshalText()
			if err != nil || string(encoded) != text {
				t.Fatalf("got %q, %v", encoded, err)
			}
		})
	}
	for _, text := range []string{".", ".A", "A.", "A..B", "A.[0]", "[", "[]", "[-1]", "[+1]", "[1x]", "[99999999999999999999999999999]", "['a']", "[\"a\"", "[\"a]", "[\"\\q\"]", "[\"a\"]B", "[0].", "[0]x", "A B", " A", "A ]", "[\"unterminated\\"} {
		t.Run(text, func(t *testing.T) {
			if _, err := path.Parse(text); !errors.Is(err, path.ErrSyntax) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := path.Parse(strings.Repeat("a", path.MaxTextBytes+1)); !errors.Is(err, path.ErrLimit) {
		t.Fatal(err)
	}
	if _, err := path.Parse("[\"\xff\"]"); !errors.Is(err, path.ErrSyntax) {
		t.Fatal(err)
	}
}

func TestTypedPathsAreImmutableAndLossless(t *testing.T) {
	type key string
	for _, p := range []path.Path{path.Of(path.Key(1)), path.Of(path.Key(key("x"))), path.Of(path.Deref()), path.Of(path.Field("a.b")), path.Of(path.Index(-1)), path.Of(path.Step{})} {
		if _, err := p.MarshalText(); !errors.Is(err, path.ErrUnrepresentable) {
			t.Fatal(err)
		}
	}
	steps := []path.Step{path.Field("A")}
	p := path.Of(steps...)
	steps[0] = path.Field("B")
	exposed := p.Steps()
	exposed[0] = path.Field("C")
	child := p.Append(path.Index(0))
	if p.Len() != 1 || child.Len() != 2 || p.Steps()[0].Name() != "A" {
		t.Fatal("path was mutated")
	}
}

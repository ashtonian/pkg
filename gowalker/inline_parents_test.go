package gowalker_test

import (
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

type selectorInner struct {
	ID int
}

type selectorMid struct {
	selectorInner
}

type selectorSection struct {
	Port int `col:"port"`
}

type selectorRow struct {
	selectorMid
	Name    string
	Section selectorSection `col:"section,inline"`
}

// lookup is the root field of s named name, failing the test without one.
func lookup(t *testing.T, s *gowalker.Schema, name string) *gowalker.Field {
	t.Helper()
	f, ok := s.Root().Lookup(name)
	if !ok {
		t.Fatalf("%s has no field %q", s.Type(), name)
	}
	return f
}

func TestFieldGoSelectorNamesTheInlineParents(t *testing.T) {
	json := schemaFor[selectorRow](t, engine(t, gowalker.WithJSONFields("json")))
	assert.Equal(t, "selectorMid.selectorInner.ID", lookup(t, json, "ID").GoSelector())
	assert.Equal(t, "Name", lookup(t, json, "Name").GoSelector())

	// A resolver may inline a named field: its name is a selector step too.
	inlined := schemaFor[selectorRow](t, engine(t, gowalker.WithFieldResolver(tagged("col"))))
	assert.Equal(t, "Section.Port", lookup(t, inlined, "port").GoSelector())
}

//go:build unit

package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type enumSettings struct {
	Mode  string   `json:"mode"  parse:"filter" enum:"a,b"`
	Kinds []string `json:"kinds" parse:"filter" enum:"x,y"`
}

type enumAccount struct {
	ID       string       `json:"id"       db:"id"       parse:"filter,sort"`
	Terms    string       `json:"terms"    db:"terms"    parse:"filter" enum:"'net 30, eom',' padded',due"`
	Settings enumSettings `json:"settings" db:"settings" parse:"filter"`
}

// A value holding a comma or edge spaces is declared in quotes and
// round-trips through the tag, request validation and the schema document;
// a struct member's enum holds on its path as a column's does.
func TestEnum_ValuesRoundTrip(t *testing.T) {
	accounts := parse.MustResource[enumAccount]("enum_accounts")
	for _, ok := range []string{`{"terms":"net 30, eom"}`, `{"terms":" padded"}`, `{"terms":"due"}`, `{"settings.mode":"a"}`, `{"settings.kinds":{"$contains":["y"]}}`} {
		_, err := accounts.ParseFilter(ok)
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{`{"terms":"net 30"}`, `{"terms":"padded"}`, `{"settings.mode":"zzz"}`, `{"settings.kinds":{"$contains":["z"]}}`} {
		_, err := accounts.ParseFilter(bad)
		pe, ok := errors.AsType[*parse.ParseError](err)
		require.True(t, ok, "%s: %v", bad, err)
		assert.Equal(t, filter.ECodeInvalidEnumValue.String(), pe.Code, bad)
	}

	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, accounts))
	doc := reg.SchemaFor("enum_accounts")
	require.NotNil(t, doc)
	assert.Equal(t, []string{"net 30, eom", " padded", "due"}, doc.Filter.Fields["terms"].EnumValues)
	assert.Equal(t, []string{"a", "b"}, doc.Filter.Fields["settings.mode"].EnumValues)
}

// An enum is text: on a field whose values are not strings it would never
// be checked, so generation fails, naming the field.
func TestEnum_OnlyStringsAndArraysOfStrings(t *testing.T) {
	type intEnum struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Rank int    `json:"rank" db:"rank" parse:"filter" enum:"1,2"`
	}
	type unfilteredIntEnum struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Rank int    `json:"rank" db:"rank" enum:"1,2"`
	}
	type intMember struct {
		Level int `json:"level" parse:"filter" enum:"1,2"`
	}
	type memberEnum struct {
		ID       string    `json:"id"       db:"id"       parse:"filter"`
		Settings intMember `json:"settings" db:"settings" parse:"filter"`
	}
	type badQuote struct {
		ID   string `json:"id"   db:"id"   parse:"filter"`
		Mode string `json:"mode" db:"mode" parse:"filter" enum:"'a"`
	}
	for name, tc := range map[string]struct {
		build func() error
		want  string
	}{
		"an int field":         {func() error { _, err := parse.NewResource[intEnum]("t"); return err }, "field Rank: enum on a int field"},
		"an unfiltered int":    {func() error { _, err := parse.NewResource[unfilteredIntEnum]("t"); return err }, "field Rank: enum on a int field"},
		"an int member":        {func() error { _, err := parse.NewResource[memberEnum]("t"); return err }, "field Settings: path level: enum on a int field"},
		"an unterminated tick": {func() error { _, err := parse.NewResource[badQuote]("t"); return err }, `field Mode: enum:"'a": value 1: unterminated quote`},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorContains(t, tc.build(), tc.want)
		})
	}
}

//go:build unit

package parseprovider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

// compiled is a Database on a compile-only provider.
type compiled struct{ provider *parse.Provider }

func (c compiled) Provider() *parse.Provider                            { return c.provider }
func (compiled) Ping(context.Context) error                             { return nil }
func (compiled) Close() error                                           { return nil }
func (c compiled) open(context.Context) (parseprovider.Database, error) { return c, nil }

// choice is a provider named name that opens a compile-only provider for d.
func choice(name string, d filter.Dialect) parseprovider.Choice {
	c := parseprovider.Choice{Name: name, Open: compiled{providertest.Compile(d)}.open}
	return c
}

func TestOpen_OpensTheNamedChoice(t *testing.T) {
	opened, err := parseprovider.Open(t.Context(), "postgres", choice("sqlite", filter.SQLiteDialect{}), choice("postgres", filter.PostgresDialect{}))
	require.NoError(t, err)
	assert.Equal(t, "postgres", opened.Provider().Name())
}

// Open hands back the chosen provider's error as it is.
func TestOpen_ReturnsTheChoicesError(t *testing.T) {
	refused := errors.New("refused")
	failing := parseprovider.Choice{Name: "sqlite", Open: func(context.Context) (parseprovider.Database, error) { return nil, refused }}
	opened, err := parseprovider.Open(t.Context(), "sqlite", failing)
	require.ErrorIs(t, err, refused)
	assert.Nil(t, opened)
}

func TestOpen_Refuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		chosen  string
		choices []parseprovider.Choice
		want    string
	}{
		{"a name the binary does not link", "mysql", []parseprovider.Choice{choice("sqlite", filter.SQLiteDialect{}), choice("postgres", filter.PostgresDialect{})}, `parseprovider: provider: "mysql": this binary links postgres, sqlite`},
		{"an empty name", "", []parseprovider.Choice{choice("sqlite", filter.SQLiteDialect{})}, `parseprovider: provider: "": this binary links sqlite`},
		{"no choice", "sqlite", nil, "parseprovider: no provider to choose from"},
		{"a choice given twice", "sqlite", []parseprovider.Choice{choice("sqlite", filter.SQLiteDialect{}), choice("sqlite", filter.SQLiteDialect{})}, `parseprovider: choice "sqlite": given twice`},
		{"a choice without a name", "sqlite", []parseprovider.Choice{choice("", filter.SQLiteDialect{})}, "parseprovider: choice 0: no name"},
		{"a choice without Open", "sqlite", []parseprovider.Choice{{Name: "sqlite"}}, `parseprovider: choice "sqlite": no Open`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := parseprovider.Open(t.Context(), tc.chosen, tc.choices...)
			require.EqualError(t, err, tc.want)
			assert.Nil(t, opened)
		})
	}
}

//go:build unit

package parsepgx_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

type note struct {
	ID   string `json:"id"   db:"id"   parse:"filter,sort"`
	Body string `json:"body" db:"body" parse:"filter,patch"`
}

// A plan compiled for another dialect is refused before any statement
// runs: its placeholders and expressions are not PostgreSQL's.
func TestRefusesAnotherDialect(t *testing.T) {
	notes := parse.MustResource[note]("notes")
	list, err := notes.BuildList(filter.SQLiteDialect{}, nil, parse.Scope{})
	require.NoError(t, err)
	_, err = parsepgx.QueryPage(context.Background(), nil, notes, list)
	assert.ErrorIs(t, err, parsepgx.ErrDialect)

	p, err := notes.ParseMergePatch([]byte(`{"body":"x"}`))
	require.NoError(t, err)
	upd, err := notes.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("n1"), parse.Scope{})
	require.NoError(t, err)
	_, err = parsepgx.Update(context.Background(), nil, notes, upd)
	assert.ErrorIs(t, err, parsepgx.ErrDialect)

	_, err = parsepgx.QueryPage(context.Background(), nil, notes, nil)
	assert.ErrorContains(t, err, "nil list plan")
	_, err = parsepgx.Update(context.Background(), nil, notes, nil)
	assert.ErrorContains(t, err, "nil update plan")
}

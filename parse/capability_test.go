//go:build unit

package parse_test

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/providertest"
)

type bareNote struct {
	ID        int64     `json:"id"         db:"id"         parse:"filter,sort"`
	Title     string    `json:"title"      db:"title"      parse:"filter,search,patch,facet" facet:"terms"`
	Slug      string    `json:"slug"       db:"slug"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// requireUnsupported asserts err is the bare provider's refusal of
// capability for the notes resource, before any request reached its
// executor.
func requireUnsupported(t *testing.T, err error, capability string) {
	t.Helper()
	require.ErrorIs(t, err, parse.ErrUnsupported)
	require.NotErrorIs(t, err, providertest.ErrNoDatabase)
	unsupported, ok := errors.AsType[*parse.UnsupportedError](err)
	require.True(t, ok)
	assert.Equal(t, "bare", unsupported.Provider)
	assert.Equal(t, capability, unsupported.Capability)
	require.EqualError(t, err, "parse: notes: the bare provider does not support "+capability)
}

// A provider that compiles no SQL and supports none of what a provider may
// lack refuses each such operation before its executor sees a request, and
// hands every other request to the executor: the bare provider's fails
// with ErrNoDatabase.
func TestCapabilities_RefusedBeforeTheExecutor(t *testing.T) {
	bare := providertest.Bare("bare")

	_, err := parse.NewResource[bareNote](bare, "notes", parse.ReadExpr("slug", parse.SQL("lower(title)")))
	requireUnsupported(t, err, "computed columns")

	notes, err := parse.NewResource[bareNote](bare, "notes")
	require.NoError(t, err)
	assert.Nil(t, notes.Columns(), "no projection without a dialect")
	patch, err := notes.ParseMergePatch([]byte(`{"title":"x"}`))
	require.NoError(t, err)
	search, err := notes.ParseList(url.Values{"search": {"x"}})
	require.NoError(t, err)
	facets, err := notes.ParseFacets(url.Values{"facets": {"title"}})
	require.NoError(t, err)

	t.Run("operations", func(t *testing.T) {
		ctx := t.Context()
		_, err := notes.Query(ctx, parse.Statement{SQL: "SELECT 1"})
		requireUnsupported(t, err, "Query")
		_, err = notes.List(ctx, search, parse.Scope{})
		requireUnsupported(t, err, "search")
		_, err = notes.Facets(ctx, search, parse.Scope{})
		requireUnsupported(t, err, "search")
		_, err = notes.Update(ctx, patch, parse.ByID(int64(1)), parse.Scope{}, parse.StampSQL("updated_at", "CURRENT_TIMESTAMP"))
		requireUnsupported(t, err, "StampSQL")
		_, err = notes.UpdateBatch(ctx, []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{}, parse.StampSQL("updated_at", "CURRENT_TIMESTAMP"))
		requireUnsupported(t, err, "StampSQL")
	})

	t.Run("SQL builders", func(t *testing.T) {
		_, err := notes.BuildList(nil, parse.Scope{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.BuildFacets(facets, parse.Scope{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.IncludeQueries(&parse.ListPlan{}, []parse.Key{{int64(1)}})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.Page(nil, &parse.ListPlan{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.PagePointers(nil, &parse.ListPlan{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.ListQuery()
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.BuildUpdate(patch, parse.ByID(int64(1)), parse.Scope{})
		requireUnsupported(t, err, "SQL builders")
		_, err = notes.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{})
		requireUnsupported(t, err, "SQL builders")
	})

	t.Run("requests the executor runs", func(t *testing.T) {
		ctx := t.Context()
		_, err := notes.List(ctx, nil, parse.Scope{})
		require.ErrorIs(t, err, providertest.ErrNoDatabase)
		_, err = notes.Facets(ctx, facets, parse.Scope{})
		require.ErrorIs(t, err, providertest.ErrNoDatabase)
		_, err = notes.Update(ctx, patch, parse.ByID(int64(1)), parse.Scope{}, parse.Stamp("updated_at", time.Unix(1, 0)))
		require.ErrorIs(t, err, providertest.ErrNoDatabase)
		_, err = notes.UpdateBatch(ctx, []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.ErrorIs(t, err, providertest.ErrNoDatabase)

		scoped, err := notes.In(parse.NewTx(backend.Transaction{Provider: "bare"}))
		require.NoError(t, err)
		_, err = scoped.List(ctx, nil, parse.Scope{})
		require.ErrorIs(t, err, providertest.ErrNoDatabase)
	})
}

// A SQL provider supports every operation a provider may lack.
func TestCapabilities_SQLProvidersSupportEvery(t *testing.T) {
	assert.Equal(t, "SQL builders, Query, search, computed columns, StampSQL", backend.AllCapabilities.String())
	assert.Equal(t, "no capability", backend.Capabilities(0).String())
	notes, err := parse.NewResource[bareNote](onSQLite, "notes", parse.ReadExpr("slug", parse.SQL("lower(title)")))
	require.NoError(t, err)
	_, err = notes.ListQuery()
	require.NoError(t, err)
}

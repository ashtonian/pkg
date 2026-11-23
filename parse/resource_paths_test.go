//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsesql"
	"github.com/ashtonian/pkg/parse/patch"
)

type customItem struct {
	ID     string          `json:"id"     db:"id"     parse:"filter,sort"`
	Custom json.RawMessage `json:"custom" db:"custom" parse:"filter"`
}

// A path sort key's cursor reads its document's row column, which a column
// mapper's computed SQL does not name; the document field's Column does.
func TestResource_WithPathsSortsAMappedDocument(t *testing.T) {
	r, err := parse.NewResource[customItem]("custom_items", mappedColumns)
	require.NoError(t, err)
	view, err := r.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true})
	require.NoError(t, err)
	req, err := view.ParseList(url.Values{filter.ParamSort: {"custom.tier"}})
	require.NoError(t, err)
	plan, err := view.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Keyset.Keys)
	assert.Equal(t, "custom.tier", plan.Keyset.Keys[0].Name)
	assert.Equal(t, "custom", plan.Keyset.Keys[0].Column)
}

// A view is a value for one request: it declares no relation and is no
// relation's target, and the refusal leaves the resource it came from as it
// was.
func TestResource_WithPathsViewRelatesNothing(t *testing.T) {
	type tag struct {
		ID string `json:"id" db:"id" parse:"filter"`
	}
	tags := parse.MustResource[tag]("tags")
	items := parse.MustResource[customItem]("custom_items")
	itemView, err := items.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true})
	require.NoError(t, err)
	tagView, err := tags.WithPaths()
	require.NoError(t, err)
	through := parse.Through("item_tags", "item_id", "tag_id")
	shape := func() (filters, sorts, patches []string) {
		return slices.Sorted(maps.Keys(items.FilterSchema().Fields)),
			slices.Sorted(maps.Keys(items.SortSchema().Fields)),
			pathNames(items.PatchSchema())
	}
	filters, sorts, patches := shape()

	err = itemView.Relate("tags", parse.ManyToMany(tags, through, parse.Writable()))
	require.ErrorContains(t, err, `parse: custom_items: relation "tags": a WithPaths view declares no relations`)
	err = items.Relate("tags", parse.ManyToMany(tagView, through, parse.Writable()))
	require.ErrorContains(t, err, `parse: custom_items: relation "tags": the target is a WithPaths view`)

	gotFilters, gotSorts, gotPatches := shape()
	assert.Equal(t, filters, gotFilters)
	assert.Equal(t, sorts, gotSorts)
	assert.Equal(t, patches, gotPatches)
	_, err = items.ParseMergePatch([]byte(`{"tags":["t1"]}`))
	require.Error(t, err)

	require.NoError(t, items.Relate("tags", parse.ManyToMany(tags, through, parse.Writable())))
	_, err = items.ParseMergePatch([]byte(`{"tags":["t1"]}`))
	require.NoError(t, err)
}

// A request declares its tenant's custom fields as paths on a view of the
// resource; the shared resource never learns them.
func TestResource_WithPathsIsARequestView(t *testing.T) {
	r, err := parse.NewResource[customItem]("custom_items", parse.PageSize(10, 10), parse.AcceptParams("facets"))
	require.NoError(t, err)
	view, err := r.WithPaths(filter.PathDecl{Path: "custom.tier", Type: core.FieldTypeString, Sort: true, Facet: &filter.FacetSchema{Type: filter.FacetTerms}})
	require.NoError(t, err)

	q := url.Values{"filter": {`{"custom.tier":{"$ne":"bronze"}}`}, "sort": {"-custom.tier"}, "facets": {"custom.tier"}}
	_, err = r.ParseList(q)
	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "the shared resource does not know the path: %v", err)
	assert.Equal(t, "E2071", pe.Code)

	req, err := view.ParseList(q)
	require.NoError(t, err)
	plan, err := view.BuildList(filter.SQLiteDialect{}, req, parse.Scope{})
	require.NoError(t, err)

	db := dbtest.SQLite(t)
	_, err = db.Exec(`CREATE TABLE custom_items (id TEXT PRIMARY KEY, custom TEXT)`)
	require.NoError(t, err)
	for id, doc := range map[string]any{"a": `{"tier":"gold"}`, "b": `{"tier":"silver"}`, "c": `{"tier":"bronze"}`, "d": `{}`, "e": nil} {
		_, insertErr := db.Exec(`INSERT INTO custom_items VALUES (?1, ?2)`, id, doc)
		require.NoError(t, insertErr)
	}
	page, err := parsesql.QueryPage(t.Context(), db, view, plan)
	require.NoError(t, err)
	var ids []string
	for _, it := range page.Items {
		ids = append(ids, it.ID)
	}
	assert.Equal(t, []string{"b", "a"}, ids, "silver then gold; bronze filtered out, absent tiers never match $ne")

	require.NotNil(t, plan.Facets)
	require.Len(t, page.Facets, 1)
	assert.ElementsMatch(t, []filter.FacetBucket{{Key: "gold", Count: 1}, {Key: "silver", Count: 1}}, page.Facets["custom.tier"])

	_, err = r.WithPaths(filter.PathDecl{Path: "id.x", Type: core.FieldTypeString})
	assert.Error(t, err, "a path needs a document field")
}

func pathNames(s *patch.PatchSchema) []string {
	var out []string
	for path := range s.Paths() {
		out = append(out, path)
	}
	return out
}

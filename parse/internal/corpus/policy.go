//go:build integration

package corpus

import (
	"database/sql"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/patch"
)

func policyServerFields(t *testing.T, engine Engine) {
	runOn(t, engine, runPolicyServerFields)
}

func runPolicyServerFields(t *testing.T, b sqlBackend) {
	p, db := b.open(t)
	integer := func(name string) sqlColumn { return column[int64](name).required() }
	text := func(name string) sqlColumn { return column[string](name).required() }
	b.load(t, db, sqlTable{name: "documents", key: []string{"id"}, columns: []sqlColumn{integer("id"), integer("owner_id"), text("region_code"), text("title"), integer("folder_id")}},
		[][]any{{1, 7, "eu", "alpha", 1}, {2, 7, "us", "beta", 1}, {3, 8, "eu", "alpha", 2}, {4, 7, "eu", "gamma", 2}})
	docs := parsetest.ServerFieldDocuments(t, p)
	req, err := docs.ParseList(url.Values{filter.ParamIncludeTotal: {"true"}, filter.ParamFacets: {"title"}})
	require.NoError(t, err)
	page := listPage(t, docs, req, parsetest.OwnerEUPolicy())

	assert.Equal(t, []int{1, 4}, parsetest.ItemIDs(page.Items, func(d parsetest.ServerFieldDocument) int { return d.ID }))
	require.NotNil(t, page.PageInfo.TotalCount)
	assert.Equal(t, int64(2), *page.PageInfo.TotalCount)
	counts := map[string]int64{}
	for _, bucket := range page.Facets["title"] {
		counts[bucket.Key] = bucket.Count
	}
	assert.Equal(t, map[string]int64{"alpha": 1, "gamma": 1}, counts, "the facet counts the policy's population")
}

// A raw policy fragment whose top level is an OR, under every statement
// that ANDs it with something: the tenant floor, a user filter, a cursor, a
// search, an update's target and a relation's correlation. Each must hold
// for the whole fragment, not its first disjunct.

type rawPolicyNote struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	OwnerID  string `json:"owner_id"  db:"owner_id"  parse:"filter"`
	Public   bool   `json:"public"    db:"public"    parse:"filter"`
	Title    string `json:"title"     db:"title"     parse:"filter,search,patch"`
}

type rawPolicyLabel struct {
	ID    string `json:"id"    db:"id"    parse:"filter,sort"`
	Level string `json:"level" db:"level" parse:"filter"`
	Open  bool   `json:"open"  db:"open"  parse:"filter"`
}

func rawPolicyNotes(t *testing.T, p *parse.Provider) *parse.Resource[rawPolicyNote] {
	t.Helper()
	notes, err := parse.NewResource[rawPolicyNote](p, "notes", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	labels, err := parse.NewResource[rawPolicyLabel](p, "labels")
	require.NoError(t, err)
	require.NoError(t, notes.Relate("labels", parse.ManyToMany(labels, parse.Through("note_labels", "note_id", "label_id"), parse.PolicyScoped(), parse.Indexed())))
	return notes
}

// loadRawPolicy seeds a fresh database of b with t1's notes n1 to n5 and
// t2's n6 and n7, and returns the notes resource on it and the test's own
// handle on it. Under the owner-or-public policy for alice, t1 admits n1,
// n2 and n5; the search "alpha" drops n4 and the user filter drops n5.
// Labels visible under the label policy are high-closed (its level) and
// low-open (open).
func loadRawPolicy(t *testing.T, b sqlBackend) (*parse.Resource[rawPolicyNote], *sql.DB) {
	t.Helper()
	p, db := b.open(t)
	text := func(name string) sqlColumn { return column[string](name).required() }
	b.load(t, db, sqlTable{name: "notes", key: []string{"id"}, columns: []sqlColumn{text("id"), text("tenant_id"), text("owner_id"), column[bool]("public").required(), text("title")}}, [][]any{
		{"n1", "t1", "alice", false, "alpha one"}, {"n2", "t1", "bob", true, "alpha two"}, {"n3", "t1", "bob", false, "alpha three"},
		{"n4", "t1", "alice", false, "beta four"}, {"n5", "t1", "alice", true, "alpha five"},
		{"n6", "t2", "bob", true, "alpha six"}, {"n7", "t2", "alice", false, "alpha seven"},
	})
	b.load(t, db, sqlTable{name: "labels", key: []string{"id"}, columns: []sqlColumn{text("id"), text("level"), column[bool]("open").required()}}, [][]any{
		{"high-closed", "high", false}, {"low-open", "low", true}, {"low-closed", "low", false},
	})
	b.load(t, db, sqlTable{name: "note_labels", key: []string{"note_id", "label_id"}, columns: []sqlColumn{text("note_id"), text("label_id")}}, [][]any{
		{"n1", "low-closed"}, {"n2", "high-closed"}, {"n3", "low-open"}, {"n6", "low-open"},
	})
	return rawPolicyNotes(t, p), db
}

// rawOwnerOrPublic admits alice's notes and public ones.
func rawOwnerOrPublic() *filter.PolicyConstraints {
	policy := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{
		RawSQL: "notes.owner_id = $1 OR notes.public = $2", RawArgs: []any{"alice", true},
	}}}
	return policy
}

func rawPolicyScope() parse.Scope {
	scope := parse.Scope{Tenant: "t1", Policy: rawOwnerOrPublic()}
	return scope
}

// A raw policy fragment whose top level is an OR holds for the whole
// fragment under every statement that ANDs it with something, on each
// backend.
func rawPolicy(t *testing.T, engine Engine) {
	runOn(t, engine, runRawPolicy)
}

// runRawPolicy runs every case on a fresh database of b holding
// loadRawPolicy's rows. The Memory engine evaluates no SQL, so it refuses
// each of them.
func runRawPolicy(t *testing.T, b sqlBackend) {
	t.Helper()
	noteID := func(n rawPolicyNote) string { return n.ID }
	rename := func(t *testing.T, r *parse.Resource[rawPolicyNote]) *patch.ParsedPatch {
		t.Helper()
		parsed, err := r.ParseMergePatch([]byte(`{"title":"changed"}`))
		require.NoError(t, err)
		return parsed
	}
	if b.engine == Memory {
		r, _ := loadRawPolicy(t, b)
		req, err := r.ParseList(url.Values{})
		require.NoError(t, err)
		_, err = r.List(t.Context(), req, rawPolicyScope())
		require.ErrorIs(t, err, parse.ErrUnsupported, "list")
		_, err = r.Update(t.Context(), rename(t, r), parse.ByID("n2"), rawPolicyScope())
		require.ErrorIs(t, err, parse.ErrUnsupported, "update")
		_, err = r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{"n2"}, Patch: rename(t, r)}}, rawPolicyScope(), parse.UpdateBatchLimits{})
		require.ErrorIs(t, err, parse.ErrUnsupported, "update batch")
		related, err := r.ParseList(url.Values{filter.ParamFilter: {`{"labels":{"$some":{}}}`}})
		require.NoError(t, err)
		_, err = r.List(t.Context(), related, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"labels": {Constraints: []*filter.PolicyConstraint{{RawSQL: "labels.level = $1", RawArgs: []any{"high"}}}},
		}})
		require.ErrorIs(t, err, parse.ErrUnsupported, "a raw related policy")
		return
	}
	renamed := func(t *testing.T, db *sql.DB) []string {
		t.Helper()
		return dbtest.QueryRows[string](t, db, "SELECT id FROM notes WHERE title = 'changed' ORDER BY id")
	}

	t.Run("list walks the tenant's admitted rows after each cursor", func(t *testing.T) {
		r, _ := loadRawPolicy(t, b)
		q := url.Values{
			filter.ParamFilter:       {`{"title":{"$ne":"alpha five"}}`},
			filter.ParamSearch:       {"alpha"},
			filter.ParamPageSize:     {"1"},
			filter.ParamIncludeTotal: {"true"},
		}
		var seen []string
		for n := 0; ; n++ {
			require.Less(t, n, 10, "walk did not terminate")
			req, err := r.ParseList(q)
			require.NoError(t, err)
			page := listPage(t, r, req, rawPolicyScope())
			require.NotNil(t, page.PageInfo.TotalCount)
			assert.Equal(t, int64(2), *page.PageInfo.TotalCount)
			seen = append(seen, parsetest.ItemIDs(page.Items, noteID)...)
			if !page.PageInfo.HasNextPage {
				break
			}
			q.Set(filter.ParamCursor, string(page.PageInfo.EndCursor))
		}
		assert.Equal(t, []string{"n1", "n2"}, seen)
	})

	t.Run("update changes only the addressed row of the tenant", func(t *testing.T) {
		for _, tc := range []struct {
			key  string
			want []string
		}{
			{key: "n2", want: []string{"n2"}},
			{key: "n3", want: nil}, // t1, refused by the policy
			{key: "n6", want: nil}, // public, but t2's
		} {
			t.Run(tc.key, func(t *testing.T) {
				r, db := loadRawPolicy(t, b)
				res, err := r.Update(t.Context(), rename(t, r), parse.ByID(tc.key), rawPolicyScope())
				require.NoError(t, err)
				assert.Equal(t, tc.want, parsetest.ItemIDs(res.Changed, noteID))
				assert.Equal(t, tc.want, renamed(t, db))
			})
		}
	})

	t.Run("update batch changes only the addressed row of the tenant", func(t *testing.T) {
		for _, tc := range []struct {
			key     string
			want    []string
			wantErr error
		}{
			{key: "n2", want: []string{"n2"}},
			{key: "n6", want: nil, wantErr: parse.ErrUpdateBatchTargets},
		} {
			t.Run(tc.key, func(t *testing.T) {
				r, db := loadRawPolicy(t, b)
				_, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{tc.key}, Patch: rename(t, r)}}, rawPolicyScope(), parse.UpdateBatchLimits{})
				if tc.wantErr != nil {
					require.ErrorIs(t, err, tc.wantErr)
				} else {
					require.NoError(t, err)
				}
				assert.Equal(t, tc.want, renamed(t, db))

				// The batch refuses a target its Current does not read before
				// it writes; its Owner statement holds the scope by itself
				// too, run alone on a fresh database.
				r, db = loadRawPolicy(t, b)
				plan, err := r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{tc.key}, Patch: rename(t, r)}}, rawPolicyScope(), parse.UpdateBatchLimits{})
				require.NoError(t, err)
				require.Len(t, plan.Chunks, 1)
				owner := plan.Chunks[0].Owner
				require.NotNil(t, owner)
				dbtest.QueryRows[[]any](t, db, owner.SQL, owner.Args...)
				assert.Equal(t, tc.want, renamed(t, db), owner.SQL)
			})
		}
	})

	t.Run("relation filter under a raw related policy needs a visible related row", func(t *testing.T) {
		r, _ := loadRawPolicy(t, b)
		req, err := r.ParseList(url.Values{filter.ParamFilter: {`{"labels":{"$some":{}}}`}, filter.ParamPageSize: {"50"}})
		require.NoError(t, err)
		page := listPage(t, r, req, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"labels": {Constraints: []*filter.PolicyConstraint{{RawSQL: "labels.level = $1 OR labels.open = $2", RawArgs: []any{"high", true}}}},
		}})
		assert.Equal(t, []string{"n2", "n3"}, parsetest.ItemIDs(page.Items, noteID))
	})
}

//go:build integration

package parse_test

import (
	"database/sql"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

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

func rawPolicyNotes(t *testing.T) *parse.Resource[rawPolicyNote] {
	t.Helper()
	notes, err := parse.NewResource[rawPolicyNote]("notes", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	labels, err := parse.NewResource[rawPolicyLabel]("labels")
	require.NoError(t, err)
	require.NoError(t, notes.Relate("labels", parse.ManyToMany(labels, parse.Through("note_labels", "note_id", "label_id"), parse.PolicyScoped(), parse.Indexed())))
	return notes
}

// loadRawPolicy seeds a fresh database of b with t1's notes n1 to n5 and
// t2's n6 and n7. Under the
// owner-or-public policy for alice, t1 admits n1, n2 and n5; the search
// "alpha" drops n4 and the user filter drops n5. Labels visible under the
// label policy are high-closed (its level) and low-open (open).
func loadRawPolicy(t *testing.T, b sqlBackend) *sql.DB {
	t.Helper()
	db := b.open(t)
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
	return db
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
func TestRawPolicy(t *testing.T) {
	runSQLBackends(t, runRawPolicy)
}

// runRawPolicy runs every case on a fresh database of b holding
// loadRawPolicy's rows.
func runRawPolicy(t *testing.T, b sqlBackend) {
	t.Helper()
	r := rawPolicyNotes(t)
	d := b.dialect
	seeded := func(t *testing.T) *sql.DB { return loadRawPolicy(t, b) }

	t.Run("list walks the tenant's admitted rows after each cursor", func(t *testing.T) {
		db := seeded(t)
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
			plan, err := r.BuildList(d, req, rawPolicyScope())
			require.NoError(t, err)
			ids := queryRows[string](t, db, "SELECT id FROM notes "+plan.Fragment, plan.Page.Args...)
			assert.Equal(t, []int64{2}, queryRows[int64](t, db, plan.Count.SQL, plan.Count.Args...), plan.Count.SQL)
			items := make([]rawPolicyNote, len(ids))
			for i, id := range ids {
				items[i] = rawPolicyNote{ID: id}
			}
			page, err := r.Page(items, plan)
			require.NoError(t, err)
			for _, it := range page.Items {
				seen = append(seen, it.ID)
			}
			if !page.PageInfo.HasNextPage {
				break
			}
			q.Set(filter.ParamCursor, string(page.PageInfo.EndCursor))
		}
		assert.Equal(t, []string{"n1", "n2"}, seen)
	})

	t.Run("update changes only the addressed row of the tenant", func(t *testing.T) {
		p, err := r.ParseMergePatch([]byte(`{"title":"changed"}`))
		require.NoError(t, err)
		for _, tc := range []struct {
			key  string
			want []string
		}{
			{key: "n2", want: []string{"n2"}},
			{key: "n3", want: nil}, // t1, refused by the policy
			{key: "n6", want: nil}, // public, but t2's
		} {
			t.Run(tc.key, func(t *testing.T) {
				db := seeded(t)
				plan, err := r.BuildUpdate(d, p, parse.ByID(tc.key), rawPolicyScope())
				require.NoError(t, err)
				queryRows[[]any](t, db, plan.Owner.SQL, plan.Owner.Args...)
				assert.Equal(t, tc.want, queryRows[string](t, db, "SELECT id FROM notes WHERE title = 'changed' ORDER BY id"), plan.Owner.SQL)
			})
		}
	})

	t.Run("update batch changes only the addressed row of the tenant", func(t *testing.T) {
		p, err := r.ParseMergePatch([]byte(`{"title":"changed"}`))
		require.NoError(t, err)
		for _, tc := range []struct {
			key  string
			want []string
		}{
			{key: "n2", want: []string{"n2"}},
			{key: "n6", want: nil},
		} {
			t.Run(tc.key, func(t *testing.T) {
				db := seeded(t)
				plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{{Key: parse.Key{tc.key}, Patch: p}}, rawPolicyScope(), parse.UpdateBatchLimits{})
				require.NoError(t, err)
				require.Len(t, plan.Chunks, 1)
				chunk := plan.Chunks[0]
				current := itemIDs(queryRows[[]any](t, db, chunk.Current.SQL, chunk.Current.Args...), func(row []any) parse.Key {
					key := parse.Key{row[0]}
					return key
				})
				if tc.want == nil {
					require.ErrorIs(t, chunk.CheckCurrent(current), parse.ErrUpdateBatchTargets)
				} else {
					require.NoError(t, chunk.CheckCurrent(current))
				}
				// The statement holds the scope itself, whatever its caller
				// made of Current.
				require.NotNil(t, chunk.Owner)
				queryRows[[]any](t, db, chunk.Owner.SQL, chunk.Owner.Args...)
				assert.Equal(t, tc.want, queryRows[string](t, db, "SELECT id FROM notes WHERE title = 'changed' ORDER BY id"), chunk.Owner.SQL)
			})
		}
	})

	t.Run("relation filter under a raw related policy needs a visible related row", func(t *testing.T) {
		db := seeded(t)
		req, err := r.ParseList(url.Values{filter.ParamFilter: {`{"labels":{"$some":{}}}`}, filter.ParamPageSize: {"50"}})
		require.NoError(t, err)
		plan, err := r.BuildList(d, req, parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
			"labels": {Constraints: []*filter.PolicyConstraint{{RawSQL: "labels.level = $1 OR labels.open = $2", RawArgs: []any{"high", true}}}},
		}})
		require.NoError(t, err)
		assert.Equal(t, []string{"n2", "n3"}, queryRows[string](t, db, "SELECT id FROM notes "+plan.Fragment, plan.Page.Args...), plan.Fragment)
	})
}

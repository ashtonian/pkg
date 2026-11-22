//go:build unit || integration

package parse_test

import (
	"database/sql"
	"net/url"
	"strconv"
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

// rawPolicyFixtureSQL seeds t1's notes n1 to n5 and t2's n6 and n7. Under
// the owner-or-public policy for alice, t1 admits n1, n2 and n5; the search
// "alpha" drops n4 and the user filter drops n5. Labels visible under the
// label policy are high-closed (its level) and low-open (open).
var rawPolicyFixtureSQL = []string{
	`CREATE TABLE notes (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, owner_id TEXT NOT NULL, public BOOLEAN NOT NULL, title TEXT NOT NULL)`,
	`CREATE TABLE labels (id TEXT PRIMARY KEY, level TEXT NOT NULL, open BOOLEAN NOT NULL)`,
	`CREATE TABLE note_labels (note_id TEXT NOT NULL, label_id TEXT NOT NULL, PRIMARY KEY (note_id, label_id))`,
	`INSERT INTO notes VALUES
		('n1','t1','alice',false,'alpha one'), ('n2','t1','bob',true,'alpha two'), ('n3','t1','bob',false,'alpha three'),
		('n4','t1','alice',false,'beta four'), ('n5','t1','alice',true,'alpha five'),
		('n6','t2','bob',true,'alpha six'), ('n7','t2','alice',false,'alpha seven')`,
	`INSERT INTO labels VALUES ('high-closed','high',false), ('low-open','low',true), ('low-closed','low',false)`,
	`INSERT INTO note_labels VALUES ('n1','low-closed'), ('n2','high-closed'), ('n3','low-open'), ('n6','low-open')`,
}

// rawOwnerOrPublic admits alice's notes and public ones.
func rawOwnerOrPublic() *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{
		RawSQL: "notes.owner_id = $1 OR notes.public = $2", RawArgs: []any{"alice", true},
	}}}
}

func rawPolicyScope() parse.Scope {
	return parse.Scope{Tenant: "t1", Policy: rawOwnerOrPublic()}
}

// runRawPolicy runs every case on a fresh database from open, seeded with
// rawPolicyFixtureSQL.
func runRawPolicy(t *testing.T, d parse.Dialect, open func(t *testing.T) *sql.DB) {
	t.Helper()
	r := rawPolicyNotes(t)
	seeded := func(t *testing.T) *sql.DB {
		db := open(t)
		for _, stmt := range rawPolicyFixtureSQL {
			_, err := db.Exec(stmt)
			require.NoError(t, err, stmt)
		}
		return db
	}

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
			ids := rawPolicyIDs(t, db, "SELECT id FROM notes "+plan.Fragment, plan.Page.Args)
			total := queryRows(t, db, plan.Count.SQL, plan.Count.Args)
			assert.Equal(t, "2", rawPolicyText(total[0][0]), plan.Count.SQL)
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
				queryRows(t, db, plan.Owner.SQL, plan.Owner.Args)
				assert.Equal(t, tc.want, rawPolicyIDs(t, db, "SELECT id FROM notes WHERE title = 'changed' ORDER BY id", nil), plan.Owner.SQL)
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
				plan, err := r.BuildUpdateBatch(d, []parse.RowUpdate{{Key: tc.key, Patch: p}}, rawPolicyScope(), parse.UpdateBatchLimits{})
				require.NoError(t, err)
				require.Len(t, plan.Chunks, 1)
				chunk := plan.Chunks[0]
				var current []any
				for _, row := range queryRows(t, db, chunk.Current.SQL, chunk.Current.Args) {
					current = append(current, rawPolicyText(row[0]))
				}
				if tc.want == nil {
					require.ErrorIs(t, chunk.CheckCurrent(current), parse.ErrUpdateBatchTargets)
				} else {
					require.NoError(t, chunk.CheckCurrent(current))
				}
				// The statement holds the scope itself, whatever its caller
				// made of Current.
				require.NotNil(t, chunk.Owner)
				queryRows(t, db, chunk.Owner.SQL, chunk.Owner.Args)
				assert.Equal(t, tc.want, rawPolicyIDs(t, db, "SELECT id FROM notes WHERE title = 'changed' ORDER BY id", nil), chunk.Owner.SQL)
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
		assert.Equal(t, []string{"n2", "n3"}, rawPolicyIDs(t, db, "SELECT id FROM notes "+plan.Fragment, plan.Page.Args), plan.Fragment)
	})
}

func rawPolicyIDs(t *testing.T, db *sql.DB, q string, args []any) []string {
	t.Helper()
	var ids []string
	for _, row := range queryRows(t, db, q, args) {
		ids = append(ids, rawPolicyText(row[0]))
	}
	return ids
}

// rawPolicyText is a scanned id or count as text: drivers return strings,
// byte slices or integers.
func rawPolicyText(v any) string {
	switch v := v.(type) {
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case string:
		return v
	}
	return ""
}

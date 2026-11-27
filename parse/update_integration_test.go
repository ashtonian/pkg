//go:build integration

package parse_test

import (
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/patch"
)

func gadgetTable() sqlTable {
	table := sqlTable{name: "gadgets", key: []string{"id"}, columns: []sqlColumn{
		column[string]("id"), column[string]("tenant_id").required(), column[string]("name"), column[int64]("rank"),
		column[int64]("version").required(), column[time.Time]("updated_at"), column[time.Time]("deleted_at"),
	}}
	return table
}

func gadgetTagTable() sqlTable {
	table := sqlTable{name: "gadget_tags", columns: []sqlColumn{
		column[string]("gadget_id").required(), column[string]("tag_id").required(), column[string]("tenant_id").required(),
	}}
	return table
}

// loadGadgets seeds a fresh database of b: t1 holds g1, g2 and a deleted
// g3, t2 holds h1. Tags pair in each row's tenant.
func loadGadgets(t *testing.T, b sqlBackend) *sql.DB {
	t.Helper()
	db := b.open(t)
	b.load(t, db, gadgetTable(), [][]any{
		{"g1", "t1", "one", 1, 1, nil, nil},
		{"g2", "t1", "two", 1, 1, nil, nil},
		{"g3", "t1", "three", 1, 1, nil, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"h1", "t2", "other", 1, 1, nil, nil},
	})
	b.load(t, db, gadgetTagTable(), [][]any{{"g1", "red", "t1"}, {"h1", "red", "t2"}})
	return db
}

func gadgetID(g gadget) string { return g.ID }

func gadgetNames(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range queryRows[[]any](t, db, `SELECT id, name FROM gadgets`) {
		id, idOK := row[0].(string)
		name, nameOK := row[1].(string)
		require.True(t, idOK && nameOK, "row %v", row)
		out[id] = name
	}
	return out
}

func tagPairs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	return queryRows[string](t, db, `SELECT gadget_id || ':' || tag_id || '@' || tenant_id FROM gadget_tags ORDER BY 1`)
}

func byFilter(t *testing.T, r *parse.Resource[gadget], f string) parse.Target {
	t.Helper()
	pf, err := r.ParseFilter(f)
	require.NoError(t, err)
	return parse.ByFilter(pf)
}

func nameIs(name string) *filter.PolicyConstraints {
	policy := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "name", Operator: filter.OpEq, Value: name}}}
	return policy
}

func TestBuildUpdate_ChangesOnlyTheScopedTargetRows(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"renamed","rank":7}`))
	require.NoError(t, err)
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		for name, tc := range map[string]struct {
			target parse.Target
			scope  parse.Scope
			want   []string
		}{
			"by key":                    {parse.ByID("g1"), parse.Scope{Tenant: "t1"}, []string{"g1"}},
			"by keys":                   {parse.ByIDs("g1", "g2"), parse.Scope{Tenant: "t1"}, []string{"g1", "g2"}},
			"another tenant's key":      {parse.ByID("h1"), parse.Scope{Tenant: "t1"}, nil},
			"a deleted row":             {parse.ByID("g3"), parse.Scope{Tenant: "t1"}, nil},
			"by filter, in its tenant":  {byFilter(t, r, `{"rank":1}`), parse.Scope{Tenant: "t1"}, []string{"g1", "g2"}},
			"policy narrows the target": {parse.ByIDs("g1", "g2"), parse.Scope{Tenant: "t1", Policy: nameIs("two")}, []string{"g2"}},
		} {
			t.Run(name, func(t *testing.T) {
				db := loadGadgets(t, b)
				plan, err := r.BuildUpdate(b.dialect, p, tc.target, tc.scope)
				require.NoError(t, err)
				require.True(t, plan.Writes)
				got := resourceRows(t, db, r, b.dialect, plan.Owner)
				assert.Equal(t, tc.want, sortedIDs(got, gadgetID), "the rows RETURNING reports")
				for _, g := range got {
					assert.Equal(t, "renamed", g.Name)
					assert.Equal(t, int64(7), g.Rank)
				}
				for id, name := range gadgetNames(t, db) {
					if slices.Contains(tc.want, id) {
						assert.Equal(t, "renamed", name, id)
					} else {
						assert.NotEqual(t, "renamed", name, "%s is outside the target or scope", id)
					}
				}
			})
		}
	})
}

// A stamp is the time given, or an expression the database evaluates: its
// clock here.
func TestBuildUpdate_Stamps(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := loadGadgets(t, b)
		plan, err := r.BuildUpdate(b.dialect, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.Stamp("updated_at", at))
		require.NoError(t, err)
		got := resourceRows(t, db, r, b.dialect, plan.Owner)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].UpdatedAt)
		assert.True(t, at.Equal(*got[0].UpdatedAt), "stamped %v", *got[0].UpdatedAt)

		plan, err = r.BuildUpdate(b.dialect, p, parse.ByIDs("g2", "h1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", b.clock()))
		require.NoError(t, err)
		got = resourceRows(t, db, r, b.dialect, plan.Owner)
		require.Len(t, got, 1, "h1 is t2's")
		require.NotNil(t, got[0].UpdatedAt)
		assert.WithinDuration(t, time.Now(), *got[0].UpdatedAt, time.Minute, "the database clock")

		rel, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
		require.NoError(t, err)
		plan, err = r.BuildUpdate(b.dialect, rel, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.Stamp("updated_at", at.Add(time.Hour)))
		require.NoError(t, err)
		got = resourceRows(t, db, r, b.dialect, plan.Owner)
		require.Len(t, got, 1)
		require.NotNil(t, got[0].UpdatedAt)
		assert.True(t, at.Equal(*got[0].UpdatedAt), "a relation change alone writes no column, so it stamps none")
	})
}

type revision struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"    parse:"filter,server"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
}

// An incremented version moves one past each row's own and is returned
// moved; no patch sets it, the server's own included.
func TestBuildUpdate_Increments(t *testing.T) {
	r, err := parse.NewResource[revision]("revisions", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	at := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		b.load(t, db, sqlTable{name: "revisions", key: []string{"id"}, columns: []sqlColumn{
			column[string]("id"), column[string]("tenant_id").required(), column[string]("name"), column[int64]("version").required(), column[time.Time]("updated_at"),
		}}, [][]any{{"r1", "t1", "one", 1, nil}, {"r2", "t1", "two", 1, nil}})
		update := func(t *testing.T, p *patch.ParsedPatch, target parse.Target) []revision {
			t.Helper()
			plan, err := r.BuildUpdate(b.dialect, p, target, parse.Scope{Tenant: "t1"},
				parse.Increment("version"), parse.Stamp("updated_at", at))
			require.NoError(t, err)
			res, err := applyUpdate(t, db, r, b.dialect, plan, false)
			require.NoError(t, err)
			assert.Empty(t, res.Unchanged, "a plain update writes every target")
			return res.Changed
		}

		rename, err := r.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		got := update(t, rename, parse.ByID("r1"))
		require.Len(t, got, 1)
		assert.Equal(t, int64(2), got[0].Version, "RETURNING reports the moved version")
		require.NotNil(t, got[0].UpdatedAt)
		assert.True(t, at.Equal(*got[0].UpdatedAt), "the stamp still applies")

		got = update(t, rename, parse.ByIDs("r1", "r2"))
		require.Len(t, got, 2)
		versions := map[string]int64{got[0].ID: got[0].Version, got[1].ID: got[1].Version}
		assert.Equal(t, map[string]int64{"r1": 3, "r2": 2}, versions, "each row moves one past its own version")

		server, err := r.ServerMergePatchMap(map[string]any{"name": "server", "version": 40})
		require.NoError(t, err)
		got = update(t, server, parse.ByID("r2"))
		require.Len(t, got, 1)
		assert.Equal(t, int64(3), got[0].Version, "the server's own patch does not set the version either")
		assert.Equal(t, "server", got[0].Name)
	})
}

func TestBuildUpdate_RelationsChangeOnlyTheRowsTheOwnerMatched(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"tags":["blue","green"]}`))
	require.NoError(t, err)
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := loadGadgets(t, b)
		plan, err := r.BuildUpdate(b.dialect, p, parse.ByIDs("g1", "h1", "g3"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.True(t, plan.Writes)
		assert.True(t, plan.HasRelations())
		assert.True(t, strings.HasPrefix(plan.Owner.SQL, "SELECT "), "a relation-only patch selects its owners")

		res, err := applyUpdate(t, db, r, b.dialect, plan, false)
		require.NoError(t, err)
		assert.Empty(t, res.Changed)
		assert.Equal(t, []string{"g1"}, sortedIDs(res.Unchanged, gadgetID), "h1 is t2's, g3 is deleted")
		assert.Equal(t, int64(3), res.RelationRows, "red out of g1, blue and green in")
		assert.Equal(t, []string{"g1:blue@t1", "g1:green@t1", "h1:red@t2"}, tagPairs(t, db), "the requested h1 is not the matched one")

		none, err := r.RelationStatements(plan, nil)
		require.NoError(t, err)
		assert.Empty(t, none, "no matched owners, no junction writes")
	})
}

func TestBuildUpdate_ColumnsAndRelationsTogether(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"both","tags":["blue"]}`))
	require.NoError(t, err)
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := loadGadgets(t, b)
		plan, err := r.BuildUpdate(b.dialect, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(plan.Owner.SQL, `UPDATE "gadgets" SET `))
		assert.True(t, plan.HasRelations())
		res, err := applyUpdate(t, db, r, b.dialect, plan, false)
		require.NoError(t, err)
		require.Len(t, res.Changed, 1)
		assert.Equal(t, "both", res.Changed[0].Name)
		assert.Equal(t, "both", gadgetNames(t, db)["g1"])
		assert.Equal(t, []string{"g1:blue@t1", "h1:red@t2"}, tagPairs(t, db))
	})
}

func TestBuildUpdate_AnEmptyPatchIsAScopedRead(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{}`))
	require.NoError(t, err)
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		plan, err := r.BuildUpdate(b.dialect, p, parse.ByIDs("g1", "h1"), parse.Scope{Tenant: "t1"})
		require.NoError(t, err)
		assert.False(t, plan.Writes)
		assert.False(t, plan.HasRelations())
		res, err := applyUpdate(t, loadGadgets(t, b), r, b.dialect, plan, false)
		require.NoError(t, err)
		assert.Empty(t, res.Changed)
		assert.Equal(t, []string{"g1"}, sortedIDs(res.Unchanged, gadgetID))
	})
}

// A relation change's owner statement locks its owner rows until the
// executor's transaction ends, so a second writer waits for them.
// PostgreSQL alone locks rows: SQLite's write transactions serialize.
func TestBuildUpdate_RelationChangeLocksItsOwners(t *testing.T) {
	r := gadgets(t)
	b := sqlBackendOf(postgresEngine)
	db := loadGadgets(t, b)
	ctx := t.Context()
	p, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(b.dialect, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)

	// The executor's transaction, opened by hand so a second writer can run
	// between its owner statement and its relation statements.
	require.NoError(t, withPgx(t, db, func(conn *pgx.Conn) error {
		return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			owners, err := parsepgx.Query(ctx, tx, r, plan.Owner)
			require.NoError(t, err)
			require.Len(t, owners, 1)

			other, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			_, err = other.ExecContext(ctx, `SET LOCAL lock_timeout = '200ms'`)
			require.NoError(t, err)
			_, err = other.ExecContext(ctx, plan.Owner.SQL, plan.Owner.Args...)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr, "a second writer waits for the owner rows")
			assert.Equal(t, "55P03", pgErr.Code, "lock_not_available")
			require.NoError(t, other.Rollback())

			stmts, err := r.RelationStatements(plan, []parse.Key{{owners[0].ID}})
			require.NoError(t, err)
			for _, st := range stmts {
				_, err := tx.Exec(ctx, st.SQL, st.Args...)
				require.NoError(t, err, st.SQL)
			}
			return nil
		})
	}))
	assert.Equal(t, []string{"g1:blue@t1", "h1:red@t2"}, tagPairs(t, db))
}

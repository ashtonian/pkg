//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type pgGadget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,patch" relation:"pg_gadget_tags,gadget_id,tag_id,id,tenant_id" indexed:"true"`
}

func pgGadgetDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Postgres(t)
	for _, stmt := range []string{
		`CREATE TABLE pg_gadgets (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT, updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE pg_gadget_tags (gadget_id TEXT NOT NULL, tag_id TEXT NOT NULL, tenant_id TEXT NOT NULL)`,
		`INSERT INTO pg_gadgets VALUES ('g1','t1','one',NULL,NULL), ('h1','t2','other',NULL,NULL)`,
		`INSERT INTO pg_gadget_tags VALUES ('g1','red','t1'), ('h1','red','t2')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func queryPGGadgets(t *testing.T, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, st parse.Statement) []pgGadget {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), st.SQL, st.Args...)
	require.NoError(t, err, st.SQL)
	defer rows.Close()
	var out []pgGadget
	for rows.Next() {
		var g pgGadget
		require.NoError(t, rows.Scan(&g.ID, &g.TenantID, &g.Name, &g.UpdatedAt, &g.DeletedAt))
		out = append(out, g)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestBuildUpdate_Postgres(t *testing.T) {
	r, err := parse.NewResource[pgGadget]("pg_gadgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	db := pgGadgetDB(t)
	ctx := context.Background()
	t1 := parse.Scope{Tenant: "t1"}

	t.Run("columns, stamped by the database clock", func(t *testing.T) {
		p, err := r.ParseMergePatch([]byte(`{"name":"renamed"}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByIDs("g1", "h1"), t1, parse.StampSQL("updated_at", "NOW()"))
		require.NoError(t, err)
		got := queryPGGadgets(t, db, plan.Owner)
		require.Len(t, got, 1, "h1 is t2's")
		assert.Equal(t, "renamed", got[0].Name)
		require.NotNil(t, got[0].UpdatedAt)
		assert.WithinDuration(t, time.Now(), *got[0].UpdatedAt, time.Minute)
	})

	t.Run("a relation change locks its owners", func(t *testing.T) {
		p, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("g1"), t1)
		require.NoError(t, err)

		tx, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		owners := queryPGGadgets(t, tx, plan.Owner)
		require.Len(t, owners, 1)

		other, err := db.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = other.ExecContext(ctx, `SET LOCAL lock_timeout = '200ms'`)
		require.NoError(t, err)
		_, err = other.QueryContext(ctx, plan.Owner.SQL, plan.Owner.Args...)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "a second writer waits for the owner rows")
		assert.Equal(t, "55P03", pgErr.Code, "lock_not_available")
		require.NoError(t, other.Rollback())

		stmts, err := r.RelationStatements(plan, []any{owners[0].ID})
		require.NoError(t, err)
		for _, st := range stmts {
			_, err := tx.ExecContext(ctx, st.SQL, st.Args...)
			require.NoError(t, err, st.SQL)
		}
		require.NoError(t, tx.Commit())

		rows, err := db.QueryContext(ctx, `SELECT gadget_id || ':' || tag_id || '@' || tenant_id FROM pg_gadget_tags ORDER BY 1`)
		require.NoError(t, err)
		defer rows.Close()
		var pairs []string
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			pairs = append(pairs, s)
		}
		require.NoError(t, rows.Err())
		assert.Equal(t, []string{"g1:blue@t1", "h1:red@t2"}, pairs)
	})
}

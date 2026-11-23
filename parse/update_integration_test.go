//go:build integration

package parse_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

type pgGadget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,patch" relation:"pg_gadget_tags,gadget_id,tag_id,id,tenant_id" indexed:"true"`
}

func pgGadgetDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Postgres(t)
	for _, stmt := range []string{
		`CREATE TABLE pg_gadgets (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT, version BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE pg_gadget_tags (gadget_id TEXT NOT NULL, tag_id TEXT NOT NULL, tenant_id TEXT NOT NULL)`,
		`INSERT INTO pg_gadgets (id, tenant_id, name) VALUES ('g1','t1','one'), ('h1','t2','other')`,
		`INSERT INTO pg_gadget_tags VALUES ('g1','red','t1'), ('h1','red','t2')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
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
		got := resourceRows(t, db, r, filter.PostgresDialect{}, plan.Owner)
		require.Len(t, got, 1, "h1 is t2's")
		assert.Equal(t, "renamed", got[0].Name)
		require.NotNil(t, got[0].UpdatedAt)
		assert.WithinDuration(t, time.Now(), *got[0].UpdatedAt, time.Minute)
	})

	t.Run("a version the server moves, returned moved", func(t *testing.T) {
		p, err := r.ParseMergePatch([]byte(`{"name":"versioned"}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("g1"), t1, parse.StampSQL("updated_at", "NOW()"), parse.Increment("version"))
		require.NoError(t, err)
		before := resourceRows(t, db, r, filter.PostgresDialect{}, parse.Statement{SQL: `SELECT id, tenant_id, name, version, updated_at, deleted_at FROM pg_gadgets WHERE id = 'g1'`})
		require.Len(t, before, 1)
		got := resourceRows(t, db, r, filter.PostgresDialect{}, plan.Owner)
		require.Len(t, got, 1)
		assert.Equal(t, before[0].Version+1, got[0].Version)
	})

	t.Run("a relation change locks its owners", func(t *testing.T) {
		p, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("g1"), t1)
		require.NoError(t, err)

		// The executor's transaction, opened by hand so a second writer can
		// run between its owner statement and its relation statements.
		require.NoError(t, withPgx(t, db, func(conn *pgx.Conn) error {
			return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				owners, err := parsepgx.Query(ctx, tx, r, plan.Owner)
				require.NoError(t, err)
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

				stmts, err := r.RelationStatements(plan, []parse.Key{{owners[0].ID}})
				require.NoError(t, err)
				for _, st := range stmts {
					_, err := tx.Exec(ctx, st.SQL, st.Args...)
					require.NoError(t, err, st.SQL)
				}
				return nil
			})
		}))

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

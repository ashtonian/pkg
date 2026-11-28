//go:build integration

package parsepgx_test

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/corpus"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsepgx/internal/pgtest"
)

// TestMain registers -update, with which the corpus's fixture cases
// (SDKWire) rewrite parse's testdata from what this provider serves instead
// of comparing it.
func TestMain(m *testing.M) {
	flag.Bool("update", false, "rewrite parse's testdata/sdk_wire.json from the exchanges this provider serves")
	flag.Parse()
	os.Exit(m.Run())
}

func TestCorpus(t *testing.T) {
	corpus.Run(t, corpus.Engine{Kind: corpus.Postgres, Open: openCorpusDB, CallerTx: callerTx})
}

// openCorpusDB opens a fresh schema of the PostgreSQL test server, or of the
// PostGIS one, on a pool whose connections start with v's session
// parameters, and the provider on it with v's dialect settings.
func openCorpusDB(t *testing.T, v corpus.Variant) (*parse.Provider, *sql.DB) {
	t.Helper()
	var config *pgxpool.Config
	if v.PostGIS {
		config = pgtest.PostGISPoolConfig(t)
	} else {
		config = pgtest.PostgresPoolConfig(t)
	}
	maps.Copy(config.ConnConfig.RuntimeParams, v.SessionParams)
	pool, err := pgxpool.NewWithConfig(pgtest.Context(t), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pgtest.ProviderOn(t, pool,
		parsepgx.Collation(v.Collation), parsepgx.ContainmentMaxValues(v.ContainmentMaxValues), parsepgx.SettleTimeout(v.Settle))
}

// callerTx runs fn in a pgx.Tx begun on conn's pgx connection, through
// parsepgx.Tx, and rolls it back unless fn settled it. The transaction
// reports each statement it sends to dbtest.OnStatement's hook (announced).
func callerTx(ctx context.Context, conn *sql.Conn, fn func(tx *parse.Tx, query func(stmt string) ([]string, error)) error) error {
	return conn.Raw(func(driverConn any) (err error) {
		c, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("conn is no pgx stdlib connection: %T", driverConn)
		}
		pgxTx, err := c.Conn().Begin(ctx)
		if err != nil {
			return err
		}
		tx := parsepgx.Tx(announced{Tx: pgxTx})
		defer func() { err = errors.Join(err, tx.Rollback(context.WithoutCancel(ctx))) }()
		return fn(tx, func(stmt string) ([]string, error) {
			rows, err := pgxTx.Query(ctx, stmt)
			if err != nil {
				return nil, err
			}
			return pgx.CollectRows(rows, pgx.RowTo[string])
		})
	})
}

// announced is a caller's pgx.Tx that reports each statement it sends to
// the hook dbtest.OnStatement put in the statement's context, as the
// database parsesql.HookedMemoryDB opens does on SQLite (dbtest.Hooked).
type announced struct {
	pgx.Tx
}

func (c announced) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	for _, q := range b.QueuedQueries {
		// pgx refuses the batch itself once a hook has ended ctx.
		_ = dbtest.Announce(ctx, q.SQL)
	}
	return c.Tx.SendBatch(ctx, b)
}

func (c announced) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	err := dbtest.Announce(ctx, query)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	return c.Tx.Exec(ctx, query, args...)
}

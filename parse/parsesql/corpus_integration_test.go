//go:build integration

package parsesql_test

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/corpus"
	"github.com/ashtonian/pkg/parse/parsesql"
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
	corpus.Run(t, corpus.Engine{Kind: corpus.SQLite, Open: openCorpusDB, CallerTx: callerTx})
}

// openCorpusDB opens a fresh in-memory SQLite database as v asks, and the
// provider on it.
func openCorpusDB(t *testing.T, v corpus.Variant) (*parse.Provider, *sql.DB) {
	t.Helper()
	require.False(t, v.PostGIS || len(v.SessionParams) > 0, "SQLite has no PostGIS server or session parameters")
	var db *sql.DB
	if v.Hooked {
		db = parsesql.HookedMemoryDB(t)
	} else {
		db = parsesql.MemoryDB(t)
	}
	p, err := parsesql.New(t.Context(), db, parsesql.SettleTimeout(v.Settle))
	require.NoError(t, err)
	return p, db
}

// callerTx runs fn in a *sql.Tx begun on conn, through parsesql.Tx, and
// rolls it back unless fn settled it.
func callerTx(ctx context.Context, conn *sql.Conn, fn func(tx *parse.Tx, query func(stmt string) ([]string, error)) error) (err error) {
	sqlTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	tx := parsesql.Tx(sqlTx)
	defer func() { err = errors.Join(err, tx.Rollback(context.WithoutCancel(ctx))) }()
	return fn(tx, func(stmt string) ([]string, error) { return queryText(ctx, sqlTx, stmt) })
}

// queryText is the first column of stmt's rows as text.
func queryText(ctx context.Context, tx *sql.Tx, stmt string) (out []string, err error) {
	rows, err := tx.QueryContext(ctx, stmt)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var s string
		err = rows.Scan(&s)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

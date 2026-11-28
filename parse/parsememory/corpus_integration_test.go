//go:build integration

package parsememory_test

import (
	"context"
	"maps"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/corpus"
	"github.com/ashtonian/pkg/parse/parsememory"
)

// corpusMaxRows bounds each corpus table far above any fixture's rows.
const corpusMaxRows = 100000

func TestCorpus(t *testing.T) {
	corpus.Run(t, corpus.Engine{Kind: corpus.Memory, OpenStore: openCorpusStore})
}

// openCorpusStore opens a fresh database, closed when the case ends, and
// the provider on it.
func openCorpusStore(t *testing.T) (*parse.Provider, corpus.Store) {
	t.Helper()
	db, err := parsememory.Open(t.Context(), parsememory.Config{MaxRows: corpusMaxRows})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	store := &corpusStore{db: db, rowKeyed: map[string]int64{}}
	return db.Provider(), store
}

// corpusRow is the key column of a fixture table that declares no key, as
// a SQL table without a primary key holds duplicate rows and NULLs: each
// row written to it is numbered.
const corpusRow = "corpus_row"

// corpusStore is a database as the corpus writes its fixtures to it.
// rowKeyed holds each keyless fixture table's last row number.
type corpusStore struct {
	db       *parsememory.Database
	mu       sync.Mutex
	rowKeyed map[string]int64
}

func (s *corpusStore) Declare(table string, columns []corpus.StoreColumn, key []string) error {
	declared := make([]backend.Column, 0, len(columns)+1)
	for _, c := range columns {
		declared = append(declared, backend.Column{Name: c.Name, Type: c.Type})
	}
	keyless := len(key) == 0
	if keyless {
		declared = append(declared, backend.Column{Name: corpusRow, Type: core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64}})
		key = []string{corpusRow}
	}
	created, err := s.db.DeclareTable(table, declared, key)
	if err != nil || !created || !keyless {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowKeyed[table] = 0
	return nil
}

func (s *corpusStore) Put(ctx context.Context, table string, rows ...map[string]any) error {
	return s.db.Put(ctx, table, s.numbered(table, rows)...)
}

// numbered are rows as table holds them: numbered when it is keyless.
func (s *corpusStore) numbered(table string, rows []map[string]any) []parsememory.Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	last, keyless := s.rowKeyed[table]
	out := make([]parsememory.Row, len(rows))
	for i, r := range rows {
		out[i] = r
		if keyless {
			last++
			out[i] = maps.Clone(r)
			out[i][corpusRow] = last
		}
	}
	if keyless {
		s.rowKeyed[table] = last
	}
	return out
}

func (s *corpusStore) Select(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, scope filter.RelationScope, column string) ([]string, error) {
	return s.db.Select(ctx, schema, f, scope, column)
}

func (s *corpusStore) Facets(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, reqs []filter.FacetRequest, scope filter.RelationScope) ([]filter.FacetResult, error) {
	return s.db.FacetsOf(ctx, schema, f, reqs, scope)
}

func (s *corpusStore) Walk(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, keys []filter.CursorKey, token *filter.PageToken) ([]string, error) {
	return s.db.Walk(ctx, schema, f, keys, token)
}

func (s *corpusStore) Rows(_ context.Context, table string) ([]map[string]any, error) {
	return s.db.RowsOf(table)
}

func (s *corpusStore) Include(ctx context.Context, field *filter.FieldSchema, owners []core.Key, opts ...filter.IncludeQueryOption) (filter.IncludeRows, error) {
	return s.db.IncludeOf(ctx, field, owners, opts...)
}

func (s *corpusStore) Begin(ctx context.Context) (corpus.StoreTx, error) {
	txn, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	tx := corpusTx{store: s, txn: txn}
	return tx, nil
}

// corpusTx is a transaction of the application's own as the corpus runs
// resources in it and writes beside them.
type corpusTx struct {
	store *corpusStore
	txn   *parsememory.Txn
}

func (c corpusTx) Tx() *parse.Tx { return parsememory.Tx(c.txn) }

func (c corpusTx) Put(ctx context.Context, table string, rows ...map[string]any) error {
	return c.txn.Put(ctx, table, c.store.numbered(table, rows)...)
}

func (c corpusTx) Rows(_ context.Context, table string) ([]map[string]any, error) {
	return c.txn.RowsOf(table)
}

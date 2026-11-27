//go:build unit || concurrency

package parsememory_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/parsememory"
	"github.com/ashtonian/pkg/parse/patch"
)

// member is a row of a table whose rows come in pairs, 2k and 2k+1, one
// in group a and the other in b: every batch that writes a pair writes
// both, so a state any read sees holds as many a as b.
type member struct {
	ID    int64             `json:"id"    db:"id"    parse:"filter,sort"`
	Group string            `json:"group" db:"group" parse:"filter,patch,facet" facet:"terms"`
	Attrs map[string]string `json:"attrs" db:"attrs" parse:"patch"`
}

const (
	memberPairs   = 32
	memberWriters = 4
	memberReaders = 4
	memberRounds  = 200
)

// openMembers opens a database holding the pairs, and the resource on it,
// whose patches may write members of attrs.
func openMembers(t *testing.T) (*parsememory.Database, *parse.Resource[member]) {
	t.Helper()
	db, err := parsememory.Open(t.Context(), parsememory.Config{MaxRows: 1000})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	r, err := parse.NewResource[member](db.Provider(), "members", parse.PageSize(2*memberPairs, 2*memberPairs),
		parse.CustomizeSchemas(func(s parse.Schemas) error {
			return s.Patch.AddPath("/attrs/*", &patch.PathSchema{
				AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove},
				DataType:   core.FieldTypeString, SQLColumn: "attrs",
			})
		}))
	require.NoError(t, err)
	rows := make([]parsememory.Row, 0, 2*memberPairs)
	for k := range memberPairs {
		rows = append(rows,
			parsememory.Row{"id": int64(2 * k), "group": "a", "attrs": map[string]string{}},
			parsememory.Row{"id": int64(2*k + 1), "group": "b", "attrs": map[string]string{}})
	}
	require.NoError(t, db.Put(t.Context(), "members", rows...))
	return db, r
}

// groupPatches are the patches that move a row to group a and to b.
func groupPatches(t *testing.T, r *parse.Resource[member]) map[string]*patch.ParsedPatch {
	t.Helper()
	patches := map[string]*patch.ParsedPatch{}
	for _, group := range []string{"a", "b"} {
		p, err := r.ParseMergePatch([]byte(`{"group":"` + group + `"}`))
		require.NoError(t, err)
		patches[group] = p
	}
	return patches
}

// swap is the batch that moves pair k's rows: 2k to first, 2k+1 to the
// other group.
func swap(patches map[string]*patch.ParsedPatch, k int, first string) []parse.RowUpdate {
	second := map[string]string{"a": "b", "b": "a"}[first]
	updates := []parse.RowUpdate{
		{Key: parse.Key{int64(2 * k)}, Patch: patches[first]},
		{Key: parse.Key{int64(2*k + 1)}, Patch: patches[second]},
	}
	return updates
}

// checkOneState lists every row with its count and facets and reports
// whether they read one state that holds the invariant: as many rows in a
// as in b, by the page, the facet and the count alike.
func checkOneState(ctx context.Context, r *parse.Resource[member]) error {
	req, err := r.ParseListInput(parse.ListInput{PageSize: 2 * memberPairs, IncludeTotal: true, Facets: []string{"group"}})
	if err != nil {
		return err
	}
	page, err := r.List(ctx, req, parse.Scope{})
	if err != nil {
		return err
	}
	tally := map[string]int64{}
	for _, m := range page.Items {
		tally[m.Group]++
	}
	faceted := map[string]int64{}
	for _, b := range page.Facets["group"] {
		faceted[b.Key] = b.Count
	}
	want := map[string]int64{"a": memberPairs, "b": memberPairs}
	switch {
	case page.PageInfo.TotalCount == nil || *page.PageInfo.TotalCount != 2*memberPairs:
		return fmt.Errorf("count %v, want %d", page.PageInfo.TotalCount, 2*memberPairs)
	case !maps.Equal(tally, want):
		return fmt.Errorf("page holds %v, want %v", tally, want)
	case !maps.Equal(faceted, want):
		return fmt.Errorf("facets hold %v, want %v", faceted, want)
	}
	return nil
}

// A list's page, count and facets read one published state, whatever
// commits meanwhile: batches that each move both rows of a pair are seen
// whole or not at all.
func TestConcurrentUpdates_ListsReadOneState(t *testing.T) {
	_, r := openMembers(t)
	ctx := t.Context()
	patches := groupPatches(t, r)
	var workers sync.WaitGroup
	for w := range memberWriters {
		workers.Go(func() {
			for i := range memberRounds {
				first := []string{"a", "b"}[(w+i)%2]
				_, err := r.UpdateBatch(ctx, swap(patches, (w*memberRounds+i)%memberPairs, first), parse.Scope{}, parse.UpdateBatchLimits{})
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	for range memberReaders {
		workers.Go(func() {
			for range memberRounds {
				err := checkOneState(ctx, r)
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	workers.Wait()
	require.NoError(t, checkOneState(ctx, r))
}

// An update that fails leaves the store as it was, while other updates
// commit around it: no read ever sees a row it would have written, and
// every update that succeeded is kept.
func TestConcurrentUpdates_AFailedUpdateWritesNothing(t *testing.T) {
	_, r := openMembers(t)
	ctx := t.Context()
	patches := groupPatches(t, r)
	// Every row's attrs lacks "missing", so the remove fails on the first
	// target, after the replace of group would have written it.
	failing, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/group","value":"never"},{"op":"remove","path":"/attrs/missing"}]`))
	require.NoError(t, err)
	var workers sync.WaitGroup
	for w := range memberWriters {
		workers.Go(func() {
			for i := range memberRounds {
				_, err := r.Update(ctx, failing, parse.ByIDs(int64(2*w), int64(2*w+1)), parse.Scope{})
				if err == nil {
					t.Error("the update that removes a missing member succeeded")
					return
				}
				_, err = r.UpdateBatch(ctx, swap(patches, w, []string{"a", "b"}[i%2]), parse.Scope{}, parse.UpdateBatchLimits{})
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	never, err := r.ParseListInput(parse.ListInput{Filter: `{"group":"never"}`, IncludeTotal: true})
	require.NoError(t, err)
	workers.Go(func() {
		for range memberRounds {
			page, err := r.List(ctx, never, parse.Scope{})
			if err != nil {
				t.Error(err)
				return
			}
			if *page.PageInfo.TotalCount != 0 {
				t.Errorf("a read saw %d rows the failed update would have written", *page.PageInfo.TotalCount)
				return
			}
		}
	})
	workers.Wait()
	require.NoError(t, checkOneState(ctx, r))
	last := []string{"a", "b"}[(memberRounds-1)%2]
	for w := range memberWriters {
		assert.Equal(t, last, groupOf(t, r, int64(2*w)), "the last swap of pair %d is kept", w)
	}
}

// groupOf is the group of row id.
func groupOf(t *testing.T, r *parse.Resource[member], id int64) string {
	t.Helper()
	req, err := r.ParseListInput(parse.ListInput{Filter: `{"id":` + strconv.FormatInt(id, 10) + `}`})
	require.NoError(t, err)
	page, err := r.List(t.Context(), req, parse.Scope{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	return page.Items[0].Group
}

// A write waits for the writer under its own context: cancelled while a
// transaction holds the writer, it returns the cancellation and writes
// nothing, and the transaction goes on to commit.
func TestWriterWait_EndsWithItsContext(t *testing.T) {
	db, r := openMembers(t)
	txn, err := db.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, txn.Put(t.Context(), "members", parsememory.Row{"id": int64(0), "group": "b", "attrs": map[string]string{}},
		parsememory.Row{"id": int64(1), "group": "a", "attrs": map[string]string{}}))

	p, err := r.ParseMergePatch([]byte(`{"group":"c"}`))
	require.NoError(t, err)
	waiting, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	results := make(chan error, memberWriters+1)
	for w := range memberWriters {
		workers.Go(func() {
			_, err := r.Update(waiting, p, parse.ByID(int64(2*w)), parse.Scope{})
			results <- err
		})
	}
	workers.Go(func() { results <- db.Put(waiting, "members", parsememory.Row{"id": int64(99), "group": "c"}) })
	cancel()
	workers.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorContains(t, err, "parsememory: waiting for the writer")
	}

	require.NoError(t, txn.Commit(t.Context()))
	require.NoError(t, checkOneState(t.Context(), r), "no waiter wrote")
	assert.Equal(t, "b", groupOf(t, r, 0), "the transaction's swap is kept")
}

// putSwapped puts pair k's rows into the transaction with row 2k in group
// first: the transaction's own write of the pair, which keeps as many rows
// in a as in b.
func putSwapped(ctx context.Context, txn *parsememory.Txn, k int, first string) error {
	second := map[string]string{"a": "b", "b": "a"}[first]
	return txn.Put(ctx, "members",
		parsememory.Row{"id": int64(2 * k), "group": first, "attrs": map[string]string{}},
		parsememory.Row{"id": int64(2*k + 1), "group": second, "attrs": map[string]string{}})
}

// A transaction is safe for concurrent use: its Put, the batches and lists
// resources run in it, and its Commit or Rollback run one at a time, each
// operation seeing the state the one before left. Settling it waits for
// the operation in flight, and every operation after it is ErrTxnDone.
func TestTxn_RunsConcurrentOperationsOneAtATime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		settle func(*parsememory.Txn, context.Context) error
		group  string
	}{
		{"commit", (*parsememory.Txn).Commit, "b"},
		{"rollback", (*parsememory.Txn).Rollback, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, r := openMembers(t)
			ctx := t.Context()
			patches := groupPatches(t, r)
			txn, err := db.Begin(ctx)
			require.NoError(t, err)
			require.NoError(t, putSwapped(ctx, txn, 0, "b"))
			scoped, err := r.In(parsememory.Tx(txn))
			require.NoError(t, err)

			// Each worker runs a bounded number of rounds and the settle
			// waits for half of them, never for a worker to yield: wasip1
			// has no preemption, so a worker may run all its rounds before
			// another goroutine runs.
			var rounds atomic.Int64
			halfway := make(chan struct{})
			var workers sync.WaitGroup
			for w := range memberWriters {
				workers.Go(func() {
					for i := range memberRounds {
						// Pair 0 is the transaction's first write, which no
						// worker moves.
						k := 1 + (w*memberRounds+i)%(memberPairs-1)
						first := []string{"a", "b"}[(w+i)%2]
						_, err := scoped.UpdateBatch(ctx, swap(patches, k, first), parse.Scope{}, parse.UpdateBatchLimits{})
						if err == nil {
							err = putSwapped(ctx, txn, k, first)
						}
						if err == nil {
							err = checkOneState(ctx, scoped)
						}
						if errors.Is(err, parsememory.ErrTxnDone) {
							return
						}
						if err != nil {
							t.Error(err)
							return
						}
						if rounds.Add(1) == memberWriters*memberRounds/2 {
							close(halfway)
						}
					}
				})
			}
			finished := make(chan struct{})
			go func() {
				workers.Wait()
				close(finished)
			}()
			select {
			case <-halfway:
			case <-finished:
			}
			require.NoError(t, tc.settle(txn, ctx))
			<-finished

			require.ErrorIs(t, putSwapped(ctx, txn, 1, "a"), parsememory.ErrTxnDone)
			_, err = scoped.UpdateBatch(ctx, swap(patches, 1, "a"), parse.Scope{}, parse.UpdateBatchLimits{})
			require.ErrorIs(t, err, parsememory.ErrTxnDone)
			require.ErrorIs(t, checkOneState(ctx, scoped), parsememory.ErrTxnDone)
			require.NoError(t, checkOneState(ctx, r))
			assert.Equal(t, tc.group, groupOf(t, r, 0), "the transaction's first write is kept by a commit alone")
			require.NoError(t, db.Put(ctx, "members", parsememory.Row{"id": int64(0), "group": "a", "attrs": map[string]string{}},
				parsememory.Row{"id": int64(1), "group": "b", "attrs": map[string]string{}}), "the writer is free")
		})
	}
}

// Database.Begin binds the transaction to its context, as database/sql
// binds a Tx: once the context ends, the transaction rolls back and
// releases the writer, and every later operation is ErrTxnDone with the
// context's cause. parse.Provider.Begin's transaction is bound to no
// context, as its contract says for every provider.
func TestTxn_ItsContextBoundsIt(t *testing.T) {
	db, r := openMembers(t)
	bound, cancel := context.WithCancel(t.Context())
	txn, err := db.Begin(bound)
	require.NoError(t, err)
	require.NoError(t, putSwapped(t.Context(), txn, 0, "b"))
	cancel()

	require.NoError(t, db.Put(t.Context(), "members", parsememory.Row{"id": int64(2), "group": "a", "attrs": map[string]string{}}),
		"the rollback released the writer")
	assert.Equal(t, "a", groupOf(t, r, 0), "the transaction's write is gone")
	err = putSwapped(t.Context(), txn, 0, "b")
	require.ErrorIs(t, err, parsememory.ErrTxnDone)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, txn.Commit(t.Context()), parsememory.ErrTxnDone)
	require.NoError(t, txn.Rollback(t.Context()))

	unbound, cancel := context.WithCancel(t.Context())
	tx, err := db.Provider().Begin(unbound)
	require.NoError(t, err)
	cancel()
	scoped, err := r.In(tx)
	require.NoError(t, err)
	_, err = scoped.UpdateBatch(t.Context(), swap(groupPatches(t, r), 0, "b"), parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	assert.Equal(t, "b", groupOf(t, r, 0))
}

//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// CheckPlan is the refusal every executor makes before its first statement;
// the executor contract (executor_contract_cases_test.go) shows both
// executors make it.
func TestCheckPlan(t *testing.T) {
	sqlite := filter.SQLiteDialect{}
	list, err := execItems.BuildList(sqlite, nil, execScope)
	require.NoError(t, err)
	require.NoError(t, execItems.CheckPlan(list, sqlite))

	err = execItems.CheckPlan(list, filter.PostgresDialect{})
	require.ErrorIs(t, err, parse.ErrDialect)
	require.ErrorContains(t, err, "parse: exec_items: list plan for sqlite, run on postgres")

	var noUpdate *parse.UpdatePlan
	require.ErrorContains(t, execItems.CheckPlan(noUpdate, sqlite), "parse: exec_items: nil update plan")
	var noBatch *parse.UpdateBatchPlan
	require.ErrorContains(t, execItems.CheckPlan(noBatch, sqlite), "parse: exec_items: nil update batch plan")
	require.ErrorContains(t, execItems.CheckPlan(nil, sqlite), "parse: exec_items: nil plan")
	require.ErrorIs(t, execItems.CheckPlan(list, nil), parse.ErrDialect)
}

// The batch steps take one read per chunk: an executor that lost a chunk's
// rows is refused rather than reported as a missing or unchanged target.
func TestBatchSteps_RefuseReadsOfAnotherChunkCount(t *testing.T) {
	p, err := execItems.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	plan, err := execItems.BuildUpdateBatch(filter.SQLiteDialect{}, []parse.RowUpdate{{Key: parse.Key{"e1"}, Patch: p}}, execScope, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.Len(t, plan.Chunks, 1)

	err = execItems.CheckBatchCurrent(plan, nil)
	require.ErrorContains(t, err, "parse: exec_items: Current rows of 0 chunks for a batch of 1")
	_, err = execItems.BatchResult(plan, [][]execItem{{{ID: "e1"}}}, nil)
	require.ErrorContains(t, err, "parse: exec_items: Owner rows of 0 chunks for a batch of 1")
	_, err = execItems.BatchResult(nil, nil, nil)
	require.ErrorContains(t, err, "parse: exec_items: nil update batch plan")

	res, err := execItems.BatchResult(plan, [][]execItem{{{ID: "e1"}}}, [][]execItem{nil})
	require.NoError(t, err)
	assert.Empty(t, res.Changed)
	assert.Equal(t, []execItem{{ID: "e1"}}, res.Unchanged, "a chunk's read row its owner left is unchanged")
}

func TestStatementFailed(t *testing.T) {
	require.NoError(t, parse.StatementFailed("owner", nil))
	cause := assert.AnError
	err := parse.StatementFailed("include tags", cause)
	require.ErrorIs(t, err, cause)
	require.EqualError(t, err, "include tags: "+cause.Error())
	var failed *parse.StatementError
	require.ErrorAs(t, err, &failed)
	assert.Equal(t, "include tags", failed.Statement)
}

func TestNewExecConfig(t *testing.T) {
	cfg, err := parse.NewExecConfig()
	require.NoError(t, err)
	assert.Equal(t, parse.DefaultSettleTimeout, cfg.SettleTimeout)
	cfg, err = parse.NewExecConfig(parse.WithSettleTimeout(time.Second), nil)
	require.NoError(t, err)
	assert.Equal(t, time.Second, cfg.SettleTimeout, "a nil option is skipped")
	for _, d := range []time.Duration{0, -time.Second} {
		_, err := parse.NewExecConfig(parse.WithSettleTimeout(d))
		require.ErrorContains(t, err, "must be positive", "%s", d)
	}
}

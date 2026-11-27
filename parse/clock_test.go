//go:build unit

package parse_test

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type clockDevice struct {
	ID        string    `json:"id"         db:"id"         parse:"filter,sort"`
	TypeID    *string   `json:"type_id"    db:"type_id"    parse:"filter"`
	CreatedAt time.Time `json:"created_at" db:"created_at" parse:"filter" filter_ops:"gt,lt"`
}

type clockType struct {
	ID        string    `json:"id"         db:"id"         parse:"filter,sort"`
	CreatedAt time.Time `json:"created_at" db:"created_at" parse:"filter"`
}

// A list request reads the clock once: its JSON filter, its flat
// parameters, the policy BuildList applies and the related tables'
// policies, each time a relation names one, resolve `now` to one instant,
// however far the clock moves between them.
func TestClock_OneInstantPerListRequest(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stepping := func() time.Time {
		at = at.Add(time.Hour)
		return at
	}
	types := parse.MustResource[clockType]("clock_types")
	devices, err := parse.NewResource[clockDevice]("clock_devices", parse.ParserOptions(core.WithClock(stepping), core.WithMaxCost(1<<10)))
	require.NoError(t, err)
	devices.MustRelate("type", parse.BelongsTo(types, "type_id", parse.PolicyScoped()))
	devices.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("clock_device_types", "device_id", "type_id"), parse.PolicyScoped(), parse.Includable()))
	q := url.Values{
		filter.ParamFilter: {`{"created_at":{"$gt":"now-1h"}}`},
		"created_at_lt":    {"now"},
	}
	scope := parse.Scope{
		Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+1h"}}},
		Related: map[string]*filter.PolicyConstraints{
			"clock_types": {Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+2h"}}},
		},
	}

	t.Run("list", func(t *testing.T) {
		related := url.Values{
			filter.ParamFilter:  {`{"created_at":{"$gt":"now-1h"},"$or":[{"type":{"$is":{}}},{"shared_types":{"$some":{}}}]}`},
			"created_at_lt":     {"now"},
			filter.ParamInclude: {"shared_types"},
		}
		req, err := devices.ParseList(related)
		require.NoError(t, err)
		plan, err := devices.BuildList(filter.PostgresDialect{}, req, scope)
		require.NoError(t, err)
		instants := timestamps(t, plan.Page.Args)
		require.Len(t, instants, 5)
		assert.Equal(t, time.Hour, instants[1].Sub(instants[0]), "now-1h and now are an hour apart")
		assert.Equal(t, time.Hour, instants[2].Sub(instants[1]), "the policy's now is the request's")
		assert.Equal(t, time.Hour, instants[3].Sub(instants[2]), "the related policy's now is the request's")
		assert.Equal(t, instants[3], instants[4], "a related table's policy is one instant however often it is named")

		includes, err := devices.IncludeQueries(plan, []parse.Key{{"d1"}})
		require.NoError(t, err)
		require.Len(t, includes, 1)
		assert.Equal(t, []time.Time{instants[3]}, timestamps(t, includes[0].Args), "an include admits the related rows its list did")
	})
	t.Run("request filter", func(t *testing.T) {
		parsed, err := devices.ParseRequestFilter(q)
		require.NoError(t, err)
		var values []any
		for _, c := range parsed.AST().Root.Children {
			values = append(values, c.Value)
		}
		instants := timestamps(t, values)
		require.Len(t, instants, 2)
		assert.Equal(t, time.Hour, instants[1].Sub(instants[0]))
	})
}

// timestamps are the bound values that are timestamps, in ascending order.
func timestamps(t *testing.T, args []any) []time.Time {
	t.Helper()
	var out []time.Time
	for _, a := range args {
		s, ok := a.(string)
		if !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			continue
		}
		out = append(out, ts)
	}
	slices.SortFunc(out, time.Time.Compare)
	return out
}

type clockItem struct {
	ID        string    `json:"id"         db:"id"         parse:"filter,sort"`
	Name      string    `json:"name"       db:"name"       parse:"filter,patch"`
	CreatedAt time.Time `json:"created_at" db:"created_at" parse:"filter"`
}

// An update plan reads the resource clock once: its Owner, its Current and
// the relation statements that admit the related rows resolve `now` to one
// instant, so the rows Current locks are the rows Owner compares and the
// targets a membership change may name are the ones the policy admits at
// that instant. A batch reads it once for every row.
func TestClock_OneInstantPerUpdatePlan(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stepping := func() time.Time {
		at = at.Add(time.Hour)
		return at
	}
	types := parse.MustResource[clockType]("clock_types")
	items, err := parse.NewResource[clockItem]("clock_items", parse.ParserOptions(core.WithClock(stepping)))
	require.NoError(t, err)
	items.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("clock_item_types", "item_id", "type_id"), parse.PolicyScoped(), parse.Writable()))
	scope := parse.Scope{
		Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+1h"}}},
		Related: map[string]*filter.PolicyConstraints{
			"clock_types": {Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+2h"}}},
		},
	}
	d := filter.PostgresDialect{}

	t.Run("update", func(t *testing.T) {
		p, err := items.ParseMergePatch([]byte(`{"name":"x","shared_types":["k1"]}`))
		require.NoError(t, err)
		plan, err := items.BuildUpdate(d, p, parse.ByID("i1"), scope, parse.SkipUnchanged())
		require.NoError(t, err)
		read := at
		require.NotNil(t, plan.Current)
		assert.Equal(t, []time.Time{read.Add(time.Hour)}, timestamps(t, plan.Owner.Args), "the policy's now is the clock's one reading")
		assert.Equal(t, []time.Time{read.Add(time.Hour)}, timestamps(t, plan.Current.Args), "Current admits the rows Owner does")

		stmts, err := items.RelationStatements(plan, []parse.Key{{"i1"}})
		require.NoError(t, err)
		var related []time.Time
		for _, s := range stmts {
			related = append(related, timestamps(t, s.Args)...)
		}
		require.NotEmpty(t, related)
		for _, ts := range related {
			assert.Equal(t, read.Add(2*time.Hour), ts, "a membership change admits the related rows at the plan's instant")
		}
	})
	t.Run("batch", func(t *testing.T) {
		rename := func(name string) *patch.ParsedPatch {
			p, err := items.ParseMergePatch([]byte(`{"name":"` + name + `"}`))
			require.NoError(t, err)
			return p
		}
		rows := []parse.RowUpdate{{Key: parse.Key{"i1"}, Patch: rename("x")}, {Key: parse.Key{"i2"}, Patch: rename("y")}}
		plan, err := items.BuildUpdateBatch(d, rows, scope, parse.UpdateBatchLimits{}, parse.SkipUnchanged())
		require.NoError(t, err)
		read := at
		require.Len(t, plan.Chunks, 1)
		chunk := plan.Chunks[0]
		require.NotNil(t, chunk.Owner)
		policy := []time.Time{read.Add(time.Hour), read.Add(time.Hour)}
		assert.Equal(t, policy, timestamps(t, chunk.Current.Args), "every row's Current at the batch's one instant")
		assert.Equal(t, policy, timestamps(t, chunk.Owner.Args), "every row's Owner at the batch's one instant")
	})
}

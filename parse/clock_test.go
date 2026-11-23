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

		includes, err := devices.IncludeQueries(plan, []any{"d1"})
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

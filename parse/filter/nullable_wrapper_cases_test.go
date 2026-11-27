//go:build unit || integration

package filter_test

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// visit's nullable columns are database/sql's wrappers, which a column holds
// as their value, NULL while Valid is false. A scalar's default operators
// leave out the NULL tests, as a pointer's do; score and seen_at add them.
type visit struct {
	ID        string        `json:"id"         db:"id"         parse:"filter,sort"`
	Score     sql.NullInt64 `json:"score"      db:"score"      parse:"filter,sort" filter_ops:"eq,gt,gte,lt,lte,null,not_null"`
	SeenAt    sql.NullTime  `json:"seen_at"    db:"seen_at"    parse:"filter,sort" filter_ops:"eq,gt,gte,lt,lte,null,not_null"`
	DeletedAt sql.NullTime  `json:"deleted_at" db:"deleted_at" parse:"filter"`
}

func visitAt(sec int) sql.NullTime {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	at := sql.NullTime{Time: base.Add(time.Duration(sec) * time.Second), Valid: true}
	return at
}

func visitScore(n int64) sql.NullInt64 {
	score := sql.NullInt64{Int64: n, Valid: true}
	return score
}

// seededVisits are the stored visits: c and e hold NULL scores, b and e
// NULL seen_at, and d is deleted.
func seededVisits() []visit {
	visits := []visit{
		{ID: "a", Score: visitScore(5), SeenAt: visitAt(1)},
		{ID: "b", Score: visitScore(2)},
		{ID: "c", SeenAt: visitAt(3)},
		{ID: "d", Score: visitScore(7), SeenAt: visitAt(2), DeletedAt: visitAt(4)},
		{ID: "e"},
		{ID: "f", Score: visitScore(5), SeenAt: visitAt(5)},
	}
	return visits
}

// visitDB is a database the visits are stored in: the column type of a
// timestamp, and the value a timestamp column binds.
type visitDB struct {
	db            *sql.DB
	dialect       filter.Dialect
	timestampType string
	stamp         func(sql.NullTime) any
}

// runNullableWrappers holds a database and Evaluate to one meaning of a
// wrapper column: a range compares its value, $null its NULL, a sort orders
// NULL greatest and a keyset walk visits every row once, and the soft-delete
// floor admits exactly the rows whose deleted_at is NULL.
func runNullableWrappers(t *testing.T, vdb visitDB) {
	t.Helper()
	ctx := t.Context()
	visits := seededVisits()
	_, err := vdb.db.ExecContext(ctx, "CREATE TABLE visits (id TEXT PRIMARY KEY, score BIGINT, seen_at "+vdb.timestampType+", deleted_at "+vdb.timestampType+")")
	require.NoError(t, err)
	insert := "INSERT INTO visits (id, score, seen_at, deleted_at) VALUES (" +
		vdb.dialect.Placeholder(1) + ", " + vdb.dialect.Placeholder(2) + ", " + vdb.dialect.Placeholder(3) + ", " + vdb.dialect.Placeholder(4) + ")"
	for _, v := range visits {
		_, err = vdb.db.ExecContext(ctx, insert, v.ID, v.Score, vdb.stamp(v.SeenAt), vdb.stamp(v.DeletedAt))
		require.NoError(t, err)
	}

	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[visit](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[visit](g)
	require.NoError(t, err)
	assert.Equal(t, core.FieldTypeInt, fs.Fields["score"].DataType)
	assert.Equal(t, core.FieldTypeTimestamp, fs.Fields["seen_at"].DataType)
	assert.Equal(t, reflect.Int64, ss.Fields["score"].NumberKind, "a sort key ranges as its value's kind")
	assert.True(t, ss.Fields["score"].Nullable)
	lq := mustListQuery(t, fs, ss, vdb.dialect)
	floor := filter.ComposeConstraints(filter.RepositoryScope{DeleteColumn: "deleted_at"}, nil, nil)

	t.Run("filters", func(t *testing.T) {
		for _, tt := range []struct {
			name   string
			filter string
			policy *filter.PolicyConstraints
			want   string
		}{
			{"a score above", `{"score":{"$gt":3}}`, nil, "adf"},
			{"a score at most", `{"score":{"$lte":2}}`, nil, "b"},
			{"a score that is NULL", `{"score":{"$null":true}}`, nil, "ce"},
			{"a time before", `{"seen_at":{"$lt":"2026-03-04T05:06:10Z"}}`, nil, "ad"},
			{"a time from", `{"seen_at":{"$gte":"2026-03-04T05:06:10Z"}}`, nil, "cf"},
			{"a time that is NULL", `{"seen_at":{"$null":true}}`, nil, "be"},
			{"the live rows", "", floor, "abcef"},
			{"the live rows scoring above", `{"score":{"$gt":3}}`, floor, "af"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				opts := []filter.ListOption{filter.ListFilter(tt.filter), filter.ListSort("id"), filter.ListLimit(len(visits))}
				if tt.policy != nil {
					opts = append(opts, filter.ListPolicy(tt.policy))
				}
				res, err := lq.Build(opts...)
				require.NoError(t, err)
				assert.Equal(t, tt.want, visitIDs(selectVisits(t, vdb, res)), "SQL")

				ast, err := filter.NewFilterParser(fs, nil).Parse(tt.filter)
				require.NoError(t, err)
				if tt.policy != nil {
					ast, err = filter.ApplyPolicyConstraints(fs, ast, tt.policy)
					require.NoError(t, err)
				}
				var evaluated []visit
				for _, v := range visits {
					truth, err := filter.Evaluate(ast.Root, visitRow(v))
					require.NoError(t, err)
					if truth == filter.True {
						evaluated = append(evaluated, v)
					}
				}
				assert.Equal(t, tt.want, visitIDs(evaluated), "Evaluate")
			})
		}
	})

	t.Run("keyset walks", func(t *testing.T) {
		for _, tt := range []struct {
			sort string
			want string
		}{
			{"score", "bafdce"},
			{"-score", "ecdfab"},
			{"seen_at", "adcfbe"},
			{"-seen_at", "ebfcda"},
		} {
			t.Run(tt.sort, func(t *testing.T) {
				var walked []visit
				cursor := ""
				for range len(visits) + 1 {
					opts := []filter.ListOption{filter.ListSort(tt.sort), filter.ListLimit(2)}
					if cursor != "" {
						opts = append(opts, filter.ListCursor(cursor))
					}
					res, err := lq.Build(opts...)
					require.NoError(t, err)
					page, err := filter.NewPage(selectVisits(t, vdb, res), res.Limit, res.Cursor, func(v visit, column string) (any, error) {
						return filter.CursorValueByTag(v, "db", column)
					})
					require.NoError(t, err)
					walked = append(walked, page.Items...)
					if !page.PageInfo.HasNextPage {
						break
					}
					cursor = string(page.PageInfo.EndCursor)
				}
				assert.Equal(t, tt.want, visitIDs(walked))
			})
		}
	})
}

// selectVisits runs res over the visits table.
func selectVisits(t *testing.T, vdb visitDB, res *filter.ListResult) []visit {
	t.Helper()
	rows, err := vdb.db.QueryContext(t.Context(), "SELECT id, score, seen_at, deleted_at FROM visits "+res.Fragment, res.Args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []visit
	for rows.Next() {
		var v visit
		require.NoError(t, rows.Scan(&v.ID, &v.Score, &v.SeenAt, &v.DeletedAt))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())
	return out
}

// visitRow is v as an in-memory store holds it: its Go values by column.
func visitRow(v visit) filter.ColumnRow {
	columns := map[string]any{"visits.id": v.ID, "visits.score": v.Score, "visits.seen_at": v.SeenAt, "visits.deleted_at": v.DeletedAt}
	return func(column string) (any, bool) {
		value, ok := columns[column]
		return value, ok
	}
}

func visitIDs(vs []visit) string {
	var b strings.Builder
	for _, v := range vs {
		b.WriteString(v.ID)
	}
	return b.String()
}

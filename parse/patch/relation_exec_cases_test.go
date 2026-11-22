//go:build unit || integration

package patch_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// relationDB is a database relation statements execute on. Owners are
// integers, targets and tenants UUIDs (PostgreSQL's uuid, SQLite's text)
// given as Go strings, as a resource's string key of a uuid column is.
type relationDB struct {
	db      *sql.DB
	dialect patch.SQLDialect
	// schema creates widgets (id, tenant_id), tags (id, tenant_id,
	// deleted_at) and widget_tags (widget_id, tag_id, tenant_id), the pair
	// table keyed on (widget_id, tag_id).
	schema []string
}

const (
	tagA     = "00000000-0000-0000-0000-00000000000a"
	tagB     = "00000000-0000-0000-0000-00000000000b"
	tagC     = "00000000-0000-0000-0000-00000000000c"
	tagGone  = "00000000-0000-0000-0000-00000000000d" // a deleted tag
	tenant1  = "00000000-0000-0000-0000-000000000101"
	tenant2  = "00000000-0000-0000-0000-000000000102"
	tagOther = "00000000-0000-0000-0000-0000000000e0" // another tenant's tag
)

// relationStart is every case's pairs: owner, target, tenant.
var relationStart = [][3]any{
	{1, tagA, tenant1}, {1, tagB, tenant1}, {1, tagGone, tenant1}, {2, tagA, tenant1}, {1, tagOther, tenant2},
}

// relationCases change the pairs of owners through the pair table, and give
// the pairs that remain, as "owner:tag" with tags by their last character.
var relationCases = []struct {
	name   string
	verb   patch.RelationVerb
	owners []any
	values []any
	// tenant scopes the pair table to tenant1; guarded reaches targets
	// through the tags table, live ones only.
	tenant, guarded bool
	want            []string
}{
	{"replace sets the owner's pairs", patch.RelationVerbReplace, []any{1}, []any{tagB, tagC}, false, false, []string{"1:b", "1:c", "2:a"}},
	{"replace with none clears", patch.RelationVerbReplace, []any{1, 2}, nil, false, false, nil},
	{"add pairs every owner with every value", patch.RelationVerbAdd, []any{1, 2}, []any{tagC}, false, false, []string{"1:a", "1:b", "1:c", "1:d", "1:e0", "2:a", "2:c"}},
	{"add of a pair already there", patch.RelationVerbAdd, []any{1}, []any{tagA}, false, false, []string{"1:a", "1:b", "1:d", "1:e0", "2:a"}},
	{"remove", patch.RelationVerbRemove, []any{1}, []any{tagA, tagC}, false, false, []string{"1:b", "1:d", "1:e0", "2:a"}},
	{"tenant: replace keeps another tenant's pairs", patch.RelationVerbReplace, []any{1}, []any{tagC}, true, false, []string{"1:c", "1:e0", "2:a"}},
	{"tenant: clear keeps another tenant's pairs", patch.RelationVerbReplace, []any{1}, nil, true, false, []string{"1:e0", "2:a"}},
	{"tenant: add", patch.RelationVerbAdd, []any{2}, []any{tagB}, true, false, []string{"1:a", "1:b", "1:d", "1:e0", "2:a", "2:b"}},
	{"tenant: remove", patch.RelationVerbRemove, []any{1}, []any{tagOther, tagA}, true, false, []string{"1:b", "1:d", "1:e0", "2:a"}},
	{"guarded: replace reaches visible targets", patch.RelationVerbReplace, []any{1}, []any{tagC, tagGone}, true, true, []string{"1:c", "1:d", "1:e0", "2:a"}},
	{"guarded: add of an invisible target", patch.RelationVerbAdd, []any{2}, []any{tagGone, tagOther, tagB}, true, true, []string{"1:a", "1:b", "1:d", "1:e0", "2:a", "2:b"}},
	{"guarded: remove of an invisible target", patch.RelationVerbRemove, []any{1}, []any{tagGone, tagA}, true, true, []string{"1:b", "1:d", "1:e0", "2:a"}},
	{"guarded: clear reaches visible targets", patch.RelationVerbReplace, []any{1}, nil, true, true, []string{"1:d", "1:e0", "2:a"}},
}

func relationOp(tenant, guarded bool, verb patch.RelationVerb, values []any) (patch.RelationPatchOp, patch.RenderRelationOpOptions) {
	rel := &core.RelationSchema{JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id"}
	var opts patch.RenderRelationOpOptions
	if tenant {
		rel.TenantColumn = "tenant_id"
		opts.TenantValue = tenant1
	}
	if guarded {
		rel.TargetTable, rel.TargetKey, rel.TargetTenant = "tags", "id", "tenant_id"
		opts.Owner = patch.RelationOwner{Table: "widgets", Key: "id", Tenant: "tenant_id"}
		opts.TargetVisible = func(alias string, bind func(any) string) (string, error) {
			return " AND " + alias + ".tenant_id = " + bind(tenant1) + " AND " + alias + ".deleted_at IS NULL", nil
		}
	}
	return patch.RelationPatchOp{Field: "tags", Schema: rel, Verb: verb, Values: values}, opts
}

func (rdb relationDB) reset(t *testing.T) {
	t.Helper()
	for _, stmt := range append([]string{"DROP TABLE IF EXISTS widget_tags", "DROP TABLE IF EXISTS tags", "DROP TABLE IF EXISTS widgets"}, rdb.schema...) {
		_, err := rdb.db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	ph := rdb.dialect.Placeholder
	for _, w := range [][2]any{{1, tenant1}, {2, tenant1}, {3, tenant1}, {4, tenant1}} {
		_, err := rdb.db.Exec("INSERT INTO widgets (id, tenant_id) VALUES ("+ph(1)+", "+ph(2)+")", w[0], w[1])
		require.NoError(t, err)
	}
	for _, tag := range [][3]any{{tagA, tenant1, nil}, {tagB, tenant1, nil}, {tagC, tenant1, nil}, {tagGone, tenant1, "2026-01-01"}, {tagOther, tenant2, nil}} {
		_, err := rdb.db.Exec("INSERT INTO tags (id, tenant_id, deleted_at) VALUES ("+ph(1)+", "+ph(2)+", "+ph(3)+")", tag[0], tag[1], tag[2])
		require.NoError(t, err)
	}
	for _, pair := range relationStart {
		_, err := rdb.db.Exec("INSERT INTO widget_tags (widget_id, tag_id, tenant_id) VALUES ("+ph(1)+", "+ph(2)+", "+ph(3)+")", pair[0], pair[1], pair[2])
		require.NoError(t, err)
	}
}

func (rdb relationDB) execute(t *testing.T, stmts []patch.RenderedRelationStatement) {
	t.Helper()
	tx, err := rdb.db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	for _, st := range stmts {
		_, err := tx.Exec(st.SQL, st.Args...)
		require.NoError(t, err, st.SQL)
	}
	require.NoError(t, tx.Commit())
}

func (rdb relationDB) pairs(t *testing.T) []string {
	t.Helper()
	rows, err := rdb.db.Query("SELECT CAST(widget_id AS TEXT), CAST(tag_id AS TEXT) FROM widget_tags ORDER BY 1, 2")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var owner, tag string
		require.NoError(t, rows.Scan(&owner, &tag))
		out = append(out, owner+":"+strings.TrimLeft(tag[len(tag)-2:], "0"))
	}
	require.NoError(t, rows.Err())
	return out
}

// runRelationCases executes each case's statements and asserts the pairs
// left, then a limit-sized change of four owners: owners times values pairs,
// in statements whose arguments do not grow with either.
func runRelationCases(t *testing.T, rdb relationDB) {
	t.Helper()
	for _, tc := range relationCases {
		t.Run(tc.name, func(t *testing.T) {
			rdb.reset(t)
			op, opts := relationOp(tc.tenant, tc.guarded, tc.verb, tc.values)
			stmts, err := patch.RenderRelationOp(op, rdb.dialect, tc.owners, opts)
			require.NoError(t, err)
			rdb.execute(t, stmts)
			assert.Equal(t, tc.want, rdb.pairs(t))
		})
	}

	t.Run("a limit-sized change of four owners", func(t *testing.T) {
		values := make([]any, patch.DefaultMaxRelationValues)
		for i := range values {
			values[i] = fmt.Sprintf("00000000-0000-0000-0001-%012d", i)
		}
		owners := []any{1, 2, 3, 4}
		for _, guarded := range []bool{false, true} {
			rdb.reset(t)
			if guarded {
				for _, v := range values {
					_, err := rdb.db.Exec("INSERT INTO tags (id, tenant_id) VALUES ("+rdb.dialect.Placeholder(1)+", "+rdb.dialect.Placeholder(2)+")", v, tenant1)
					require.NoError(t, err)
				}
			}
			for _, verb := range []patch.RelationVerb{patch.RelationVerbAdd, patch.RelationVerbReplace, patch.RelationVerbRemove} {
				op, opts := relationOp(true, guarded, verb, values)
				stmts, err := patch.RenderRelationOp(op, rdb.dialect, owners, opts)
				require.NoError(t, err)
				small, smallOpts := relationOp(true, guarded, verb, values[:1])
				few, err := patch.RenderRelationOp(small, rdb.dialect, owners[:1], smallOpts)
				require.NoError(t, err)
				require.Len(t, few, len(stmts))
				for i := range stmts {
					assert.Len(t, stmts[i].Args, len(few[i].Args), "%s %s: arguments grow with owners and values", verb, stmts[i].SQL)
				}
				rdb.execute(t, stmts)
			}
			// The replace and the remove reach visible targets only: a guarded
			// relation keeps the pair to the deleted tag.
			want := []string{"1:e0"}
			if guarded {
				want = []string{"1:d", "1:e0"}
			}
			assert.Equal(t, want, rdb.pairs(t), "add, replace and remove of one set")
		}
	})
}

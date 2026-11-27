//go:build unit

package parse_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestBuildUpdate_Refuses(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	empty, err := r.ParseFilterMap(map[string]any{})
	require.NoError(t, err)
	t1 := parse.Scope{Tenant: "t1"}
	for name, tc := range map[string]struct {
		target parse.Target
		scope  parse.Scope
		opts   []parse.UpdateOption
		want   error
	}{
		"no target":                {parse.Target{}, t1, nil, parse.ErrNoTarget},
		"no keys":                  {parse.ByIDs(), t1, nil, parse.ErrNoTarget},
		"no filter":                {parse.ByFilter(nil), t1, nil, parse.ErrNoTarget},
		"empty filter":             {parse.ByFilter(empty), t1, nil, parse.ErrNoTarget},
		"no tenant":                {parse.ByID("g1"), parse.Scope{}, nil, parse.ErrTenantRequired},
		"governance":               {parse.ByID("g1"), parse.Scope{Tenant: "t1", Governance: &filter.PolicyConstraints{}}, nil, parse.ErrUpdateScope},
		"deleted rows":             {parse.ByID("g1"), parse.Scope{Tenant: "t1", IncludeDeleted: true}, nil, parse.ErrUpdateScope},
		"stamp a non-column":       {parse.ByID("g1"), t1, []parse.UpdateOption{parse.StampSQL("touched_at", "NOW()")}, nil},
		"stamp without time":       {parse.ByID("g1"), t1, []parse.UpdateOption{parse.Stamp("updated_at", time.Time{})}, nil},
		"increment a non-column":   {parse.ByID("g1"), t1, []parse.UpdateOption{parse.Increment("revision")}, nil},
		"increment a text column":  {parse.ByID("g1"), t1, []parse.UpdateOption{parse.Increment("name")}, nil},
		"increment a column twice": {parse.ByID("g1"), t1, []parse.UpdateOption{parse.Increment("rank"), parse.Increment("rank")}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.BuildUpdate(filter.SQLiteDialect{}, p, tc.target, tc.scope, tc.opts...)
			require.Error(t, err)
			if tc.want != nil {
				assert.True(t, errors.Is(err, tc.want), "%v", err)
			}
		})
	}
}

func TestBuildUpdate_LocksRelationOwnersOnPostgres(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(plan.Owner.SQL, " FOR UPDATE"), plan.Owner.SQL)

	sc, err := r.ParseMergePatch([]byte(`{"name":"n","rank":2}`))
	require.NoError(t, err)
	plan, err = r.BuildUpdate(filter.PostgresDialect{}, sc, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", "NOW()"))
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "gadgets" SET "name" = $1, "rank" = $2, "updated_at" = NOW() WHERE (("gadgets"."tenant_id" = $3 AND "gadgets"."deleted_at" IS NULL) AND "gadgets"."id" = $4) `+
		`RETURNING "gadgets"."id", "gadgets"."tenant_id", "gadgets"."name", "gadgets"."rank", "gadgets"."version", "gadgets"."updated_at", "gadgets"."deleted_at"`, plan.Owner.SQL)
	assert.Equal(t, []any{"n", int64(2), "t1", "g1"}, plan.Owner.Args)
}

func TestRelationStatements_RefusesAnotherResourcesPlan(t *testing.T) {
	other, err := parse.NewResource[gadget]("other_gadgets", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	p, err := other.ParseMergePatch([]byte(`{"tags":["blue"]}`))
	require.NoError(t, err)
	plan, err := other.BuildUpdate(filter.PostgresDialect{}, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.Equal(t, filter.PostgresDialect{}, plan.Dialect())

	_, err = gadgets(t).RelationStatements(plan, []parse.Key{{"g1"}})
	assert.ErrorContains(t, err, `update plan built for "other_gadgets"`)
	_, err = gadgets(t).RelationStatements(nil, []parse.Key{{"g1"}})
	assert.ErrorContains(t, err, "nil update plan")
}

//go:build unit

package parse_test

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

type gadget struct {
	ID        string   `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string   `json:"tenant_id"  db:"tenant_id"`
	Name      string   `json:"name"       db:"name"       parse:"filter,sort,patch"`
	Rank      int64    `json:"rank"       db:"rank"       parse:"filter,patch"`
	UpdatedAt string   `json:"updated_at" db:"updated_at"`
	DeletedAt *string  `json:"deleted_at" db:"deleted_at"`
	Tags      []string `json:"tags"       db:"-"          parse:"filter,patch" relation:"gadget_tags,gadget_id,tag_id,id,tenant_id" indexed:"true"`
}

func gadgets(t *testing.T) *parse.Resource[gadget] {
	t.Helper()
	r, err := parse.NewResource[gadget]("gadgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	return r
}

// gadgetDB: t1 holds g1, g2 and a deleted g3; t2 holds h1. Tags pair in
// each row's tenant.
func gadgetDB(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE gadgets (id TEXT PRIMARY KEY, tenant_id TEXT, name TEXT, rank INTEGER, updated_at TEXT, deleted_at TEXT)`,
		`CREATE TABLE gadget_tags (gadget_id TEXT, tag_id TEXT, tenant_id TEXT)`,
		`INSERT INTO gadgets VALUES ('g1','t1','one',1,'old',NULL), ('g2','t1','two',1,'old',NULL),
			('g3','t1','three',1,'old','2026-01-01T00:00:00Z'), ('h1','t2','other',1,'old',NULL)`,
		`INSERT INTO gadget_tags VALUES ('g1','red','t1'), ('h1','red','t2')`,
	} {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func scanGadgets(t *testing.T, db *sql.DB, st parse.Statement) []gadget {
	t.Helper()
	rows, err := db.Query(st.SQL, st.Args...)
	require.NoError(t, err, st.SQL)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []gadget
	for rows.Next() {
		var g gadget
		require.NoError(t, rows.Scan(&g.ID, &g.TenantID, &g.Name, &g.Rank, &g.UpdatedAt, &g.DeletedAt))
		out = append(out, g)
	}
	require.NoError(t, rows.Err())
	return out
}

func gadgetIDs(gs []gadget) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.ID
	}
	slices.Sort(out)
	return out
}

func gadgetNames(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT id, name FROM gadgets`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		require.NoError(t, rows.Scan(&id, &name))
		out[id] = name
	}
	require.NoError(t, rows.Err())
	return out
}

func tagPairs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT gadget_id || ':' || tag_id || '@' || tenant_id FROM gadget_tags ORDER BY 1`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestBuildUpdate_Refuses(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	t1 := parse.Scope{Tenant: "t1"}
	for name, tc := range map[string]struct {
		target parse.Target
		scope  parse.Scope
		opts   []parse.UpdateOption
		want   error
	}{
		"no target":          {parse.Target{}, t1, nil, parse.ErrNoTarget},
		"no keys":            {parse.ByIDs(), t1, nil, parse.ErrNoTarget},
		"no filter":          {parse.ByFilter(nil), t1, nil, parse.ErrNoTarget},
		"empty filter":       {parse.ByFilter(&filter.ParsedFilter{AST: &filter.FilterAST{}}), t1, nil, parse.ErrNoTarget},
		"no tenant":          {parse.ByID("g1"), parse.Scope{}, nil, parse.ErrTenantRequired},
		"governance":         {parse.ByID("g1"), parse.Scope{Tenant: "t1", Governance: &filter.PolicyConstraints{}}, nil, parse.ErrUpdateScope},
		"deleted rows":       {parse.ByID("g1"), parse.Scope{Tenant: "t1", IncludeDeleted: true}, nil, parse.ErrUpdateScope},
		"stamp a non-column": {parse.ByID("g1"), t1, []parse.UpdateOption{parse.StampSQL("touched_at", "NOW()")}, nil},
		"stamp without time": {parse.ByID("g1"), t1, []parse.UpdateOption{parse.Stamp("updated_at", time.Time{})}, nil},
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

func TestBuildUpdate_ChangesOnlyTheScopedTargetRows(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"renamed","rank":7}`))
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		target parse.Target
		scope  parse.Scope
		want   []string
	}{
		"by key":                    {parse.ByID("g1"), parse.Scope{Tenant: "t1"}, []string{"g1"}},
		"by keys":                   {parse.ByIDs("g1", "g2"), parse.Scope{Tenant: "t1"}, []string{"g1", "g2"}},
		"another tenant's key":      {parse.ByID("h1"), parse.Scope{Tenant: "t1"}, nil},
		"a deleted row":             {parse.ByID("g3"), parse.Scope{Tenant: "t1"}, nil},
		"by filter, in its tenant":  {byFilter(t, r, `{"rank":1}`), parse.Scope{Tenant: "t1"}, []string{"g1", "g2"}},
		"policy narrows the target": {parse.ByIDs("g1", "g2"), parse.Scope{Tenant: "t1", Policy: nameIs("two")}, []string{"g2"}},
	} {
		t.Run(name, func(t *testing.T) {
			db := gadgetDB(t)
			plan, err := r.BuildUpdate(filter.SQLiteDialect{}, p, tc.target, tc.scope)
			require.NoError(t, err)
			require.True(t, plan.Writes)
			got := scanGadgets(t, db, plan.Owner)
			assert.Equal(t, tc.want, nilIfEmpty(gadgetIDs(got)), "the rows RETURNING reports")
			for _, g := range got {
				assert.Equal(t, "renamed", g.Name)
				assert.Equal(t, int64(7), g.Rank)
			}
			names := gadgetNames(t, db)
			for id, name := range names {
				if slices.Contains(tc.want, id) {
					assert.Equal(t, "renamed", name, id)
				} else {
					assert.NotEqual(t, "renamed", name, "%s is outside the target or scope", id)
				}
			}
		})
	}
}

func byFilter(t *testing.T, r *parse.Resource[gadget], f string) parse.Target {
	t.Helper()
	pf, err := r.ParseFilter(f)
	require.NoError(t, err)
	return parse.ByFilter(pf)
}

func nameIs(name string) *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "name", Operator: filter.OpEq, Value: name}}}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestBuildUpdate_Stamps(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	db := gadgetDB(t)
	plan, err := r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.Stamp("updated_at", at))
	require.NoError(t, err)
	got := scanGadgets(t, db, plan.Owner)
	require.Len(t, got, 1)
	assert.Equal(t, core.FormatTimestamp(at), got[0].UpdatedAt)

	plan, err = r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("g2"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", "'database clock'"))
	require.NoError(t, err)
	got = scanGadgets(t, db, plan.Owner)
	require.Len(t, got, 1)
	assert.Equal(t, "database clock", got[0].UpdatedAt)

	rel, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
	require.NoError(t, err)
	plan, err = r.BuildUpdate(filter.SQLiteDialect{}, rel, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.Stamp("updated_at", at.Add(time.Hour)))
	require.NoError(t, err)
	got = scanGadgets(t, db, plan.Owner)
	assert.Equal(t, core.FormatTimestamp(at), got[0].UpdatedAt, "a relation change alone writes no column, so it stamps none")
}

func TestBuildUpdate_RelationsChangeOnlyTheRowsTheOwnerMatched(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"tags":["blue","green"]}`))
	require.NoError(t, err)
	db := gadgetDB(t)

	plan, err := r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByIDs("g1", "h1", "g3"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.True(t, plan.Writes)
	assert.True(t, plan.HasRelations())
	assert.True(t, strings.HasPrefix(plan.Owner.SQL, "SELECT "), "a relation-only patch selects its owners")

	tx, err := db.Begin()
	require.NoError(t, err)
	owners := scanGadgetsTx(t, tx, plan.Owner)
	assert.Equal(t, []string{"g1"}, gadgetIDs(owners), "h1 is t2's, g3 is deleted")
	keys := make([]any, len(owners))
	for i, o := range owners {
		keys[i], err = r.Key(o)
		require.NoError(t, err)
	}
	stmts, err := r.RelationStatements(plan, keys)
	require.NoError(t, err)
	require.NotEmpty(t, stmts)
	for _, st := range stmts {
		_, execErr := tx.Exec(st.SQL, st.Args...)
		require.NoError(t, execErr, st.SQL)
	}
	require.NoError(t, tx.Commit())
	assert.Equal(t, []string{"g1:blue@t1", "g1:green@t1", "h1:red@t2"}, tagPairs(t, db), "the requested h1 is not the matched one")

	none, err := r.RelationStatements(plan, nil)
	require.NoError(t, err)
	assert.Empty(t, none, "no matched owners, no junction writes")
}

func scanGadgetsTx(t *testing.T, tx *sql.Tx, st parse.Statement) []gadget {
	t.Helper()
	rows, err := tx.Query(st.SQL, st.Args...)
	require.NoError(t, err, st.SQL)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []gadget
	for rows.Next() {
		var g gadget
		require.NoError(t, rows.Scan(&g.ID, &g.TenantID, &g.Name, &g.Rank, &g.UpdatedAt, &g.DeletedAt))
		out = append(out, g)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestBuildUpdate_ColumnsAndRelationsTogether(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{"name":"both","tags":["blue"]}`))
	require.NoError(t, err)
	db := gadgetDB(t)
	plan, err := r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plan.Owner.SQL, "UPDATE gadgets SET "))
	assert.True(t, plan.HasRelations())
	owners := scanGadgets(t, db, plan.Owner)
	require.Len(t, owners, 1)
	stmts, err := r.RelationStatements(plan, []any{owners[0].ID})
	require.NoError(t, err)
	for _, st := range stmts {
		_, err := db.Exec(st.SQL, st.Args...)
		require.NoError(t, err)
	}
	assert.Equal(t, "both", gadgetNames(t, db)["g1"])
	assert.Equal(t, []string{"g1:blue@t1", "h1:red@t2"}, tagPairs(t, db))
}

func TestBuildUpdate_AnEmptyPatchIsAScopedRead(t *testing.T) {
	r := gadgets(t)
	p, err := r.ParseMergePatch([]byte(`{}`))
	require.NoError(t, err)
	db := gadgetDB(t)
	plan, err := r.BuildUpdate(filter.SQLiteDialect{}, p, parse.ByIDs("g1", "h1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.False(t, plan.Writes)
	assert.False(t, plan.HasRelations())
	assert.Equal(t, []string{"g1"}, gadgetIDs(scanGadgets(t, db, plan.Owner)))
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
	assert.Equal(t, "UPDATE gadgets SET name = $1, rank = $2, updated_at = NOW() WHERE (gadgets.tenant_id = $3 AND gadgets.deleted_at IS NULL AND gadgets.id = $4) "+
		"RETURNING gadgets.id, gadgets.tenant_id, gadgets.name, gadgets.rank, gadgets.updated_at, gadgets.deleted_at", plan.Owner.SQL)
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

	_, err = gadgets(t).RelationStatements(plan, []any{"g1"})
	assert.ErrorContains(t, err, `update plan built for "other_gadgets"`)
	_, err = gadgets(t).RelationStatements(nil, []any{"g1"})
	assert.ErrorContains(t, err, "nil update plan")
}

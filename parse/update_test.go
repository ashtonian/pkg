//go:build unit

package parse_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestBuildUpdate_Refuses(t *testing.T) {
	r := parsetest.Gadgets(t, onPostgres)
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
			_, err := r.BuildUpdate(p, tc.target, tc.scope, tc.opts...)
			require.Error(t, err)
			if tc.want != nil {
				assert.True(t, errors.Is(err, tc.want), "%v", err)
			}
		})
	}
}

func TestBuildUpdate_LocksRelationOwnersOnPostgres(t *testing.T) {
	r := parsetest.Gadgets(t, onPostgres)
	p, err := r.ParseMergePatch([]byte(`{"tags":["blue"]}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, parse.ByID("g1"), parse.Scope{Tenant: "t1"})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(plan.Owner.SQL, " FOR UPDATE"), plan.Owner.SQL)

	sc, err := r.ParseMergePatch([]byte(`{"name":"n","rank":2}`))
	require.NoError(t, err)
	plan, err = r.BuildUpdate(sc, parse.ByID("g1"), parse.Scope{Tenant: "t1"}, parse.StampSQL("updated_at", "NOW()"))
	require.NoError(t, err)
	assert.Equal(t, `UPDATE "gadgets" SET "name" = $1, "rank" = $2, "updated_at" = NOW() WHERE (("gadgets"."tenant_id" = $3 AND "gadgets"."deleted_at" IS NULL) AND "gadgets"."id" = $4) `+
		`RETURNING "gadgets"."id", "gadgets"."tenant_id", "gadgets"."name", "gadgets"."rank", "gadgets"."version", "gadgets"."updated_at", "gadgets"."deleted_at"`, plan.Owner.SQL)
	assert.Equal(t, []any{"n", int64(2), "t1", "g1"}, plan.Owner.Args)
}

func TestUpdateBatchRefusals(t *testing.T) {
	r := parsetest.UpdateBatchResource(t, onPostgres)
	patch := parsetest.UpdateBatchPatch(t, r, `{"reference":"new"}`)
	for _, test := range []struct {
		name   string
		rows   []parse.RowUpdate
		scope  parse.Scope
		limits parse.UpdateBatchLimits
		opts   []parse.UpdateOption
	}{
		{"duplicate-normalized", []parse.RowUpdate{{Key: parse.Key{int32(1)}, Patch: patch}, {Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"missing-tenant", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{}, parse.UpdateBatchLimits{}, nil},
		{"widened-scope", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1", IncludeDeleted: true}, parse.UpdateBatchLimits{}, nil},
		{"negative-limit", nil, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxRows: -1}, nil},
		{"row-limit", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}, {Key: parse.Key{int64(2)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxRows: 1}, nil},
		{"parameter-limit", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{MaxParameters: 1}, nil},
		{"invalid-key", []parse.RowUpdate{{Key: parse.Key{"not-an-integer"}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"relation", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: parsetest.UpdateBatchPatch(t, r, `{"tags":["x"]}`)}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, nil},
		{"key-write", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, []parse.UpdateOption{parse.Increment("id")}},
		{"tenant-write", []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: patch}}, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{}, []parse.UpdateOption{parse.StampSQL("tenant_id", "'t2'")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, err := r.BuildUpdateBatch(test.rows, test.scope, test.limits, test.opts...)
			require.Error(t, err)
			require.Nil(t, plan)
		})
	}
	empty, err := r.BuildUpdateBatch(nil, parse.Scope{Tenant: "t1"}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	require.Empty(t, empty.Chunks)
}

// A batch's targets are found by their keys' canonical values: the row a
// key names is its target, a key no row holds fails the batch, and a key
// spelled two ways is one row twice.
func TestUpdateBatchCanonicalKeys(t *testing.T) {
	type row struct {
		ID   uuid.UUID `db:"id"`
		Name string    `json:"name" db:"name" parse:"patch"`
	}
	id := uuid.MustParse("018c018c-0123-7123-8123-0123456789ab")
	provider, db := providertest.SQLite(t)
	dbtest.Exec(t, db, `CREATE TABLE uuid_rows (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO uuid_rows VALUES ('`+id.String()+`', 'old')`)
	r, err := parse.NewResource[row](provider, "uuid_rows")
	require.NoError(t, err)
	p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	res, err := r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{id}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
	assert.Equal(t, []row{{ID: id, Name: "new"}}, res.Changed)
	_, err = r.UpdateBatch(t.Context(), []parse.RowUpdate{{Key: parse.Key{uuid.MustParse("018c018c-0123-7123-8123-0123456789ac")}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.ErrorIs(t, err, parse.ErrUpdateBatchTargets)
	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{id}, Patch: p}, {Key: parse.Key{strings.ToUpper(id.String())}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.Error(t, err)
}

// A batch locks its rows in the key's code-point order, under the collation
// every list orders the key by.
func TestUpdateBatchLocksInTheDialectsCodePointOrder(t *testing.T) {
	type row struct {
		Code string `db:"code"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.PostgresDialect{Collation: filter.CollationC}, filter.SQLiteDialect{}} {
		r, err := parse.NewResource[row](providertest.Compile(d), "coded_rows", parse.PrimaryKey("code"))
		require.NoError(t, err)
		p, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
		require.NoError(t, err)
		plan, err := r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{"b"}, Patch: p}, {Key: parse.Key{"a"}, Patch: p}}, parse.Scope{}, parse.UpdateBatchLimits{})
		require.NoError(t, err)
		require.Contains(t, plan.Chunks[0].Current.SQL, " ORDER BY "+d.CodePointOrder(`"coded_rows"."code"`)+d.RowLock())
	}
}

// An update reaches the rows a list in the same scope reads: its WHERE, for
// a filter target, is the list's, the tenant floor, live rows and the
// policy composed by one rule (filter.ComposeConstraints), so a floor that
// rule gains bounds updates as it bounds lists.
func TestBuildUpdate_ScopesRowsAsAListDoes(t *testing.T) {
	s := parse.Scope{Tenant: "t1", Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "rank", Operator: filter.OpGte, Value: 1}}}}

	for _, provider := range []*parse.Provider{onPostgres, onSQLite} {
		t.Run(provider.Name(), func(t *testing.T) {
			r := parsetest.Notes(t, provider)
			req, err := r.ParseList(url.Values{"title": {"one"}})
			require.NoError(t, err)
			p, err := r.ParseMergePatch([]byte(`{"rank":2}`))
			require.NoError(t, err)
			list, err := r.BuildList(req, s)
			require.NoError(t, err)
			update, err := r.BuildUpdate(p, parse.ByFilter(req.Filter()), s, parse.SkipUnchanged())
			require.NoError(t, err)
			require.NotNil(t, update.Current, "SkipUnchanged reads the target rows first")

			// Current selects the rows the update may write, its WHERE's
			// placeholders numbered from the first as the list's are.
			_, where, found := strings.Cut(update.Current.SQL, " WHERE ")
			require.True(t, found, update.Current.SQL)
			where, _, _ = strings.Cut(where, " ORDER BY ")
			assert.True(t, strings.HasPrefix(list.Fragment, "WHERE "+where+" ORDER BY "),
				"update: %s\nlist:   %s", where, list.Fragment)
			require.GreaterOrEqual(t, len(list.Page.Args), len(update.Current.Args))
			assert.Equal(t, update.Current.Args, list.Page.Args[:len(update.Current.Args)])
		})
	}
}

func TestSkipUnchanged_SQL(t *testing.T) {
	at := core.FormatTimestamp(parsetest.SkipAt())
	const columns = `"notes"."id", "notes"."tenant_id", "notes"."title", "notes"."rank", "notes"."version", "notes"."updated_at", "notes"."deleted_at"`
	for _, tc := range []struct {
		provider       *parse.Provider
		owner, current string
	}{
		{onPostgres,
			`UPDATE "notes" SET "rank" = $1, "title" = $2, "updated_at" = $3, "version" = "version" + 1 ` +
				`WHERE (("notes"."tenant_id" = $4 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $5) ` +
				`AND ("rank" IS DISTINCT FROM $1 OR "rank"::text COLLATE "C" IS DISTINCT FROM ($1)::text ` +
				`OR "title" IS DISTINCT FROM $2 OR "title"::text COLLATE "C" IS DISTINCT FROM ($2)::text) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM "notes" WHERE (("notes"."tenant_id" = $1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = $2) FOR UPDATE`},
		{onSQLite,
			`UPDATE "notes" SET "rank" = ?1, "title" = ?2, "updated_at" = ?3, "version" = "version" + 1 ` +
				`WHERE (("notes"."tenant_id" = ?4 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = ?5) ` +
				`AND ("rank" COLLATE BINARY IS NOT ?1 OR "title" COLLATE BINARY IS NOT ?2) ` +
				`RETURNING ` + columns,
			`SELECT ` + columns + ` FROM "notes" WHERE (("notes"."tenant_id" = ?1 AND "notes"."deleted_at" IS NULL) AND "notes"."id" = ?2)`},
	} {
		t.Run(tc.provider.Name(), func(t *testing.T) {
			plan := parsetest.SkipUpdate(t, parsetest.Notes(t, tc.provider), `{"title":"n","rank":2}`, parse.ByID("n1"))
			assert.Equal(t, tc.owner, plan.Owner.SQL)
			assert.Equal(t, []any{int64(2), "n", at, "t1", "n1"}, plan.Owner.Args, "the predicate binds nothing of its own")
			require.NotNil(t, plan.Current)
			assert.Equal(t, tc.current, plan.Current.SQL)
			assert.Equal(t, []any{"t1", "n1"}, plan.Current.Args)
		})
	}
}

// A statement that locks the rows of a target that can hold several takes
// them in key order, so two updates over overlapping rows lock in one order
// and cannot deadlock on each other: Current, a relation-only patch's Owner,
// and Lock, which an unguarded UPDATE of several rows runs first, since the
// UPDATE takes its own locks in the order its plan reads the rows. One row
// needs no order, and SQLite locks no rows. The corpus's ExecutorContract
// case runs the deadlock itself on PostgreSQL (parsepgx's TestCorpus).
func TestBuildUpdate_LocksSeveralRowsInKeyOrder(t *testing.T) {
	pg := parsetest.Notes(t, onPostgres)
	byTitle, err := pg.ParseFilterMap(map[string]any{"title": "one"})
	require.NoError(t, err)
	const ordered = ` ORDER BY "notes"."id" COLLATE pg_c_utf8 FOR UPDATE`
	build := func(t *testing.T, r *parse.Resource[parsetest.Note], body string, target parse.Target, opts ...parse.UpdateOption) *parse.UpdatePlan {
		t.Helper()
		p, err := r.ParseMergePatch([]byte(body))
		require.NoError(t, err)
		plan, err := r.BuildUpdate(p, target, parse.Scope{Tenant: "t1"}, opts...)
		require.NoError(t, err)
		return plan
	}
	for _, tc := range []struct {
		name   string
		target parse.Target
		suffix string
	}{
		{"several keys", parse.ByIDs("n2", "n1"), ordered},
		{"a filter", parse.ByFilter(byTitle), ordered},
		{"one key", parse.ByID("n1"), ") FOR UPDATE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := build(t, pg, `{"title":"x"}`, tc.target, parse.SkipUnchanged()).Current
			require.NotNil(t, current)
			assert.True(t, strings.HasSuffix(current.SQL, tc.suffix), current.SQL)
			owner := build(t, pg, `{"tags":["blue"]}`, tc.target).Owner
			assert.True(t, strings.HasSuffix(owner.SQL, tc.suffix), owner.SQL)

			unguarded := build(t, pg, `{"title":"x"}`, tc.target)
			assert.Nil(t, unguarded.Current)
			if tc.suffix != ordered {
				assert.Nil(t, unguarded.Lock, "one row's UPDATE locks it alone")
				return
			}
			require.NotNil(t, unguarded.Lock)
			assert.True(t, strings.HasPrefix(unguarded.Lock.SQL, `SELECT 1 FROM "notes" WHERE `), unguarded.Lock.SQL)
			assert.True(t, strings.HasSuffix(unguarded.Lock.SQL, tc.suffix), unguarded.Lock.SQL)
			assert.Nil(t, build(t, pg, `{"title":"x"}`, tc.target, parse.SkipUnchanged()).Lock, "Current locks the rows of a guarded update")
		})
	}
	t.Run("sqlite", func(t *testing.T) {
		lite := parsetest.Notes(t, onSQLite)
		current := build(t, lite, `{"title":"x"}`, parse.ByIDs("n2", "n1"), parse.SkipUnchanged()).Current
		require.NotNil(t, current)
		assert.NotContains(t, current.SQL, "ORDER BY")
		assert.Nil(t, build(t, lite, `{"title":"x"}`, parse.ByIDs("n2", "n1")).Lock)
	})
}

func TestSkipUnchanged_LeavesTheDefaultAlone(t *testing.T) {
	r := parsetest.Notes(t, onPostgres)
	p, err := r.ParseMergePatch([]byte(`{"title":"n","rank":2}`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, parse.ByID("n1"), parse.Scope{Tenant: "t1"}, parse.Increment("version"))
	require.NoError(t, err)
	assert.Nil(t, plan.Current)
	assert.NotContains(t, plan.Owner.SQL, "DISTINCT")
}

type pointerKeyed struct {
	ID   *string `json:"id"   db:"id"   parse:"filter"`
	Name string  `json:"name" db:"name" parse:"filter,patch"`
}

// keyedTable holds rows a and b, both named new, on a fresh SQLite
// database; a key column that is no INTEGER PRIMARY KEY holds NULL there.
func keyedTable(t *testing.T) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, db := providertest.SQLite(t)
	dbtest.Exec(t, db, `CREATE TABLE keyed (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO keyed VALUES ('a', 'old'), ('b', 'new')`)
	return p, db
}

// renameSkipping applies {"name":"new"} to target under SkipUnchanged.
func renameSkipping[T any](t *testing.T, r *parse.Resource[T], target func(*parse.Resource[T]) parse.Target) (parse.UpdateResult[T], error) {
	t.Helper()
	rename, err := r.ParseMergePatch([]byte(`{"name":"new"}`))
	require.NoError(t, err)
	return r.Update(t.Context(), rename, target(r), parse.Scope{}, parse.SkipUnchanged())
}

func byName[T any](t *testing.T) func(*parse.Resource[T]) parse.Target {
	return func(r *parse.Resource[T]) parse.Target {
		f, err := r.ParseFilterMap(map[string]any{"name": "new"})
		require.NoError(t, err)
		return parse.ByFilter(f)
	}
}

// Rows scanned apart hold distinct pointers to one key: the update splits
// the rows it read by their keys' values.
func TestUnchanged_ComparesPointerKeysByValue(t *testing.T) {
	p, db := keyedTable(t)
	r, err := parse.NewResource[pointerKeyed](p, "keyed")
	require.NoError(t, err)
	res, err := renameSkipping(t, r, func(*parse.Resource[pointerKeyed]) parse.Target { return parse.ByIDs("a", "b") })
	require.NoError(t, err)
	require.Len(t, res.Changed, 1)
	assert.Equal(t, "a", *res.Changed[0].ID)
	require.Len(t, res.Unchanged, 1)
	assert.Equal(t, "b", *res.Unchanged[0].ID)

	dbtest.Exec(t, db, `INSERT INTO keyed VALUES (NULL, 'new')`)
	_, err = renameSkipping(t, r, byName[pointerKeyed](t))
	assert.ErrorContains(t, err, "has no key")
}

// optionalCode is a key codec that writes NULL for its zero value.
type optionalCode string

func (c optionalCode) Value() (driver.Value, error) {
	if c == "" {
		return nil, nil
	}
	return string(c), nil
}

func (c *optionalCode) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*c = ""
	case string:
		*c = optionalCode(v)
	default:
		return fmt.Errorf("optionalCode: %T", src)
	}
	return nil
}

type valuerKeyed struct {
	ID   optionalCode `json:"id"   db:"id"   parse:"filter"`
	Name string       `json:"name" db:"name" parse:"filter,patch"`
}

// A key column the database holds as NULL — a Valuer that writes nil, as
// a nil pointer — is a row without a key.
func TestUnchanged_RefusesARowWhoseKeyIsNull(t *testing.T) {
	p, db := keyedTable(t)
	r, err := parse.NewResource[valuerKeyed](p, "keyed")
	require.NoError(t, err)
	res, err := renameSkipping(t, r, func(*parse.Resource[valuerKeyed]) parse.Target { return parse.ByIDs("a", "b") })
	require.NoError(t, err)
	assert.Equal(t, []valuerKeyed{{ID: "a", Name: "new"}}, res.Changed)
	assert.Equal(t, []valuerKeyed{{ID: "b", Name: "new"}}, res.Unchanged)

	dbtest.Exec(t, db, `INSERT INTO keyed VALUES (NULL, 'new')`)
	_, err = renameSkipping(t, r, byName[valuerKeyed](t))
	require.ErrorContains(t, err, "keyed: a row has no key: key column id holds no value")
}

// A patch of document members is planned to fail on a row it does not apply
// to: Current locks the targets, Conflict names the failing operation, and
// Owner's WHERE holds the same precondition.
func TestBuildUpdate_MembersPlanAConflictRead(t *testing.T) {
	r := parsetest.GadgetAttrsResource(t, onPostgres)
	parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"red"}]`))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(parsed, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	require.NotNil(t, plan.Current)
	require.NotNil(t, plan.Conflict)
	assert.Contains(t, plan.Owner.SQL, `#> $3::text[]) IS NOT NULL`)
	assert.Contains(t, plan.Conflict.SQL, `SELECT CASE WHEN NOT (("attrs"::jsonb #> $2::text[]) IS NOT NULL) THEN 0 END`)

	whole, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs","value":{"color":"red"}}]`))
	require.NoError(t, err)
	plan, err = r.BuildUpdate(whole, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	assert.Nil(t, plan.Conflict, "a whole-document write needs nothing of the stored row")
}

// The row Conflict reads fails the update with the operation it fails,
// before anything is written.
func TestUpdate_MembersConflictNamesTheFailingOperation(t *testing.T) {
	provider, db := providertest.SQLite(t)
	dbtest.Exec(t, db, `CREATE TABLE gadget_attrs (id INTEGER PRIMARY KEY, attrs TEXT NOT NULL)`,
		`INSERT INTO gadget_attrs VALUES (1, '{}')`)
	r := parsetest.GadgetAttrsResource(t, provider)
	parsed, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs/color","value":"red"}]`))
	require.NoError(t, err)
	_, err = r.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{})
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%v is no ParseError", err)
	assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
	assert.Equal(t, "/attrs/color", pe.Path)
	assert.Equal(t, []string{"{}"}, dbtest.QueryRows[string](t, db, `SELECT attrs FROM gadget_attrs`), "no row is written")
}

// A batch compiles its rows' documents whole: a member operation the stored
// row decides is refused, one the row's own patch settles is not.
func TestBuildUpdateBatch_MembersTheStoredRowDecides(t *testing.T) {
	r := parsetest.GadgetAttrsResource(t, onPostgres)
	member, err := r.ParsePatch([]byte(`[{"op":"remove","path":"/attrs/old"}]`))
	require.NoError(t, err)
	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: member}}, parse.Scope{}, parse.UpdateBatchLimits{})
	pe, ok := errors.AsType[*core.ParseError](err)
	require.True(t, ok, "%v is no ParseError", err)
	assert.Equal(t, patch.ECodeOpNotSQLCompilable.String(), pe.Code)

	settled, err := r.ParsePatch([]byte(`[{"op":"replace","path":"/attrs","value":{}},{"op":"add","path":"/attrs/new","value":1}]`))
	require.NoError(t, err)
	_, err = r.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: settled}}, parse.Scope{}, parse.UpdateBatchLimits{})
	require.NoError(t, err)
}

type gadgetSpec struct {
	W     int               `json:"w"`
	Label string            `json:"label,omitempty"`
	Count int               `json:"count,string"`
	Tags  map[string]string `json:"tags"`
}

type typedGadget struct {
	ID    int64       `json:"id"    db:"id"`
	Spec  gadgetSpec  `json:"spec"  db:"spec"  parse:"patch"`
	Sizes map[int]int `json:"sizes" db:"sizes" parse:"patch"`
}

// A member of a typed document is held to the field's Go type when the
// patch parses, for SQL and ApplyValidatedPatch alike: an exact member name,
// a map key as encoding/json writes it, and a value that decodes as the
// member, tag options included.
func TestParsePatch_TypedDocumentMembers(t *testing.T) {
	r, err := parse.NewResource[typedGadget](onPostgres, "typed_gadgets", parse.CustomizeSchemas(func(s parse.Schemas) error {
		ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove}
		for path, column := range map[string]string{"/spec/**": "spec", "/sizes/*": "sizes"} {
			err := s.Patch.AddPath(path, &patch.PathSchema{AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: column})
			if err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		ops  string
		ok   bool
	}{
		{"a struct member", `[{"op":"replace","path":"/spec/w","value":3}]`, true},
		{"a member of a nested map", `[{"op":"add","path":"/spec/tags/k","value":"v"}]`, true},
		{"a member with the string option", `[{"op":"replace","path":"/spec/count","value":"4"}]`, true},
		{"an integer key", `[{"op":"add","path":"/sizes/7","value":1}]`, true},
		{"a remove of a struct member", `[{"op":"remove","path":"/spec/label"}]`, true},
		{"a struct member in another case", `[{"op":"replace","path":"/spec/W","value":3}]`, false},
		{"a struct member it does not declare", `[{"op":"add","path":"/spec/extra","value":3}]`, false},
		{"a value of another type", `[{"op":"replace","path":"/spec/w","value":"three"}]`, false},
		{"a number for a member with the string option", `[{"op":"replace","path":"/spec/count","value":4}]`, false},
		{"a map value of another type", `[{"op":"add","path":"/spec/tags/k","value":1}]`, false},
		{"a member of a scalar", `[{"op":"add","path":"/spec/w/x","value":1}]`, false},
		{"a key encoding/json does not decode", `[{"op":"add","path":"/sizes/x","value":1}]`, false},
		{"a key encoding/json writes otherwise", `[{"op":"remove","path":"/sizes/07"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ParsePatch([]byte(tc.ops))
			if tc.ok {
				require.NoError(t, err)
				return
			}
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
			require.NotNil(t, pe.Index)
			assert.Zero(t, *pe.Index)
		})
	}
}

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
	types := parse.MustResource[clockType](onPostgres, "clock_types")
	devices, err := parse.NewResource[clockDevice](onPostgres, "clock_devices", parse.ParserOptions(core.WithClock(stepping), core.WithMaxCost(1<<10)))
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
		plan, err := devices.BuildList(req, scope)
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
	types := parse.MustResource[clockType](onPostgres, "clock_types")
	items, err := parse.NewResource[clockItem](onPostgres, "clock_items", parse.ParserOptions(core.WithClock(stepping)))
	require.NoError(t, err)
	items.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("clock_item_types", "item_id", "type_id"), parse.PolicyScoped(), parse.Writable()))
	scope := parse.Scope{
		Policy: &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+1h"}}},
		Related: map[string]*filter.PolicyConstraints{
			"clock_types": {Constraints: []*filter.PolicyConstraint{{Field: "created_at", Operator: filter.OpLt, Value: "now+2h"}}},
		},
	}

	t.Run("update", func(t *testing.T) {
		p, err := items.ParseMergePatch([]byte(`{"name":"x","shared_types":["k1"]}`))
		require.NoError(t, err)
		plan, err := items.BuildUpdate(p, parse.ByID("i1"), scope, parse.SkipUnchanged())
		require.NoError(t, err)
		read := at
		require.NotNil(t, plan.Current)
		assert.Equal(t, []time.Time{read.Add(time.Hour)}, timestamps(t, plan.Owner.Args), "the policy's now is the clock's one reading")
		assert.Equal(t, []time.Time{read.Add(time.Hour)}, timestamps(t, plan.Current.Args), "Current admits the rows Owner does")
	})
	t.Run("membership change", func(t *testing.T) {
		p, db := providertest.SQLite(t)
		types := parse.MustResource[clockType](p, "clock_types")
		items, err := parse.NewResource[clockItem](p, "clock_items", parse.ParserOptions(core.WithClock(stepping)))
		require.NoError(t, err)
		items.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("clock_item_types", "item_id", "type_id"), parse.PolicyScoped(), parse.Writable()))
		parsed, err := items.ParseMergePatch([]byte(`{"name":"x","shared_types":["k0","k1"]}`))
		require.NoError(t, err)
		// The update's plan takes the clock's next reading. k0 is visible at
		// that instant (created before now+2h); k1 only at a later reading.
		instant := at.Add(time.Hour)
		dbtest.Exec(t, db,
			`CREATE TABLE clock_items (id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
			`CREATE TABLE clock_types (id TEXT PRIMARY KEY, created_at TEXT NOT NULL)`,
			`CREATE TABLE clock_item_types (item_id TEXT NOT NULL, type_id TEXT NOT NULL, PRIMARY KEY (item_id, type_id))`,
		)
		_, err = db.ExecContext(t.Context(), `INSERT INTO clock_items (id, name, created_at) VALUES ('i1', 'old', ?1)`,
			core.FormatTimestamp(instant.Add(-time.Hour)))
		require.NoError(t, err)
		_, err = db.ExecContext(t.Context(), `INSERT INTO clock_types (id, created_at) VALUES ('k0', ?1), ('k1', ?2)`,
			core.FormatTimestamp(instant.Add(time.Hour)), core.FormatTimestamp(instant.Add(150*time.Minute)))
		require.NoError(t, err)

		res, err := items.Update(t.Context(), parsed, parse.ByID("i1"), scope)
		require.NoError(t, err)
		assert.Equal(t, instant, at, "the update read the clock once")
		assert.Equal(t, int64(1), res.RelationRows)
		assert.Equal(t, []string{"i1:k0"}, dbtest.QueryRows[string](t, db, `SELECT item_id || ':' || type_id FROM clock_item_types ORDER BY 1`),
			"a membership change admits the related rows at the plan's instant")
	})
	t.Run("batch", func(t *testing.T) {
		rename := func(name string) *patch.ParsedPatch {
			p, err := items.ParseMergePatch([]byte(`{"name":"` + name + `"}`))
			require.NoError(t, err)
			return p
		}
		rows := []parse.RowUpdate{{Key: parse.Key{"i1"}, Patch: rename("x")}, {Key: parse.Key{"i2"}, Patch: rename("y")}}
		plan, err := items.BuildUpdateBatch(rows, scope, parse.UpdateBatchLimits{}, parse.SkipUnchanged())
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

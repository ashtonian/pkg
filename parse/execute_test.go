//go:build unit

package parse_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// A statement's failure names its resource's table once, and a
// transaction's own statement names none.
func TestStatementFailed(t *testing.T) {
	cause := assert.AnError
	for _, tc := range []struct {
		name, table, statement, want string
	}{
		{"a resource's statement", "widgets", "include tags", "parse: widgets: include tags: " + cause.Error()},
		{"a transaction's own statement", "", "commit", "commit: " + cause.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, parse.StatementFailed(tc.table, tc.statement, nil))
			err := parse.StatementFailed(tc.table, tc.statement, cause)
			require.ErrorIs(t, err, cause)
			require.EqualError(t, err, tc.want)
			failed, ok := errors.AsType[*parse.StatementError](err)
			require.True(t, ok)
			assert.Equal(t, tc.table, failed.Table)
			assert.Equal(t, tc.statement, failed.Statement)
		})
	}
}

// A resource scoped to a wrapped transaction of its kind of provider is on
// its provider and is a view; one of another kind of provider, or none, is
// refused before it is used. A transaction a provider began, or another
// provider of the kind, needs a database: the corpus's
// In_TransactionsOfItsProvider.
func TestIn_RefusesATransactionOfAnotherKind(t *testing.T) {
	r := parsetest.ExecItems(onSQLite)
	assert.Same(t, onSQLite, r.Provider())

	scoped, err := r.In(parse.NewTx(backend.Transaction{Provider: "sqlite"}))
	require.NoError(t, err)
	assert.Same(t, onSQLite, scoped.Provider())
	err = scoped.Relate("again", parse.HasMany(r, "id"))
	require.ErrorContains(t, err, `parse: exec_items: relation "again": a view (WithPaths, In) declares no relations`)

	_, err = r.In(parse.NewTx(backend.Transaction{Provider: "postgres"}))
	require.ErrorIs(t, err, parse.ErrProviderMismatch)
	require.ErrorContains(t, err, "parse: exec_items: postgres transaction for a resource on sqlite")
	_, err = r.In(nil)
	require.ErrorContains(t, err, "parse: exec_items: nil transaction")

	devices := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "devices")
	_, err = devices.In(parse.NewTx(backend.Transaction{Provider: "sqlite"}))
	require.ErrorIs(t, err, parse.ErrProviderMismatch)
	require.ErrorContains(t, err, "parse: devices: sqlite transaction for a resource on postgres")
}

type faultLabel struct {
	ID       string `json:"id"        db:"id"        parse:"filter"`
	TenantID string `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Name     string `json:"name"      db:"name"      parse:"filter"`
}

type faultDevice struct {
	ID       int    `json:"id"        db:"id"        parse:"filter"`
	TenantID string `json:"-"         db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter"`
}

type faultBoard struct {
	ID   int    `json:"id"   db:"id"   parse:"filter"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type unsignedRow struct {
	ID string `json:"id" db:"id"`
	N  uint64 `json:"n"  db:"n"  parse:"filter"`
}

// What the server supplies and cannot build — a policy-scoped relation
// without its policy, a tenant-scoped pair table without a tenant, a policy
// naming a field the resource lacks or holding a value of the wrong shape —
// is the server's fault: a plain error matching its sentinel, which
// parsehttp.WriteError declines without writing, so the handler logs it and
// answers its own 500 instead of telling the client its request was invalid.
func TestBuildList_ServerFaultsAreNoParseError(t *testing.T) {
	labels := parse.MustResource[faultLabel](onPostgres, "labels", parse.TenantColumn("tenant_id"))
	devices := parse.MustResource[faultDevice](onPostgres, "devices", parse.TenantColumn("tenant_id"))
	devices.MustRelate("labels", parse.HasMany(labels, "device_id", parse.PolicyScoped(), parse.Indexed()))
	boards := parse.MustResource[faultBoard](onPostgres, "boards")
	boards.MustRelate("labels", parse.ManyToMany(labels,
		parse.Junction{Table: "board_labels", Source: []string{"board_id"}, Target: []string{"label_id"}, Tenant: "tenant_id"}, parse.Indexed()))

	build := func(t *testing.T, r interface {
		ParseListInput(parse.ListInput) (*parse.ListRequest, error)
		BuildList(*parse.ListRequest, parse.Scope) (*parse.ListPlan, error)
	}, f string, scope parse.Scope) error {
		t.Helper()
		req, err := r.ParseListInput(parse.ListInput{Filter: f})
		require.NoError(t, err, "the request is valid")
		_, err = r.BuildList(req, scope)
		return err
	}
	policy := func(c *filter.PolicyConstraint) *filter.PolicyConstraints {
		constraints := &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{c}}
		return constraints
	}
	for _, tt := range []struct {
		name     string
		err      func(t *testing.T) error
		sentinel error
	}{
		{"a policy-scoped relation built without the target's policy", func(t *testing.T) error {
			return build(t, devices, `{"labels":{"$some":{"name":"x"}}}`, parse.Scope{Tenant: "t1"})
		}, filter.ErrRelatedPolicyRequired},
		{"a tenant-scoped pair table on a resource built without a tenant", func(t *testing.T) error {
			return build(t, boards, `{"labels":"l1"}`, parse.Scope{})
		}, filter.ErrRelationTenantRequired},
		{"a policy naming a field the resource lacks", func(t *testing.T) error {
			return build(t, devices, `{"name":"a"}`, parse.Scope{Tenant: "t1", Policy: policy(&filter.PolicyConstraint{Field: "owner_org", Operator: filter.OpEq, Value: "o"})})
		}, filter.ErrPolicyInvalid},
		{"a policy $in with a scalar", func(t *testing.T) error {
			return build(t, devices, `{"name":"a"}`, parse.Scope{Tenant: "t1", Policy: policy(&filter.PolicyConstraint{Field: "name", Operator: filter.OpIn, Value: "a"})})
		}, filter.ErrPolicyInvalid},
		{"a policy operator the field's type does not take", func(t *testing.T) error {
			return build(t, devices, `{"name":"a"}`, parse.Scope{Tenant: "t1", Policy: policy(&filter.PolicyConstraint{Field: "id", Operator: filter.OpLike, Value: "a%"})})
		}, filter.ErrPolicyInvalid},
		{"a policy negating an empty group", func(t *testing.T) error {
			return build(t, devices, `{"name":"a"}`, parse.Scope{Tenant: "t1", Policy: &filter.PolicyConstraints{And: []*filter.PolicyConstraints{{Not: true}}}})
		}, filter.ErrPolicyInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err(t)
			require.ErrorIs(t, err, tt.sentinel)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError, "%v", err)

			w := httptest.NewRecorder()
			assert.False(t, parsehttp.WriteError(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/devices", nil), err))
			assert.Zero(t, w.Body.Len(), "nothing is written")
			assert.Empty(t, w.Header())
		})
	}
}

// A request value the dialect's integers cannot hold is the request's E2073,
// as a typed field's Go kind refuses one: SQLite's integers end at int64.
func TestBuildList_AnUnsignedOperandSQLiteCannotHold(t *testing.T) {
	rows := parse.MustResource[unsignedRow](onSQLite, "rows")
	numeric := parse.MustResource[unsignedRow](onPostgres, "rows")
	for _, f := range []string{
		`{"n":18446744073709551615}`,
		`{"n":{"$gt":9223372036854775808}}`,
		`{"n":{"$in":[1,18446744073709551615]}}`,
	} {
		req, err := rows.ParseListInput(parse.ListInput{Filter: f})
		require.NoError(t, err, f)
		_, err = rows.BuildList(req, parse.Scope{})
		require.ErrorIs(t, err, filter.ECodeValueTypeMismatch, f)
		pe, ok := errors.AsType[*core.ParseError](err)
		require.True(t, ok, f)
		assert.Equal(t, "n", pe.Details[core.DetailField], f)
		assert.Contains(t, pe.Message, "exceeds SQLite's 64-bit signed integers", f)

		req, err = numeric.ParseListInput(parse.ListInput{Filter: f})
		require.NoError(t, err, f)
		_, err = numeric.BuildList(req, parse.Scope{})
		assert.NoError(t, err, "a numeric column holds it: %s", f)
	}
}

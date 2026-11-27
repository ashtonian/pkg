//go:build unit || integration

package parse_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/patch"
)

func TestDecimalRequestContract(t *testing.T) {
	for _, tt := range []struct {
		provider *parse.Provider
		amount   string
		fee      string
		order    string
	}{
		{onPostgres, `"ledger_lines"."amount" > $`, `"ledger_lines"."fee" <= $`, `"ledger_lines"."amount" DESC`},
		{onSQLite, `parse_decimal_key("ledger_lines"."amount") > parse_decimal_key(?`, `parse_decimal_key("ledger_lines"."fee") <= parse_decimal_key(?`, `parse_decimal_key("ledger_lines"."amount") DESC`},
	} {
		resource := parse.MustResource[parsetest.LedgerLine](tt.provider, "ledger_lines")
		query, err := resource.ParseList(url.Values{
			"filter":  {`{"amount":{"$gt":"9.50"}}`},
			"fee_lte": {"1.0"},
			"sort":    {"-amount,id"},
		})
		require.NoError(t, err)
		plan, err := resource.BuildList(query, parse.Scope{})
		require.NoError(t, err)
		name := tt.provider.Name()
		assert.Contains(t, plan.Page.SQL, tt.amount, name)
		assert.Contains(t, plan.Page.SQL, tt.fee, name)
		assert.Contains(t, plan.Page.SQL, tt.order, name)
		assert.Subset(t, plan.Page.Args, []any{"9.5", "1"}, name)
	}

	resource := parse.MustResource[parsetest.LedgerLine](onSQLite, "ledger_lines")

	for _, bad := range []url.Values{
		{"filter": {`{"amount":"ten"}`}},
		{"amount_gt": {"1e5000"}},
	} {
		_, err := resource.ParseList(bad)
		var pe *core.ParseError
		require.True(t, errors.As(err, &pe), "%v: %v", bad, err)
		assert.Equal(t, filter.ECodeValueTypeMismatch.String(), pe.Code, bad)
	}
}

func TestDecimalMergePatch(t *testing.T) {
	resource := parse.MustResource[parsetest.LedgerLine](onSQLite, "ledger_lines")
	tests := []struct {
		body       string
		wantAmount string
		wantFee    *string
	}{
		{`{"amount":"10.50","fee":"0.25"}`, "10.5", new("0.25")},
		{`{"amount":10.5,"fee":2}`, "10.5", new("2")},
		{`{"amount":"0.10000000000000001","fee":null}`, "0.10000000000000001", nil},
		{`{"amount":"-1.5e1"}`, "-15", new("3")},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			p, err := resource.ParseMergePatch([]byte(tt.body))
			require.NoError(t, err)
			target, err := resource.ApplyValidatedPatch(p, parsetest.LedgerLine{ID: "one", Fee: new(decimal.RequireFromString("3"))})
			require.NoError(t, err)
			assert.Equal(t, tt.wantAmount, target.Amount.String())
			if tt.wantFee == nil {
				assert.Nil(t, target.Fee)
			} else {
				require.NotNil(t, target.Fee)
				assert.Equal(t, *tt.wantFee, target.Fee.String())
			}

			update, err := resource.BuildUpdate(p, parse.ByID("one"), parse.Scope{})
			require.NoError(t, err)
			assert.Contains(t, update.Owner.Args, tt.wantAmount, "SET binds the canonical text")
		})
	}

	for _, body := range []string{`{"amount":"ten"}`, `{"amount":true}`, `{"amount":null}`, `{"fee":"1e5000"}`} {
		_, err := resource.ParseMergePatch([]byte(body))
		var pe *core.ParseError
		require.True(t, errors.As(err, &pe), "%s: %v", body, err)
		assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code, body)
	}
}

// The wire form a decimal marshals to is the form a patch and a filter
// accept back.
func TestDecimalWireRoundTrip(t *testing.T) {
	resource := parse.MustResource[parsetest.LedgerLine](onSQLite, "ledger_lines")
	wire, err := json.Marshal(map[string]any{"amount": decimal.RequireFromString("10.25")})
	require.NoError(t, err)
	require.JSONEq(t, `{"amount":"10.25"}`, string(wire))
	p, err := resource.ParseMergePatch(wire)
	require.NoError(t, err)
	target, err := resource.ApplyValidatedPatch(p, parsetest.LedgerLine{})
	require.NoError(t, err)
	assert.True(t, decimal.RequireFromString("10.25").Equal(target.Amount))

	_, err = resource.ParseList(url.Values{"filter": {string(wire)}})
	require.NoError(t, err)
}

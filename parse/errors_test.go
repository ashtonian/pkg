//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type isEntity struct {
	ID   string `json:"id"   parse:"filter"`
	Name string `json:"name" parse:"filter,patch"`
}

// Code identity survives wrapping alongside cause, path and operation index.
func TestParseError_IsByCode(t *testing.T) {
	p, err := parse.NewResource[isEntity](onSQLite, "is_entities")
	require.NoError(t, err)

	_, err = p.ParseFilter(`{"absent":1}`)
	wrapped := fmt.Errorf("list devices: %w", err)
	assert.ErrorIs(t, wrapped, filter.ECodeUnknownField)
	assert.NotErrorIs(t, wrapped, filter.ECodeInvalidOperator)

	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":"/nope","value":1}]`))
	var pe *core.ParseError
	require.ErrorAs(t, fmt.Errorf("update: %w", err), &pe)
	assert.ErrorIs(t, err, patch.ECodePatchFieldNotFound)
	assert.Equal(t, "/nope", pe.Path)
	require.NotNil(t, pe.Index)
	assert.Equal(t, 0, *pe.Index)

	cause := errors.New("decoder failed")
	coded := core.NewError(filter.ECodeInvalidJSON, "", core.WithCause(cause))
	assert.ErrorIs(t, coded, cause, "the cause stays reachable")
	assert.ErrorIs(t, coded, filter.ECodeInvalidJSON)
	uncoded, alsoUncoded := core.NewError("", ""), core.NewError("", "")
	assert.NotErrorIs(t, uncoded, alsoUncoded, "an empty code matches nothing")
}

// Each root sentinel is reached with errors.Is from the call that returns
// it, under one prefix naming the package and the table once; the sentinel
// itself carries none.
func TestRootSentinels_OnePrefixShape(t *testing.T) {
	type device struct {
		ID       string `json:"id"        db:"id"        parse:"filter"`
		TenantID int64  `json:"tenant_id" db:"tenant_id"`
		Name     string `json:"name"      db:"name"      parse:"filter,patch"`
	}
	devices := parse.MustResource[device](onSQLite, "devices", parse.TenantColumn("tenant_id"))
	other := parse.MustResource[device](onSQLite, "devices", parse.TenantColumn("tenant_id"))
	rename, err := devices.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	foreign, err := other.ParseMergePatch([]byte(`{"name":"x"}`))
	require.NoError(t, err)
	tenant := parse.Scope{Tenant: "1"}
	reg := parse.NewSchemaRegistry()
	parse.MustRegisterResource(reg, devices)
	// A batch's targets are judged against the rows its Current reads, so
	// ErrUpdateBatchTargets comes from a database: the corpus holds it to
	// this shape (UpdateBatch_MissingTargetNamesItsResourceOnce).
	run := func(build func() error) error { return build() }

	for _, tt := range []struct {
		name string
		err  error
		want error
	}{
		{"a list without a tenant", run(func() error { _, err := devices.BuildList(nil, parse.Scope{}); return err }), parse.ErrTenantRequired},
		{"an update without a tenant", run(func() error { _, err := devices.BuildUpdate(rename, parse.ByID("a"), parse.Scope{}); return err }), parse.ErrTenantRequired},
		{"a tenant the column cannot hold", run(func() error { _, err := devices.BuildList(nil, parse.Scope{Tenant: "abc"}); return err }), parse.ErrTenantInvalid},
		{"no target", run(func() error { _, err := devices.BuildUpdate(rename, parse.Target{}, tenant); return err }), parse.ErrNoTarget},
		{"a key of another type", run(func() error { _, err := devices.BuildUpdate(rename, parse.ByID(7), tenant); return err }), parse.ErrKeyInvalid},
		{"one of several keys of another type", run(func() error { _, err := devices.BuildUpdate(rename, parse.ByIDs("a", 7), tenant); return err }), parse.ErrKeyInvalid},
		{"a key holding U+0000", run(func() error { _, err := devices.BuildUpdate(rename, parse.ByID("a\x00b"), tenant); return err }), parse.ErrKeyInvalid},
		{"a key of invalid UTF-8", run(func() error { _, err := devices.BuildUpdate(rename, parse.ByIDs("a", "\xff"), tenant); return err }), parse.ErrKeyInvalid},
		{"a batch key holding U+0000", run(func() error {
			_, err := devices.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{"a\x00"}, Patch: rename}}, tenant, parse.UpdateBatchLimits{})
			return err
		}), parse.ErrKeyInvalid},
		{"a batch key of another type", run(func() error {
			_, err := devices.BuildUpdateBatch([]parse.RowUpdate{{Key: parse.Key{7}, Patch: rename}}, tenant, parse.UpdateBatchLimits{})
			return err
		}), parse.ErrKeyInvalid},
		{"a widening scope", run(func() error {
			_, err := devices.BuildUpdate(rename, parse.ByID("a"), parse.Scope{Tenant: "1", IncludeDeleted: true})
			return err
		}), parse.ErrUpdateScope},
		{"another resource's patch", run(func() error { _, err := devices.BuildUpdate(foreign, parse.ByID("a"), tenant); return err }), parse.ErrForeign},
		{"a taken name", parse.RegisterResource(reg, other), parse.ErrRegistered},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorIs(t, tt.err, tt.want)
			msg := tt.err.Error()
			assert.True(t, strings.HasPrefix(msg, "parse: devices: "), msg)
			assert.Equal(t, 1, strings.Count(msg, "parse:"), msg)
			assert.Equal(t, 1, strings.Count(msg, "devices"), msg)
			assert.NotContains(t, tt.want.Error(), "parse:")
			_, isParseError := errors.AsType[*core.ParseError](tt.err)
			assert.False(t, isParseError, "the server's mistake, never the request's")
		})
	}
}

func TestParseCodesCatalog(t *testing.T) {
	codes := parse.Codes()
	require.NotEmpty(t, codes)
	seen := make(map[parse.Code]bool)
	for _, code := range codes {
		assert.False(t, seen[code], "duplicate code %s", code)
		seen[code] = true
		assert.NotEmpty(t, code.ErrorMessage(), code)
		assert.Contains(t, []int{400, 404, 422, 500}, code.ErrorHTTPStatus(), code)
		assert.True(t, code.IsErrorUserFacing(), code)
	}
	assert.Contains(t, seen, parse.Code("E2061"))
	assert.Contains(t, seen, parse.Code("E2064"))
}

func TestCodeCatalog_MatchesItsWireSnapshot(t *testing.T) {
	type entry struct {
		Code    string `json:"code"`
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	codes := parse.Codes()
	slices.Sort(codes)
	got := make([]entry, len(codes))
	for i, code := range codes {
		got[i] = entry{code.String(), code.ErrorHTTPStatus(), code.ErrorMessage()}
	}
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/error_codes.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(encoded))
}

func TestParseError_AnswersItsCodesStatus(t *testing.T) {
	for _, code := range parse.Codes() {
		assert.Equal(t, code.ErrorHTTPStatus(), core.NewError(code, "").ErrorHTTPStatus(), code)
	}
	assert.Equal(t, http.StatusUnprocessableEntity, core.NewError("E9999", "").ErrorHTTPStatus())
	assert.Equal(t, http.StatusBadRequest, core.NewError(filter.ECodeInvalidUUID, "").ErrorHTTPStatus())
}

// tagMarker is a tag value an error must not repeat: reflect writes an
// unnamed struct type with its fields' tags, values included.
const tagMarker = "tag-value-marker"

// markedRow is an unnamed struct type, as a row type may be.
type markedRow = struct {
	ID string `json:"id" db:"id" note:"tag-value-marker"`
}

type markedDocRow struct {
	ID  string `json:"id" db:"id"`
	Doc struct {
		Kind string `json:"kind" note:"tag-value-marker"`
	} `json:"doc" db:"doc" parse:"patch"`
}

type markedFilterRow struct {
	ID   string `json:"id" db:"id"`
	Meta struct {
		X int `json:"x" note:"tag-value-marker"`
	} `json:"meta" db:"meta" parse:"filter"`
}

// An error naming a row, document, patch-target or filter-field type names
// it as gowalker.TypeText writes it: without its fields' tags.
func TestTypeText_ErrorsLeaveOutTagValues(t *testing.T) {
	docs := func(t *testing.T) *parse.Resource[markedDocRow] {
		t.Helper()
		r, err := parse.NewResource[markedDocRow](onPostgres, "docs", parse.ColumnType("doc", core.SQLJSONB), parse.CustomizeSchemas(func(s parse.Schemas) error {
			return s.Patch.AddPath("/doc/*", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeAny, SQLColumn: "doc"})
		}))
		require.NoError(t, err)
		return r
	}
	for _, tt := range []struct {
		name string
		err  func(t *testing.T) error
		want string
	}{
		{
			name: "row type",
			err: func(t *testing.T) error {
				_, err := parse.NewResource[markedRow](onPostgres, "rows", parse.TenantColumn("tenant_id"))
				return err
			},
			want: `tenant column "tenant_id" is not a column of struct { ID string }`,
		},
		{
			name: "stored document type",
			err: func(t *testing.T) error {
				scanner, err := docs(t).RowScanner()
				require.NoError(t, err)
				_, err = scanner.Scan(scanStrings(`{"Kind":"x"}`))
				return err
			},
			want: `JSON member "/Kind" is noncanonical for struct { Kind string }; use "kind"`,
		},
		{
			name: "patch target type",
			err: func(t *testing.T) error {
				_, err := docs(t).ParsePatch([]byte(`[{"op":"replace","path":"/doc/nope","value":1}]`))
				return err
			},
			want: `struct { Kind string } has no member "nope"`,
		},
		{
			name: "filter field type",
			err: func(t *testing.T) error {
				_, err := parse.NewResource[markedFilterRow](onPostgres, "filters", parse.ColumnType("meta", core.SQLDate))
				return err
			},
			want: `column "meta" declares a civil date; use core.Date, not struct { X int }`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.err(t)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), tagMarker)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

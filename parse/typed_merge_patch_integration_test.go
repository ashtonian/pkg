//go:build integration

package parse_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/parsehttp"
)

// mergePrefs is a typed document as the TypeScript SDK's types.test.ts
// declares it: City, Units and Note always written (Note as null when
// nil), Label left out when empty.
type mergePrefs struct {
	City  string  `json:"city"`
	Units string  `json:"units"`
	Label string  `json:"label,omitempty"`
	Note  *string `json:"note"`
}

type mergePrefsRow struct {
	ID    string     `json:"id"    db:"id"`
	Prefs mergePrefs `json:"prefs" db:"prefs" parse:"patch"`
}

func TestTypedMergePatchPreservesSiblings(t *testing.T) {
	runSQLBackends(t, runTypedMergePatch)
}

// runTypedMergePatch serves the merge patches the TypeScript SDK's typed
// calls send (sdk/ts/src/patch/types.test.ts) as a parse server does:
// parsehttp.ParsePatch reads the body, BuildUpdate and the dialect's
// executor write it. A partial document changes the members it names and
// keeps its siblings, in the row the update returns, in the stored row and
// in ApplyValidatedPatch alike. null removes a member the document may
// leave out and is refused on one its type always writes, which is the
// rule the SDK's MergeDocument type states.
func runTypedMergePatch(t *testing.T, b sqlBackend) {
	open := b.open
	d := b.dialect
	docType := b.columnType(core.TypeInfo{Type: core.FieldTypeJSON})
	r, err := parse.NewResource[mergePrefsRow]("merge_prefs")
	require.NoError(t, err)
	initial := mergePrefsRow{ID: "one", Prefs: mergePrefs{City: "York", Units: "metric", Label: "home"}}

	for _, tc := range []struct {
		name, body string
		want       mergePrefs
	}{
		{"one member, its siblings kept", `{"prefs":{"city":"Bath"}}`, mergePrefs{City: "Bath", Units: "metric", Label: "home"}},
		{"a complete document", `{"prefs":{"city":"Bath","units":"imperial","label":"work"}}`, mergePrefs{City: "Bath", Units: "imperial", Label: "work"}},
		{"a member the document may leave out, removed", `{"prefs":{"label":null}}`, mergePrefs{City: "York", Units: "metric"}},
		{"a member written as null, given a value", `{"prefs":{"note":"call"}}`, mergePrefs{City: "York", Units: "metric", Label: "home", Note: new("call")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := open(t)
			_, err := db.ExecContext(t.Context(), `CREATE TABLE merge_prefs (id TEXT PRIMARY KEY, prefs `+docType+` NOT NULL)`)
			require.NoError(t, err)
			insert := `INSERT INTO merge_prefs VALUES (?, ?)`
			if isPostgres(d) {
				insert = `INSERT INTO merge_prefs VALUES ($1, $2)`
			}
			stored, err := json.Marshal(initial.Prefs)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), insert, initial.ID, string(stored))
			require.NoError(t, err)

			parsed, err := parsehttp.ParsePatch(sdkPatchRequest(t, tc.body), r)
			require.NoError(t, err)
			plan, err := r.BuildUpdate(d, parsed, parse.ByID(initial.ID), parse.Scope{})
			require.NoError(t, err)
			res, err := applyUpdate(t, db, r, d, plan, false)
			require.NoError(t, err)
			want := mergePrefsRow{ID: initial.ID, Prefs: tc.want}
			assert.Equal(t, []mergePrefsRow{want}, res.Changed)

			req, err := r.ParseList(nil)
			require.NoError(t, err)
			list, err := r.BuildList(d, req, parse.Scope{})
			require.NoError(t, err)
			assert.Equal(t, []mergePrefsRow{want}, listPage(t, db, r, d, list).Items)

			applied, err := r.ApplyValidatedPatch(parsed, initial)
			require.NoError(t, err)
			assert.Equal(t, want, applied)
		})
	}

	for _, tc := range []struct{ name, body string }{
		{"a member the document always writes takes no null", `{"prefs":{"units":null}}`},
		{"nor does one it writes as null", `{"prefs":{"note":null}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsehttp.ParsePatch(sdkPatchRequest(t, tc.body), r)
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
		})
	}
}

// sdkPatchRequest is the request the SDK's patch sends: body as its JSON
// Merge Patch, Content-Type application/json.
func sdkPatchRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPatch, "/merge_prefs/one", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

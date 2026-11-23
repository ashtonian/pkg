//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// A patch writes the row it returns, never the row it is given, nor a parent
// that row embeds by pointer and the caller still holds.

type PatchParent struct {
	Name  string `json:"name"  db:"name"  parse:"patch" patch_ops:"replace,test"`
	Label string `json:"label" db:"label" parse:"patch" patch_ops:"replace"`
	Note  string `json:"note"  db:"note"`
}

type PatchMiddle struct {
	*PatchParent
	Rank int `json:"rank" db:"rank" parse:"patch" patch_ops:"replace"`
}

type patchPrivate struct {
	Extra string            `json:"extra" db:"extra" parse:"patch" patch_ops:"replace"`
	Attrs map[string]string `json:"attrs" db:"attrs" parse:"patch" patch_ops:"replace"`
}

type promotedPatchRow struct {
	ID      string `json:"id"      db:"id"`
	Version int    `json:"version" db:"version" parse:"patch" patch_ops:"test"`
	*PatchMiddle
	*patchPrivate
}

func TestApplyPatch_LeavesTheRowAndItsEmbeddedParents(t *testing.T) {
	const (
		direct   = `[{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/rank","value":2},{"op":"replace","path":"/label","value":"labelled"}]`
		document = `[{"op":"test","path":"/version","value":1},{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/rank","value":2},{"op":"replace","path":"/label","value":"labelled"}]`
		// The second write fails after the first: the private parent can be
		// neither allocated nor copied. The document path's writes are its
		// last step, and a JSON column's the last of them.
		directFails   = `[{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/extra","value":"x"}]`
		documentFails = `[{"op":"test","path":"/version","value":1},{"op":"replace","path":"/name","value":"after"},{"op":"replace","path":"/attrs","value":{"k":"v"}}]`
	)
	allocated := func() promotedPatchRow {
		row := promotedPatchRow{ID: "1", Version: 1, PatchMiddle: &PatchMiddle{Rank: 1, PatchParent: &PatchParent{Name: "before", Label: "before", Note: "kept"}}}
		return row
	}
	nilMiddle := func() promotedPatchRow {
		row := promotedPatchRow{ID: "1", Version: 1}
		return row
	}
	nilParent := func() promotedPatchRow {
		row := promotedPatchRow{ID: "1", Version: 1, PatchMiddle: &PatchMiddle{Rank: 1}}
		return row
	}
	withPrivate := func() promotedPatchRow {
		row := allocated()
		row.patchPrivate = &patchPrivate{Extra: "private", Attrs: map[string]string{"k": "before"}}
		return row
	}
	for _, tc := range []struct {
		name, body string
		row        func() promotedPatchRow
		// wantNote is the untouched parent field the result carries over;
		// wantCode is the error, when the patch fails.
		wantNote, wantCode string
	}{
		{name: "direct, allocated parents", body: direct, row: allocated, wantNote: "kept"},
		{name: "document, allocated parents", body: document, row: allocated, wantNote: "kept"},
		{name: "direct, nil middle", body: direct, row: nilMiddle},
		{name: "document, nil middle", body: document, row: nilMiddle},
		{name: "direct, nil parent", body: direct, row: nilParent},
		{name: "document, nil parent", body: document, row: nilParent},
		{name: "direct, fails after a write, nil private parent", body: directFails, row: allocated, wantCode: patch.ECodePatchFieldNotFound.String()},
		{name: "direct, fails after a write, allocated private parent", body: directFails, row: withPrivate, wantCode: patch.ECodePatchFieldNotFound.String()},
		{name: "document, fails after a write, nil private parent", body: documentFails, row: allocated, wantCode: patch.ECodePatchFieldNotFound.String()},
		{name: "document, fails after a write, allocated private parent", body: documentFails, row: withPrivate, wantCode: patch.ECodePatchFieldNotFound.String()},
	} {
		check := func(t *testing.T, got promotedPatchRow, err error) {
			t.Helper()
			if tc.wantCode != "" {
				requireCode(t, err, tc.wantCode)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got.PatchMiddle)
			require.NotNil(t, got.PatchParent)
			assert.Equal(t, PatchParent{Name: "after", Label: "labelled", Note: tc.wantNote}, *got.PatchParent)
			assert.Equal(t, 2, got.Rank)
			assert.Equal(t, "1", got.ID)
		}
		t.Run(tc.name+", value row", func(t *testing.T) {
			r, err := parse.NewResource[promotedPatchRow]("promoted_rows", parse.ColumnType("attrs", core.SQLJSONB))
			require.NoError(t, err)
			in := tc.row()
			kept := retainParents(in)
			got, err := r.ApplyPatch([]byte(tc.body), in)
			check(t, got, err)
			kept.assertUnchanged(t, tc.row(), in)
		})
		t.Run(tc.name+", pointer row", func(t *testing.T) {
			r, err := parse.NewResource[*promotedPatchRow]("promoted_rows", parse.ColumnType("attrs", core.SQLJSONB))
			require.NoError(t, err)
			in := new(promotedPatchRow)
			*in = tc.row()
			kept := retainParents(*in)
			got, err := r.ApplyPatch([]byte(tc.body), in)
			var out promotedPatchRow
			if err == nil {
				require.NotNil(t, got)
				assert.NotSame(t, in, got)
				out = *got
			}
			check(t, out, err)
			kept.assertUnchanged(t, tc.row(), *in)
		})
	}
}

// retainedParents are the embedded parents a caller kept pointers to before
// a patch.
type retainedParents struct {
	middle  *PatchMiddle
	parent  *PatchParent
	private *patchPrivate
}

func retainParents(row promotedPatchRow) retainedParents {
	kept := retainedParents{middle: row.PatchMiddle, private: row.patchPrivate}
	if row.PatchMiddle != nil {
		kept.parent = row.PatchParent
	}
	return kept
}

// assertUnchanged checks that row, the caller's row after a patch, still
// holds the parents it held and equals want, a fresh build of it, parents
// included.
func (k retainedParents) assertUnchanged(t *testing.T, want, row promotedPatchRow) {
	t.Helper()
	assert.Equal(t, want, row)
	assert.Same(t, k.middle, row.PatchMiddle)
	assert.Same(t, k.private, row.patchPrivate)
	if k.middle != nil {
		assert.Same(t, k.parent, row.PatchParent)
	}
}

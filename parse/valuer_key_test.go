//go:build unit

package parse_test

import (
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

// skuCode is a key whose stored value is not its Go value: the driver
// writes Value(), upper-cased, and scans the text back as it is.
type skuCode string

func (c skuCode) Value() (driver.Value, error) { return strings.ToUpper(string(c)), nil }

type skuItem struct {
	ID   skuCode  `json:"id"   db:"id"  parse:"filter,sort"`
	Qty  int64    `json:"qty"  db:"qty" parse:"patch"`
	Tags []string `json:"tags" db:"-"   parse:"patch" relation:"sku_tags,sku_id,tag_id"`
}

// A key is normalized by one rule (core.NormalizeKey) wherever it binds: a
// Valuer key stands for the value it writes, as an update's target, as the
// owner of the relation rows it writes and as a relation's member, so each
// names the row the driver stored.
func TestValuerKey_BindsAsTheValueItWrites(t *testing.T) {
	ctx := t.Context()
	provider, db := providertest.SQLite(t)
	items, err := parse.NewResource[skuItem](provider, "sku_items")
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE sku_items (id TEXT PRIMARY KEY, qty INTEGER NOT NULL)`,
		`CREATE TABLE sku_tags (sku_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (sku_id, tag_id))`,
	} {
		_, err = db.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO sku_items VALUES (?, 0)`, skuCode("ab"))
	require.NoError(t, err)

	parsed, err := items.ParsePatch([]byte(`[{"op":"replace","path":"/qty","value":7},{"op":"add","path":"/tags","value":["t1"]}]`))
	require.NoError(t, err)
	res, err := items.Update(ctx, parsed, parse.ByKey(parse.Key{skuCode("ab")}), parse.Scope{})
	require.NoError(t, err)
	require.Len(t, res.Changed, 1, "the target names the stored row")

	var qty int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT qty FROM sku_items WHERE id = 'AB'`).Scan(&qty))
	assert.Equal(t, int64(7), qty)

	assert.Equal(t, []string{"AB:t1"}, queryRows[string](t, db, `SELECT sku_id || ':' || tag_id FROM sku_tags`), "an owner key read back binds as the target did")

	rel := &core.RelationSchema{JoinTable: "sku_tags", SourceColumns: []string{"tag_id"}, TargetColumns: []string{"sku_id"},
		SourceTypes: []core.TypeInfo{{Type: core.FieldTypeString}}, TargetTypes: []core.TypeInfo{{Type: core.FieldTypeString}}}
	members, err := patch.RenderRelationOp(patch.RelationPatchOp{Field: "items", Schema: rel, Verb: patch.RelationVerbRemove, Values: []any{skuCode("ab")}},
		filter.SQLiteDialect{}, []core.Key{{"t1"}}, patch.RenderRelationOpOptions{})
	require.NoError(t, err)
	require.Len(t, members, 1)
	removed, err := db.ExecContext(ctx, members[0].SQL, members[0].Args...)
	require.NoError(t, err)
	n, err := removed.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "the member names the pair the update wrote")
}

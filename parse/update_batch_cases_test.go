//go:build unit || integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
	"github.com/stretchr/testify/require"
)

type updateBatchDocument struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type updateBatchRow struct {
	ID        int64               `json:"id" db:"id" parse:"filter,server"`
	TenantID  string              `json:"tenant_id" db:"tenant_id" parse:"server"`
	Reference *string             `json:"reference" db:"reference" parse:"filter,patch"`
	Version   int64               `json:"version" db:"version" parse:"filter,server"`
	Meta      updateBatchDocument `json:"meta" db:"meta" parse:"patch"`
	UpdatedAt string              `json:"updated_at" db:"updated_at"`
	DeletedAt *string             `json:"deleted_at" db:"deleted_at"`
	Tags      []string            `json:"tags" db:"-" parse:"patch" relation:"batch_tags,row_id,tag_id,id,tenant_id" indexed:"true"`
}

func updateBatchResource(t *testing.T) *parse.Resource[updateBatchRow] {
	t.Helper()
	r, err := parse.NewResource[updateBatchRow]("batch_rows", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.ColumnType("meta", core.SQLJSONB))
	require.NoError(t, err)
	return r
}

func updateBatchPatch(t *testing.T, r *parse.Resource[updateBatchRow], raw string) *patch.ParsedPatch {
	t.Helper()
	p, err := r.ParseServerMergePatch([]byte(raw))
	require.NoError(t, err)
	return p
}

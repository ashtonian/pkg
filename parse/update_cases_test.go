//go:build unit || integration

package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

type gadget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort,patch"`
	Rank      int64      `json:"rank"       db:"rank"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,patch" relation:"gadget_tags,gadget_id,tag_id,id,tenant_id" indexed:"true"`
}

func gadgets(t *testing.T, p *parse.Provider) *parse.Resource[gadget] {
	t.Helper()
	r, err := parse.NewResource[gadget](p, "gadgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	return r
}

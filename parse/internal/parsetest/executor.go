//go:build unit || integration

package parsetest

import (
	"time"

	"github.com/ashtonian/pkg/parse"
)

type ExecItem struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,include,patch" relation:"exec_tags,item_id,tag_id,id,tenant_id" indexed:"true"`
}

// ExecItems is the contract's resource on p.
func ExecItems(p *parse.Provider) *parse.Resource[ExecItem] {
	return parse.MustResource[ExecItem](p, "exec_items", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
}

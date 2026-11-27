//go:build unit || integration

package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

type note struct {
	ID        string         `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string         `json:"tenant_id"  db:"tenant_id"`
	Title     *string        `json:"title"      db:"title"      parse:"filter,patch"`
	Rank      int64          `json:"rank"       db:"rank"       parse:"filter,patch"`
	Labels    []string       `json:"labels"     db:"labels"     parse:"patch"`
	Attrs     map[string]any `json:"attrs"      db:"attrs"      parse:"patch"`
	Version   int64          `json:"version"    db:"version"`
	UpdatedAt *time.Time     `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at" db:"deleted_at"`
	Tags      []string       `json:"tags"       db:"-"          parse:"filter,patch" relation:"note_tags,note_id,tag_id,id,tenant_id" indexed:"true"`
}

// noteColumns are the columns the statements return; labels and attrs are
// compared by the database and read back on their own.
func noteColumns() []string {
	columns := []string{"id", "tenant_id", "title", "rank", "version", "updated_at", "deleted_at"}
	return columns
}

func notes(t *testing.T) *parse.Resource[note] {
	t.Helper()
	r, err := parse.NewResource[note]("notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"),
		parse.ReadColumns(noteColumns()...))
	require.NoError(t, err)
	return r
}

func skipAt() time.Time {
	return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
}

// skipUpdate builds a SkipUnchanged plan that stamps and counts.
func skipUpdate(t *testing.T, r *parse.Resource[note], d parse.Dialect, body string, target parse.Target) *parse.UpdatePlan {
	t.Helper()
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(d, p, target, parse.Scope{Tenant: "t1"},
		parse.Stamp("updated_at", skipAt()), parse.Increment("version"), parse.SkipUnchanged())
	require.NoError(t, err)
	return plan
}

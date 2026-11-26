//go:build unit || integration

package parse_test

import (
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A governed list: tenant A reads its own documents and tenant B's b1,
// granted. Each document's notes, labels and topics are rows of its own
// tenant; the fixture also holds rows of the other tenant hanging from
// them (A's note n3 and pairs on b1, B's pair on a2), which are no
// document's relations.

type govDocument struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Title    string `json:"title"     db:"title"     parse:"filter"`
	// Topics is a pair table compared by key (the relation tag).
	Topics []string `json:"-" db:"-" parse:"filter" relation_name:"topics" relation:"document_topics,document_id,topic_id,id,tenant_id" indexed:"true"`
}

type govNote struct {
	ID         string     `json:"id"          db:"id"          parse:"filter,sort"`
	TenantID   string     `json:"tenant_id"   db:"tenant_id"`
	DocumentID string     `json:"document_id" db:"document_id" parse:"filter"`
	Body       string     `json:"body"        db:"body"        parse:"filter"`
	DeletedAt  *time.Time `json:"deleted_at"  db:"deleted_at"`
}

type govReaction struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	NoteID   string `json:"note_id"   db:"note_id"   parse:"filter"`
	Kind     string `json:"kind"      db:"kind"      parse:"filter"`
}

type govLabel struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter"`
}

func governedDocuments(t *testing.T) *parse.Resource[govDocument] {
	t.Helper()
	documents := parse.MustResource[govDocument]("documents", parse.TenantColumn("tenant_id"), parse.ParserOptions(core.WithMaxCost(1<<10)))
	notes := parse.MustResource[govNote]("notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	reactions := parse.MustResource[govReaction]("reactions", parse.TenantColumn("tenant_id"))
	labels := parse.MustResource[govLabel]("labels", parse.TenantColumn("tenant_id"))
	notes.MustRelate("reactions", parse.HasMany(reactions, "note_id", parse.Indexed()))
	documents.MustRelate("notes", parse.HasMany(notes, "document_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	documents.MustRelate("labels", parse.ManyToMany(labels,
		parse.Junction{Table: "document_labels", Source: []string{"document_id"}, Target: []string{"label_id"}, Tenant: "tenant_id"},
		parse.Indexed(), parse.Includable(), parse.Facetable()))
	return documents
}

// govTable is one fixture table: its rows are the memory store's and,

func govEq(field string, v any) *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Constraints: []*filter.PolicyConstraint{{Field: field, Operator: filter.OpEq, Value: v}}}
}

// governanceA is tenant A's rows and B's b1, granted.
func governanceA() *filter.PolicyConstraints {
	return &filter.PolicyConstraints{Or: []*filter.PolicyConstraints{govEq("tenant_id", "A"), govEq("id", "b1")}}
}

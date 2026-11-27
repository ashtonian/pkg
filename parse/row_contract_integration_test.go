//go:build integration

package parse_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// The row shapes below are ones whose columns a by-name driver mapping (the
// db tag, else the Go name) assigns to the wrong field or to none. Each
// table holds one row whose every column holds "value-of-<column>".

type ContractStamps struct {
	CreatedBy string `json:"created_by" db:"created_by"`
}

// contractEmbedded promotes a column from an embedded pointer.
type contractEmbedded struct {
	ID string `json:"id" db:"id" parse:"filter,sort"`
	*ContractStamps
}

// contractConvention names a column by its JSON name, untagged.
type contractConvention struct {
	ID    string `json:"id" db:"id" parse:"filter,sort"`
	Title string `json:"name" parse:"filter"`
}

// contractColumnTag names its columns by the sql tag.
type contractColumnTag struct {
	ID   string `json:"id" sql:"doc_id" parse:"filter,sort"`
	Name string `json:"name" sql:"doc_name"`
}

// contractCrossed's untagged JSON names cross its Go names.
type contractCrossed struct {
	ID    string `json:"id" db:"id" parse:"filter,sort"`
	Left  string `json:"right"`
	Right string `json:"left"`
}

func rowContractFixture() []string {
	stmts := []string{
		`CREATE TABLE "embedded" ("id" TEXT PRIMARY KEY, "created_by" TEXT)`,
		`INSERT INTO "embedded" VALUES ('value-of-id', 'value-of-created_by')`,
		`CREATE TABLE "convention" ("id" TEXT PRIMARY KEY, "name" TEXT)`,
		`INSERT INTO "convention" VALUES ('value-of-id', 'value-of-name')`,
		`CREATE TABLE "column_tag" ("doc_id" TEXT PRIMARY KEY, "doc_name" TEXT)`,
		`INSERT INTO "column_tag" VALUES ('value-of-doc_id', 'value-of-doc_name')`,
		`CREATE TABLE "crossed" ("id" TEXT PRIMARY KEY, "left" TEXT, "right" TEXT)`,
		`INSERT INTO "crossed" ("id", "left", "right") VALUES ('value-of-id', 'value-of-left', 'value-of-right')`,
	}
	return stmts
}

// Each shape pages on each backend with every field holding the column the
// resource compiled it from.
func TestRowContract(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		p, db := b.open(t)
		execStatements(t, db, rowContractFixture()...)
		runRowContract(t, p)
	})
}

func runRowContract(t *testing.T, p *parse.Provider) {
	t.Helper()
	t.Run("embedded pointer", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractEmbedded](p, "embedded"))
		require.NotNil(t, items[0].ContractStamps, "the embedded pointer is allocated")
		assert.Equal(t, contractEmbedded{ID: "value-of-id", ContractStamps: &ContractStamps{CreatedBy: "value-of-created_by"}}, items[0])
	})
	t.Run("convention-named column", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractConvention](p, "convention"))
		assert.Equal(t, contractConvention{ID: "value-of-id", Title: "value-of-name"}, items[0])
	})
	t.Run("configured column tag", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractColumnTag](p, "column_tag", parse.PrimaryKey("doc_id"),
			parse.ParserOptions(core.WithGeneratorOptions(core.WithColumnTag("sql")))))
		assert.Equal(t, contractColumnTag{ID: "value-of-doc_id", Name: "value-of-doc_name"}, items[0])
	})
	t.Run("crossed names", func(t *testing.T) {
		items := contractPage(t, parse.MustResource[contractCrossed](p, "crossed"))
		assert.Equal(t, contractCrossed{ID: "value-of-id", Left: "value-of-right", Right: "value-of-left"}, items[0],
			"each field holds the column its JSON name compiles to")
	})
}

func contractPage[T any](t *testing.T, r *parse.Resource[T]) []T {
	t.Helper()
	req, err := r.ParseList(url.Values{})
	require.NoError(t, err)
	page := listPage(t, r, req, parse.Scope{})
	require.Len(t, page.Items, 1)
	return page.Items
}

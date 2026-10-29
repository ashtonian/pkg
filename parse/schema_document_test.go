//go:build unit

package parse_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

const schemaDocumentPath = "testdata/schema_document.json"

// schemaDocEntity exercises every member of the schema document: field metadata,
// enums, bounds, facets, search, geo, JSON paths, array elements, a junction
// relation, a declared relation (schemaDocPart), and patch paths of every kind.
type schemaDocEntity struct {
	ID       string          `json:"id"        db:"id"        parse:"filter,sort" indexed:"true" immutable:"true"`
	TenantID string          `json:"tenant_id" db:"tenant_id"`
	Name     string          `json:"name"      db:"name"      parse:"filter,sort,facet,search,patch" facet:"terms:10" label:"Name" description:"Display name" group:"general" order:"1" minlen:"1" maxlen:"64"`
	Status   string          `json:"status"    db:"status"    parse:"filter,patch" enum:"active,offline" patch_ops:"replace"`
	Rank     int32           `json:"rank"      db:"rank"      parse:"filter,sort,patch" min:"0" max:"100"`
	Note     *string         `json:"note"      db:"note"      parse:"patch"`
	Scores   []int16         `json:"scores"    db:"scores"    parse:"patch"`
	Location map[string]any  `json:"location"  db:"location"  parse:"filter" filter_ops:"near,within" geo:"jsonb" geo_srid:"4326"`
	Meta     map[string]any  `json:"meta"      db:"meta"      parse:"filter" paths:"region:string"`
	Lines    []schemaDocLine `json:"lines"     db:"lines"     parse:"filter"`
	Tags     []string        `json:"tags"      db:"-"         parse:"filter,include,patch" relation:"doc_tags,entity_id,tag_id,id,tenant_id"`
}

type schemaDocLine struct {
	SKU string `json:"sku" parse:"filter" filter_ops:"eq,in"`
	Qty int    `json:"qty" parse:"filter" filter_ops:"eq,gt"`
}

type schemaDocPart struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	EntityID string `json:"entity_id" db:"entity_id"`
	State    string `json:"state"     db:"state"     parse:"filter" enum:"ok,worn"`
}

func schemaDocument(t *testing.T) *parse.SchemaResponse {
	t.Helper()
	entities, err := parse.NewResource[schemaDocEntity]("doc_entities", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	parts, err := parse.NewResource[schemaDocPart]("doc_parts", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	require.NoError(t, entities.Relate("parts", parse.HasMany(parts, "entity_id", parse.Includable(), parse.Facetable(),
		parse.Describe("Parts", "The entity's parts"))))
	reg := parse.NewSchemaRegistry()
	parse.RegisterResource(reg, entities)
	parse.RegisterResource(reg, parts)
	doc := reg.SchemaFor("doc_entities")
	require.NotNil(t, doc)
	return doc
}

// The schema document is a contract: its golden snapshot changes only with a
// deliberate format change, and a change that removes, renames or redefines a
// member increments SchemaVersion. Regenerate with -update.
func TestSchemaDocument_MatchesItsGoldenSnapshot(t *testing.T) {
	got, err := json.MarshalIndent(schemaDocument(t), "", "  ")
	require.NoError(t, err)
	got = append(got, '\n')
	if *updateConformance {
		require.NoError(t, os.WriteFile(schemaDocumentPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(schemaDocumentPath)
	require.NoError(t, err, "missing snapshot; regenerate with -update")
	assert.Equal(t, string(want), string(got), "the schema document changed; regenerate with -update and, for a breaking change, increment SchemaVersion")
}

func TestSchemaDocument_CarriesItsVersion(t *testing.T) {
	doc := schemaDocument(t)
	assert.Equal(t, parse.SchemaVersion, doc.Version)

	data, err := json.Marshal(doc)
	require.NoError(t, err)
	var decoded parse.SchemaResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, doc, &decoded, "the document decodes to what was served")
}

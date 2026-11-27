//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/patch"
)

type wireTag struct {
	ID   int64  `json:"id,string" db:"id"`
	Name string `json:"name" db:"name" parse:"filter"`
}

type wirePlainTag struct {
	ID int64 `json:"id" db:"id"`
}

type wirePost struct {
	ID int64 `json:"id,string" db:"id"`
}

// wirePosts relates posts to quoted-key tags and to plain-key labels, both
// writable, on p.
func wirePosts(t *testing.T, p *parse.Provider) *parse.Resource[wirePost] {
	t.Helper()
	tags, err := parse.NewResource[wireTag](p, "tags")
	require.NoError(t, err)
	labels, err := parse.NewResource[wirePlainTag](p, "labels")
	require.NoError(t, err)
	posts, err := parse.NewResource[wirePost](p, "posts")
	require.NoError(t, err)
	require.NoError(t, posts.Relate("tags", parse.ManyToMany(tags, parse.Through("post_tags", "post_id", "tag_id"), parse.Writable())))
	require.NoError(t, posts.Relate("labels", parse.ManyToMany(labels, parse.Through("post_labels", "post_id", "label_id"), parse.Writable())))
	return posts
}

// A membership's keys travel as the target's key does: a json:",string"
// key's as JSON strings, as responses write them and the relation's filter
// shorthands take them, a plain key's as numbers.
func TestRelationKeysTakeTheTargetKeysWire(t *testing.T) {
	posts := wirePosts(t, onPostgres)

	parsed, err := posts.ParseMergePatch([]byte(`{"tags":["5","9007199254740993"]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{int64(5), int64(9007199254740993)}, parsed.Operations[0].Value)
	_, err = posts.ParseMergePatch([]byte(`{"tags":[5]}`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	for _, body := range []string{
		`[{"op":"add","path":"/tags","value":["5"]}]`,
		`[{"op":"remove","path":"/tags","value":["5"]}]`,
		`[{"op":"replace","path":"/tags","value":["5","6"]}]`,
	} {
		_, err := posts.ParsePatch([]byte(body))
		assert.NoError(t, err, body)
	}
	_, err = posts.ParsePatch([]byte(`[{"op":"add","path":"/tags","value":[5]}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())

	// A Go value code passes is the key, as for a scalar path.
	parsed, err = posts.MergePatchMap(map[string]any{"tags": []int64{7}})
	require.NoError(t, err)
	assert.Equal(t, []any{int64(7)}, parsed.Operations[0].Value)

	// A target key without the option is unchanged.
	parsed, err = posts.ParseMergePatch([]byte(`{"labels":[5]}`))
	require.NoError(t, err)
	assert.Equal(t, []any{int64(5)}, parsed.Operations[0].Value)
	_, err = posts.ParseMergePatch([]byte(`{"labels":["5"]}`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())

	// The schema document says each key is quoted.
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, posts))
	paths := reg.SchemaFor("posts").Patch.Paths
	assert.Equal(t, "string", string(paths["/tags"].Wire))
	assert.Empty(t, paths["/labels"].Wire)
}

// parse/client's Relation[Quoted[K]] sends the keys the server takes
// (testdata/client_requests.json), and the relation statements bind them as
// the target's integers: the junction's untyped column keeps what is bound.
func TestClientQuotedRelationOperationsParse(t *testing.T) {
	provider, db := providertest.SQLite(t)
	dbtest.Exec(t, db,
		`CREATE TABLE posts (id INTEGER PRIMARY KEY)`, `INSERT INTO posts VALUES (1)`,
		`CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT)`, `INSERT INTO tags VALUES (5, 'five'), (6, 'six')`,
		`CREATE TABLE labels (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE post_tags (post_id, tag_id)`, `INSERT INTO post_tags VALUES (1, 6)`,
		`CREATE TABLE post_labels (post_id, label_id)`)
	posts := wirePosts(t, provider)
	sent := readClientRequests(t).QuotedRelation
	parsed, err := posts.ParsePatch(sent.JSONPatch)
	require.NoError(t, err, "%s", sent.JSONPatch)
	assert.Equal(t, []any{int64(5), int64(9007199254740993)}, parsed.Operations[0].Value)

	parsed, err = posts.ParseMergePatch(sent.MergePatch)
	require.NoError(t, err, "%s", sent.MergePatch)
	res, err := posts.Update(t.Context(), parsed, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	assert.Positive(t, res.RelationRows)
	assert.Equal(t, [][]any{{int64(5), "integer"}}, dbtest.QueryRows[[]any](t, db, `SELECT tag_id, typeof(tag_id) FROM post_tags WHERE post_id = 1`))
}

//go:build unit

package parse_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/client"
	"github.com/ashtonian/pkg/parse/filter"
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
// writable.
func wirePosts(t *testing.T) *parse.Resource[wirePost] {
	t.Helper()
	tags, err := parse.NewResource[wireTag]("tags")
	require.NoError(t, err)
	labels, err := parse.NewResource[wirePlainTag]("labels")
	require.NoError(t, err)
	posts, err := parse.NewResource[wirePost]("posts")
	require.NoError(t, err)
	require.NoError(t, posts.Relate("tags", parse.ManyToMany(tags, parse.Through("post_tags", "post_id", "tag_id"), parse.Writable())))
	require.NoError(t, posts.Relate("labels", parse.ManyToMany(labels, parse.Through("post_labels", "post_id", "label_id"), parse.Writable())))
	return posts
}

// A membership's keys travel as the target's key does: a json:",string"
// key's as JSON strings, as responses write them and the relation's filter
// shorthands take them, a plain key's as numbers.
func TestRelationKeysTakeTheTargetKeysWire(t *testing.T) {
	posts := wirePosts(t)

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

// client.Relation[client.Quoted[K]] sends the keys the server takes, and
// the relation statements bind them as the target's integers.
func TestClientQuotedRelationOperationsParse(t *testing.T) {
	posts := wirePosts(t)
	tags := client.NewRelation[client.Quoted[int64]]("tags")
	body, err := json.Marshal([]client.Operation{
		tags.Add(client.Quote(int64(5)), client.Quote(int64(9007199254740993))),
		tags.Remove(client.Quote(int64(6))),
	})
	require.NoError(t, err)
	parsed, err := posts.ParsePatch(body)
	require.NoError(t, err, "%s", body)
	assert.Equal(t, []any{int64(5), int64(9007199254740993)}, parsed.Operations[0].Value)

	merge, err := json.Marshal(struct {
		Tags client.Nullable[[]client.Quoted[int64]] `json:"tags,omitzero"`
	}{client.Set([]client.Quoted[int64]{client.Quote(int64(5))})})
	require.NoError(t, err)
	parsed, err = posts.ParseMergePatch(merge)
	require.NoError(t, err, "%s", merge)
	plan, err := posts.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(int64(1)), parse.Scope{})
	require.NoError(t, err)
	stmts, err := posts.RelationStatements(plan, []parse.Key{{int64(1)}})
	require.NoError(t, err)
	require.NotEmpty(t, stmts)
	assert.Contains(t, stmts[len(stmts)-1].Args, []int64{5})
}

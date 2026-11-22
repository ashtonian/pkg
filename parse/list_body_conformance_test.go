//go:build unit

package parse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// confPartRow is confPart with the foreign key a has-many relation joins on;
// the key is no filter field, so the target's filter schema is confPart's.
type confPartRow struct {
	ID       string `json:"id"    parse:"filter" filter_ops:"eq,in"`
	EntityID string `json:"-"     db:"entity_id"`
	State    string `json:"state" parse:"filter" filter_ops:"eq,ne" enum:"ok,worn"`
}

// confResource serves confEntity, the filter conformance corpus's schema, as
// a resource, so the corpus can be replayed through the list transports
// rather than the bare filter parser. The corpus leaves the cost guard out
// (it is deployment configuration, not grammar), so the resource lifts it.
func confResource(t *testing.T) *parse.Resource[confEntity] {
	t.Helper()
	parts, err := parse.NewResource[confPartRow]("parts")
	require.NoError(t, err)
	entities, err := parse.NewResource[confEntity]("entities", parse.ParserOptions(core.WithClock(confClock), core.WithMaxCost(1<<20)))
	require.NoError(t, err)
	require.NoError(t, entities.Relate("parts", parse.HasMany(parts, "entity_id")))
	return entities
}

func parseErrorCode(err error) string {
	if pe, ok := errors.AsType[*core.ParseError](err); ok {
		return pe.Code
	}
	return ""
}

// Every corpus case reaches the same verdict through ?filter= and through a
// POST body's filter member, and that verdict is the corpus's: the same
// validity, error code and canonical AST.
func TestFilterConformance_ThroughBothListTransports(t *testing.T) {
	corpus := readFilterCorpus(t)
	require.NotEmpty(t, corpus)

	res := confResource(t)
	for _, c := range corpus {
		t.Run(c.Name, func(t *testing.T) {
			doc, err := json.Marshal(c.Input)
			require.NoError(t, err)
			body, err := json.Marshal(map[string]json.RawMessage{"filter": doc})
			require.NoError(t, err)

			fromQuery, queryErr := res.ParseList(url.Values{"filter": {string(doc)}})
			fromBody, bodyErr := res.ParseListBody(bytes.NewReader(body))

			assert.Equal(t, parseErrorCode(queryErr), parseErrorCode(bodyErr), "both transports refuse alike")
			assert.Equal(t, fromQuery, fromBody, "both transports parse to one request")

			if !c.Valid {
				require.Error(t, queryErr)
				assert.Equal(t, c.ErrorCode, parseErrorCode(queryErr))
				return
			}
			require.NoError(t, queryErr)
			var got *astNode
			if fromQuery.Filter != nil && fromQuery.Filter.AST != nil {
				got = toASTNode(fromQuery.Filter.AST.Root)
			}
			assert.Equal(t, normalize(t, c.CanonicalAST), normalize(t, got))
		})
	}
}

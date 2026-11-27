//go:build unit

package parse_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

type limitedDoc struct {
	ID   string         `json:"id"   db:"id"   parse:"filter"`
	Name string         `json:"name" db:"name" parse:"filter,patch"`
	Doc  map[string]any `json:"doc"  db:"doc"  parse:"patch"`
}

// nestedFilter nests a condition in levels $and groups.
func nestedFilter(levels int) string {
	f := `{"name":"a"}`
	for range levels {
		f = `{"$and":[` + f + `,{"name":"b"}]}`
	}
	return f
}

// nestedMergePatch is a merge patch whose deepest member is levels deep.
func nestedMergePatch(levels int) string {
	if levels == 1 {
		return `{"name":"x"}`
	}
	v := "1"
	for range levels - 1 {
		v = `{"k":` + v + `}`
	}
	return `{"doc":` + v + `}`
}

// conditions is a filter of n leaf conditions.
func conditions(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"name":"v%d"}`, i)
	}
	return `{"$or":[` + strings.Join(parts, ",") + `]}`
}

func inSet(n int) string {
	values := make([]string, n)
	for i := range values {
		values[i] = fmt.Sprintf(`"v%d"`, i)
	}
	return `{"name":{"$in":[` + strings.Join(values, ",") + `]}}`
}

// The parser limits mean one thing for filters and patches alike: zero is
// the default for the count limits, zero or less is unlimited for the input
// size, and a negative count limit or list body size fails construction
// naming the option.
func TestParserLimits_OneRule(t *testing.T) {
	resource := func(t *testing.T, opts ...core.Option) *parse.Resource[limitedDoc] {
		t.Helper()
		r, err := parse.NewResource[limitedDoc](onSQLite, "limited", parse.ParserOptions(opts...))
		require.NoError(t, err)
		return r
	}
	def := core.DefaultConfig()

	// depthBound asserts r refuses filters and patches one level past depth,
	// and accepts them at depth.
	depthBound := func(t *testing.T, r *parse.Resource[limitedDoc], depth int) {
		t.Helper()
		// Cost and conditions grow with the depth; only depth is measured.
		_, err := r.ParseFilterWithCost(nestedFilter(depth), 1<<20)
		assert.NoError(t, err, "a filter %d deep", depth)
		_, err = r.ParseFilterWithCost(nestedFilter(depth+1), 1<<20)
		assert.ErrorContains(t, err, fmt.Sprintf("maximum %d", depth), "a filter %d deep", depth+1)
		_, err = r.ParseMergePatch([]byte(nestedMergePatch(depth)))
		assert.NoError(t, err, "a merge patch %d deep", depth)
		_, err = r.ParseMergePatch([]byte(nestedMergePatch(depth + 1)))
		assert.ErrorContains(t, err, fmt.Sprintf("maximum is %d", depth), "a merge patch %d deep", depth+1)
		pointer := "/doc" + strings.Repeat("/k", depth)
		_, err = r.ParsePatch([]byte(`[{"op":"replace","path":"` + pointer + `","value":1}]`))
		assert.ErrorContains(t, err, fmt.Sprintf("maximum is %d", depth), "a pointer of %d segments", depth+1)
	}

	t.Run("depth", func(t *testing.T) {
		depthBound(t, resource(t), def.MaxDepth)
		depthBound(t, resource(t, core.WithMaxDepth(0)), def.MaxDepth)
		depthBound(t, resource(t, core.WithMaxDepth(1)), 1)
	})
	t.Run("conditions", func(t *testing.T) {
		for _, r := range []*parse.Resource[limitedDoc]{resource(t), resource(t, core.WithMaxConditions(0))} {
			_, err := r.ParseFilterWithCost(conditions(def.MaxConditions), 1<<20)
			assert.NoError(t, err)
			_, err = r.ParseFilterWithCost(conditions(def.MaxConditions+1), 1<<20)
			assert.ErrorContains(t, err, fmt.Sprintf("exceeds maximum %d", def.MaxConditions))
		}
	})
	t.Run("cost", func(t *testing.T) {
		for _, r := range []*parse.Resource[limitedDoc]{resource(t), resource(t, core.WithMaxCost(0))} {
			_, err := r.ParseFilter(conditions(def.MaxConditions))
			assert.ErrorContains(t, err, fmt.Sprintf("exceeds maximum %d", def.MaxCost))
		}
	})
	t.Run("array size", func(t *testing.T) {
		for _, r := range []*parse.Resource[limitedDoc]{resource(t), resource(t, core.WithMaxArraySize(0))} {
			_, err := r.ParseFilter(inSet(def.MaxArraySize))
			assert.NoError(t, err)
			_, err = r.ParseFilter(inSet(def.MaxArraySize + 1))
			assert.ErrorContains(t, err, fmt.Sprintf("exceeds maximum %d", def.MaxArraySize))
		}
	})
	t.Run("input size", func(t *testing.T) {
		long := strings.Repeat("x", def.MaxInputSize)
		filterBody, patchBody := `{"name":"`+long+`"}`, []byte(`{"name":"`+long+`"}`)
		_, err := resource(t).ParseFilter(filterBody)
		assert.ErrorContains(t, err, "exceeds maximum")
		_, err = resource(t).ParseMergePatch(patchBody)
		assert.ErrorContains(t, err, "exceeds maximum")
		for _, size := range []int{0, -1} {
			r := resource(t, core.WithMaxInputSize(size))
			_, err = r.ParseFilter(filterBody)
			assert.NoError(t, err, "input size %d is unlimited", size)
			_, err = r.ParseMergePatch(patchBody)
			assert.NoError(t, err, "input size %d is unlimited", size)
		}
	})
	t.Run("negative", func(t *testing.T) {
		for option, opt := range map[string]core.Option{
			"WithMaxDepth(-1)":        core.WithMaxDepth(-1),
			"WithMaxCost(-1)":         core.WithMaxCost(-1),
			"WithMaxConditions(-1)":   core.WithMaxConditions(-1),
			"WithMaxArraySize(-1)":    core.WithMaxArraySize(-1),
			"WithMaxListBodySize(-1)": core.WithMaxListBodySize(-1),
		} {
			_, err := parse.NewResource[limitedDoc](onSQLite, "limited", parse.ParserOptions(opt))
			assert.ErrorContains(t, err, "parse: limited: parser options: "+option+": a limit is never negative")
		}
	})
}

// Two WithGeneratorOptions keep both: the column tag of one and the field
// name tag of the other.
func TestParserOptions_GeneratorOptionsAccumulate(t *testing.T) {
	type row struct {
		ID   string `sql:"id"        bson:"id"   parse:"filter"`
		Name string `sql:"full_name" bson:"name" parse:"filter,patch"`
	}
	r, err := parse.NewResource[row](onSQLite, "rows", parse.ParserOptions(
		core.WithGeneratorOptions(core.WithColumnTag("sql")),
		core.WithGeneratorOptions(core.WithFieldNameTag("bson")),
	))
	require.NoError(t, err)
	require.Contains(t, r.FilterSchema().Fields, "name")
	assert.Equal(t, "full_name", r.FilterSchema().Fields["name"].Column)
}

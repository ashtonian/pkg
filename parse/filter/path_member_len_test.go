//go:build unit

package filter_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// The member cap the pattern enforces is the one its error states.
func TestWithPaths_MemberLengthIsStated(t *testing.T) {
	type doc struct {
		Labels map[string]any `json:"labels" parse:"filter"`
	}
	s, err := filter.GenerateFilterSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)

	longest := strings.Repeat("a", filter.MaxPathMemberLen)
	_, err = s.WithPaths(filter.PathDecl{Path: "labels." + longest, Type: core.FieldTypeString})
	require.NoError(t, err)

	over := longest + "a"
	_, err = s.WithPaths(filter.PathDecl{Path: "labels." + over, Type: core.FieldTypeString})
	assert.ErrorContains(t, err, strconv.Quote(over)+" is not an identifier: 1 to "+strconv.Itoa(filter.MaxPathMemberLen)+" of [A-Za-z0-9_-]")
	assert.Equal(t, 64, filter.MaxPathMemberLen)
}

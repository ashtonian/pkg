//go:build unit

package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A tag with several bad parts reports the first in the tag's order,
// every time.
func TestParseRelationTag_ReportsTheFirstBadPart(t *testing.T) {
	for range 50 {
		_, err := core.ParseRelationTag("bad junction,bad source,bad target,bad key")
		require.Error(t, err)
		assert.EqualError(t, err, `relation tag junction "bad junction" is not a SQL identifier`)
	}
	_, err := core.ParseRelationTag("widget_tags,widget_id,bad target,bad key")
	assert.EqualError(t, err, `relation tag target column "bad target" is not a SQL identifier`)
}

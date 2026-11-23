//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// A tag generation would otherwise ignore fails it, naming the field and
// the tag, before any request arrives.
func TestGenerate_RefusesTagsItWouldIgnore(t *testing.T) {
	type orderWord struct {
		Name string `json:"name" parse:"filter" order:"abc"`
	}
	type descending struct {
		Name string `json:"name" parse:"sort" sort_default:"descending"`
	}
	type twoDefaults struct {
		Name string `json:"name" parse:"sort" sort_default:"asc"`
		Rank int    `json:"rank" parse:"sort" sort_default:"desc"`
	}
	type unsortedDefault struct {
		ID   string `json:"id"   parse:"sort"`
		Name string `json:"name" parse:"filter" sort_default:"asc"`
	}
	g := core.NewSchemaGenerator()
	for name, build := range map[string]func() error{
		`order:"abc"`:                 func() error { _, err := filter.GenerateFilterSchema[orderWord](g); return err },
		`sort_default:"descending"`:   func() error { _, err := filter.GenerateSortSchema[descending](g); return err },
		"a second sort_default":       func() error { _, err := filter.GenerateSortSchema[twoDefaults](g); return err },
		"sort_default without a sort": func() error { _, err := filter.GenerateSortSchema[unsortedDefault](g); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := build()
			require.Error(t, err)
			assert.Regexp(t, `^field (Name|Rank): (order|sort_default):"`, err.Error())
		})
	}
}

func TestGenerate_ReadsOrderAndSortDefault(t *testing.T) {
	type row struct {
		ID   string `json:"id"   parse:"filter,sort" order:"-2"`
		Name string `json:"name" parse:"sort" sort_default:"desc"`
	}
	fs, err := filter.GenerateFilterSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, -2, fs.Fields["id"].Order)
	ss, err := filter.GenerateSortSchema[row](core.NewSchemaGenerator())
	require.NoError(t, err)
	assert.Equal(t, "name", ss.DefaultField)
	assert.Equal(t, filter.SortDesc, ss.DefaultDir)
}

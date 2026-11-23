//go:build unit

package parse_test

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type ownedDoc struct {
	Kind string `json:"kind"`
}

type ownedRow struct {
	ID   string   `json:"id"   db:"id"`
	Name string   `json:"name" db:"name" parse:"filter,patch" enum:"a,b"`
	Code string   `json:"code" db:"code" parse:"patch" maxlen:"2"`
	Doc  ownedDoc `json:"doc"  db:"doc"`
}

// describedWalk is what a walk tells its readers, read through the
// accessors.
type describedWalk struct {
	Names  []string
	Routes [][]int
	Enums  [][]string
	MaxLen []int
}

func describeWalk(walk *core.StructFields) describedWalk {
	var d describedWalk
	for f := range walk.Fields() {
		bounds := f.Bounds()
		maxLen := -1
		if bounds.MaxLen != nil {
			maxLen = *bounds.MaxLen
		}
		d.Names = append(d.Names, f.External())
		d.Routes = append(d.Routes, f.Route())
		d.Enums = append(d.Enums, f.Enum())
		d.MaxLen = append(d.MaxLen, maxLen)
	}
	return d
}

// scribble changes everything a walk hands its caller.
func scribble(walk *core.StructFields) {
	fields := slices.Collect(walk.Fields())
	for _, f := range fields {
		route := f.Route()
		route[0] = 99
		enum := f.Enum()
		for i := range enum {
			enum[i] = "z"
		}
		bounds := f.Bounds()
		if bounds.MaxLen != nil {
			*bounds.MaxLen = 99
		}
	}
	slices.Reverse(fields)
	clear(fields)
}

// The walks are shared by every generator, resource and stored document of
// a type; what one caller does with what a walk returns reaches none of them.
func TestFieldMetadata_CallerChangesReachNoOtherReader(t *testing.T) {
	rowType := reflect.TypeFor[ownedRow]()
	walk, err := core.StructFieldsOf(rowType, core.JSONTagKey, core.DefaultColumnTagKey)
	require.NoError(t, err)
	want := describeWalk(walk)
	require.Equal(t, describedWalk{
		Names:  []string{"id", "name", "code", "doc"},
		Routes: [][]int{{0}, {1}, {2}, {3}},
		Enums:  [][]string{nil, {"a", "b"}, nil, nil},
		MaxLen: []int{-1, -1, 2, -1},
	}, want)

	scribble(walk)
	generated, err := core.NewSchemaGenerator().StructFields(rowType)
	require.NoError(t, err)
	scribble(generated)
	docWalk, err := core.JSONFieldsOf(reflect.TypeFor[ownedDoc]())
	require.NoError(t, err)
	scribble(docWalk)

	t.Run("another generator", func(t *testing.T) {
		other, err := core.NewSchemaGenerator(core.WithSortMaxFields(1)).StructFields(rowType)
		require.NoError(t, err)
		assert.Equal(t, want, describeWalk(other))
		doc, err := core.JSONFieldsOf(reflect.TypeFor[*ownedDoc]())
		require.NoError(t, err)
		kind, ok := doc.External("kind")
		require.True(t, ok)
		assert.Equal(t, []int{0}, kind.Route())
	})

	r := parse.MustResource[ownedRow]("owned", parse.ColumnType("doc", core.SQLJSONB))
	t.Run("a later resource", func(t *testing.T) {
		_, err := r.ParseFilter(`{"name":"a"}`)
		require.NoError(t, err)
		_, err = r.ParseFilter(`{"name":"z"}`)
		require.Error(t, err, "the enum still refuses z")
		assert.Equal(t, []string{"a", "b"}, r.FilterSchema().Fields["name"].EnumValues)

		_, err = r.ParsePatch([]byte(`[{"op":"replace","path":"/code","value":"abc"}]`))
		require.Error(t, err, "maxlen 2 still refuses three characters")
		got, err := r.ApplyPatch([]byte(`[{"op":"replace","path":"/name","value":"b"},{"op":"replace","path":"/code","value":"xy"}]`), ownedRow{ID: "1", Name: "a"})
		require.NoError(t, err)
		assert.Equal(t, ownedRow{ID: "1", Name: "b", Code: "xy"}, got)
	})

	t.Run("stored-document validation", func(t *testing.T) {
		scanner, err := r.RowScanner(filter.PostgresDialect{})
		require.NoError(t, err)
		_, err = scanner.Scan(scanStrings(`{"Kind":"x"}`))
		require.ErrorContains(t, err, `JSON member "/Kind" is noncanonical`)
		row, err := scanner.Scan(scanStrings(`{"kind":"x"}`))
		require.NoError(t, err)
		assert.Equal(t, ownedRow{ID: "v", Name: "v", Code: "v", Doc: ownedDoc{Kind: "x"}}, row)
	})
}

// scanStrings is a driver scan that fills each string column with "v" and
// hands each scanner doc.
func scanStrings(doc string) func(dest ...any) error {
	return func(dest ...any) error {
		for _, d := range dest {
			scanner, ok := d.(sql.Scanner)
			if !ok {
				*d.(*string) = "v"
				continue
			}
			err := scanner.Scan([]byte(doc))
			if err != nil {
				return err
			}
		}
		return nil
	}
}

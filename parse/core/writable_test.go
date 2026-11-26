//go:build unit

package core

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

type hiddenPointer struct {
	A int `json:"a"`
}

type hiddenValue struct {
	B int `json:"b"`
}

type hiddenInner struct {
	E int `json:"e"`
}

type ShownPointer struct {
	C int `json:"c"`
	*hiddenInner
}

type writableRow struct {
	*hiddenPointer
	hiddenValue
	*ShownPointer
	N int `json:"n"`
}

// A field is unwritable exactly when an unexported embedded pointer is on
// its route, at any depth, in a row walk and a document walk alike.
func TestField_CheckWritable(t *testing.T) {
	want := map[string]string{
		"a": "field A: a write reaches it through the unexported embedded *core.hiddenPointer, which reflection can neither allocate nor copy; export the type or embed it by value",
		"b": "",
		"c": "",
		"e": "field E: a write reaches it through the unexported embedded *core.hiddenInner, which reflection can neither allocate nor copy; export the type or embed it by value",
		"n": "",
	}
	rows, err := StructFieldsOf(reflect.TypeFor[writableRow](), JSONTagKey, DefaultColumnTagKey)
	require.NoError(t, err)
	documents, err := JSONFieldsOf(reflect.TypeFor[writableRow]())
	require.NoError(t, err)
	for walk, fields := range map[string]*StructFields{"row": rows, "document": documents} {
		require.Equal(t, len(want), fields.Len(), walk)
		for name, message := range want {
			f, ok := fields.External(name)
			require.True(t, ok, "%s %s", walk, name)
			err := f.CheckWritable()
			if message == "" {
				require.NoError(t, err, "%s %s", walk, name)
				continue
			}
			require.EqualError(t, err, message, "%s %s", walk, name)
		}
	}
}

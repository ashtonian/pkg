//go:build unit

package core

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every key parse lists as its own is one it reads: setting it changes
// what the walk reads from the field. A listed key no reader looks at would
// let a field carry it without a word, and point a misspelling at it.
func TestParseTagKeys_AreRead(t *testing.T) {
	values := map[string]string{
		"parse": "filter", "indexed": "true", "immutable": "true",
		ConstraintEnum: "a", ConstraintMinLen: "1", ConstraintMaxLen: "1", ConstraintMin: "1", ConstraintMax: "1",
		ConstraintMinItems: "1", ConstraintMaxItems: "1",
	}
	k := fieldsKey{name: "json", column: "db"}
	read := func(t *testing.T, tag string) (Tags, []string, Bounds) {
		t.Helper()
		sf := reflect.StructField{Name: "F", Type: reflect.TypeFor[string](), Tag: reflect.StructTag(tag)}
		tags, err := readTags(sf, k)
		require.NoError(t, err, tag)
		enum, bounds, err := readConstraints(sf)
		require.NoError(t, err, tag)
		return tags, enum, bounds
	}
	zeroTags, zeroEnum, zeroBounds := read(t, `json:"f"`)
	for _, key := range parseTagKeys {
		t.Run(key, func(t *testing.T) {
			value, ok := values[key]
			if !ok {
				value = "x"
			}
			tags, enum, bounds := read(t, `json:"f" `+key+`:"`+value+`"`)
			changed := !reflect.DeepEqual(tags, zeroTags) || !reflect.DeepEqual(enum, zeroEnum) || !reflect.DeepEqual(bounds, zeroBounds)
			assert.True(t, changed, "tag key %q changes nothing parse reads", key)
		})
	}
}

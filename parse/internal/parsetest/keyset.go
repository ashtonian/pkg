//go:build unit || integration

package parsetest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// ResumeKeyValue is the value a cursor minted at raw resumes from: raw's
// KeyValue, encoded, decoded and validated against key.
func ResumeKeyValue(t *testing.T, key filter.CursorKey, raw any) any {
	t.Helper()
	v, err := filter.KeyValue(key, raw)
	require.NoError(t, err, "%#v", raw)
	encoded, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{{Name: key.Name, Value: v, Direction: key.Direction}}})
	require.NoError(t, err, "%#v", raw)
	token, err := filter.DecodeCursor(encoded)
	require.NoError(t, err, "%#v", raw)
	require.NoError(t, filter.ValidateCursor(token, []filter.CursorKey{key}), "%#v minted %#v", raw, v)
	return token.Fields[0].Value
}

// MeasureRow is a row whose document's number member n a float path sorts
// by.
type MeasureRow struct {
	ID  int64  `db:"id"`
	Doc string `db:"doc"`
}

// MeasureSeeds hold members that differ beyond float64 precision: 0.1,
// 0.10000000000000001 and 0.09999999999999999999 are one float64, as are 1
// and 1.0000000000000001, and the two 23-digit integers; and members at the
// bound of a number member (core.MaxDecimalDigits digits in plain
// notation): 1e-999 and ±1e400 are within it, 1e-1000 and a 1001-digit
// integer beyond it, so they are NULL, as a string member and an absent one
// are. ns holds elements on either side of float64 precision and of the
// bound.
func MeasureSeeds() []MeasureRow {
	seeds := []MeasureRow{
		{1, `{"n": 0.1, "ns": [0.1, 1e-1000]}`},
		{2, `{"n": 0.10000000000000001, "ns": [0.10000000000000001]}`},
		{3, `{"n": 0.1}`},
		{4, `{"n": 0.09999999999999999999}`},
		{5, `{"n": 1}`},
		{6, `{"n": 1.0000000000000001}`},
		{7, `{"n": "0.5"}`},
		{8, `{}`},
		{9, `{"n": 12345678901234567890123}`},
		{10, `{"n": 12345678901234567890124}`},
		{11, `{"n": 1e-1000, "ns": [1e-1000]}`},
		{12, `{"n": 1` + strings.Repeat("0", core.MaxDecimalDigits) + `}`},
		{13, `{"n": 1e400, "ns": [1e400]}`},
		{14, `{"n": -1e400}`},
		{15, `{"n": 1e-999}`},
	}
	return seeds
}

// PostgresMeasureOrder is the seeds ascending by n as PostgreSQL orders the
// member, exactly (::numeric), ties on the id.
func PostgresMeasureOrder() []int64 {
	order := []int64{14, 15, 4, 1, 3, 2, 5, 6, 9, 10, 13, 7, 8, 11, 12}
	return order
}

// MeasurePath is member of the measures' doc column, a jsonb document on
// PostgreSQL (SQLite holds JSON text whatever the storage).
func MeasurePath(member string) *filter.JSONPath {
	path := &filter.JSONPath{Table: "measures", Column: "doc", Segments: []string{member}, Storage: filter.StorageJSONB}
	return path
}

// MeasureSorts sort the measures by the float path n.
func MeasureSorts() *filter.SortSchema {
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"n": {DataType: core.FieldTypeFloat, Path: MeasurePath("n")},
	}}
	return sorts
}

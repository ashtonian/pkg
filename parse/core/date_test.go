package core_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDate(t *testing.T) {
	for _, text := range []string{"0001-01-01", "2028-02-29", "2026-10-26", "9999-12-31"} {
		date, err := core.ParseDate(text)
		require.NoError(t, err)
		encoded, err := json.Marshal(date)
		require.NoError(t, err)
		assert.JSONEq(t, `"`+text+`"`, string(encoded))
		var round core.Date
		require.NoError(t, json.Unmarshal(encoded, &round))
		assert.Equal(t, date, round)
		value, err := date.Value()
		require.NoError(t, err)
		assert.Equal(t, text, value)
		var scanned core.Date
		require.NoError(t, scanned.Scan(value))
		assert.Equal(t, date, scanned)
	}
	for _, value := range []string{"", "0000-01-01", "2026-02-29", "2026-02-30", "2026-1-01", "2026-10-26T00:00:00Z", "now-1d"} {
		_, err := core.ParseDate(value)
		require.Error(t, err)
		date := core.Date("2026-10-26")
		before := date
		require.Error(t, json.Unmarshal([]byte(`"`+value+`"`), &date))
		assert.Equal(t, before, date, "failed decode is atomic")
	}
	var nullable struct {
		Day *core.Date `json:"day"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"day":null}`), &nullable))
	assert.Nil(t, nullable.Day)
	date := core.Date("2026-10-26")
	require.Error(t, json.Unmarshal([]byte(`null`), &date))
	assert.Equal(t, core.Date("2026-10-26"), date)
	for _, offset := range []int{-12, 14} {
		stored := time.Date(2026, 10, 26, 0, 0, 0, 0, time.FixedZone("SQL date", offset*3600))
		require.NoError(t, date.Scan(stored))
		assert.Equal(t, "2026-10-26", date.String())
	}
}

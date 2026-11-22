package filtertest

import (
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// TimestampRecord is the timestamp fixture: a timestamp column whose rows
// were written in several UTC offsets. Its table is TimestampSchema's.
type TimestampRecord struct {
	ID string     `json:"id" db:"id" parse:"filter,sort"`
	At *time.Time `json:"at" db:"at" parse:"filter,sort,facet"`
}

// TimestampSchema returns TimestampRecord's generated filter schema.
func TimestampSchema() (*filter.ResourceSchema, error) {
	return filter.GenerateFilterSchema[TimestampRecord](core.NewSchemaGenerator())
}

// TimestampRecords is the fixture data. a, b and c hold one instant,
// 2026-01-02T01:04:05.5Z, a and b written at +02:00 and c in UTC. d, at
// -07:00, is the latest instant though its wall time is the earliest; e,
// at +05:30, the earliest though its wall time falls between. f is NULL.
// Each holds whole microseconds, PostgreSQL's precision.
func TimestampRecords() []TimestampRecord {
	plus2 := time.FixedZone("", 2*60*60)
	instant := time.Date(2026, 1, 2, 3, 4, 5, 500_000_000, plus2)
	return []TimestampRecord{
		{ID: "a", At: new(instant)},
		{ID: "b", At: new(instant)},
		{ID: "c", At: new(instant.UTC())},
		{ID: "d", At: new(time.Date(2026, 1, 1, 20, 0, 0, 0, time.FixedZone("", -7*60*60)))},
		{ID: "e", At: new(time.Date(2026, 1, 2, 2, 30, 0, 250_000_000, time.FixedZone("", 5*60*60+30*60)))},
		{ID: "f"},
	}
}

// TimestampFacets are the buckets a terms facet of TimestampRecords' at
// counts: one per instant, keyed by its canonical text
// (core.FormatTimestamp) whatever offset it was written in.
var TimestampFacets = map[string]int64{
	"2026-01-02T01:04:05.500000000Z": 3,
	"2026-01-02T03:00:00.000000000Z": 1,
	"2026-01-01T21:00:00.250000000Z": 1,
}

// TimestampOrders are the ids each sort lists TimestampRecords in: by
// instant, NULL greatest, ties on id in the sort's direction.
var TimestampOrders = map[string]string{
	"at":  "eabcdf",
	"-at": "fdcbae",
}

// TimestampFilters are filters on at and the ids each selects, in id
// order. A bound in another offset names the same instant.
var TimestampFilters = map[string]string{
	`{"at":{"$eq":"2026-01-02T01:04:05.5Z"}}`:        "abc",
	`{"at":{"$eq":"2026-01-02T03:04:05.5+02:00"}}`:   "abc",
	`{"at":{"$gt":"2026-01-02T00:00:00Z"}}`:          "abcd",
	`{"at":{"$lt":"2026-01-02T03:04:05.5+02:00"}}`:   "e",
	`{"at":{"$gte":"2026-01-01T21:00:00.25Z"}}`:      "abcde",
	`{"at":{"$lte":"2026-01-01T13:00:00.25-08:00"}}`: "e",
}

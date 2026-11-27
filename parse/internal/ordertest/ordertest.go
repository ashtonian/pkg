//go:build unit || integration

// Package ordertest is the canonical order of every scalar field type, the
// one every reader of a value is held to: for each type, values in
// ascending order, each with its canonical text and the spellings that
// hold it, and the column types SQLite and PostgreSQL store it in. The
// comparator and canonical text (core.CompareCanonical, core.CanonicalText),
// Evaluate, keysets, keys, patch policies, facet keys and the dialects'
// ORDER BY and facet statements are each run against it, so none of them
// can drift from the others.
package ordertest

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/netip"
	"reflect"
	"time"
	"uuid"

	googleuuid "github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/ashtonian/pkg/parse/core"
)

// Case is one field type's values, ascending in its order. NULL, which
// every case orders after its values, is not listed.
type Case struct {
	Name string
	Type core.TypeInfo
	// Ordered marks a type filters order ($gt, $lt and the rest); the
	// others only equate.
	Ordered bool
	// Sortable marks a type a sort key may be of, which a keyset walks.
	Sortable bool
	// Faceted marks a type a terms facet keys alike on every backend.
	Faceted bool
	// SQLite and Postgres are the column types each database stores the
	// type in.
	SQLite, Postgres string
	Values           []Value
}

// Value is one value of a case: its canonical text — the text the
// database reads the column as — and Go values that each hold it. The
// first spelling is canonical, the value a test stores.
type Value struct {
	Text      string
	Spellings []any
}

// Named types an application declares for a column's values: each holds
// the value its underlying type does.
type (
	spelled string
	count   int64
	ratio   float64
	flag    bool
)

// Cases are every scalar field type (core.IsKeyType).
func Cases() []Case {
	at := func(text string) time.Time {
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			panic(err)
		}
		return t
	}
	ref := "0198a3c2-7a1e-7c00-8000-00000000000a"
	probe := "550e8400-e29b-41d4-a716-446655440000"
	plusOne := time.FixedZone("+01:00", 3600)
	cases := []Case{
		{
			Name: "string", Type: core.TypeInfo{Type: core.FieldTypeString},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "TEXT", Postgres: "text",
			// Code point order: upper case before lower, a prefix first.
			Values: []Value{
				{"A", []any{"A"}},
				{"B", []any{"B"}},
				{"a", []any{"a", new("a"), sql.NullString{String: "a", Valid: true}, spelled("a"), new(spelled("a"))}},
				{"ab", []any{"ab"}},
				{"b", []any{"b"}},
				{"z", []any{"z"}},
				{"é", []any{"é"}},
			},
		},
		{
			Name: "int", Type: core.TypeInfo{Type: core.FieldTypeInt, NumberKind: reflect.Int64},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "INTEGER", Postgres: "bigint",
			Values: []Value{
				{"-9007199254740993", []any{int64(-9007199254740993), json.Number("-9007199254740993")}},
				{"-5", []any{int64(-5), int32(-5), json.Number("-5"), float64(-5), sql.NullInt64{Int64: -5, Valid: true}}},
				{"0", []any{int64(0)}},
				{"7", []any{int64(7), uint8(7), count(7)}},
				{"10", []any{int64(10), json.Number("10")}},
				{"9007199254740993", []any{int64(9007199254740993), json.Number("9007199254740993")}},
			},
		},
		{
			Name: "float", Type: core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float64},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "REAL", Postgres: "double precision",
			Values: []Value{
				{"-2.5", []any{-2.5, json.Number("-2.5")}},
				{"0", []any{0.0, math.Copysign(0, -1), int64(0)}},
				{"0.1", []any{0.1, json.Number("0.1"), ratio(0.1)}},
				{"2.5", []any{2.5, float32(2.5), sql.NullFloat64{Float64: 2.5, Valid: true}}},
				{"10", []any{10.0, int64(10), count(10)}},
				{"1000000000000000000000", []any{1e21}},
			},
		},
		{
			Name: "float32", Type: core.TypeInfo{Type: core.FieldTypeFloat, NumberKind: reflect.Float32},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "REAL", Postgres: "real",
			// At float32 precision: 0.1 is the float32 nearest it, so a
			// float64 0.1 and float32(0.1) are one value, as are 2^24 and
			// 2^24+1, which no float32 tells apart.
			Values: []Value{
				{"-2.5", []any{-2.5, float32(-2.5)}},
				{"0", []any{0.0, float32(0), int64(0)}},
				{"0.1", []any{0.1, float32(0.1), json.Number("0.1"), float64(float32(0.1)), ratio(0.1)}},
				{"0.3", []any{0.3, float32(0.3)}},
				{"2.5", []any{2.5, float32(2.5), sql.NullFloat64{Float64: 2.5, Valid: true}}},
				{"16777216", []any{16777216.0, int64(16777217), json.Number("16777217")}},
			},
		},
		{
			Name: "decimal", Type: core.TypeInfo{Type: core.FieldTypeDecimal},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "TEXT", Postgres: "numeric",
			// By value, never by text: "10" after "9.5", 20 digits one apart.
			Values: []Value{
				{"-100", []any{"-100", json.Number("-100")}},
				{"-0.5", []any{"-0.5", decimal.RequireFromString("-0.50")}},
				{"0", []any{"0", "0.000", "-0"}},
				{"1.5", []any{"1.5", "1.50", decimal.RequireFromString("1.500"), json.Number("1.5"), 1.5, spelled("1.50")}},
				{"9.5", []any{"9.5"}},
				{"10", []any{"10", int64(10), "1e1"}},
				{"12345678901234567890", []any{"12345678901234567890", new(decimal.RequireFromString("12345678901234567890.0"))}},
				{"12345678901234567891", []any{"12345678901234567891"}},
			},
		},
		{
			Name: "bool", Type: core.TypeInfo{Type: core.FieldTypeBool},
			Sortable: true, Faceted: true, SQLite: "INTEGER", Postgres: "boolean",
			Values: []Value{
				{"false", []any{false, sql.NullBool{Valid: true}}},
				{"true", []any{true, new(true), flag(true)}},
			},
		},
		{
			Name: "timestamp", Type: core.TypeInfo{Type: core.FieldTypeTimestamp},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "TEXT", Postgres: "timestamptz",
			// By instant, in any zone; the canonical text is fixed width.
			Values: []Value{
				{"1999-12-31T23:59:59.999999000Z", []any{"1999-12-31T23:59:59.999999000Z", "2000-01-01T00:59:59.999999+01:00"}},
				{"2026-01-02T03:04:05.000000000Z", []any{
					"2026-01-02T03:04:05.000000000Z", "2026-01-02T03:04:05Z", "2026-01-02T04:04:05+01:00",
					at("2026-01-02T03:04:05Z"), time.Date(2026, time.January, 2, 4, 4, 5, 0, plusOne),
					sql.NullTime{Time: at("2026-01-02T03:04:05Z"), Valid: true},
				}},
				{"2026-01-02T03:04:05.500000000Z", []any{"2026-01-02T03:04:05.500000000Z", "2026-01-02T03:04:05.5Z", spelled("2026-01-02T04:04:05.5+01:00")}},
				{"2026-01-02T03:04:06.000000000Z", []any{"2026-01-02T03:04:06.000000000Z", "2026-01-02T05:04:06+02:00"}},
				// Finer than PostgreSQL's microseconds: it holds each rounded
				// (filter.Dialect.StoredKeyValue), down, to even at a tie,
				// and into the next second, still one value apiece.
				{"2026-01-02T03:04:07.000000400Z", []any{"2026-01-02T03:04:07.000000400Z", time.Date(2026, time.January, 2, 3, 4, 7, 400, time.UTC)}},
				{"2026-01-02T03:04:07.000001500Z", []any{"2026-01-02T03:04:07.000001500Z", "2026-01-02T03:04:07.0000015Z"}},
				{"2026-01-02T03:04:07.000004500Z", []any{"2026-01-02T03:04:07.000004500Z"}},
				{"2026-01-02T03:04:07.000005600Z", []any{"2026-01-02T03:04:07.000005600Z"}},
				{"2026-01-02T03:04:07.999999499Z", []any{"2026-01-02T03:04:07.999999499Z"}},
				{"2026-01-02T03:04:07.999999500Z", []any{"2026-01-02T03:04:07.999999500Z", spelled("2026-01-02T04:04:07.9999995+01:00")}},
				{"2100-01-01T00:00:00.000000000Z", []any{"2100-01-01T00:00:00.000000000Z"}},
			},
		},
		{
			Name: "date", Type: core.TypeInfo{Type: core.FieldTypeDate},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "TEXT", Postgres: "date",
			Values: []Value{
				{"1999-12-31", []any{"1999-12-31"}},
				{"2026-01-02", []any{"2026-01-02", core.Date("2026-01-02"), time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC)}},
				{"2026-01-10", []any{"2026-01-10", spelled("2026-01-10")}},
				{"2026-02-01", []any{"2026-02-01", time.Date(2026, time.February, 1, 0, 0, 0, 0, plusOne)}},
			},
		},
		{
			Name: "time", Type: core.TypeInfo{Type: core.FieldTypeTime},
			Ordered: true, Sortable: true, Faceted: true, SQLite: "TEXT", Postgres: "time",
			Values: []Value{
				{"00:00:00", []any{"00:00:00"}},
				{"03:04:05", []any{"03:04:05", core.LocalTime("03:04:05"), "03:04:05.000000", time.Date(0, time.January, 1, 3, 4, 5, 0, time.UTC)}},
				{"03:04:05.25", []any{"03:04:05.25", spelled("03:04:05.250")}},
				{"03:04:05.5", []any{"03:04:05.5", core.LocalTime("03:04:05.5"), time.Date(0, time.January, 1, 3, 4, 5, 500000000, time.UTC)}},
				{"23:59:59.999999", []any{"23:59:59.999999"}},
			},
		},
		{
			Name: "uuid", Type: core.TypeInfo{Type: core.FieldTypeUUID},
			Sortable: true, Faceted: true, SQLite: "TEXT", Postgres: "uuid",
			// A UUID's bytes, which its lower-case hyphenated text orders
			// alike, in any spelling.
			Values: []Value{
				{"00000000-0000-0000-0000-000000000001", []any{"00000000-0000-0000-0000-000000000001"}},
				{ref, []any{
					ref, "0198A3C2-7A1E-7C00-8000-00000000000A", "0198a3c27a1e7c00800000000000000a",
					googleuuid.MustParse(ref), uuid.MustParse(ref),
				}},
				{probe, []any{probe, "550e8400e29b41d4a716446655440000", "{550e8400-e29b-41d4-a716-446655440000}", spelled("550E8400-E29B-41D4-A716-446655440000")}},
				{"ffffffff-ffff-ffff-ffff-ffffffffffff", []any{"ffffffff-ffff-ffff-ffff-ffffffffffff", "FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF"}},
			},
		},
		{
			Name: "interval", Type: core.TypeInfo{Type: core.FieldTypeInterval, SQLType: core.SQLInterval},
			SQLite: "TEXT", Postgres: "interval",
			// No sort orders an interval; a key of one locks in its text's
			// order (filter.KeyOrder).
			Values: []Value{
				{"P1D", []any{"P1D"}},
				{"P1M", []any{"P1M"}},
				{"P1Y", []any{"P1Y", "P12M"}},
				{"P7D", []any{"P7D", "P1W", spelled("P1W")}},
				{"PT1H", []any{"PT1H", "PT60M"}},
				{"PT30M", []any{"PT30M"}},
			},
		},
		{
			Name: "inet", Type: core.TypeInfo{Type: core.FieldTypeInet, SQLType: core.SQLInet},
			SQLite: "TEXT", Postgres: "inet",
			Values: []Value{
				{"10.0.0.1", []any{"10.0.0.1", "10.0.0.1/32", netip.MustParseAddr("10.0.0.1")}},
				{"10.0.0.1/24", []any{"10.0.0.1/24"}},
				{"192.168.0.5", []any{"192.168.0.5", spelled("192.168.0.5/32")}},
				{"2001:db8::1", []any{"2001:db8::1", "2001:DB8:0:0:0:0:0:1"}},
				{"9.9.9.9", []any{"9.9.9.9"}},
			},
		},
		{
			Name: "cidr", Type: core.TypeInfo{Type: core.FieldTypeCIDR, SQLType: core.SQLCIDR},
			SQLite: "TEXT", Postgres: "cidr",
			Values: []Value{
				{"10.0.0.0/8", []any{"10.0.0.0/8", netip.MustParsePrefix("10.0.0.0/8")}},
				{"192.168.0.0/16", []any{"192.168.0.0/16", spelled("192.168.0.0/16")}},
				{"2001:db8::/32", []any{"2001:db8::/32", "2001:DB8::/32"}},
			},
		},
		{
			Name: "macaddr", Type: core.TypeInfo{Type: core.FieldTypeMACAddr},
			Faceted: true, SQLite: "TEXT", Postgres: "macaddr",
			Values: []Value{
				{"08:00:2b:01:02:03", []any{"08:00:2b:01:02:03", "08-00-2B-01-02-03", "0800.2b01.0203", core.MACAddr("08:00:2b:01:02:03")}},
				{"ff:ff:ff:ff:ff:ff", []any{"ff:ff:ff:ff:ff:ff", spelled("FF-FF-FF-FF-FF-FF")}},
			},
		},
	}
	return cases
}

// Stored is v's canonical value as its case's column holds it: what a
// statement binds to store it.
func (c Case) Stored(v Value) (any, error) {
	return core.NormalizeScalar(c.Type.Type, c.Type.NumberKind, v.Spellings[0])
}

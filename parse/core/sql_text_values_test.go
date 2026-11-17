//go:build unit

package core_test

import (
	"database/sql/driver"
	"encoding/json"
	"math"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// textValue is the codec surface every value type shares.
type textValue interface {
	MarshalText() ([]byte, error)
	MarshalJSON() ([]byte, error)
	Value() (driver.Value, error)
}

// assertCanonical checks that v writes canonical as text, JSON and a SQL
// parameter.
func assertCanonical(t *testing.T, v textValue, canonical string) {
	t.Helper()
	text, err := v.MarshalText()
	require.NoError(t, err)
	assert.Equal(t, canonical, string(text))
	encoded, err := v.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, `"`+canonical+`"`, string(encoded))
	value, err := v.Value()
	require.NoError(t, err)
	assert.Equal(t, canonical, value)
}

func TestLocalTime(t *testing.T) {
	for _, tt := range []struct{ in, canonical string }{
		{"00:00:00", "00:00:00"},
		{"23:59:59", "23:59:59"},
		{"10:30:00.5", "10:30:00.5"},
		{"10:30:00.500", "10:30:00.5"},
		{"10:30:00.000000", "10:30:00"},
		{"10:30:00.123456", "10:30:00.123456"},
		{"10:30:00.123456000", "10:30:00.123456"},
	} {
		t.Run(tt.in, func(t *testing.T) {
			parsed, err := core.ParseLocalTime(tt.in)
			require.NoError(t, err)
			assert.Equal(t, core.LocalTime(tt.canonical), parsed)
			assertCanonical(t, core.LocalTime(tt.in), tt.canonical)
			var decoded core.LocalTime
			require.NoError(t, json.Unmarshal([]byte(`"`+tt.in+`"`), &decoded))
			assert.Equal(t, parsed, decoded)
			var scanned core.LocalTime
			require.NoError(t, scanned.Scan([]byte(tt.in)))
			assert.Equal(t, parsed, scanned)
		})
	}
	for _, invalid := range []string{"", "24:00:00", "23:59:60", "10:60:00", "1:30:00", "10:30", "10:30:00Z",
		"10:30:00+02:00", "10:30:00.", "10:30:00.1234567", "10:30:00.1234567890", "T10:30:00", " 10:30:00", "10:30:00.-1"} {
		_, err := core.ParseLocalTime(invalid)
		assert.Error(t, err, invalid)
	}
	var missing core.LocalTime
	assert.Error(t, json.Unmarshal([]byte(`null`), &missing))
	_, err := missing.Value()
	assert.Error(t, err, "the zero LocalTime is invalid")

	var fromDriver core.LocalTime
	require.NoError(t, fromDriver.Scan(time.Date(0, 1, 1, 10, 30, 0, 500_000_000, time.FixedZone("x", 3600))))
	assert.Equal(t, core.LocalTime("10:30:00.5"), fromDriver, "the clock as written, not converted")
	assert.Error(t, fromDriver.Scan(time.Date(0, 1, 1, 10, 30, 0, 1, time.UTC)), "nanoseconds have no TIME value")
}

// TestLocalTimeTextOrder checks that canonical text order is time order,
// which SQLite's TEXT comparison and keyset cursors rely on.
func TestLocalTimeTextOrder(t *testing.T) {
	ordered := []string{"00:00:00", "09:59:59.999999", "10:00:00", "10:00:00.000001", "10:00:00.05", "10:00:00.5", "10:00:01", "23:59:59.999999"}
	assert.True(t, slices.IsSorted(ordered))
	for _, text := range ordered {
		parsed, err := core.ParseLocalTime(text)
		require.NoError(t, err)
		assert.Equal(t, text, string(parsed))
	}
}

func TestInterval(t *testing.T) {
	const second = int64(time.Second / time.Microsecond)
	// The canonical texts are PostgreSQL 18's under IntervalStyle iso_8601
	// for the same field values.
	for _, tt := range []struct {
		value     core.Interval
		canonical string
	}{
		{core.Interval{}, "PT0S"},
		{core.Interval{Months: 14, Days: 3, Microseconds: 4*3600*second + 5*60*second + 6*second + 500_000}, "P1Y2M3DT4H5M6.5S"},
		{core.Interval{Days: -1}, "P-1D"},
		{core.Interval{Microseconds: -(4*3600*second + 5*60*second + 6*second)}, "PT-4H-5M-6S"},
		{core.Interval{Microseconds: 36 * 3600 * second}, "PT36H"},
		{core.Interval{Months: -14}, "P-1Y-2M"},
		{core.Interval{Days: 1, Microseconds: -3600 * second}, "P1DT-1H"},
		{core.Interval{Microseconds: -500_000}, "PT-0.5S"},
		{core.Interval{Months: -1, Days: 1}, "P-1M1D"},
		{core.Interval{Months: 1, Days: -30}, "P1M-30D"},
		{core.Interval{Microseconds: 1}, "PT0.000001S"},
		{core.Interval{Microseconds: 100*3600*second + 59*60*second + 59*second + 999_999}, "PT100H59M59.999999S"},
		{core.Interval{Months: math.MinInt32 + 1, Days: math.MinInt32 + 1, Microseconds: math.MinInt64 + 1}, "P-178956970Y-7M-2147483647DT-2562047788H-54.775807S"},
	} {
		t.Run(tt.canonical, func(t *testing.T) {
			assert.Equal(t, tt.canonical, tt.value.String())
			assertCanonical(t, tt.value, tt.canonical)
			parsed, err := core.ParseInterval(tt.canonical)
			require.NoError(t, err)
			assert.Equal(t, tt.value, parsed)
			var decoded core.Interval
			require.NoError(t, json.Unmarshal([]byte(`"`+tt.canonical+`"`), &decoded))
			assert.Equal(t, tt.value, decoded)
		})
	}
	for _, tt := range []struct{ in, canonical string }{
		{"P1Y2M3W4DT5H6M7.5S", "P1Y2M25DT5H6M7.5S"},
		{"P14M", "P1Y2M"},
		{"PT90M", "PT1H30M"},
		{"PT100000S", "PT27H46M40S"},
		{"PT0.500000000S", "PT0.5S"},
		{"P0D", "PT0S"},
		{"PT-0S", "PT0S"},
		{"P2W", "P14D"},
	} {
		parsed, err := core.ParseInterval(tt.in)
		require.NoError(t, err, tt.in)
		assert.Equal(t, tt.canonical, parsed.String(), tt.in)
	}
	for _, invalid := range []string{"", "P", "PT", "P1DT", "1D", "-P1D", "P+1D", "p1d", "P1.5D", "PT1.5H", "P1D2Y", "P1Y1Y",
		"P1H", "PT1D", "PT0.0000001S", "PT1.S", "P0001-02-03T04:05:06", "P2147483648D", "P178956971Y",
		"PT9223372036854775807S", "P178956970Y7M2147483647DT2562047788H54.775807S", "infinity"} {
		_, err := core.ParseInterval(invalid)
		assert.Error(t, err, invalid)
	}
	_, err := core.Interval{Months: math.MaxInt32, Days: math.MaxInt32, Microseconds: math.MaxInt64}.Value()
	assert.Error(t, err, "PostgreSQL would read the reserved fields back as infinity")
	var missing core.Interval
	assert.Error(t, json.Unmarshal([]byte(`null`), &missing))
}

// TestIntervalScan reads the texts drivers return for a PostgreSQL interval.
func TestIntervalScan(t *testing.T) {
	const second = int64(time.Second / time.Microsecond)
	for _, tt := range []struct {
		in   string
		want core.Interval
	}{
		{"P1Y2M3DT4H5M6.5S", core.Interval{Months: 14, Days: 3, Microseconds: 4*3600*second + 5*60*second + 6*second + 500_000}},
		// IntervalStyle postgres, the server's default.
		{"1 year 2 mons 3 days 04:05:06.5", core.Interval{Months: 14, Days: 3, Microseconds: 4*3600*second + 5*60*second + 6*second + 500_000}},
		{"-1 days +02:00:00", core.Interval{Days: -1, Microseconds: 2 * 3600 * second}},
		{"1 mon -30 days", core.Interval{Months: 1, Days: -30}},
		{"-00:00:00.5", core.Interval{Microseconds: -500_000}},
		{"00:00:00", core.Interval{}},
		{"36:00:00", core.Interval{Microseconds: 36 * 3600 * second}},
		{"-1 years -2 mons", core.Interval{Months: -14}},
		// pgx's text.
		{"14 mon 3 day 04:05:06.500000", core.Interval{Months: 14, Days: 3, Microseconds: 4*3600*second + 5*60*second + 6*second + 500_000}},
		{"-1 day -02:00:00", core.Interval{Days: -1, Microseconds: -2 * 3600 * second}},
	} {
		var scanned core.Interval
		require.NoError(t, scanned.Scan(tt.in), tt.in)
		assert.Equal(t, tt.want, scanned, tt.in)
	}
	for _, invalid := range []any{"infinity", "-infinity", "@ 1 mon", "1-2 3 4:05:06", "1 mon 1 mon", "1 week", "1 day 25:61:00", "", 42} {
		var scanned core.Interval
		assert.Error(t, scanned.Scan(invalid), invalid)
	}
}

func TestInet(t *testing.T) {
	for _, tt := range []struct{ in, canonical string }{
		{"192.168.0.5", "192.168.0.5"},
		{"192.168.0.5/32", "192.168.0.5"},
		{"192.168.0.5/24", "192.168.0.5/24"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"2001:0DB8:0000:0000:0000:0000:0000:0001", "2001:db8::1"},
		{"2001:db8::1/128", "2001:db8::1"},
		{"2001:db8::1/64", "2001:db8::1/64"},
		{"::ffff:1.2.3.4", "::ffff:1.2.3.4"},
		{"2001:db8:0:1:1:1:1:1", "2001:db8:0:1:1:1:1:1"},
		{"::0.2.0.3", "::2:3"}, // PostgreSQL's abbrev() of an IPv4-compatible address
	} {
		t.Run(tt.in, func(t *testing.T) {
			parsed, err := core.ParseInet(tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.canonical, parsed.String())
			assertCanonical(t, parsed, tt.canonical)
			var scanned core.Inet
			require.NoError(t, scanned.Scan(tt.in))
			assert.Equal(t, parsed, scanned)
		})
	}
	host, err := core.ParseInet("192.168.0.5")
	require.NoError(t, err)
	network, err := core.ParseInet("192.168.0.5/24")
	require.NoError(t, err)
	assert.NotEqual(t, host, network, "the prefix length is part of the value, as in PostgreSQL")
	assert.Equal(t, netip.MustParseAddr("192.168.0.5"), network.Addr())

	for _, invalid := range []string{"", "not-an-ip", "fe80::1%eth0", "192.168.0.256", "192.168.000.5", "192.168.0.5/33", "2001:db8::/129", "10/8"} {
		_, err := core.ParseInet(invalid)
		assert.Error(t, err, invalid)
	}
	_, err = core.InetFromAddr(netip.MustParseAddr("fe80::1%eth0"))
	assert.Error(t, err)
	_, err = core.Inet{}.Value()
	assert.Error(t, err, "the zero Inet is invalid")
}

func TestCIDR(t *testing.T) {
	for _, tt := range []struct{ in, canonical string }{
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"192.168.0.5/32", "192.168.0.5/32"},
		{"2001:DB8::/32", "2001:db8::/32"},
		{"0.0.0.0/0", "0.0.0.0/0"},
	} {
		parsed, err := core.ParseCIDR(tt.in)
		require.NoError(t, err, tt.in)
		assertCanonical(t, parsed, tt.canonical)
	}
	for _, invalid := range []string{"10.0.0.1/8", "10.0.0.0", "2001:db8::1/32", "not/8", ""} {
		_, err := core.ParseCIDR(invalid)
		assert.Error(t, err, invalid)
	}
	_, err := core.CIDRFromPrefix(netip.MustParsePrefix("10.1.0.0/8"))
	assert.Error(t, err)
}

func TestMACAddr(t *testing.T) {
	for _, in := range []string{"08:00:2b:01:02:03", "08:00:2B:01:02:03", "08-00-2b-01-02-03", "0800.2b01.0203", "08002b010203"} {
		parsed, err := core.ParseMACAddr(in)
		require.NoError(t, err, in)
		assert.Equal(t, core.MACAddr("08:00:2b:01:02:03"), parsed)
		assertCanonical(t, core.MACAddr(in), "08:00:2b:01:02:03")
	}
	for _, invalid := range []string{"", "0800-2b01-0203", "08:00:2b:ff:fe:01:02:03", "08:00:2b:01:02", "zz:00:2b:01:02:03"} {
		_, err := core.ParseMACAddr(invalid)
		assert.Error(t, err, invalid)
	}
	hw, err := core.MACAddr("08-00-2B-01-02-03").HardwareAddr()
	require.NoError(t, err)
	assert.Equal(t, net.HardwareAddr{0x08, 0x00, 0x2b, 0x01, 0x02, 0x03}, hw)
}

func TestSQLTextValueTypes(t *testing.T) {
	for _, tt := range []struct {
		goType reflect.Type
		want   core.FieldType
	}{
		{reflect.TypeFor[core.LocalTime](), core.FieldTypeTime},
		{reflect.TypeFor[*core.Interval](), core.FieldTypeInterval},
		{reflect.TypeFor[core.Inet](), core.FieldTypeInet},
		{reflect.TypeFor[core.CIDR](), core.FieldTypeCIDR},
		{reflect.TypeFor[core.MACAddr](), core.FieldTypeMACAddr},
	} {
		assert.Equal(t, core.TypeInfo{Type: tt.want}, core.TypeOf(tt.goType), tt.goType.String())
	}
	assert.Equal(t, core.TypeInfo{Type: core.FieldTypeArray, Elem: core.FieldTypeInet}, core.TypeOf(reflect.TypeFor[[]core.Inet]()))
	assert.Equal(t, core.TypeInfo{Type: core.FieldTypeObject}, core.TypeOf(reflect.TypeFor[netip.Prefix]()), "only the value types carry the contract")
}

func TestNormalizeSQLTextValues(t *testing.T) {
	for _, tt := range []struct {
		name      string
		fieldType core.FieldType
		in        any
		canonical string
	}{
		{"time string", core.FieldTypeTime, "10:30:00.500", "10:30:00.5"},
		{"time value", core.FieldTypeTime, new(core.LocalTime("10:30:00")), "10:30:00"},
		{"interval string", core.FieldTypeInterval, "PT90M", "PT1H30M"},
		{"interval value", core.FieldTypeInterval, core.Interval{Months: 1}, "P1M"},
		{"inet string", core.FieldTypeInet, "2001:0db8::0001", "2001:db8::1"},
		{"inet addr", core.FieldTypeInet, netip.MustParseAddr("10.0.0.1"), "10.0.0.1"},
		{"inet prefix", core.FieldTypeInet, netip.MustParsePrefix("10.0.0.1/8"), "10.0.0.1/8"},
		{"cidr prefix", core.FieldTypeCIDR, netip.MustParsePrefix("10.0.0.0/8"), "10.0.0.0/8"},
		{"mac hardware address", core.FieldTypeMACAddr, net.HardwareAddr{8, 0, 0x2b, 1, 2, 3}, "08:00:2b:01:02:03"},
		{"mac string", core.FieldTypeMACAddr, "08-00-2B-01-02-03", "08:00:2b:01:02:03"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.NormalizeScalar(tt.fieldType, reflect.Invalid, tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.canonical, got)
		})
	}
	for _, tt := range []struct {
		fieldType core.FieldType
		in        any
	}{
		{core.FieldTypeTime, "24:00:00"}, {core.FieldTypeTime, 1030}, {core.FieldTypeTime, nil},
		{core.FieldTypeInterval, "1 day"}, {core.FieldTypeInterval, time.Hour},
		{core.FieldTypeInet, "fe80::1%eth0"}, {core.FieldTypeInet, net.ParseIP("10.0.0.1")},
		{core.FieldTypeCIDR, "10.0.0.1/8"}, {core.FieldTypeCIDR, netip.MustParseAddr("10.0.0.0")},
		{core.FieldTypeMACAddr, net.HardwareAddr{1, 2, 3, 4, 5, 6, 7, 8}},
	} {
		_, err := core.NormalizeScalar(tt.fieldType, reflect.Invalid, tt.in)
		assert.Error(t, err, "%s %v", tt.fieldType, tt.in)
	}
}

package core

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
)

// Interval is a SQL INTERVAL, held as PostgreSQL holds one: months, days and
// microseconds, three independent fields. A month is not 30 days, and a day
// is not 24 hours, so two intervals are equal only when all three fields are
// equal; there is no order between them.
//
// Its canonical text is the ISO 8601 duration PostgreSQL writes under
// IntervalStyle iso_8601. Months are written as years and months, the
// microseconds as hours, minutes and seconds, each field carrying its own
// sign: P1Y2M3DT4H5M6.5S, P1DT-1H, PT0S. JSON, text, SQL parameters and
// cursors use it. The zero value is PT0S.
type Interval struct {
	Months       int32
	Days         int32
	Microseconds int64
}

const (
	microsPerSecond = 1_000_000
	microsPerMinute = 60 * microsPerSecond
	microsPerHour   = 60 * microsPerMinute
)

// ParseInterval reads an ISO 8601 duration in the designator format:
// P[nY][nM][nW][nD][T[nH][nM][nS]], each n an integer with an optional
// leading "-", and the seconds a decimal with up to six fractional digits
// (more only when they are zeros). A week is seven days. Fractions of other
// fields, the alternative format and a leading sign before "P" are refused,
// as is a total outside PostgreSQL's interval range.
func ParseInterval(value string) (Interval, error) {
	invalid := func(reason string) (Interval, error) {
		return Interval{}, fmt.Errorf("invalid ISO 8601 duration %q: %s", value, reason)
	}
	rest, ok := strings.CutPrefix(value, "P")
	if !ok {
		return invalid("expected P[nY][nM][nW][nD][T[nH][nM][nS]]")
	}
	datePart, timePart, hasTime := strings.Cut(rest, "T")
	if datePart == "" && !hasTime || hasTime && timePart == "" {
		return invalid("no fields")
	}
	var months, days, micros fieldSum
	if err := readDesignators(datePart, "YMWD", func(unit byte, n int64, frac int64) error {
		if frac != 0 {
			return errors.New("only seconds may have a fraction")
		}
		switch unit {
		case 'Y':
			return months.add(n, 12)
		case 'M':
			return months.add(n, 1)
		case 'W':
			return days.add(n, 7)
		}
		return days.add(n, 1)
	}); err != nil {
		return invalid(err.Error())
	}
	if err := readDesignators(timePart, "HMS", func(unit byte, n int64, frac int64) error {
		switch unit {
		case 'H':
			if frac != 0 {
				return errors.New("only seconds may have a fraction")
			}
			return micros.add(n, microsPerHour)
		case 'M':
			if frac != 0 {
				return errors.New("only seconds may have a fraction")
			}
			return micros.add(n, microsPerMinute)
		}
		if err := micros.add(n, microsPerSecond); err != nil {
			return err
		}
		return micros.add(frac, 1)
	}); err != nil {
		return invalid(err.Error())
	}
	iv, err := intervalOf(months, days, micros)
	if err != nil {
		return invalid(err.Error())
	}
	return iv, nil
}

// readDesignators reads a run of [-]n<unit> fields whose units appear in
// order within units, each at most once; only an "S" field may carry a
// fraction, passed to each as signed microseconds.
func readDesignators(s, units string, each func(unit byte, n, frac int64) error) error {
	next := 0
	for s != "" {
		end := strings.IndexAny(s, units)
		if end < 0 {
			return fmt.Errorf("%q has no unit", s)
		}
		number, unit := s[:end], s[end]
		at := strings.IndexByte(units[next:], unit)
		if at < 0 {
			return fmt.Errorf("unit %c out of order or repeated", unit)
		}
		next += at + 1
		n, frac, err := designatorNumber(number, unit == 'S')
		if err != nil {
			return err
		}
		if err := each(unit, n, frac); err != nil {
			return err
		}
		s = s[end+1:]
	}
	return nil
}

// designatorNumber reads [-]digits, and with fraction [-]digits[.digits],
// returning the integer part and the fraction as signed microseconds.
func designatorNumber(s string, fraction bool) (int64, int64, error) {
	negative := strings.HasPrefix(s, "-")
	whole, frac, hasFrac := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	if hasFrac && (!fraction || frac == "") {
		return 0, 0, fmt.Errorf("%q is not a number of the field", s)
	}
	n, ok := decimalDigits(whole)
	if !ok {
		return 0, 0, fmt.Errorf("%q is not a number of the field", s)
	}
	micros, err := microFraction(frac)
	if err != nil {
		return 0, 0, err
	}
	if negative {
		return -n, -micros, nil
	}
	return n, micros, nil
}

// microFraction reads the digits after a decimal point as microseconds: up
// to nine digits, those past the sixth zeros.
func microFraction(frac string) (int64, error) {
	if frac == "" {
		return 0, nil
	}
	if len(frac) > 9 || !digits(frac) {
		return 0, fmt.Errorf("fraction %q is not 1 to 9 digits", frac)
	}
	if len(frac) > 6 && strings.TrimRight(frac[6:], "0") != "" {
		return 0, errors.New("precision finer than a microsecond")
	}
	micros, _ := decimalDigits((frac + "000000")[:6])
	return micros, nil
}

// decimalDigits reads 1 to 18 ASCII digits, which always fit an int64.
func decimalDigits(s string) (int64, bool) {
	if len(s) > 18 || !digits(s) {
		return 0, false
	}
	var n int64
	for i := range len(s) {
		n = n*10 + int64(s[i]-'0')
	}
	return n, true
}

// fieldSum accumulates one interval field, refusing int64 overflow.
type fieldSum struct{ total int64 }

// add adds n×scale, scale positive.
func (f *fieldSum) add(n, scale int64) error {
	if n > math.MaxInt64/scale || n < math.MinInt64/scale {
		return errors.New("out of range")
	}
	product := n * scale
	if product > 0 && f.total > math.MaxInt64-product || product < 0 && f.total < math.MinInt64-product {
		return errors.New("out of range")
	}
	f.total += product
	return nil
}

// intervalOf checks accumulated fields against PostgreSQL's ranges.
func intervalOf(months, days, micros fieldSum) (Interval, error) {
	if months.total < math.MinInt32 || months.total > math.MaxInt32 || days.total < math.MinInt32 || days.total > math.MaxInt32 {
		return Interval{}, errors.New("out of range")
	}
	iv := Interval{Months: int32(months.total), Days: int32(days.total), Microseconds: micros.total}
	if err := iv.check(); err != nil {
		return Interval{}, err
	}
	return iv, nil
}

// check refuses the two field combinations PostgreSQL 17 reserves for
// infinite intervals: written, either would be read back as infinity.
func (iv Interval) check() error {
	if iv == (Interval{math.MaxInt32, math.MaxInt32, math.MaxInt64}) || iv == (Interval{math.MinInt32, math.MinInt32, math.MinInt64}) {
		return errors.New("the field values PostgreSQL reserves for an infinite interval")
	}
	return nil
}

// String is the canonical ISO 8601 text, the form PostgreSQL's iso_8601
// IntervalStyle writes.
func (iv Interval) String() string {
	years, months := iv.Months/12, iv.Months%12
	t := iv.Microseconds
	hours := t / microsPerHour
	t -= hours * microsPerHour
	minutes := t / microsPerMinute
	t -= minutes * microsPerMinute
	seconds, fraction := t/microsPerSecond, t%microsPerSecond
	if years == 0 && months == 0 && iv.Days == 0 && hours == 0 && minutes == 0 && t == 0 {
		return "PT0S"
	}
	var b strings.Builder
	b.WriteByte('P')
	writeDesignator(&b, int64(years), 'Y')
	writeDesignator(&b, int64(months), 'M')
	writeDesignator(&b, int64(iv.Days), 'D')
	if hours == 0 && minutes == 0 && t == 0 {
		return b.String()
	}
	b.WriteByte('T')
	writeDesignator(&b, hours, 'H')
	writeDesignator(&b, minutes, 'M')
	if t != 0 {
		if t < 0 {
			b.WriteByte('-')
			seconds, fraction = -seconds, -fraction
		}
		b.WriteString(strconv.FormatInt(seconds, 10))
		if fraction != 0 {
			b.WriteByte('.')
			b.WriteString(strings.TrimRight(fmt.Sprintf("%06d", fraction), "0"))
		}
		b.WriteByte('S')
	}
	return b.String()
}

func writeDesignator(b *strings.Builder, n int64, unit byte) {
	if n != 0 {
		b.WriteString(strconv.FormatInt(n, 10))
		b.WriteByte(unit)
	}
}

func (iv Interval) MarshalText() ([]byte, error) {
	if err := iv.check(); err != nil {
		return nil, fmt.Errorf("interval %v: %w", iv, err)
	}
	return []byte(iv.String()), nil
}

func (iv *Interval) UnmarshalText(value []byte) error {
	parsed, err := ParseInterval(string(value))
	if err != nil {
		return err
	}
	*iv = parsed
	return nil
}

func (iv Interval) MarshalJSON() ([]byte, error) {
	text, err := iv.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (iv *Interval) UnmarshalJSON(value []byte) error {
	text, err := jsonText(value, "an interval", "*Interval")
	if err != nil {
		return err
	}
	return iv.UnmarshalText([]byte(text))
}

// Value writes the canonical ISO 8601 text, which PostgreSQL reads under any
// IntervalStyle.
func (iv Interval) Value() (driver.Value, error) {
	text, err := iv.MarshalText()
	if err != nil {
		return nil, err
	}
	return string(text), nil
}

// Scan accepts the canonical text, and the text drivers read a PostgreSQL
// interval as: the postgres IntervalStyle ("1 year 2 mons -3 days
// +04:05:06.5", the server's default) and pgx's ("14 mon -3 day
// 04:05:06.500000"). The sql_standard and postgres_verbose styles are
// refused. An infinite interval is refused: it has no ISO 8601 form.
func (iv *Interval) Scan(value any) error {
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case []byte:
		text = string(v)
	default:
		return fmt.Errorf("scanning %s as an interval: unsupported source type", gowalker.TypeText(reflect.TypeOf(value)))
	}
	if strings.HasPrefix(text, "P") {
		return iv.UnmarshalText([]byte(text))
	}
	parsed, err := parsePostgresInterval(text)
	if err != nil {
		return err
	}
	*iv = parsed
	return nil
}

// parsePostgresInterval reads the postgres IntervalStyle and pgx's text:
// signed counts of years, months and days, each unit at most once, then an
// optional signed clock [-]H:MM:SS[.ffffff] with any number of hours.
func parsePostgresInterval(text string) (Interval, error) {
	invalid := func() (Interval, error) {
		return Interval{}, fmt.Errorf("invalid interval %q: expected ISO 8601 or PostgreSQL's postgres IntervalStyle", text)
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return invalid()
	}
	var months, days, micros fieldSum
	seen := map[string]bool{}
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if strings.Contains(f, ":") {
			if i != len(fields)-1 {
				return invalid()
			}
			if err := readClock(f, &micros); err != nil {
				return invalid()
			}
			break
		}
		if i+1 == len(fields) {
			return invalid()
		}
		n, err := strconv.ParseInt(strings.TrimPrefix(f, "+"), 10, 64)
		if err != nil || f == "+" || f == "-" {
			return invalid()
		}
		unit := strings.TrimSuffix(fields[i+1], "s")
		if seen[unit] {
			return invalid()
		}
		seen[unit] = true
		switch unit {
		case "year":
			err = months.add(n, 12)
		case "mon":
			err = months.add(n, 1)
		case "day":
			err = days.add(n, 1)
		default:
			return invalid()
		}
		if err != nil {
			return invalid()
		}
		i++
	}
	iv, err := intervalOf(months, days, micros)
	if err != nil {
		return invalid()
	}
	return iv, nil
}

// readClock adds [+-]H:MM:SS[.ffffff], any number of hours, to micros.
func readClock(f string, micros *fieldSum) error {
	sign := int64(1)
	if strings.HasPrefix(f, "-") {
		sign = -1
	}
	clock, frac, hasFrac := strings.Cut(strings.TrimLeft(f, "+-"), ".")
	parts := strings.Split(clock, ":")
	if len(parts) != 3 || len(parts[1]) != 2 || len(parts[2]) != 2 || hasFrac && (frac == "" || len(frac) > 6) {
		return errors.New("not a clock")
	}
	hours, hok := decimalDigits(parts[0])
	minutes, mok := decimalDigits(parts[1])
	seconds, sok := decimalDigits(parts[2])
	fraction, err := microFraction(frac)
	if !hok || !mok || !sok || err != nil || minutes > 59 || seconds > 59 {
		return errors.New("not a clock")
	}
	for _, part := range [][2]int64{{hours, microsPerHour}, {minutes, microsPerMinute}, {seconds, microsPerSecond}, {fraction, 1}} {
		if err := micros.add(sign*part[0], part[1]); err != nil {
			return err
		}
	}
	return nil
}

// normalizeInterval is NormalizeScalar's FieldTypeInterval: the canonical
// text of an Interval or of an ISO 8601 string.
func normalizeInterval(v any) (string, error) {
	switch iv := v.(type) {
	case Interval:
		text, err := iv.MarshalText()
		return string(text), err
	case string:
		parsed, err := ParseInterval(iv)
		if err != nil {
			return "", err
		}
		return parsed.String(), nil
	}
	return "", fmt.Errorf("expected ISO 8601 duration string, got %s", gowalker.TypeText(reflect.TypeOf(v)))
}

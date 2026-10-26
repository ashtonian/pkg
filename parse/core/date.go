package core

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// Date is a Gregorian calendar date, independent of timezone or time of day.
// Its JSON, text and SQL representations are exactly YYYY-MM-DD.
// The zero value is invalid; use *Date for nullable values.
type Date string

// ParseDate validates a civil date without interpreting it as an instant.
func ParseDate(value string) (Date, error) {
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil || len(value) != 10 || parsed.Year() < 1 || parsed.Format(time.DateOnly) != value {
		return "", fmt.Errorf("invalid civil date %q: expected YYYY-MM-DD", value)
	}
	return Date(value), nil
}

func (d Date) String() string { return string(d) }

// Time returns the calendar components at UTC midnight for APIs that require
// time.Time. The conversion does not apply a viewing or session timezone.
func (d Date) Time() (time.Time, error) {
	if _, err := ParseDate(string(d)); err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.DateOnly, string(d))
}

func (d Date) MarshalText() ([]byte, error) {
	if _, err := ParseDate(string(d)); err != nil {
		return nil, err
	}
	return []byte(d), nil
}

func (d *Date) UnmarshalText(value []byte) error {
	parsed, err := ParseDate(string(value))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

func (d Date) MarshalJSON() ([]byte, error) {
	if _, err := d.MarshalText(); err != nil {
		return nil, err
	}
	return json.Marshal(string(d))
}

func (d *Date) UnmarshalJSON(value []byte) error {
	var text string
	if string(value) == "null" {
		return fmt.Errorf("a civil date cannot be null; use *Date")
	}
	if err := json.Unmarshal(value, &text); err != nil {
		return err
	}
	return d.UnmarshalText([]byte(text))
}

// Value preserves the date-only spelling for database/sql and pgx's Valuer path.
func (d Date) Value() (driver.Value, error) {
	if _, err := d.MarshalText(); err != nil {
		return nil, err
	}
	return string(d), nil
}

// Scan accepts native SQL DATE values and canonical text. It never converts a
// timestamp's timezone or discards a time component supplied as text.
func (d *Date) Scan(value any) error {
	switch v := value.(type) {
	case time.Time:
		if v.Hour() != 0 || v.Minute() != 0 || v.Second() != 0 || v.Nanosecond() != 0 {
			return fmt.Errorf("SQL date contains a time of day")
		}
		return d.UnmarshalText([]byte(v.Format(time.DateOnly)))
	case string:
		return d.UnmarshalText([]byte(v))
	case []byte:
		return d.UnmarshalText(v)
	default:
		return fmt.Errorf("cannot scan %T as a civil date", value)
	}
}

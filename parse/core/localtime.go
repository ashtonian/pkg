package core

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// LocalTime is a time of day without a date or a timezone: the SQL TIME
// type. Its canonical text is HH:MM:SS, hours 00–23, then "." and one to
// six fractional digits when the time has a fraction, without trailing
// zeros. That is PostgreSQL's output, and its text order is chronological.
// JSON, text, SQL parameters and cursors use it. The zero value is invalid;
// use *LocalTime for nullable values.
type LocalTime string

// ParseLocalTime reads HH:MM:SS with an optional fraction of up to nine
// digits, and returns the canonical text. It refuses an offset, a fraction
// finer than a microsecond (PostgreSQL would round it), 24:00:00 and a leap
// second.
func ParseLocalTime(value string) (LocalTime, error) {
	clock, fraction, hasFraction := strings.Cut(value, ".")
	if len(clock) != 8 || clock[2] != ':' || clock[5] != ':' ||
		!digits(clock[0:2]) || !digits(clock[3:5]) || !digits(clock[6:8]) ||
		clock[0:2] > "23" || clock[3:5] > "59" || clock[6:8] > "59" {
		return "", fmt.Errorf("invalid time of day %q: expected HH:MM:SS with hours 00-23", value)
	}
	if !hasFraction {
		return LocalTime(clock), nil
	}
	if len(fraction) == 0 || len(fraction) > 9 || !digits(fraction) {
		return "", fmt.Errorf("invalid time of day %q: a fraction has 1 to 9 digits", value)
	}
	if len(fraction) > 6 && strings.TrimRight(fraction[6:], "0") != "" {
		return "", fmt.Errorf("invalid time of day %q: precision finer than a microsecond", value)
	}
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return LocalTime(clock), nil
	}
	return LocalTime(clock + "." + fraction), nil
}

func (t LocalTime) String() string { return string(t) }

// MarshalText writes the canonical text, also of a LocalTime converted from a
// valid non-canonical string.
func (t LocalTime) MarshalText() ([]byte, error) {
	parsed, err := ParseLocalTime(string(t))
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (t *LocalTime) UnmarshalText(value []byte) error {
	parsed, err := ParseLocalTime(string(value))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

func (t LocalTime) MarshalJSON() ([]byte, error) {
	text, err := t.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (t *LocalTime) UnmarshalJSON(value []byte) error {
	text, err := jsonText(value, "a time of day", "*LocalTime")
	if err != nil {
		return err
	}
	return t.UnmarshalText([]byte(text))
}

// Value writes the canonical text, which PostgreSQL's TIME and SQLite's TEXT
// both hold as written.
func (t LocalTime) Value() (driver.Value, error) {
	parsed, err := ParseLocalTime(string(t))
	if err != nil {
		return nil, err
	}
	return string(parsed), nil
}

// Scan accepts the text a driver reads a TIME column as, including pgx's
// six fractional digits, and a time.Time's clock, which some drivers return
// for TIME; it never converts a timezone.
func (t *LocalTime) Scan(value any) error {
	switch v := value.(type) {
	case string:
		return t.UnmarshalText([]byte(v))
	case []byte:
		return t.UnmarshalText(v)
	case time.Time:
		parsed, err := localTimeOf(v)
		if err != nil {
			return err
		}
		*t = parsed
		return nil
	}
	return fmt.Errorf("cannot scan %T as a time of day", value)
}

// localTimeOf is t's wall clock, as written: its location is not applied.
func localTimeOf(t time.Time) (LocalTime, error) {
	if t.Nanosecond()%1000 != 0 {
		return "", errors.New("a time of day has microsecond precision")
	}
	return ParseLocalTime(t.Format("15:04:05.000000"))
}

// normalizeLocalTime is NormalizeScalar's FieldTypeTime: the canonical text
// of a LocalTime or of a string.
func normalizeLocalTime(v any) (string, error) {
	switch t := v.(type) {
	case LocalTime:
		parsed, err := ParseLocalTime(string(t))
		return string(parsed), err
	case string:
		parsed, err := ParseLocalTime(t)
		return string(parsed), err
	}
	return "", fmt.Errorf("expected time of day string, got %T", v)
}

// digits reports whether s is non-empty ASCII decimal digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// jsonText decodes a JSON string; what names the value in errors and
// nullable the pointer type that admits null.
func jsonText(value []byte, what, nullable string) (string, error) {
	if string(value) == "null" {
		return "", fmt.Errorf("%s cannot be null; use %s", what, nullable)
	}
	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return "", err
	}
	return text, nil
}

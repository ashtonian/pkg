package core

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net"
)

// MACAddr is a SQL MACADDR value, a 48-bit hardware address. Its canonical
// text is six lowercase hexadecimal octets separated by colons,
// 08:00:2b:01:02:03, which is PostgreSQL's output. JSON, text, SQL
// parameters and cursors use it. The zero value is invalid; use *MACAddr
// for nullable values.
type MACAddr string

// ParseMACAddr reads the six-octet forms net.ParseMAC reads: colon- or
// hyphen-separated octets, three dot-separated groups of four digits, or
// twelve digits. An EUI-64 or 20-octet address is refused.
func ParseMACAddr(value string) (MACAddr, error) {
	hw, err := parseMAC48(value)
	if err != nil {
		return "", err
	}
	return MACAddr(hw.String()), nil
}

func parseMAC48(value string) (net.HardwareAddr, error) {
	hw, err := net.ParseMAC(value)
	if err != nil {
		return nil, fmt.Errorf("invalid MAC address %q: %w", value, err)
	}
	if len(hw) != 6 {
		return nil, fmt.Errorf("invalid MAC address %q: expected 6 octets, got %d", value, len(hw))
	}
	return hw, nil
}

func (m MACAddr) String() string { return string(m) }

// HardwareAddr is the address's octets.
func (m MACAddr) HardwareAddr() (net.HardwareAddr, error) { return parseMAC48(string(m)) }

// MarshalText writes the canonical text, also of a MACAddr converted from a
// valid non-canonical string.
func (m MACAddr) MarshalText() ([]byte, error) {
	parsed, err := ParseMACAddr(string(m))
	if err != nil {
		return nil, err
	}
	return []byte(parsed), nil
}

func (m *MACAddr) UnmarshalText(value []byte) error {
	parsed, err := ParseMACAddr(string(value))
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

func (m MACAddr) MarshalJSON() ([]byte, error) {
	text, err := m.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (m *MACAddr) UnmarshalJSON(value []byte) error {
	text, err := jsonText(value, "a MAC address", "*MACAddr")
	if err != nil {
		return err
	}
	return m.UnmarshalText([]byte(text))
}

// Value writes the canonical text, which PostgreSQL's MACADDR reads and
// SQLite's TEXT holds.
func (m MACAddr) Value() (driver.Value, error) {
	text, err := m.MarshalText()
	if err != nil {
		return nil, err
	}
	return string(text), nil
}

// Scan accepts any text ParseMACAddr reads, PostgreSQL's and pgx's
// included.
func (m *MACAddr) Scan(value any) error {
	switch v := value.(type) {
	case string:
		return m.UnmarshalText([]byte(v))
	case []byte:
		return m.UnmarshalText(v)
	}
	return fmt.Errorf("cannot scan %T as a MAC address", value)
}

// normalizeMACAddr is NormalizeScalar's FieldTypeMACAddr: the canonical
// text of a MACAddr, a six-octet net.HardwareAddr or a string.
func normalizeMACAddr(v any) (string, error) {
	switch m := v.(type) {
	case MACAddr:
		parsed, err := ParseMACAddr(string(m))
		return string(parsed), err
	case net.HardwareAddr:
		parsed, err := ParseMACAddr(m.String())
		return string(parsed), err
	case string:
		parsed, err := ParseMACAddr(m)
		return string(parsed), err
	}
	return "", fmt.Errorf("expected MAC address string, got %T", v)
}

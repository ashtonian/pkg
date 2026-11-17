package core

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Inet is a SQL INET value: an IPv4 or IPv6 host address with an optional
// network prefix length, which is kept with the address's host bits as
// PostgreSQL keeps them. Its canonical text is the address as net/netip
// writes it (RFC 5952 for IPv6, dotted decimal for IPv4), followed by
// "/bits" only when the prefix is shorter than the address: 192.168.0.5,
// 192.168.0.5/24, 2001:db8::1. JSON, text, SQL parameters and cursors use
// it. An IPv4-mapped IPv6 address stays IPv6, as in PostgreSQL. The zero
// value is invalid; use *Inet for nullable values.
type Inet struct{ prefix netip.Prefix }

// ParseInet reads an address or address/bits as net/netip parses them. A
// zone is refused: PostgreSQL has none.
func ParseInet(value string) (Inet, error) {
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return Inet{}, fmt.Errorf("invalid inet %q: %w", value, err)
		}
		return Inet{prefix}, nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return Inet{}, fmt.Errorf("invalid inet %q: %w", value, err)
	}
	inet, err := InetFromAddr(addr)
	if err != nil {
		return Inet{}, fmt.Errorf("invalid inet %q: %w", value, err)
	}
	return inet, nil
}

// InetFromAddr is the host address addr, with no shorter prefix. A
// request's remote address is usually unmapped first (netip.Addr.Unmap)
// where the application means IPv4.
func InetFromAddr(addr netip.Addr) (Inet, error) {
	if !addr.IsValid() {
		return Inet{}, errors.New("the zero netip.Addr is not an address")
	}
	if addr.Zone() != "" {
		return Inet{}, fmt.Errorf("address %s has a zone, which inet cannot hold", addr)
	}
	return Inet{netip.PrefixFrom(addr, addr.BitLen())}, nil
}

// InetFromPrefix is prefix's address with its prefix length, host bits
// kept.
func InetFromPrefix(prefix netip.Prefix) (Inet, error) {
	if !prefix.IsValid() {
		return Inet{}, errors.New("the netip.Prefix is not valid")
	}
	return Inet{prefix}, nil
}

// Prefix is the address and its prefix length; Bits equals the address's
// length for a host address.
func (i Inet) Prefix() netip.Prefix { return i.prefix }

// Addr is the address, host bits included.
func (i Inet) Addr() netip.Addr { return i.prefix.Addr() }

// IsValid reports whether i holds an address: false for the zero value.
func (i Inet) IsValid() bool { return i.prefix.IsValid() }

// String is the canonical text, or "invalid Inet" for the zero value.
func (i Inet) String() string {
	if !i.prefix.IsValid() {
		return "invalid Inet"
	}
	if i.prefix.Bits() == i.prefix.Addr().BitLen() {
		return i.prefix.Addr().String()
	}
	return i.prefix.String()
}

func (i Inet) MarshalText() ([]byte, error) {
	if !i.prefix.IsValid() {
		return nil, errors.New("the zero Inet holds no address; use *Inet for a nullable value")
	}
	return []byte(i.String()), nil
}

func (i *Inet) UnmarshalText(value []byte) error {
	parsed, err := ParseInet(string(value))
	if err != nil {
		return err
	}
	*i = parsed
	return nil
}

func (i Inet) MarshalJSON() ([]byte, error) {
	text, err := i.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (i *Inet) UnmarshalJSON(value []byte) error {
	text, err := jsonText(value, "an inet", "*Inet")
	if err != nil {
		return err
	}
	return i.UnmarshalText([]byte(text))
}

// Value writes the canonical text, which PostgreSQL's INET reads and
// SQLite's TEXT holds.
func (i Inet) Value() (driver.Value, error) {
	text, err := i.MarshalText()
	if err != nil {
		return nil, err
	}
	return string(text), nil
}

// Scan accepts any text ParseInet reads: PostgreSQL's output, abbrev()'s,
// and pgx's, which writes a host address with /32 or /128.
func (i *Inet) Scan(value any) error {
	switch v := value.(type) {
	case string:
		return i.UnmarshalText([]byte(v))
	case []byte:
		return i.UnmarshalText(v)
	}
	return fmt.Errorf("cannot scan %T as an inet", value)
}

// CIDR is a SQL CIDR value: a network, an address prefix with no host bits
// set. Its canonical text is address/bits as net/netip writes it, the length
// always present: 192.168.0.0/24, 2001:db8::/32. JSON, text, SQL parameters
// and cursors use it. The zero value is invalid; use *CIDR for nullable
// values.
type CIDR struct{ prefix netip.Prefix }

// ParseCIDR reads address/bits. A value without a length, or with host bits
// set, is refused, as PostgreSQL refuses host bits.
func ParseCIDR(value string) (CIDR, error) {
	if !strings.Contains(value, "/") {
		return CIDR{}, fmt.Errorf("invalid cidr %q: expected address/bits", value)
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		return CIDR{}, fmt.Errorf("invalid cidr %q: %w", value, err)
	}
	cidr, err := CIDRFromPrefix(prefix)
	if err != nil {
		return CIDR{}, fmt.Errorf("invalid cidr %q: %w", value, err)
	}
	return cidr, nil
}

// CIDRFromPrefix is the network prefix, which must have no host bits set.
func CIDRFromPrefix(prefix netip.Prefix) (CIDR, error) {
	if !prefix.IsValid() {
		return CIDR{}, errors.New("the netip.Prefix is not valid")
	}
	if prefix.Masked() != prefix {
		return CIDR{}, fmt.Errorf("%s has bits set to the right of the mask; the network is %s", prefix, prefix.Masked())
	}
	return CIDR{prefix}, nil
}

// Prefix is the network.
func (c CIDR) Prefix() netip.Prefix { return c.prefix }

// IsValid reports whether c holds a network: false for the zero value.
func (c CIDR) IsValid() bool { return c.prefix.IsValid() }

// String is the canonical text, or "invalid CIDR" for the zero value.
func (c CIDR) String() string {
	if !c.prefix.IsValid() {
		return "invalid CIDR"
	}
	return c.prefix.String()
}

func (c CIDR) MarshalText() ([]byte, error) {
	if !c.prefix.IsValid() {
		return nil, errors.New("the zero CIDR holds no network; use *CIDR for a nullable value")
	}
	return []byte(c.String()), nil
}

func (c *CIDR) UnmarshalText(value []byte) error {
	parsed, err := ParseCIDR(string(value))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

func (c CIDR) MarshalJSON() ([]byte, error) {
	text, err := c.MarshalText()
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(text))
}

func (c *CIDR) UnmarshalJSON(value []byte) error {
	text, err := jsonText(value, "a cidr", "*CIDR")
	if err != nil {
		return err
	}
	return c.UnmarshalText([]byte(text))
}

// Value writes the canonical text, which PostgreSQL's CIDR reads and
// SQLite's TEXT holds.
func (c CIDR) Value() (driver.Value, error) {
	text, err := c.MarshalText()
	if err != nil {
		return nil, err
	}
	return string(text), nil
}

// Scan accepts any text ParseCIDR reads, PostgreSQL's and pgx's included.
func (c *CIDR) Scan(value any) error {
	switch v := value.(type) {
	case string:
		return c.UnmarshalText([]byte(v))
	case []byte:
		return c.UnmarshalText(v)
	}
	return fmt.Errorf("cannot scan %T as a cidr", value)
}

// normalizeInet is NormalizeScalar's FieldTypeInet: the canonical text of
// an Inet, a netip.Addr or netip.Prefix, or a string.
func normalizeInet(v any) (string, error) {
	var inet Inet
	var err error
	switch x := v.(type) {
	case Inet:
		inet = x
	case netip.Addr:
		inet, err = InetFromAddr(x)
	case netip.Prefix:
		inet, err = InetFromPrefix(x)
	case string:
		inet, err = ParseInet(x)
	default:
		return "", fmt.Errorf("expected inet string, got %T", v)
	}
	if err != nil {
		return "", err
	}
	text, err := inet.MarshalText()
	return string(text), err
}

// normalizeCIDR is NormalizeScalar's FieldTypeCIDR: the canonical text of a
// CIDR, a netip.Prefix or a string.
func normalizeCIDR(v any) (string, error) {
	var cidr CIDR
	var err error
	switch x := v.(type) {
	case CIDR:
		cidr = x
	case netip.Prefix:
		cidr, err = CIDRFromPrefix(x)
	case string:
		cidr, err = ParseCIDR(x)
	default:
		return "", fmt.Errorf("expected cidr string, got %T", v)
	}
	if err != nil {
		return "", err
	}
	text, err := cidr.MarshalText()
	return string(text), err
}

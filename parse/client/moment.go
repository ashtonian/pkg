package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrSubSecond reports a relative Moment that is not a whole number of
// seconds, the server's finest relative unit.
var ErrSubSecond = errors.New("client: a relative time is a whole number of seconds")

// Moment is a timestamp operand: an absolute time (At), or a time relative to
// the server's clock (Now, Ago, FromNow), which the server resolves when it
// reads the request, so the client's clock never skews the comparison.
type Moment struct {
	at       time.Time
	offset   time.Duration
	relative bool
}

// At is the absolute time t.
func At(t time.Time) Moment { return Moment{at: t} }

// Now is the server's current time.
func Now() Moment { return Moment{relative: true} }

// Ago is d before the server's current time.
func Ago(d time.Duration) Moment { return Moment{offset: -d, relative: true} }

// FromNow is d after the server's current time.
func FromNow(d time.Duration) Moment { return Moment{offset: d, relative: true} }

// relativeUnits are the server's relative-time units, largest first. Its
// month unit is left out: the server counts a month as 30 days, which a
// duration states exactly in days.
var relativeUnits = []struct {
	suffix string
	unit   time.Duration
}{
	{"w", 7 * 24 * time.Hour},
	{"d", 24 * time.Hour},
	{"h", time.Hour},
	{"m", time.Minute},
	{"s", time.Second},
}

// MarshalJSON encodes an absolute Moment in RFC 3339 and a relative one as
// the server's token: now, or now±N with the largest unit that states the
// offset exactly (now-7d is now-1w).
func (m Moment) MarshalJSON() ([]byte, error) {
	if !m.relative {
		return json.Marshal(m.at)
	}
	if m.offset == 0 {
		return json.Marshal("now")
	}
	if m.offset%time.Second != 0 {
		return nil, fmt.Errorf("%w: %v", ErrSubSecond, m.offset)
	}
	sign, offset := "+", m.offset
	if offset < 0 {
		sign, offset = "-", -offset
	}
	for _, u := range relativeUnits {
		if offset%u.unit == 0 {
			return json.Marshal("now" + sign + strconv.FormatInt(int64(offset/u.unit), 10) + u.suffix)
		}
	}
	panic("unreachable: a whole number of seconds")
}

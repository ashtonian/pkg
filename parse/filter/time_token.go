package filter

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"
)

// relativeTimePattern matches `now` or `now±Nu` where u ∈ {s, m, h, d, w, M}.
// Months use a literal 30-day approximation so the math is timezone- and
// calendar-agnostic; callers needing calendar-correct month math should
// supply absolute timestamps.
var relativeTimePattern = regexp.MustCompile(`^now(?:([-+])(\d+)([smhdwM]))?$`)

var relativeTimeUnits = map[string]time.Duration{
	"s": time.Second,
	"m": time.Minute,
	"h": time.Hour,
	"d": 24 * time.Hour,
	"w": 7 * 24 * time.Hour,
	"M": 30 * 24 * time.Hour,
}

// ResolveRelativeTime resolves a `now` / `now±N<unit>` token against now.
// isToken reports whether s has the token shape at all; err is non-nil when
// a token-shaped value cannot be resolved because its offset overflows the
// ±292-year range of time.Duration. Recognition and resolution are one step
// so a token can never validate and then fail to resolve.
func ResolveRelativeTime(s string, now time.Time) (t time.Time, isToken bool, err error) {
	m := relativeTimePattern.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false, nil
	}
	if m[1] == "" {
		return now, true, nil
	}
	unit := relativeTimeUnits[m[3]]
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil || n > math.MaxInt64/int64(unit) {
		return time.Time{}, true, fmt.Errorf("relative time %q overflows the supported offset range", s)
	}
	d := time.Duration(n) * unit
	if m[1] == "-" {
		d = -d
	}
	return now.Add(d), true, nil
}

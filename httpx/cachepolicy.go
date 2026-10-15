package httpx

import (
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// cacheDirectives holds the Cache-Control directives the cache acts on
// (RFC 9111 §5.2). A duration of -1 means the directive is absent.
type cacheDirectives struct {
	noStore, noCache, mustRevalidate, onlyIfCached bool

	maxAge, minFresh     time.Duration // -1 when absent
	maxStale             time.Duration // -1 when absent; maxStaleAny covers a bare max-stale
	maxStaleAny          bool
	staleWhileRevalidate time.Duration // -1 when absent
	staleIfError         time.Duration // -1 when absent
}

func parseCacheControl(h http.Header) cacheDirectives {
	d := cacheDirectives{maxAge: -1, minFresh: -1, maxStale: -1, staleWhileRevalidate: -1, staleIfError: -1}
	for _, field := range headerValuesFold(h, "Cache-Control") {
		for part := range strings.SplitSeq(field, ",") {
			name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			value = strings.Trim(strings.TrimSpace(value), `"`)
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "no-store":
				d.noStore = true
			case "no-cache":
				d.noCache = true
			case "must-revalidate", "proxy-revalidate":
				d.mustRevalidate = true
			case "only-if-cached":
				d.onlyIfCached = true
			case "max-age":
				d.maxAge = deltaSeconds(value)
			case "min-fresh":
				d.minFresh = deltaSeconds(value)
			case "max-stale":
				if value == "" {
					d.maxStaleAny = true
				} else {
					d.maxStale = deltaSeconds(value)
				}
			case "stale-while-revalidate":
				d.staleWhileRevalidate = deltaSeconds(value)
			case "stale-if-error":
				d.staleIfError = deltaSeconds(value)
			}
		}
	}
	return d
}

// headerValuesFold includes non-canonical keys written directly into a
// Header map. They reach the wire too, so identity and cache restrictions
// must not silently omit them. Sorting makes duplicate spellings stable.
func headerValuesFold(h http.Header, name string) []string {
	var keys []string
	for key := range h {
		if strings.EqualFold(key, name) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	var values []string
	for _, key := range keys {
		values = append(values, h[key]...)
	}
	return values
}

// deltaSeconds parses a delta-seconds value (RFC 9111 §1.2.2): a
// non-negative integer that saturates rather than overflows. An invalid
// value yields -1 (absent).
func deltaSeconds(s string) time.Duration {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return -1
	}
	n, err := strconv.ParseInt(s, 10, 64)
	switch {
	case errors.Is(err, strconv.ErrRange):
		return time.Duration(math.MaxInt64)
	case err != nil:
		return -1
	case n > int64(math.MaxInt64/time.Second):
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(n) * time.Second
}

// freshness is a stored response's freshness at one instant.
type freshness struct {
	lifetime time.Duration // freshness lifetime (RFC 9111 §4.2.1)
	age      time.Duration // current age (§4.2.3)
}

func (f freshness) fresh() bool { return f.lifetime > f.age }

// staleness is how long the response has been stale; zero while fresh.
func (f freshness) staleness() time.Duration { return max(f.age-f.lifetime, 0) }

// computeFreshness evaluates entry at now as a private cache: s-maxage is
// ignored and there is no heuristic freshness.
func computeFreshness(entry *CachedResponse, cc cacheDirectives, now time.Time) freshness {
	date := entry.ResponseTime
	if d, err := http.ParseTime(entry.Header.Get("Date")); err == nil {
		date = d
	}

	apparentAge := max(entry.ResponseTime.Sub(date), 0)
	ageValue := max(deltaSeconds(entry.Header.Get("Age")), 0)
	responseDelay := max(entry.ResponseTime.Sub(entry.RequestTime), 0)
	correctedInitialAge := max(apparentAge, addAge(ageValue, responseDelay))
	age := addAge(correctedInitialAge, max(now.Sub(entry.ResponseTime), 0))

	var lifetime time.Duration
	switch {
	case cc.noCache:
		lifetime = 0
	case cc.maxAge >= 0:
		lifetime = cc.maxAge
	default:
		if exp, err := http.ParseTime(entry.Header.Get("Expires")); err == nil {
			lifetime = max(exp.Sub(date), 0)
		}
	}
	return freshness{lifetime: lifetime, age: age}
}

// addAge adds two non-negative ages, saturating: an Age near the largest
// duration must stay ancient, not wrap around to fresh.
func addAge(a, b time.Duration) time.Duration {
	if a > time.Duration(math.MaxInt64)-b {
		return time.Duration(math.MaxInt64)
	}
	return a + b
}

// usableWithoutValidation reports whether a stored response may be served
// as-is to a request with directives req (RFC 9111 §4.2, §5.2.1).
func usableWithoutValidation(f freshness, resp, req cacheDirectives) bool {
	if req.noCache || resp.noCache {
		return false
	}
	if req.maxAge >= 0 && f.age > req.maxAge {
		return false
	}
	if f.fresh() {
		return req.minFresh < 0 || f.lifetime-f.age >= req.minFresh
	}
	if resp.mustRevalidate {
		return false
	}
	return req.maxStaleAny || (req.maxStale >= 0 && f.staleness() <= req.maxStale)
}

// hasValidator reports whether h carries a validator a conditional request
// can use.
func hasValidator(h http.Header) bool {
	return h.Get("ETag") != "" || h.Get("Last-Modified") != ""
}

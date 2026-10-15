//go:build unit

package httpx

import (
	"net/url"
	"testing"
)

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://api.example.com", "https://api.example.com:443"},
		{"https://API.Example.com/v1/users?x=1#f", "https://api.example.com:443"},
		{"HTTP://api.example.com", "http://api.example.com:80"},
		{"http://api.example.com:8080", "http://api.example.com:8080"},
		{"https://[::1]:8443/", "https://[::1]:8443"},
	}
	for _, tt := range tests {
		got, err := ParseOrigin(tt.raw)
		if err != nil {
			t.Errorf("ParseOrigin(%q): %v", tt.raw, err)
			continue
		}
		if got.String() != tt.want {
			t.Errorf("ParseOrigin(%q) = %s, want %s", tt.raw, got, tt.want)
		}
	}
}

func TestParseOriginRejects(t *testing.T) {
	for _, raw := range []string{"", "/relative", "api.example.com", "ftp://h", "https://", "https://user:pw@h", "://bad"} {
		if o, err := ParseOrigin(raw); err == nil {
			t.Errorf("ParseOrigin(%q) = %s, want an error", raw, o)
		}
	}
}

func TestOriginEquality(t *testing.T) {
	origin := func(raw string) Origin {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return OriginOf(u)
	}
	same := [][2]string{
		{"https://h/a", "https://h:443/b"},
		{"https://H", "https://h"},
		{"http://h", "http://h:80"},
	}
	for _, p := range same {
		if origin(p[0]) != origin(p[1]) {
			t.Errorf("%s and %s should be the same origin", p[0], p[1])
		}
	}
	different := [][2]string{
		{"https://h", "http://h"}, // downgrade
		{"https://h", "https://h:8443"},
		{"https://h", "https://sub.h"},
		{"https://h", "https://h.evil.example"},
	}
	for _, p := range different {
		if origin(p[0]) == origin(p[1]) {
			t.Errorf("%s and %s should be different origins", p[0], p[1])
		}
	}
	if !OriginOf(nil).IsZero() || !origin("mailto:a@b").IsZero() {
		t.Error("nil and non-http URLs have the zero origin")
	}
}

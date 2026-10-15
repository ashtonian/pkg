package httpx

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Origin is the scheme, host and port of a URL (RFC 6454): the boundary a
// credential is bound to. https://api.example.com and http://api.example.com
// are different origins, as are two ports on one host. The zero Origin
// matches no request.
type Origin struct {
	scheme string
	host   string // lower-case host joined with an explicit port
}

// ParseOrigin returns the origin of an absolute http or https URL. Any path,
// query or fragment is ignored; userinfo is rejected so a credential cannot
// hide in a binding. Errors never repeat the input, which may hold a
// credential: a rejected URL is described by its scheme and host at most.
func ParseOrigin(rawURL string) (Origin, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // url.Error's text quotes the whole input
		}
		return Origin{}, fmt.Errorf("origin: not a valid URL: %w", err)
	}
	if u.User != nil {
		return Origin{}, fmt.Errorf("origin %s://%s: userinfo is not allowed", u.Scheme, u.Host)
	}
	o := OriginOf(u)
	if o.IsZero() {
		return Origin{}, fmt.Errorf("origin %s://%s: want an absolute http or https URL", u.Scheme, u.Host)
	}
	return o, nil
}

// OriginOf returns the origin of u, or the zero Origin when u is nil, not
// http(s), or has no host.
func OriginOf(u *url.URL) Origin {
	if u == nil {
		return Origin{}
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch scheme {
	case "http":
		if port == "" {
			port = "80"
		}
	case "https":
		if port == "" {
			port = "443"
		}
	default:
		return Origin{}
	}
	hostname := strings.ToLower(u.Hostname())
	if hostname == "" {
		return Origin{}
	}
	return Origin{scheme: scheme, host: net.JoinHostPort(hostname, port)}
}

// IsZero reports whether o is the zero Origin.
func (o Origin) IsZero() bool { return o == Origin{} }

// String returns the origin as scheme://host:port.
func (o Origin) String() string {
	if o.IsZero() {
		return ""
	}
	return o.scheme + "://" + o.host
}

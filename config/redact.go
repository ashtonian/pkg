package config

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"strings"
)

func redactByteText(v reflect.Value) (string, bool) {
	if !v.IsValid() || (v.Kind() != reflect.Slice && v.Kind() != reflect.Array) || v.Type().Elem().Kind() != reflect.Uint8 {
		return "", false
	}
	data := make([]byte, v.Len())
	for i := range data {
		data[i] = byte(v.Index(i).Uint())
	}
	text := string(data)
	masked := RedactText(text)
	return masked, masked != text
}

var (
	credentialPEM  = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?(?:-----END [A-Z0-9 ]+-----|\z)`)
	credentialURL  = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"'<>]+`)
	credentialAuth = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=\-]+`)
	credentialJWT  = regexp.MustCompile(`[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	credentialPair = regexp.MustCompile(`(?i)(["']?)([A-Za-z0-9_.\-]*(?:` +
		strings.Join(sensitivePatterns[:], "|") + `|passwd|pwd|creds|bearer|jwt|(?:tls|client)[-_]?cert)[A-Za-z0-9_.\-]*)(["']?\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.|'')*'|[^\s&;,"'<>]+)`)
)

// RedactText removes recognizable credentials from text: URL userinfo, PEM
// blocks, credential key/value pairs, Basic/Bearer credentials and JWTs.
// URLs keep their username, host and path when a password is present,
// postgres://alice:[REDACTED]@db.internal/app; userinfo without one is
// withheld whole, https://[REDACTED]@api.internal/. Other
// text is retained. This is a diagnostic filter, not a secret detector: keep
// sensitive tags on fields whose values need protection regardless of shape.
func RedactText(text string) string {
	text = credentialPEM.ReplaceAllString(text, redactedValue)
	text = credentialURL.ReplaceAllStringFunc(text, redactURL)
	text = credentialJWT.ReplaceAllStringFunc(text, func(token string) string {
		header, _, _ := strings.Cut(token, ".")
		decoded, err := base64.RawURLEncoding.DecodeString(header)
		if err != nil {
			return token
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(decoded, &fields) != nil || fields["alg"] == nil {
			return token
		}
		return redactedValue
	})
	text = credentialAuth.ReplaceAllString(text, "${1}${2}"+redactedValue)
	return credentialPair.ReplaceAllStringFunc(text, func(pair string) string {
		parts := credentialPair.FindStringSubmatch(pair)
		value := parts[4]
		if strings.EqualFold(parts[2], "authorization") || strings.EqualFold(parts[2], "proxy-authorization") {
			if strings.EqualFold(value, "Bearer") || strings.EqualFold(value, "Basic") || strings.Contains(value, redactedValue) {
				return pair
			}
		}
		if value == redactedValue {
			return pair
		}
		masked := redactedValue
		if value[0] == '\'' || value[0] == '"' {
			masked = value[:1] + masked + value[len(value)-1:]
		}
		return parts[1] + parts[2] + parts[3] + masked
	})
}

func redactURL(text string) string {
	u, err := url.Parse(text)
	if err == nil {
		if u.User == nil {
			return text
		}
		// Set as a password, the marker's brackets would be percent-escaped:
		// splice it into the rendered URL instead.
		masked := redactedValue
		if _, hasPassword := u.User.Password(); hasPassword {
			masked = url.User(u.User.Username()).String() + ":" + redactedValue
		}
		rendered := u.String()
		if rest, ok := strings.CutPrefix(rendered, u.Scheme+"://"+u.User.String()+"@"); ok {
			return u.Scheme + "://" + masked + "@" + rest
		}
		u.User = url.User(redactedValue)
		return u.String()
	}
	// An unescaped password can make URL parsing fail. Withhold the entire
	// apparent userinfo rather than echoing it in a diagnostic — unless it is
	// already redacted, which URL parsing also refuses for its brackets.
	scheme, rest, ok := strings.Cut(text, "://")
	if at := strings.LastIndexByte(rest, '@'); ok && at >= 0 {
		if redactedUserinfo(rest[:at]) {
			return text
		}
		return scheme + "://" + redactedValue + "@" + rest[at+1:]
	}
	return text
}

// redactedUserinfo reports userinfo redactURL wrote: the marker alone, or an
// escaped username and the marker as its password.
func redactedUserinfo(userinfo string) bool {
	if userinfo == redactedValue {
		return true
	}
	user, password, found := strings.Cut(userinfo, ":")
	return found && password == redactedValue && !strings.ContainsAny(user, "/?#[]@")
}

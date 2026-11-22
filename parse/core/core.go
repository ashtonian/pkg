package core

import "strings"

// Validator is a function that validates a value.
type Validator func(value any) error

// --- Policy Helpers (shared by filter and patch) ---

// PolicySubject describes the actor making the request.
// Used by both filter and patch policy enforcement.
type PolicySubject struct {
	ID          string
	TenantID    string
	TenantPath  string
	Roles       []string
	Groups      []string
	Permissions []string
	Attributes  map[string]any
}

// MatchWildcard checks if a wildcard pattern matches a JSON Pointer path,
// token by token: * matches exactly one token and ** any number. Tokens are
// the pointer's own (RFC 6901), compared in their escaped form, so an empty
// key is a token like any other — "/a//b" is three tokens and "/a/*" does not
// match it. "/*" means exactly one token under root — the old
// match-everything special case over-granted patch policies intending
// top-level fields only.
func MatchWildcard(pattern, path string) bool {
	if pattern == path {
		return true
	}
	if pattern == "**" {
		return true
	}
	return matchParts(splitPathParts(pattern), splitPathParts(path))
}

// splitPathParts returns the reference tokens of a JSON Pointer, still
// escaped: "" (the whole document) has none, "/" one empty token, and
// "/a//b" the three tokens "a", "" and "b". A leading slash is optional.
func splitPathParts(s string) []string {
	if s == "" {
		return nil
	}
	if s[0] == '/' {
		s = s[1:]
	}
	return strings.Split(s, "/")
}

func matchParts(pattern, path []string) bool {
	pi, pa := 0, 0
	for pi < len(pattern) && pa < len(path) {
		if pattern[pi] == "**" {
			if pi == len(pattern)-1 {
				return true
			}
			for pa < len(path) {
				if matchParts(pattern[pi+1:], path[pa:]) {
					return true
				}
				pa++
			}
			return false
		}
		if pattern[pi] != "*" && pattern[pi] != path[pa] {
			return false
		}
		pi++
		pa++
	}
	return pi == len(pattern) && pa == len(path)
}

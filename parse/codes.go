package parse

import (
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Code identifies a parse error without an application error catalog.
type Code = core.Code

// ParseError carries a code and the location and context of a refused request.
type ParseError = core.ParseError

// Codes returns the distinct filter and patch codes. A shared limit has one
// code even when both grammars can reject it.
func Codes() []Code {
	seen := make(map[Code]bool)
	var out []Code
	for _, list := range [][]Code{filter.Codes(), patch.Codes()} {
		for _, code := range list {
			if !seen[code] {
				seen[code] = true
				out = append(out, code)
			}
		}
	}
	return out
}

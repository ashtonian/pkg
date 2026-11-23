package parse

import (
	"github.com/ashtonian/pkg/parse/core"
)

// Code identifies a parse error without an application error catalog.
type Code = core.Code

// ParseError carries a code and the location and context of a refused request.
type ParseError = core.ParseError

// The schema discovery codes parsehttp.SchemaHandler and
// parsehttp.SchemaListHandler answer.
const (
	// ECodeSchemaNotFound is a resource the registry has not registered,
	// answered 404.
	ECodeSchemaNotFound = core.CodeSchemaNotFound
	// ECodeSchemaEncodingFailed is a schema document that could not be
	// encoded: a server defect, answered 500.
	ECodeSchemaEncodingFailed = core.CodeSchemaEncodingFailed
)

// Codes returns every code parse and its packages answer, in order, each
// with one status and message. filter.Codes and patch.Codes are the codes of
// their grammars; a limit both enforce has one code.
func Codes() []Code { return core.Codes() }

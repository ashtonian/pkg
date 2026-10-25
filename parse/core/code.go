package core

import "net/http"

// Code identifies a parse error independently of an application error catalog.
// A Code is also an errors.Is target for ParseError.
type Code string

// String returns the wire code.
func (c Code) String() string { return string(c) }

// Error describes the code's category, without occurrence-specific context.
func (c Code) Error() string { return "[" + string(c) + "] " + c.ErrorMessage() }

// ErrorCode returns the wire code for error introspection.
func (c Code) ErrorCode() string { return string(c) }

// ErrorHTTPStatus returns the code's HTTP status; unknown codes are invalid input.
func (c Code) ErrorHTTPStatus() int {
	switch c {
	case "E2112", "E2113", "E2114", "E2115", "E2116":
		return http.StatusBadRequest
	case "E2069":
		return http.StatusInternalServerError
	}
	return http.StatusUnprocessableEntity
}

// ErrorMessage returns the code's default message, or empty for an unknown code.
func (c Code) ErrorMessage() string {
	switch c {
	case "E2061":
		return "query too expensive"
	case "E2062":
		return "invalid array value"
	case "E2063":
		return "input too large"
	case "E2064":
		return "operation not allowed"
	case "E2065":
		return "sort value too large to page"
	case "E2066":
		return "too many operations"
	case "E2067":
		return "invalid list parameter"
	case "E2068":
		return "test op cannot compile to SQL"
	case "E2069":
		return "patch path has no SQL column mapping"
	case "E2070":
		return "patch operation cannot compile to SQL"
	case "E2071":
		return "unknown field"
	case "E2072":
		return "invalid operator"
	case "E2073":
		return "value type mismatch"
	case "E2074":
		return "depth exceeded"
	case "E2075":
		return "invalid patch operation"
	case "E2076":
		return "patch field not found"
	case "E2077":
		return "patch value invalid"
	case "E2078":
		return "schema not found"
	case "E2079":
		return "sort field not allowed"
	case "E2080":
		return "SQL generation failed"
	case "E2081":
		return "sort field limit exceeded"
	case "E2082":
		return "invalid cursor"
	case "E2083":
		return "invalid relation"
	case "E2084":
		return "patch policy forbidden"
	case "E2085":
		return "patch policy value violation"
	case "E2086":
		return "invalid JSON"
	case "E2087":
		return "invalid enum value"
	case "E2088":
		return "array too large"
	case "E2089":
		return "max conditions exceeded"
	case "E2112":
		return "invalid geo coordinates"
	case "E2113":
		return "invalid distance value"
	case "E2114":
		return "invalid geo polygon"
	case "E2115":
		return "geo operator not supported by dialect"
	case "E2116":
		return "invalid UUID"
	case "E2117":
		return "patch verb not allowed on relation field"
	case "E2118":
		return "relation patch value must be array"
	case "E2119":
		return "relation patch values exceed maximum"
	}
	return ""
}

// IsErrorUserFacing reports whether the category's message is safe to disclose.
func (c Code) IsErrorUserFacing() bool { return c.ErrorMessage() != "" }

package client

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Code is a parse error code, the code member of the error body a parse
// server answers a refused request with. A Code is an errors.Is target for
// *Error: errors.Is(err, client.CodeUnknownField).
type Code string

// Error makes a Code an errors.Is target.
func (c Code) Error() string { return string(c) }

// The codes parse answers (parse.Codes), named as parse's core package names
// them. This module's tests hold the list equal to the server's catalog
// (parse's testdata/error_codes.json). An application may answer codes of
// its own beside these.
const (
	CodeQueryTooExpensive      Code = "E2061"
	CodeInvalidArray           Code = "E2062"
	CodeInputTooLarge          Code = "E2063"
	CodeOpNotAllowed           Code = "E2064"
	CodeCursorTooLarge         Code = "E2065"
	CodeTooManyOps             Code = "E2066"
	CodeInvalidListParam       Code = "E2067"
	CodeTestOpSQL              Code = "E2068"
	CodeOpNotSQLCompilable     Code = "E2070"
	CodeUnknownField           Code = "E2071"
	CodeInvalidOperator        Code = "E2072"
	CodeValueTypeMismatch      Code = "E2073"
	CodeDepthExceeded          Code = "E2074"
	CodeInvalidPatchOp         Code = "E2075"
	CodePatchFieldNotFound     Code = "E2076"
	CodePatchValueInvalid      Code = "E2077"
	CodeSchemaNotFound         Code = "E2078"
	CodeSortFieldNotAllowed    Code = "E2079"
	CodeSortFieldLimit         Code = "E2081"
	CodeInvalidCursor          Code = "E2082"
	CodeInvalidRelation        Code = "E2083"
	CodePatchPolicyForbidden   Code = "E2084"
	CodePatchPolicyValue       Code = "E2085"
	CodeInvalidJSON            Code = "E2086"
	CodeInvalidEnumValue       Code = "E2087"
	CodeArrayTooLarge          Code = "E2088"
	CodeMaxConditions          Code = "E2089"
	CodeInvalidGeoCoords       Code = "E2112"
	CodeInvalidDistance        Code = "E2113"
	CodeInvalidGeoPolygon      Code = "E2114"
	CodeGeoNotSupported        Code = "E2115"
	CodeInvalidUUID            Code = "E2116"
	CodeRelationVerbNotAllowed Code = "E2117"
	CodeRelationValueInvalid   Code = "E2118"
	CodeRelationPatchTooLarge  Code = "E2119"
	CodeSchemaEncodingFailed   Code = "E2120"
)

// Error is a refused request: the HTTP status and the error body a parse
// server answers with (parsehttp's). Match it with errors.As, and its code
// with errors.Is.
type Error struct {
	// Status is the answer's HTTP status; the body does not carry it.
	Status  int    `json:"-"`
	Code    Code   `json:"code"`
	Message string `json:"message"`
	// Path is the JSON Pointer of the patch member a patch error is about.
	Path string `json:"path,omitempty"`
	// Index is the position of the JSON Patch operation a patch error is
	// about; a pointer, so operation 0 is told from none.
	Index   *int           `json:"index,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// DecodeError reads the answer to a refused request: its HTTP status and
// body. A body in parse's shape, a JSON object whose code and message are
// strings, fills the Error. Any other body (a proxy's HTML page, an empty
// one, another JSON document) leaves it the status alone, with no Code. The
// optional members are read one by one, so a path, index or details of
// another JSON type is left out rather than losing the code.
func DecodeError(status int, body []byte) *Error {
	refused := &Error{Status: status}
	var members map[string]json.RawMessage
	if json.Unmarshal(body, &members) != nil {
		return refused
	}
	var code, message string
	if !decodeMember(members, "code", &code) || !decodeMember(members, "message", &message) {
		return refused
	}
	refused.Code, refused.Message = Code(code), message
	decodeMember(members, "path", &refused.Path)
	decodeMember(members, "details", &refused.Details)
	var index int
	if decodeMember(members, "index", &index) {
		refused.Index = &index
	}
	return refused
}

// decodeMember decodes the member name of an object into v, reporting
// whether it is there, not null and of v's type; v is unchanged otherwise.
func decodeMember[T any](members map[string]json.RawMessage, name string, v *T) bool {
	raw, ok := members[name]
	if !ok || string(raw) == "null" {
		return false
	}
	var decoded T
	if json.Unmarshal(raw, &decoded) != nil {
		return false
	}
	*v = decoded
	return true
}

// Error names the code, the status, the message and the patch member or
// operation the error is about.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("client: ")
	switch {
	case e.Code != "" && e.Status != 0:
		fmt.Fprintf(&b, "%s (HTTP %d)", e.Code, e.Status)
	case e.Code != "":
		b.WriteString(string(e.Code))
	default:
		fmt.Fprintf(&b, "HTTP %d", e.Status)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Path != "" {
		fmt.Fprintf(&b, " at %s", e.Path)
	}
	if e.Index != nil {
		fmt.Fprintf(&b, " in operation %d", *e.Index)
	}
	return b.String()
}

// Is reports whether target is e's Code.
func (e *Error) Is(target error) bool {
	code, ok := target.(Code)
	return ok && code != "" && code == e.Code
}

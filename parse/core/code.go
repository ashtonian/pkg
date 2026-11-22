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

// The codes, each defined once: filter, patch and parse name the ones
// their grammars raise, and a limit both grammars enforce has one code.
const (
	CodeQueryTooExpensive      Code = "E2061"
	CodeInvalidArray           Code = "E2062"
	CodeInputTooLarge          Code = "E2063"
	CodeOpNotAllowed           Code = "E2064"
	CodeCursorTooLarge         Code = "E2065"
	CodeTooManyOps             Code = "E2066"
	CodeInvalidListParam       Code = "E2067"
	CodeTestOpSQL              Code = "E2068"
	CodeSQLColumnMissing       Code = "E2069"
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
	CodeSQLGenerationFailed    Code = "E2080"
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

// codeInfo is each code's HTTP status and default message.
var codeInfo = map[Code]struct {
	status  int
	message string
}{
	CodeQueryTooExpensive:      {http.StatusUnprocessableEntity, "query too expensive"},
	CodeInvalidArray:           {http.StatusUnprocessableEntity, "invalid array value"},
	CodeInputTooLarge:          {http.StatusUnprocessableEntity, "input too large"},
	CodeOpNotAllowed:           {http.StatusUnprocessableEntity, "operation not allowed"},
	CodeCursorTooLarge:         {http.StatusUnprocessableEntity, "sort value too large to page"},
	CodeTooManyOps:             {http.StatusUnprocessableEntity, "too many operations"},
	CodeInvalidListParam:       {http.StatusUnprocessableEntity, "invalid list parameter"},
	CodeTestOpSQL:              {http.StatusUnprocessableEntity, "test op cannot compile to SQL"},
	CodeSQLColumnMissing:       {http.StatusInternalServerError, "patch path has no SQL column mapping"},
	CodeOpNotSQLCompilable:     {http.StatusUnprocessableEntity, "patch operation cannot compile to SQL"},
	CodeUnknownField:           {http.StatusUnprocessableEntity, "unknown field"},
	CodeInvalidOperator:        {http.StatusUnprocessableEntity, "invalid operator"},
	CodeValueTypeMismatch:      {http.StatusUnprocessableEntity, "value type mismatch"},
	CodeDepthExceeded:          {http.StatusUnprocessableEntity, "depth exceeded"},
	CodeInvalidPatchOp:         {http.StatusUnprocessableEntity, "invalid patch operation"},
	CodePatchFieldNotFound:     {http.StatusUnprocessableEntity, "patch field not found"},
	CodePatchValueInvalid:      {http.StatusUnprocessableEntity, "patch value invalid"},
	CodeSchemaNotFound:         {http.StatusUnprocessableEntity, "schema not found"},
	CodeSortFieldNotAllowed:    {http.StatusUnprocessableEntity, "sort field not allowed"},
	CodeSQLGenerationFailed:    {http.StatusUnprocessableEntity, "SQL generation failed"},
	CodeSortFieldLimit:         {http.StatusUnprocessableEntity, "sort field limit exceeded"},
	CodeInvalidCursor:          {http.StatusUnprocessableEntity, "invalid cursor"},
	CodeInvalidRelation:        {http.StatusUnprocessableEntity, "invalid relation"},
	CodePatchPolicyForbidden:   {http.StatusUnprocessableEntity, "patch policy forbidden"},
	CodePatchPolicyValue:       {http.StatusUnprocessableEntity, "patch policy value violation"},
	CodeInvalidJSON:            {http.StatusUnprocessableEntity, "invalid JSON"},
	CodeInvalidEnumValue:       {http.StatusUnprocessableEntity, "invalid enum value"},
	CodeArrayTooLarge:          {http.StatusUnprocessableEntity, "array too large"},
	CodeMaxConditions:          {http.StatusUnprocessableEntity, "max conditions exceeded"},
	CodeInvalidGeoCoords:       {http.StatusBadRequest, "invalid geo coordinates"},
	CodeInvalidDistance:        {http.StatusBadRequest, "invalid distance value"},
	CodeInvalidGeoPolygon:      {http.StatusBadRequest, "invalid geo polygon"},
	CodeGeoNotSupported:        {http.StatusBadRequest, "geo operator not supported by dialect"},
	CodeInvalidUUID:            {http.StatusBadRequest, "invalid UUID"},
	CodeRelationVerbNotAllowed: {http.StatusUnprocessableEntity, "patch verb not allowed on relation field"},
	CodeRelationValueInvalid:   {http.StatusUnprocessableEntity, "relation patch value must be array"},
	CodeRelationPatchTooLarge:  {http.StatusUnprocessableEntity, "relation patch values exceed maximum"},
	CodeSchemaEncodingFailed:   {http.StatusInternalServerError, "schema encoding failed"},
}

// ErrorHTTPStatus returns the code's HTTP status; unknown codes are invalid input.
func (c Code) ErrorHTTPStatus() int {
	if info, ok := codeInfo[c]; ok {
		return info.status
	}
	return http.StatusUnprocessableEntity
}

// ErrorMessage returns the code's default message, or empty for an unknown code.
func (c Code) ErrorMessage() string { return codeInfo[c].message }

// IsErrorUserFacing reports whether the category's message is safe to disclose.
func (c Code) IsErrorUserFacing() bool { return c.ErrorMessage() != "" }

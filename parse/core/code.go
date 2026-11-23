package core

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

// codeInfo is each code's HTTP status and default message. Statuses are
// integer literals: core, filter and patch do not import net/http.
var codeInfo = map[Code]struct {
	status  int
	message string
}{
	CodeQueryTooExpensive:      {422, "query too expensive"},
	CodeInvalidArray:           {422, "invalid array value"},
	CodeInputTooLarge:          {422, "input too large"},
	CodeOpNotAllowed:           {422, "operation not allowed"},
	CodeCursorTooLarge:         {422, "sort value too large to page"},
	CodeTooManyOps:             {422, "too many operations"},
	CodeInvalidListParam:       {422, "invalid list parameter"},
	CodeTestOpSQL:              {422, "test op cannot compile to SQL"},
	CodeSQLColumnMissing:       {500, "patch path has no SQL column mapping"},
	CodeOpNotSQLCompilable:     {422, "patch operation cannot compile to SQL"},
	CodeUnknownField:           {422, "unknown field"},
	CodeInvalidOperator:        {422, "invalid operator"},
	CodeValueTypeMismatch:      {422, "value type mismatch"},
	CodeDepthExceeded:          {422, "depth exceeded"},
	CodeInvalidPatchOp:         {422, "invalid patch operation"},
	CodePatchFieldNotFound:     {422, "patch field not found"},
	CodePatchValueInvalid:      {422, "patch value invalid"},
	CodeSchemaNotFound:         {422, "schema not found"},
	CodeSortFieldNotAllowed:    {422, "sort field not allowed"},
	CodeSortFieldLimit:         {422, "sort field limit exceeded"},
	CodeInvalidCursor:          {422, "invalid cursor"},
	CodeInvalidRelation:        {422, "invalid relation"},
	CodePatchPolicyForbidden:   {422, "patch policy forbidden"},
	CodePatchPolicyValue:       {422, "patch policy value violation"},
	CodeInvalidJSON:            {422, "invalid JSON"},
	CodeInvalidEnumValue:       {422, "invalid enum value"},
	CodeArrayTooLarge:          {422, "array too large"},
	CodeMaxConditions:          {422, "max conditions exceeded"},
	CodeInvalidGeoCoords:       {400, "invalid geo coordinates"},
	CodeInvalidDistance:        {400, "invalid distance value"},
	CodeInvalidGeoPolygon:      {400, "invalid geo polygon"},
	CodeGeoNotSupported:        {400, "geo operator not supported by dialect"},
	CodeInvalidUUID:            {400, "invalid UUID"},
	CodeRelationVerbNotAllowed: {422, "patch verb not allowed on relation field"},
	CodeRelationValueInvalid:   {422, "relation patch value must be array"},
	CodeRelationPatchTooLarge:  {422, "relation patch values exceed maximum"},
	CodeSchemaEncodingFailed:   {500, "schema encoding failed"},
}

// ErrorHTTPStatus returns the code's HTTP status; unknown codes are invalid input.
func (c Code) ErrorHTTPStatus() int {
	if info, ok := codeInfo[c]; ok {
		return info.status
	}
	return 422
}

// ErrorMessage returns the code's default message, or empty for an unknown code.
func (c Code) ErrorMessage() string { return codeInfo[c].message }

// IsErrorUserFacing reports whether the category's message is safe to disclose.
func (c Code) IsErrorUserFacing() bool { return c.ErrorMessage() != "" }

package filter

import (
	"errors"

	"github.com/ashtonian/pkg/parse/core"
)

// Error codes for filter and list operations.
const (
	ECodeQueryTooExpensive core.Code = "E2061"
	ECodeInvalidArray      core.Code = "E2062"
	ECodeInputTooLarge     core.Code = "E2063"
	ECodeCursorTooLarge    core.Code = "E2065"

	ECodeUnknownField        core.Code = "E2071"
	ECodeInvalidOperator     core.Code = "E2072"
	ECodeValueTypeMismatch   core.Code = "E2073"
	ECodeDepthExceeded       core.Code = "E2074"
	ECodeSchemaNotFound      core.Code = "E2078"
	ECodeSortFieldNotAllowed core.Code = "E2079"
	ECodeSQLGenerationFailed core.Code = "E2080"
	ECodeSortFieldLimit      core.Code = "E2081"
	ECodeInvalidCursor       core.Code = "E2082"
	ECodeInvalidRelation     core.Code = "E2083"
	ECodeInvalidJSON         core.Code = "E2086"
	ECodeInvalidEnumValue    core.Code = "E2087"
	ECodeArrayTooLarge       core.Code = "E2088"
	ECodeMaxConditions       core.Code = "E2089"

	// Geo-spatial error codes.
	ECodeInvalidGeoCoords  core.Code = "E2112"
	ECodeInvalidDistance   core.Code = "E2113"
	ECodeInvalidGeoPolygon core.Code = "E2114"
	ECodeGeoNotSupported   core.Code = "E2115"

	// malformed UUID at the filter boundary returns 400 instead
	// of zero rows.
	ECodeInvalidUUID core.Code = "E2116"

	// ECodeInvalidListParam is a list request parameter that is malformed or
	// that the resource cannot honor: a page size that is not a positive
	// integer, a boolean flag that is not a boolean, search on a resource
	// without search fields.
	ECodeInvalidListParam core.Code = "E2067"
)

// Codes returns the codes defined by this package.
func Codes() []core.Code {
	return []core.Code{
		ECodeQueryTooExpensive, ECodeInvalidArray, ECodeInputTooLarge, ECodeCursorTooLarge,
		ECodeUnknownField, ECodeInvalidOperator, ECodeValueTypeMismatch,
		ECodeDepthExceeded, ECodeSchemaNotFound, ECodeSortFieldNotAllowed,
		ECodeSQLGenerationFailed, ECodeSortFieldLimit, ECodeInvalidCursor,
		ECodeInvalidRelation, ECodeInvalidJSON, ECodeInvalidEnumValue,
		ECodeArrayTooLarge, ECodeMaxConditions,
		ECodeInvalidGeoCoords, ECodeInvalidDistance, ECodeInvalidGeoPolygon, ECodeGeoNotSupported,
		ECodeInvalidUUID, ECodeInvalidListParam,
	}
}

// ErrUnsupportedDialect is returned when a DSN references an unsupported database.
var ErrUnsupportedDialect = errors.New("unsupported SQL dialect")

func newFilterError(code core.Code, message string) *core.ParseError {
	return &core.ParseError{
		Code:    code.String(),
		Message: message,
		Kind:    core.ParseErrorKindFilter,
	}
}

func newSortError(code core.Code, message string) *core.ParseError {
	return &core.ParseError{
		Code:    code.String(),
		Message: message,
		Kind:    core.ParseErrorKindSort,
	}
}

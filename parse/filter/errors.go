package filter

import (
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/parse/core"
)

// Error codes for filter and list operations.
const (
	ECodeQueryTooExpensive = core.CodeQueryTooExpensive
	ECodeInvalidArray      = core.CodeInvalidArray
	ECodeInputTooLarge     = core.CodeInputTooLarge
	ECodeCursorTooLarge    = core.CodeCursorTooLarge

	ECodeUnknownField        = core.CodeUnknownField
	ECodeInvalidOperator     = core.CodeInvalidOperator
	ECodeValueTypeMismatch   = core.CodeValueTypeMismatch
	ECodeDepthExceeded       = core.CodeDepthExceeded
	ECodeSortFieldNotAllowed = core.CodeSortFieldNotAllowed
	ECodeSortFieldLimit      = core.CodeSortFieldLimit
	ECodeInvalidCursor       = core.CodeInvalidCursor
	ECodeInvalidRelation     = core.CodeInvalidRelation
	ECodeInvalidJSON         = core.CodeInvalidJSON
	ECodeInvalidEnumValue    = core.CodeInvalidEnumValue
	ECodeArrayTooLarge       = core.CodeArrayTooLarge
	ECodeMaxConditions       = core.CodeMaxConditions

	// Geo-spatial error codes.
	ECodeInvalidGeoCoords  = core.CodeInvalidGeoCoords
	ECodeInvalidDistance   = core.CodeInvalidDistance
	ECodeInvalidGeoPolygon = core.CodeInvalidGeoPolygon
	ECodeGeoNotSupported   = core.CodeGeoNotSupported

	// malformed UUID at the filter boundary returns 400 instead
	// of zero rows.
	ECodeInvalidUUID = core.CodeInvalidUUID

	// ECodeInvalidListParam is a list request parameter that is malformed or
	// that the resource cannot honor: a page size that is not a positive
	// integer, a boolean flag that is not a boolean, search on a resource
	// without search fields, a parameter given twice, or a query string that
	// does not decode.
	ECodeInvalidListParam = core.CodeInvalidListParam
)

// Codes returns the codes defined by this package.
func Codes() []core.Code {
	return []core.Code{
		ECodeQueryTooExpensive, ECodeInvalidArray, ECodeInputTooLarge, ECodeCursorTooLarge,
		ECodeUnknownField, ECodeInvalidOperator, ECodeValueTypeMismatch,
		ECodeDepthExceeded, ECodeSortFieldNotAllowed,
		ECodeSortFieldLimit, ECodeInvalidCursor,
		ECodeInvalidRelation, ECodeInvalidJSON, ECodeInvalidEnumValue,
		ECodeArrayTooLarge, ECodeMaxConditions,
		ECodeInvalidGeoCoords, ECodeInvalidDistance, ECodeInvalidGeoPolygon, ECodeGeoNotSupported,
		ECodeInvalidUUID, ECodeInvalidListParam,
	}
}

// ErrUnsupportedDialect is what application SQL, such as a computed column's
// expression, returns for a dialect it has no rendering for.
var ErrUnsupportedDialect = errors.New("unsupported SQL dialect")

// fieldError is a refusal about one field the request named: its message
// opens with the field's name and Details[core.DetailField] holds it, so the
// two cannot disagree.
func fieldError(code core.Code, name, problem string, opts ...core.ErrorOption) *core.ParseError {
	all := append([]core.ErrorOption{core.WithField(name)}, opts...)
	return core.NewError(code, fmt.Sprintf("field '%s': %s", name, problem), all...)
}

// valueMismatch is E2073 for a request value field cannot hold, naming the
// field, caused by cause.
func valueMismatch(field *FieldSchema, cause error) *core.ParseError {
	return fieldError(ECodeValueTypeMismatch, field.ExternalName, cause.Error(), core.WithCause(cause))
}

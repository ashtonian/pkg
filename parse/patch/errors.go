package patch

import (
	"github.com/ashtonian/pkg/parse/core"
)

// Error codes for patch operations.
const (
	ECodeOpNotAllowed = core.CodeOpNotAllowed
	// ECodePathTooDeep shares E2074 with filter's ECodeDepthExceeded: one
	// ParserConfig.MaxDepth bounds filter nesting and patch pointer depth.
	ECodePathTooDeep = core.CodeDepthExceeded
	ECodeTooManyOps  = core.CodeTooManyOps
	// ECodeInputTooLarge shares E2063 with filter: one MaxInputSize bounds
	// filter and patch bodies.
	ECodeInputTooLarge        = core.CodeInputTooLarge
	ECodeInvalidPatchOp       = core.CodeInvalidPatchOp
	ECodePatchFieldNotFound   = core.CodePatchFieldNotFound
	ECodePatchValueInvalid    = core.CodePatchValueInvalid
	ECodePatchSchemaNotFound  = core.CodeSchemaNotFound
	ECodePatchPolicyForbidden = core.CodePatchPolicyForbidden
	ECodePatchPolicyValue     = core.CodePatchPolicyValue
	ECodeInvalidJSON          = core.CodeInvalidJSON

	// relation-patch error codes.
	ECodeRelationVerbNotAllowed = core.CodeRelationVerbNotAllowed
	ECodeRelationValueInvalid   = core.CodeRelationValueInvalid
	ECodeRelationPatchTooLarge  = core.CodeRelationPatchTooLarge

	//  / P-13i — SQL-compilation refusals, in the parse gap
	// E2068-E2069.
	ECodeTestOpSQL        = core.CodeTestOpSQL
	ECodeSQLColumnMissing = core.CodeSQLColumnMissing

	// an accepted operation BuildPatchSQL cannot express
	// (move, copy, a nested JSON Pointer path) is rejected, never skipped.
	ECodeOpNotSQLCompilable = core.CodeOpNotSQLCompilable
)

// Codes returns the codes defined by this package.
func Codes() []core.Code {
	return []core.Code{
		ECodeOpNotAllowed, ECodePathTooDeep, ECodeTooManyOps, ECodeInputTooLarge,
		ECodeInvalidPatchOp, ECodePatchFieldNotFound, ECodePatchValueInvalid,
		ECodePatchSchemaNotFound, ECodePatchPolicyForbidden, ECodePatchPolicyValue,
		ECodeInvalidJSON,
		ECodeRelationVerbNotAllowed, ECodeRelationValueInvalid, ECodeRelationPatchTooLarge,
		ECodeTestOpSQL, ECodeSQLColumnMissing, ECodeOpNotSQLCompilable,
	}
}

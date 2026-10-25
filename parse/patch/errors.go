package patch

import (
	"github.com/ashtonian/pkg/parse/core"
)

// Error codes for patch operations.
const (
	ECodeOpNotAllowed core.Code = "E2064"
	// ECodePathTooDeep shares E2074 with filter's ECodeDepthExceeded: one
	// ParserConfig.MaxDepth bounds filter nesting and patch pointer depth.
	ECodePathTooDeep core.Code = "E2074"
	ECodeTooManyOps  core.Code = "E2066"
	// ECodeInputTooLarge shares E2063 with filter: one MaxInputSize bounds
	// filter and patch bodies.
	ECodeInputTooLarge        core.Code = "E2063"
	ECodeInvalidPatchOp       core.Code = "E2075"
	ECodePatchFieldNotFound   core.Code = "E2076"
	ECodePatchValueInvalid    core.Code = "E2077"
	ECodePatchSchemaNotFound  core.Code = "E2078"
	ECodePatchPolicyForbidden core.Code = "E2084"
	ECodePatchPolicyValue     core.Code = "E2085"
	ECodeInvalidJSON          core.Code = "E2086"

	// relation-patch error codes.
	ECodeRelationVerbNotAllowed core.Code = "E2117"
	ECodeRelationValueInvalid   core.Code = "E2118"
	ECodeRelationPatchTooLarge  core.Code = "E2119"

	//  / P-13i — SQL-compilation refusals, in the parse gap
	// E2068-E2069.
	ECodeTestOpSQL        core.Code = "E2068"
	ECodeSQLColumnMissing core.Code = "E2069"

	// an accepted operation BuildPatchSQL cannot express
	// (move, copy, a nested JSON Pointer path) is rejected, never skipped.
	ECodeOpNotSQLCompilable core.Code = "E2070"
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

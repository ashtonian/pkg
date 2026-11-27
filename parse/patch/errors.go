package patch

import (
	"errors"

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
	ECodePatchPolicyForbidden = core.CodePatchPolicyForbidden
	ECodePatchPolicyValue     = core.CodePatchPolicyValue
	ECodeInvalidJSON          = core.CodeInvalidJSON

	// relation-patch error codes.
	ECodeRelationVerbNotAllowed = core.CodeRelationVerbNotAllowed
	ECodeRelationValueInvalid   = core.CodeRelationValueInvalid
	ECodeRelationPatchTooLarge  = core.CodeRelationPatchTooLarge

	// SQL-compilation refusals of an operation the parser accepted.
	ECodeTestOpSQL = core.CodeTestOpSQL

	// an accepted operation BuildPatchSQL cannot express
	// (move, copy, a nested JSON Pointer path) is rejected, never skipped.
	ECodeOpNotSQLCompilable = core.CodeOpNotSQLCompilable
)

// Codes returns the codes defined by this package.
func Codes() []core.Code {
	return []core.Code{
		ECodeOpNotAllowed, ECodePathTooDeep, ECodeTooManyOps, ECodeInputTooLarge,
		ECodeInvalidPatchOp, ECodePatchFieldNotFound, ECodePatchValueInvalid,
		ECodePatchPolicyForbidden, ECodePatchPolicyValue,
		ECodeInvalidJSON,
		ECodeRelationVerbNotAllowed, ECodeRelationValueInvalid, ECodeRelationPatchTooLarge,
		ECodeTestOpSQL, ECodeOpNotSQLCompilable,
	}
}

// ErrRemoveNeedsNull is a field whose patch_ops name remove while its type
// holds no NULL (core.IsNullableType): a clear would have no value to write.
// GeneratePatchSchema wraps it with the field and its type.
var ErrRemoveNeedsNull = errors.New("patch operation remove requires a type that holds null")

// ErrSQLColumnMissing is a patch schema path, neither a relation nor a
// column, that an operation names: BuildPatchSQL cannot compile it and never
// uses the request's path as a SQL identifier. The schema is the server's,
// so it is no ParseError; give the path its SQLColumn.
var ErrSQLColumnMissing = errors.New("patch: the schema path has no SQL column")

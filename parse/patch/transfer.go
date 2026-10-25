package patch

import (
	"fmt"

	"github.com/ashtonian/pkg/parse/core"
)

// ValidateTransferValue validates the runtime source of a copy or move against
// its destination. The source must be resolved from the document at the current
// operation, after earlier operations have run.
func ValidateTransferValue(op *PatchOperation, value any, codec core.Codec, index int) error {
	tokens, err := core.ParsePointer(op.Path)
	if err == nil {
		err = checkNullability(op.Schema, PatchOpAdd, value, len(tokens))
	}
	if err == nil {
		value, err = normalizeOpValue(op.Schema, PatchOpAdd, value, len(tokens), codec)
	}
	if err == nil {
		for _, validate := range op.Schema.Validators {
			if err = validate(value); err != nil {
				break
			}
		}
	}
	if err != nil {
		return &core.ParseError{Kind: core.ParseErrorKindPatch, Code: ECodePatchValueInvalid.String(), Message: fmt.Sprintf("invalid transfer to path %q at index %d: %v", op.Path, index, err), Path: op.Path, Index: new(index), Cause: err}
	}
	return nil
}

package config

import (
	"errors"
	"fmt"

	"github.com/go-playground/validator/v10"

	"github.com/ashtonian/pkg/errcontract"
	"github.com/ashtonian/pkg/errutil"
)

// Error codes for the config package (E7002-E7006 range).
// E7001 was removed — use validate:"required" instead.
var (
	CodeValidation = errcontract.CodeDef{
		Code: "E7002", HTTPStatus: 422, Class: errcontract.ClassPermanent,
		UserFacing: true, Message: "configuration validation failed",
		Sentinel: errcontract.ErrValidationSentinel, Package: "config",
	}
	CodeFileRead = errcontract.CodeDef{
		Code: "E7003", HTTPStatus: 500, Class: errcontract.ClassUnknown,
		UserFacing: false, Message: "failed to read configuration file", Package: "config",
	}
	CodeSecretProvider = errcontract.CodeDef{
		Code: "E7005", HTTPStatus: 500, Class: errcontract.ClassTransient,
		UserFacing: false, Message: "secret provider error",
		Sentinel: errcontract.ErrDependencySentinel, Package: "config",
	}
	CodeReload = errcontract.CodeDef{
		Code: "E7006", HTTPStatus: 500, Class: errcontract.ClassTransient,
		UserFacing: false, Message: "configuration reload failed", Package: "config",
	}
)

// Backward-compatible aliases for the old ErrCodeXxx names.
var (
	ErrCodeValidation     = CodeValidation
	ErrCodeFileRead       = CodeFileRead
	ErrCodeSecretProvider = CodeSecretProvider
	ErrCodeReload         = CodeReload
)

// Sentinel errors for common checks using errors.Is.
var (
	ErrValidation     = errcontract.ErrorOf(CodeValidation)
	ErrFileRead       = errcontract.ErrorOf(CodeFileRead)
	ErrSecretProvider = errcontract.ErrorOf(CodeSecretProvider)
	ErrReload         = errcontract.ErrorOf(CodeReload)
)

// ConfigError wraps an errutil.APIError with localized message support.
// It satisfies the error interface and can produce localized messages
// when a translator function is provided.
type ConfigError struct {
	*errutil.APIError
	field string
	tag   string
	param string
}

// NewConfigError creates a ConfigError from an APIError with field-level details.
func NewConfigError(apiErr *errutil.APIError, field, tag, param string) *ConfigError {
	return &ConfigError{
		APIError: apiErr,
		field:    field,
		tag:      tag,
		param:    param,
	}
}

// Field returns the config field name that caused the error.
func (e *ConfigError) Field() string {
	return e.field
}

// Tag returns the validation tag that failed.
func (e *ConfigError) Tag() string {
	return e.tag
}

// Param returns the validation parameter (e.g., "1" for min=1).
func (e *ConfigError) Param() string {
	return e.param
}

// LocalizedMessage returns a human-readable localized message using the
// provided translator function. If translator is nil or returns empty,
// falls back to the standard error message.
func (e *ConfigError) LocalizedMessage(translator func(code, field, tag, param string) string) string {
	if translator != nil {
		msg := translator(e.Code, e.field, e.tag, e.param)
		if msg != "" {
			return msg
		}
	}
	return e.Error()
}

// validationError wraps both the sentinel APIError and the original
// validator.ValidationErrors so that ToConfigErrors can extract field details.
type validationError struct {
	msg      string
	sentinel error
	fieldErr validator.ValidationErrors
}

func (e *validationError) Error() string {
	return e.msg
}

func (e *validationError) Unwrap() []error {
	return []error{e.sentinel, e.fieldErr}
}

// ToConfigErrors converts a GetConfig validation error into a slice of
// ConfigError values with field-level details. Returns nil if the error
// is not a validation error.
func ToConfigErrors(err error) []*ConfigError {
	if err == nil {
		return nil
	}

	// Unwrap through fmt.Errorf wrappers to find the validator.ValidationErrors.
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return nil
	}

	result := make([]*ConfigError, 0, len(ve))
	for _, fe := range ve {
		tag := fe.Tag()
		if fe.Param() != "" {
			tag += "=" + fe.Param()
		}
		msg := fmt.Sprintf("field %s failed validation: %s", fe.Field(), tag)
		apiErr := errutil.NewFromCodeDef(CodeValidation, msg)
		result = append(result, NewConfigError(apiErr, fe.Namespace(), fe.Tag(), fe.Param()))
	}
	return result
}

// Codes returns the package's full error catalog.
func Codes() []errcontract.CodeDef {
	return []errcontract.CodeDef{
		CodeValidation,
		CodeFileRead,
		CodeSecretProvider,
		CodeReload,
	}
}

// RegisterTo loads the package's CodeDefs into the given store.
func RegisterTo(s errcontract.Store) {
	for _, c := range Codes() {
		s.Register(c)
	}
}

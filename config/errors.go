package config

import (
	"errors"
	"fmt"

	"github.com/go-playground/validator/v10"

	"github.com/ashtonian/pkg/errutil"
)

// Error codes for the config package (E7002-E7006 range).
// E7001 was removed — use validate:"required" instead.
const (
	ErrCodeValidation     = "E7002" // Validation rule failed (includes required checks)
	ErrCodeFileRead       = "E7003" // Config file could not be read
	ErrCodeSecretProvider = "E7005" // Secret provider returned an error
	ErrCodeReload         = "E7006" // Config reload failed
)

// Sentinel errors for common checks using errors.Is.
var (
	ErrValidation     = errutil.New(ErrCodeValidation, "configuration validation failed")
	ErrFileRead       = errutil.New(ErrCodeFileRead, "failed to read configuration file")
	ErrSecretProvider = errutil.New(ErrCodeSecretProvider, "secret provider error")
	ErrReload         = errutil.New(ErrCodeReload, "configuration reload failed")
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
		apiErr := errutil.New(ErrCodeValidation, msg)
		result = append(result, NewConfigError(apiErr, fe.Namespace(), fe.Tag(), fe.Param()))
	}
	return result
}

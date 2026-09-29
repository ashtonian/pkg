package config

import (
	"errors"
	"fmt"

	"github.com/go-playground/validator/v10"

	"github.com/ashtonian/pkg/errcontract"
	"github.com/ashtonian/pkg/errutil"
)

// Error codes for the config package (E7002-E7009). E7001 was removed —
// use validate:"required" instead.
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
	// CodeFileParse separates "the bytes are not valid YAML" from "the bytes
	// could not be read", so an operator can tell a syntax error in an overlay
	// from a missing mount without parsing the message.
	CodeFileParse = errcontract.CodeDef{
		Code: "E7004", HTTPStatus: 500, Class: errcontract.ClassPermanent,
		UserFacing: false, Message: "configuration file is not valid YAML", Package: "config",
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
	CodePostLoad = errcontract.CodeDef{
		Code: "E7007", HTTPStatus: 500, Class: errcontract.ClassPermanent,
		UserFacing: false, Message: "configuration post-load hook failed", Package: "config",
	}
	// CodeReloadInProgress is deliberately NOT a flavour of CodeReload: a
	// manual reload that finds another reload in flight ran no pipeline, so
	// nothing failed. Conflating the two turns a burst of operator requests
	// into a stream of failure alerts. 409 mirrors the HTTP semantics
	// ReloadHandler serves.
	CodeReloadInProgress = errcontract.CodeDef{
		Code: "E7008", HTTPStatus: 409, Class: errcontract.ClassTransient,
		UserFacing: false, Message: "configuration reload already in progress", Package: "config",
	}
	// CodeReloaderStopped reports a reload requested after Stop. Like
	// CodeReloadInProgress it is not a reload failure — the reloader is shut
	// down, the config it last published is unchanged.
	CodeReloaderStopped = errcontract.CodeDef{
		Code: "E7009", HTTPStatus: 503, Class: errcontract.ClassPermanent,
		UserFacing: false, Message: "configuration reloader is stopped", Package: "config",
	}
)

// Sentinel errors for common checks using errors.Is.
var (
	ErrValidation     = errcontract.ErrorOf(CodeValidation)
	ErrFileRead       = errcontract.ErrorOf(CodeFileRead)
	ErrFileParse      = errcontract.ErrorOf(CodeFileParse)
	ErrSecretProvider = errcontract.ErrorOf(CodeSecretProvider)
	ErrReload         = errcontract.ErrorOf(CodeReload)
	ErrPostLoad       = errcontract.ErrorOf(CodePostLoad)
	// ErrReloadInProgress reports that Reload was called while another
	// reload held the single-flight slot. It does NOT wrap ErrReload —
	// see CodeReloadInProgress. A caller that wants "any non-success"
	// checks both sentinels.
	ErrReloadInProgress = errcontract.ErrorOf(CodeReloadInProgress)
	// ErrReloaderStopped reports that Reload was called after Stop. It does
	// NOT wrap ErrReload.
	ErrReloaderStopped = errcontract.ErrorOf(CodeReloaderStopped)
)

// TranslateFunc translates an error code, field, tag, and param into a
// localized string. It is the seam for adapting an i18n catalog without this
// package importing one.
type TranslateFunc func(code, field, tag, param string) string

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
func (e *ConfigError) LocalizedMessage(translator TranslateFunc) string {
	if translator != nil {
		msg := translator(e.Code, e.field, e.tag, e.param)
		if msg != "" {
			return msg
		}
	}
	return e.Error()
}

// validationError wraps both the sentinel APIError and the original
// validator.ValidationErrors so that ToConfigErrors can extract field
// details. When a non-govalidator implementation (e.g.
// a ModelValidator returning its own typed error)
// is wired via WithValidator, fieldErr stays nil and cause carries the
// typed error verbatim so callers can `errors.As` it themselves.
type validationError struct {
	msg      string
	sentinel error
	fieldErr validator.ValidationErrors
	cause    error
}

func (e *validationError) Error() string {
	return e.msg
}

func (e *validationError) Unwrap() []error {
	errs := []error{e.sentinel}
	if e.fieldErr != nil {
		errs = append(errs, e.fieldErr)
	}
	if e.cause != nil {
		errs = append(errs, e.cause)
	}
	return errs
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
		CodeFileParse,
		CodeSecretProvider,
		CodeReload,
		CodePostLoad,
		CodeReloadInProgress,
		CodeReloaderStopped,
	}
}

// RegisterTo loads the package's CodeDefs into the given store.
func RegisterTo(s errcontract.Store) {
	for _, c := range Codes() {
		s.Register(c)
	}
}

package config

import (
	"github.com/ashtonian/pkg/errcontract"
)

// Error codes for the config package (E7002-E7009, E7011).
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
	// CodeSource reports a source that failed to load — the defaults, the
	// environment, or a WithProvider source — as opposed to a file (E7003,
	// E7004) or a secret provider (E7005).
	CodeSource = errcontract.CodeDef{
		Code: "E7011", HTTPStatus: 500, Class: errcontract.ClassUnknown,
		UserFacing: false, Message: "configuration source failed to load", Package: "config",
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
	ErrSource         = errcontract.ErrorOf(CodeSource)
	// ErrReloadInProgress reports that Reload was called while another
	// reload held the single-flight slot. It does NOT wrap ErrReload —
	// see CodeReloadInProgress. A caller that wants "any non-success"
	// checks both sentinels.
	ErrReloadInProgress = errcontract.ErrorOf(CodeReloadInProgress)
	// ErrReloaderStopped reports that Reload was called after Stop. It does
	// NOT wrap ErrReload.
	ErrReloaderStopped = errcontract.ErrorOf(CodeReloaderStopped)
)

// validationError carries ErrValidation together with the validator's own
// error, whole: callers errors.As to validator.ValidationErrors for field
// details, or to whatever a WithValidator validator wraps them in.
type validationError struct {
	msg   string
	cause error
}

func (e *validationError) Error() string {
	return e.msg
}

func (e *validationError) Unwrap() []error {
	return []error{ErrValidation, e.cause}
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
		CodeSource,
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

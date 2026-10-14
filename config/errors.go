package config

import "errors"

// Category sentinels. Every error GetConfig, a Reloader or ReloadHandler
// returns wraps exactly one of them together with its cause, so errors.Is
// tells the categories apart and errors.As reaches the cause. Codes, HTTP
// statuses and retry classes are the caller's to assign: match a sentinel and
// map it to your own error vocabulary.
var (
	// ErrValidation reports invalid configuration: a validator rejected the
	// loaded struct, a value could not decode into its field's type, or an
	// environment variable named more than one key.
	ErrValidation = errors.New("configuration validation failed")
	// ErrFileRead reports a config file that could not be read: a required
	// file that is missing, or any file that exists but cannot be opened.
	ErrFileRead = errors.New("failed to read configuration file")
	// ErrFileParse reports a config file whose bytes are not valid YAML, as
	// distinct from one that could not be read.
	ErrFileParse = errors.New("configuration file is not valid YAML")
	// ErrSecretProvider reports a failure of the WithSecretPopulator function.
	ErrSecretProvider = errors.New("secret provider error")
	// ErrPostLoad reports a WithPostLoad hook that returned an error.
	ErrPostLoad = errors.New("configuration post-load hook failed")
	// ErrSource reports a source that failed to load — the defaults, the
	// environment, or a WithProvider source — as opposed to a file
	// (ErrFileRead, ErrFileParse) or the secret populator (ErrSecretProvider).
	ErrSource = errors.New("configuration source failed to load")
	// ErrReload wraps every reload that ran the pipeline and failed; the
	// previous config stays live. The failure's own category is wrapped too.
	ErrReload = errors.New("configuration reload failed")
	// ErrReloadInProgress reports a manual Reload that found another reload
	// in flight. No pipeline ran and nothing failed, so it does NOT wrap
	// ErrReload: a burst of operator requests must not read as failures.
	ErrReloadInProgress = errors.New("configuration reload already in progress")
	// ErrReloaderStopped reports a call after Stop. It does NOT wrap ErrReload.
	ErrReloaderStopped = errors.New("configuration reloader is stopped")
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

package config

import "errors"

// Category sentinels. Every error Load or a Reloader returns wraps exactly
// one of them together with its cause, so errors.Is tells the categories
// apart and errors.As reaches the cause. Codes, HTTP statuses and retry
// classes are the caller's to assign: match a sentinel and map it to your own
// error vocabulary.
var (
	// ErrValidation reports invalid configuration: a validator rejected the
	// loaded struct, a value could not decode into its field's type, an
	// environment variable named more than one key, a Validate method
	// failed, or a source set a key no field decodes (unless
	// WithAllowUnknownKeys).
	ErrValidation = errors.New("configuration validation failed")
	// ErrFileRead reports a config file that could not be read: a required
	// file that is missing, or any file that exists but cannot be opened.
	ErrFileRead = errors.New("failed to read configuration file")
	// ErrFileParse reports a config file the file parser (YAML unless
	// WithFileParser sets another) rejects, as distinct from one that could
	// not be read.
	ErrFileParse = errors.New("configuration file could not be parsed")
	// ErrSecretProvider reports a failure of the WithSecretPopulator function.
	ErrSecretProvider = errors.New("secret provider error")
	// ErrPostLoad reports a WithPostLoad hook that returned an error.
	ErrPostLoad = errors.New("configuration post-load hook failed")
	// ErrSource reports a source that failed to load — the defaults, the
	// environment, or a base or override provider — as opposed to a file
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
	// ErrSchema reports a config type the package cannot address: a struct
	// key holding a character keys use for structure (".", "[", "]", "*", a
	// backslash, a quote or a control character), two fields with one key, a
	// type that is not a struct where one is required, or a value Diff cannot
	// compare (a non-nil func, channel or unsafe pointer).
	ErrSchema = errors.New("invalid configuration schema")
	// ErrKey reports a configuration key that does not parse, or that names
	// no field of the config type.
	ErrKey = errors.New("invalid configuration key")
)

// Validatable is a config with rules no tag can state: which keys go
// together, which settings exclude each other. When *T implements it, Load
// calls Validate after the validator passes, on the config the post-load
// hooks produced; its error fails the load with [ErrValidation] and is kept
// as the cause, its text in the message — keep values out of it. Validate
// must not change the config (normalize in a [WithPostLoad] hook) and does no
// I/O. [WithSkipValidation] skips it; a [WithValidator] validator does not
// replace it.
type Validatable interface {
	Validate() error
}

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

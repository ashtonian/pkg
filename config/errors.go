package config

import "github.com/ashtonian/pkg/errutil"

// Error codes for the config package (E7001-E7008 range).
const (
	ErrCodeRequired       = "E7001" // Required field not set
	ErrCodeValidation     = "E7002" // Validation rule failed
	ErrCodeFileRead       = "E7003" // Config file could not be read
	ErrCodeFileParse      = "E7004" // Config file could not be parsed
	ErrCodeSecretProvider = "E7005" // Secret provider returned an error
	ErrCodeUnsupportedType = "E7006" // Unsupported field type for conversion
	ErrCodeDuplicateKey   = "E7007" // Duplicate config key from multiple sources
	ErrCodeStrictUnknown  = "E7008" // Unknown key in strict mode
)

// Sentinel errors for common checks using errors.Is.
var (
	ErrRequired       = errutil.New(ErrCodeRequired, "required configuration field not set")
	ErrValidation     = errutil.New(ErrCodeValidation, "configuration validation failed")
	ErrFileRead       = errutil.New(ErrCodeFileRead, "failed to read configuration file")
	ErrFileParse      = errutil.New(ErrCodeFileParse, "failed to parse configuration file")
	ErrSecretProvider = errutil.New(ErrCodeSecretProvider, "secret provider error")
	ErrUnsupportedType = errutil.New(ErrCodeUnsupportedType, "unsupported field type")
	ErrDuplicateKey   = errutil.New(ErrCodeDuplicateKey, "duplicate configuration key")
	ErrStrictUnknown  = errutil.New(ErrCodeStrictUnknown, "unknown configuration key in strict mode")
)

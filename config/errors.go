package config

import "github.com/ashtonian/pkg/errutil"

// Error codes for the config package (E7002-E7005 range).
// E7001 was removed — use validate:"required" instead.
const (
	ErrCodeValidation     = "E7002" // Validation rule failed (includes required checks)
	ErrCodeFileRead       = "E7003" // Config file could not be read
	ErrCodeSecretProvider = "E7005" // Secret provider returned an error
)

// Sentinel errors for common checks using errors.Is.
var (
	ErrValidation     = errutil.New(ErrCodeValidation, "configuration validation failed")
	ErrFileRead       = errutil.New(ErrCodeFileRead, "failed to read configuration file")
	ErrSecretProvider = errutil.New(ErrCodeSecretProvider, "secret provider error")
)

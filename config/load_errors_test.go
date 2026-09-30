//go:build unit

package config

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
)

type portCfg struct {
	Port int `koanf:"port" default:"8080"`
}

// An optional file may be absent; it may not be broken. Before the fix every
// load error on an optional file was swallowed, so a syntax error booted (or
// reloaded) on defaults and reported success.
func TestGetConfig_MalformedOptionalFileFails(t *testing.T) {
	path := writeYAML(t, "port: [unterminated\n")

	_, err := GetConfig[portCfg](t.Context(), WithConfigFile(path), WithSkipValidation())
	if !errors.Is(err, ErrFileParse) {
		t.Fatalf("err = %v, want ErrFileParse", err)
	}
	if errors.Is(err, ErrFileRead) {
		t.Error("a YAML syntax error must not read as ErrFileRead")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "line") {
		t.Errorf("err = %q, want the path and the parser's line position", err)
	}
}

func TestGetConfig_UnreadableOptionalFileFails(t *testing.T) {
	dir := t.TempDir() // a directory exists but cannot be read as a file

	_, err := GetConfig[portCfg](t.Context(), WithConfigFile(dir), WithSkipValidation())
	if !errors.Is(err, ErrFileRead) {
		t.Fatalf("err = %v, want ErrFileRead", err)
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Errorf("err = %v, want an *fs.PathError in the chain", err)
	}
}

// A reload of a malformed optional file fails and keeps port
// 9000 live rather than swapping in the 8080 default.
func TestReload_MalformedOptionalFileKeepsLastConfig(t *testing.T) {
	path := writeYAML(t, "port: 9000\n")
	r, err := NewReloader[portCfg](t.Context(), nil, WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}

	if err := os.WriteFile(path, []byte("port: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = r.Reload(t.Context())
	if !errors.Is(err, ErrReload) || !errors.Is(err, ErrFileParse) {
		t.Fatalf("err = %v, want ErrReload wrapping ErrFileParse", err)
	}
	if got := r.Config().Port; got != 9000 {
		t.Errorf("Port = %d, want 9000 (the last good config)", got)
	}
	if s := r.Stats(); s.Successes != 0 || s.Failures != 1 {
		t.Errorf("stats = %+v, want 0 successes / 1 failure", s)
	}
}

// Once a reloader has read an optional file, the file dropping out is a
// failure, not a silent revert to lower-precedence values.
func TestReload_OptionalFileRemovedAfterLoadFails(t *testing.T) {
	path := writeYAML(t, "port: 9000\n")
	r, err := NewReloader[portCfg](t.Context(), nil, WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, err = r.Reload(t.Context())
	if !errors.Is(err, ErrReload) || !errors.Is(err, ErrFileRead) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want ErrReload wrapping ErrFileRead and fs.ErrNotExist", err)
	}
	if got := r.Config().Port; got != 9000 {
		t.Errorf("Port = %d, want 9000", got)
	}
}

func TestReload_OptionalFileCreatedLaterIsLoadedThenRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overlay.yaml")
	r, err := NewReloader[portCfg](t.Context(), nil, WithConfigFile(path), WithSkipValidation())
	if err != nil {
		t.Fatalf("NewReloader with the optional file absent: %v", err)
	}
	if got := r.Config().Port; got != 8080 {
		t.Fatalf("Port = %d, want the 8080 default", got)
	}

	if err := os.WriteFile(path, []byte("port: 9100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reload(t.Context()); err != nil {
		t.Fatalf("Reload after the file appeared: %v", err)
	}
	if got := r.Config().Port; got != 9100 {
		t.Fatalf("Port = %d, want 9100", got)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reload(t.Context()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist once the loaded file is gone", err)
	}
}

var (
	errSecretBackend = errors.New("secret backend unreachable")
	errHook          = errors.New("hook rejected the config")
)

type causeCfg struct {
	Port     int    `koanf:"port" default:"8080" validate:"min=1"`
	Password string `koanf:"password"`
}

// Every stage exposes both its category sentinel and the underlying cause
// (the file error, the secret backend's error, the hook's error), through
// GetConfig and through a Reloader.
func TestErrorCauses_SurviveLoadAndReload(t *testing.T) {
	type breakage struct {
		opts    []Option
		breakIt func()
	}
	tests := []struct {
		name     string
		setup    func(t *testing.T) breakage
		category error
		causes   []error
		as       func(error) bool
	}{
		{
			name: "required file removed",
			setup: func(t *testing.T) breakage {
				path := writeYAML(t, "port: 1\n")
				return breakage{
					opts:    []Option{WithMustConfigFile(path)},
					breakIt: func() { _ = os.Remove(path) },
				}
			},
			category: ErrFileRead,
			causes:   []error{fs.ErrNotExist},
			as:       func(err error) bool { var pe *fs.PathError; return errors.As(err, &pe) },
		},
		{
			name: "file stops parsing",
			setup: func(t *testing.T) breakage {
				path := writeYAML(t, "port: 1\n")
				return breakage{
					opts:    []Option{WithMustConfigFile(path)},
					breakIt: func() { _ = os.WriteFile(path, []byte("port: [\n"), 0o644) },
				}
			},
			category: ErrFileParse,
		},
		{
			name: "secret backend fails",
			setup: func(t *testing.T) breakage {
				var broken bool
				return breakage{
					opts: []Option{WithSecretPopulator(func(context.Context, any) error {
						if broken {
							return errSecretBackend
						}
						return nil
					})},
					breakIt: func() { broken = true },
				}
			},
			category: ErrSecretProvider,
			causes:   []error{errSecretBackend},
		},
		{
			name: "secret backend times out",
			setup: func(t *testing.T) breakage {
				var broken bool
				return breakage{
					opts: []Option{WithSecretPopulator(func(context.Context, any) error {
						if broken {
							return context.DeadlineExceeded
						}
						return nil
					})},
					breakIt: func() { broken = true },
				}
			},
			category: ErrSecretProvider,
			causes:   []error{context.DeadlineExceeded},
		},
		{
			name: "post-load hook rejects",
			setup: func(t *testing.T) breakage {
				var broken bool
				return breakage{
					opts: []Option{WithPostLoad(func(context.Context, *causeCfg) error {
						if broken {
							return errHook
						}
						return nil
					})},
					breakIt: func() { broken = true },
				}
			},
			category: ErrPostLoad,
			causes:   []error{errHook},
		},
		{
			name: "validation fails",
			setup: func(t *testing.T) breakage {
				t.Setenv("CAUSE_PORT", "1")
				return breakage{
					opts:    []Option{WithEnvPrefix("CAUSE")},
					breakIt: func() { t.Setenv("CAUSE_PORT", "0") },
				}
			},
			category: ErrValidation,
			as:       func(err error) bool { var ve validator.ValidationErrors; return errors.As(err, &ve) },
		},
	}

	check := func(t *testing.T, err error, category error, causes []error, as func(error) bool) {
		t.Helper()
		if !errors.Is(err, category) {
			t.Errorf("errors.Is(%v, category %v) = false", err, category)
		}
		for _, cause := range causes {
			if !errors.Is(err, cause) {
				t.Errorf("errors.Is(%v, cause %v) = false", err, cause)
			}
		}
		if as != nil && !as(err) {
			t.Errorf("errors.As failed to extract the typed cause from %v", err)
		}
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.setup(t)
			r, err := NewReloader[causeCfg](t.Context(), nil, b.opts...)
			if err != nil {
				t.Fatalf("initial load: %v", err)
			}
			b.breakIt()

			_, err = GetConfig[causeCfg](t.Context(), b.opts...)
			check(t, err, tt.category, tt.causes, tt.as)

			_, err = r.Reload(t.Context())
			if !errors.Is(err, ErrReload) {
				t.Errorf("reload err = %v, want ErrReload", err)
			}
			check(t, err, tt.category, tt.causes, tt.as)
		})
	}
}

type secretValidatedCfg struct {
	Password string `koanf:"password" sensitive:"true" validate:"min=20"`
}

// A validation failure logs no field values: they can be credentials.
func TestGetConfig_ValidationFailureDoesNotLogValues(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	_, err := GetConfig[secretValidatedCfg](t.Context(),
		WithLogger(logger),
		WithDefaults(map[string]any{"password": "SECRET_LOG"}),
	)
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if strings.Contains(logs.String(), "SECRET_LOG") {
		t.Errorf("the failing value reached the log:\n%s", logs.String())
	}
	if strings.Contains(err.Error(), "SECRET_LOG") {
		t.Errorf("the failing value reached the error: %v", err)
	}
}

func TestGetConfig_UsesInjectedLogger(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if _, err := GetConfig[portCfg](t.Context(), WithLogger(logger)); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !strings.Contains(logs.String(), "config sources loaded") {
		t.Errorf("loader diagnostics did not reach the injected logger:\n%s", logs.String())
	}
}

func TestWithDefaults_Merges(t *testing.T) {
	type cfg struct {
		Host string `koanf:"host"`
		Port int    `koanf:"port"`
	}
	first := map[string]any{"host": "a", "port": 1}
	got, err := GetConfig[cfg](t.Context(),
		WithDefaults(first),
		WithDefaults(map[string]any{"port": 2}),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Host != "a" || got.Port != 2 {
		t.Errorf("got %+v, want host from the first call and port from the second", *got)
	}
	if first["port"] != 1 {
		t.Error("WithDefaults modified the caller's map")
	}
}

type ctxRecordingValidator struct{ got context.Context }

func (v *ctxRecordingValidator) Struct(any) error {
	return errors.New("Struct called instead of StructCtx")
}
func (v *ctxRecordingValidator) StructCtx(ctx context.Context, _ any) error {
	v.got = ctx
	return nil
}

type ctxKey struct{}

func TestGetConfig_PassesCtxToContextAwareValidator(t *testing.T) {
	v := &ctxRecordingValidator{}
	ctx := context.WithValue(t.Context(), ctxKey{}, "load")
	if _, err := GetConfig[portCfg](ctx, WithValidator(v)); err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if v.got == nil || v.got.Value(ctxKey{}) != "load" {
		t.Error("StructCtx did not receive the load's ctx")
	}
}

// A value of the wrong type is a validation error, not an uncategorized one.
func TestGetConfig_UndecodableValueIsAValidationError(t *testing.T) {
	path := writeYAML(t, "port: not-a-number\n")
	_, err := GetConfig[portCfg](t.Context(), WithMustConfigFile(path), WithSkipValidation())
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	if !strings.Contains(err.Error(), "port") {
		t.Errorf("err = %q, want the key named", err)
	}
}

type namedCfg struct {
	DB struct {
		MaxConns int `koanf:"max_conns" validate:"min=1"`
	} `koanf:"db"`
	Region string `validate:"required"`
}

// The default validator names a failing field by the key the operator wrote.
func TestGetConfig_ValidationNamesFieldsByKey(t *testing.T) {
	path := writeYAML(t, "db:\n  max_conns: 0\n")
	_, err := GetConfig[namedCfg](t.Context(), WithMustConfigFile(path))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation", err)
	}
	for _, want := range []string{"db.max_conns(min=1)", "region(required)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

func TestFieldName(t *testing.T) {
	type s struct {
		Tagged   int `koanf:"max_conns"`
		Options  int `koanf:"opts,omitempty"`
		Untagged int
		Skipped  int `koanf:"-"`
	}
	typ := reflect.TypeFor[s]()
	for field, want := range map[string]string{
		"Tagged":   "max_conns",
		"Options":  "opts",
		"Untagged": "untagged",
		"Skipped":  "Skipped",
	} {
		f, _ := typ.FieldByName(field)
		if got := FieldName(f); got != want {
			t.Errorf("FieldName(%s) = %q, want %q", field, got, want)
		}
	}
}

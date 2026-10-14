//go:build unit

package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-playground/validator/v10"
	"github.com/knadh/koanf/v2"
)

var errPolicy = errors.New("policy violation")

// wrappingValidator returns go-playground's field errors inside a wrapper that
// adds its own sentinel, as a policy layer over the validator would.
type wrappingValidator struct{ inner *validator.Validate }

func (w wrappingValidator) Struct(v any) error {
	if err := w.inner.Struct(v); err != nil {
		return fmt.Errorf("%w: %w", errPolicy, err)
	}
	return nil
}

// A custom validator's whole error stays in the chain: its own sentinel as
// well as the field errors inside it.
func TestValidation_KeepsTheValidatorsWholeError(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port" validate:"min=1"`
	}
	v := validator.New()
	v.RegisterTagNameFunc(FieldName)
	_, err := GetConfig[cfg](t.Context(), WithValidator(wrappingValidator{v}))
	var fields validator.ValidationErrors
	if !errors.Is(err, ErrValidation) || !errors.Is(err, errPolicy) || !errors.As(err, &fields) {
		t.Fatalf("err = %v: want ErrValidation, the validator's sentinel and its field errors", err)
	}
}

// A squashed struct contributes no key segment, so a validation message
// names the field by the key an operator writes.
func TestValidation_NamesSquashedFieldsByKey(t *testing.T) {
	type shared struct {
		Port int `koanf:"port" validate:"min=1"`
	}
	type cfg struct {
		Shared shared `koanf:",squash"`
		DB     struct {
			Pools []struct {
				Max int `koanf:"max" validate:"min=1"`
			} `koanf:"pools" validate:"dive"`
		} `koanf:"db"`
	}
	_, err := GetConfig[cfg](t.Context(), WithDefaults(map[string]any{"db.pools": []map[string]any{{"max": 0}}}))
	if err == nil {
		t.Fatal("want a validation error")
	}
	for _, want := range []string{"port(min=1)", "db.pools[0].max(min=1)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "shared.port") {
		t.Errorf("error %q invents a key for the squashed struct", err)
	}
}

// WithDefaults copies the map, nested values included, when it is called, so
// a Reloader built from it reads the same defaults on every reload.
func TestWithDefaults_SnapshotsTheCallersMap(t *testing.T) {
	type cfg struct {
		Port int      `koanf:"port"`
		Tags []string `koanf:"tags"`
	}
	tags := []any{"a"}
	defaults := map[string]any{"port": 1, "tags": tags}
	r, err := NewReloader[cfg](t.Context(), nil, WithDefaults(defaults), WithSkipValidation())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	defaults["port"] = 2
	tags[0] = "b"
	got, err := r.Reload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != 1 || len(got.Tags) != 1 || got.Tags[0] != "a" {
		t.Fatalf("reload read %+v: the caller's later changes reached the defaults", got)
	}
}

type failingProvider struct{ err error }

func (p failingProvider) ReadBytes() ([]byte, error)    { return nil, p.err }
func (p failingProvider) Read() (map[string]any, error) { return nil, p.err }

var _ koanf.Provider = failingProvider{}

// A failing source carries its category and its cause like every other stage.
func TestSourceFailureIsCategorized(t *testing.T) {
	cause := errors.New("store unavailable")
	_, err := GetConfig[struct{}](t.Context(), WithProvider(failingProvider{cause}))
	if !errors.Is(err, ErrSource) || !errors.Is(err, cause) {
		t.Fatalf("err = %v: want ErrSource with its cause", err)
	}
}

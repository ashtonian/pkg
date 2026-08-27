//go:build unit

package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// postLoadCfg exercises the interesting ordering: Raw is what the file sets,
// Derived is what only a post-load hook can compute, and Derived carries a
// validate tag so the test can prove the validator runs AFTER the hook.
type postLoadCfg struct {
	Raw     string `koanf:"raw"`
	Derived string `koanf:"derived" validate:"required"`
	Secret  string `koanf:"secret"`
}

func TestWithPostLoad_RunsAndMutates(t *testing.T) {
	path := writeYAML(t, "raw: hello\n")

	cfg, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error {
			c.Derived = strings.ToUpper(c.Raw)
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Derived != "HELLO" {
		t.Errorf("Derived = %q, want %q", cfg.Derived, "HELLO")
	}
}

// The hook must run BEFORE validation, otherwise a field only the hook can
// populate can never satisfy validate:"required". This is the ordering the
// whole option exists for.
func TestWithPostLoad_RunsBeforeValidation(t *testing.T) {
	path := writeYAML(t, "raw: hello\n")

	// Without the hook, Derived is empty and validation must fail.
	if _, err := GetConfig[postLoadCfg](t.Context(), WithMustConfigFile(path)); err == nil {
		t.Fatal("expected validation to fail with Derived unset")
	} else if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got: %v", err)
	}

	// With the hook, Derived is populated by the time the validator sees it.
	if _, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error {
			c.Derived = c.Raw
			return nil
		}),
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Secrets are injected before the hook, so a hook may derive from a resolved
// secret value.
func TestWithPostLoad_RunsAfterSecrets(t *testing.T) {
	path := writeYAML(t, "raw: hello\n")

	cfg, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithSecretPopulator(func(_ context.Context, target any) error {
			target.(*postLoadCfg).Secret = "resolved"
			return nil
		}),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error {
			if c.Secret != "resolved" {
				return fmt.Errorf("hook ran before secrets: Secret = %q", c.Secret)
			}
			c.Derived = c.Secret + "-derived"
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Derived != "resolved-derived" {
		t.Errorf("Derived = %q, want %q", cfg.Derived, "resolved-derived")
	}
}

func TestWithPostLoad_ErrorAbortsLoad(t *testing.T) {
	path := writeYAML(t, "raw: hello\n")
	sentinel := errors.New("derivation blew up")

	_, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, _ *postLoadCfg) error { return sentinel }),
	)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrPostLoad) {
		t.Errorf("expected ErrPostLoad, got: %v", err)
	}
	if !strings.Contains(err.Error(), sentinel.Error()) {
		t.Errorf("error should carry the hook's message, got: %v", err)
	}
}

func TestWithPostLoad_MultipleHooksRunInOrder(t *testing.T) {
	path := writeYAML(t, "raw: a\n")

	cfg, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error { c.Derived = c.Raw + "1"; return nil }),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error { c.Derived += "2"; return nil }),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error { c.Derived += "3"; return nil }),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Derived != "a123" {
		t.Errorf("Derived = %q, want %q (hooks must run in registration order)", cfg.Derived, "a123")
	}
}

func TestWithPostLoad_FirstErrorStopsTheChain(t *testing.T) {
	path := writeYAML(t, "raw: a\n")
	var ran []string

	_, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, _ *postLoadCfg) error { ran = append(ran, "first"); return nil }),
		WithPostLoad(func(_ context.Context, _ *postLoadCfg) error {
			ran = append(ran, "second")
			return errors.New("stop here")
		}),
		WithPostLoad(func(_ context.Context, _ *postLoadCfg) error { ran = append(ran, "third"); return nil }),
	)
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(ran) != 2 || ran[0] != "first" || ran[1] != "second" {
		t.Errorf("ran = %v, want [first second] — the third hook must not run", ran)
	}
}

// A hook typed for one struct handed to a load of another is caller error, but
// it must surface as a described error rather than a panic.
func TestWithPostLoad_TypeMismatchIsAnError(t *testing.T) {
	path := writeYAML(t, "raw: a\n")

	_, err := GetConfig[postLoadCfg](t.Context(),
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, _ *reloaderCfg) error { return nil }),
	)
	if err == nil {
		t.Fatal("expected an error for a mistyped hook")
	}
	if !errors.Is(err, ErrPostLoad) {
		t.Errorf("expected ErrPostLoad, got: %v", err)
	}
}

func TestWithPostLoad_ReceivesCallerContext(t *testing.T) {
	path := writeYAML(t, "raw: a\n")
	type ctxKey struct{}
	ctx := context.WithValue(t.Context(), ctxKey{}, "carried")

	_, err := GetConfig[postLoadCfg](ctx,
		WithMustConfigFile(path),
		WithPostLoad(func(hookCtx context.Context, c *postLoadCfg) error {
			if got := hookCtx.Value(ctxKey{}); got != "carried" {
				return fmt.Errorf("hook ctx value = %v, want %q", got, "carried")
			}
			c.Derived = "ok"
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The reason WithPostLoad exists: under a Reloader, a hook failure must
// preserve the previous config exactly as a validation failure does.
func TestWithPostLoad_ReloaderPreservesConfigOnHookFailure(t *testing.T) {
	path := writeYAML(t, "raw: first\n")
	failing := false

	r, err := NewReloader[postLoadCfg](t.Context(), nil,
		WithMustConfigFile(path),
		WithPostLoad(func(_ context.Context, c *postLoadCfg) error {
			if failing {
				return errors.New("derivation rejected the new file")
			}
			c.Derived = c.Raw
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Derived != "first" {
		t.Fatalf("initial Derived = %q, want %q", r.Config().Derived, "first")
	}

	failing = true
	if _, err := r.Reload(context.Background()); err == nil {
		t.Fatal("expected the reload to fail")
	} else if !errors.Is(err, ErrReload) {
		t.Errorf("expected ErrReload, got: %v", err)
	}

	if got := r.Config().Derived; got != "first" {
		t.Errorf("Derived = %q, want %q — a failed hook must not publish a config", got, "first")
	}
}

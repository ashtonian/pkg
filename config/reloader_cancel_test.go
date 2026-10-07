//go:build unit

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A reload whose context ends — before it starts, or inside a post-load hook
// that then returns nil — publishes nothing, counts as no attempt and fires no
// callback.
func TestReload_CancelledContextPublishesNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	write := func(port string) {
		if err := os.WriteFile(file, []byte("port: "+port+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("9000")
	var cancelInHook context.CancelFunc
	callbacks := 0
	r, err := NewReloader[reloaderCfg](t.Context(),
		[]ReloadOption[reloaderCfg]{WithOnChange(func(_, _ *reloaderCfg) { callbacks++ })},
		WithConfigFile(file), WithSkipValidation(),
		WithPostLoad(func(context.Context, *reloaderCfg) error {
			if cancelInHook != nil {
				cancelInHook()
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop(context.Background())
	write("9001")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Reload(cancelled); !errors.Is(err, context.Canceled) || errors.Is(err, ErrReload) {
		t.Fatalf("Reload with a cancelled context = %v, want context.Canceled and not ErrReload", err)
	}

	hookCtx, cancel := context.WithCancel(context.Background())
	cancelInHook = cancel
	if _, err := r.Reload(hookCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Reload cancelled inside the post-load hook = %v, want context.Canceled", err)
	}
	cancelInHook = nil

	if got := r.Config().Port; got != 9000 {
		t.Errorf("an abandoned reload published port %d", got)
	}
	if s := r.Stats(); s.Attempts != 0 || callbacks != 0 {
		t.Errorf("abandoned reloads counted %d attempts and fired %d callbacks", s.Attempts, callbacks)
	}
}

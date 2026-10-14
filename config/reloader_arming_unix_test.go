//go:build unix

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A config directory the process may open files in but not list — mode 0311 —
// loads but cannot be watched. Arm reports it before Start serves anything;
// Start without Arm reports it too, instead of serving the other triggers.
func TestArm_UnwatchableDirectoryFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can watch any directory")
	}
	dir := filepath.Join(t.TempDir(), "conf")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte("host: loaded\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o311); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // runs before TempDir's removal

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithFile(file), WithSkipValidation()},
		WithFileWatch(0),
		WithSignalReload(syscall.SIGWINCH),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	if got := r.Config().Host; got != "loaded" {
		t.Fatalf("Host = %q, want the file's value", got)
	}

	err = r.Arm(t.Context())
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), dir) {
		t.Fatalf("Arm = %v, want a permission error naming %s", err, dir)
	}
	if err := r.Run(t.Context()); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Start = %v, want the same permission error", err)
	}
	if got := r.Config().Host; got != "loaded" {
		t.Fatalf("Host = %q after the failed arm, want the loaded config", got)
	}
}

// Triggers that cannot fail to arm report armed without a file watch. A signal
// received after Arm and before Start is served once Start runs, instead of
// taking its default action. SIGWINCH's default is to ignore, so a reloader
// that registered late fails here by timeout rather than by killing the test.
func TestArm_SignalAndPollTriggersArmWithoutAFileWatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []ReloadOption
	}{
		{"signal and poll", []ReloadOption{
			WithSignalReload(syscall.SIGWINCH),
			WithPollInterval(time.Hour),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()}, tc.opts...)
			if err != nil {
				t.Fatalf("NewReloader: %v", err)
			}
			armReloader(t, r)
			if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
				t.Fatal(err)
			}
			runReloader(t, r)
			eventually(t, func() bool { return r.Stats().LastTrigger == TriggerSignal },
				"the signal received before Start to reload")
		})
	}
}

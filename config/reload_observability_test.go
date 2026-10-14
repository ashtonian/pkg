//go:build unit

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// --- ReloadStats ---

func TestReloadStats_ZeroBeforeAnyReload(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The initial load NewReloader performs is not a reload.
	s := r.Stats()
	if s.Attempts != 0 || s.Successes != 0 || s.Failures != 0 {
		t.Errorf("stats = %+v, want all counters zero before the first reload", s)
	}
	if !s.LastSuccess.IsZero() || !s.LastFailure.IsZero() {
		t.Errorf("timestamps = %v / %v, want zero", s.LastSuccess, s.LastFailure)
	}
	if s.LastError != nil {
		t.Errorf("LastError = %v, want nil", s.LastError)
	}
	if s.LastTrigger != "" {
		t.Errorf("LastTrigger = %q, want empty", s.LastTrigger)
	}
}

func TestReloadStats_CountsSuccess(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	before := time.Now()
	if _, err := r.Reload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	s := r.Stats()
	if s.Attempts != 1 || s.Successes != 1 || s.Failures != 0 {
		t.Errorf("stats = %+v, want 1 attempt / 1 success / 0 failures", s)
	}
	if s.LastTrigger != TriggerManual {
		t.Errorf("LastTrigger = %q, want %q", s.LastTrigger, TriggerManual)
	}
	if s.LastError != nil {
		t.Errorf("LastError = %v, want nil after a success", s.LastError)
	}
	if s.LastSuccess.Before(before) {
		t.Errorf("LastSuccess = %v, want >= %v", s.LastSuccess, before)
	}
	if !s.LastFailure.IsZero() {
		t.Errorf("LastFailure = %v, want zero", s.LastFailure)
	}
}

func TestReloadStats_CountsFailureAndKeepsLastSuccess(t *testing.T) {
	path := writeYAML(t, "name: valid\nport: 9090\n")

	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := r.Reload(context.Background()); err != nil {
		t.Fatalf("unexpected error on the good reload: %v", err)
	}
	goodAt := r.Stats().LastSuccess

	// Break the file: name is validate:"required".
	if err := os.WriteFile(path, []byte("port: 1111\n"), 0o644); err != nil {
		t.Fatalf("writing invalid yaml: %v", err)
	}
	if _, err := r.Reload(context.Background()); err == nil {
		t.Fatal("expected the reload to fail")
	}

	s := r.Stats()
	if s.Attempts != 2 || s.Successes != 1 || s.Failures != 1 {
		t.Errorf("stats = %+v, want 2 attempts / 1 success / 1 failure", s)
	}
	if s.LastError == nil || s.LastError.Kind != KindValidation {
		t.Errorf("LastError = %v, want a validation problem", s.LastError)
	}
	if !s.LastSuccess.Equal(goodAt) {
		t.Errorf("LastSuccess = %v, want it preserved at %v", s.LastSuccess, goodAt)
	}
	if s.LastFailure.IsZero() {
		t.Error("LastFailure should be set after a failure")
	}
}

func TestReloadStats_SuccessClearsLastError(t *testing.T) {
	path := writeYAML(t, "name: ok\n")

	r, err := NewReloader[validatedCfg](t.Context(), []Option{WithRequiredFile(path)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Break it: name is validate:"required".
	if err := os.WriteFile(path, []byte("port: 1\n"), 0o644); err != nil {
		t.Fatalf("writing invalid yaml: %v", err)
	}
	if _, err := r.Reload(context.Background()); err == nil {
		t.Fatal("expected a failure")
	}
	if r.Stats().LastError == nil {
		t.Fatal("LastError should be set after a failure")
	}

	// Fix it: the next success must clear the stale error.
	if err := os.WriteFile(path, []byte("name: back\n"), 0o644); err != nil {
		t.Fatalf("restoring yaml: %v", err)
	}
	if _, err := r.Reload(context.Background()); err != nil {
		t.Fatalf("unexpected error on recovery: %v", err)
	}

	s := r.Stats()
	if s.LastError != nil {
		t.Errorf("LastError = %v, want nil once a reload succeeds again", s.LastError)
	}
	if s.Attempts != 2 || s.Successes != 1 || s.Failures != 1 {
		t.Errorf("stats = %+v, want 2 attempts / 1 success / 1 failure", s)
	}
	if s.LastFailure.IsZero() {
		t.Error("LastFailure should survive a later success — it records when the failure happened")
	}
}

// --- WithOnReloadError ---

func TestWithOnReloadError_FiresOnFailure(t *testing.T) {
	path := writeYAML(t, "name: valid\n")

	type call struct {
		trigger string
		err     error
	}
	var mu sync.Mutex
	var calls []call

	r, err := NewReloader[validatedCfg](
		t.Context(),
		[]Option{WithRequiredFile(path)},
		WithOnReloadError(func(trigger string, err error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, call{trigger, err})
		}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A success must not fire it.
	if _, err := r.Reload(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	n := len(calls)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("callback fired %d times on a successful reload, want 0", n)
	}

	if err := os.WriteFile(path, []byte("port: 1\n"), 0o644); err != nil {
		t.Fatalf("writing invalid yaml: %v", err)
	}
	if _, err := r.Reload(context.Background()); err == nil {
		t.Fatal("expected a failure")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("callback fired %d times, want 1", len(calls))
	}
	if calls[0].trigger != TriggerManual {
		t.Errorf("trigger = %q, want %q", calls[0].trigger, TriggerManual)
	}
	if !errors.Is(calls[0].err, ErrReload) {
		t.Errorf("err = %v, want an ErrReload", calls[0].err)
	}
}

func TestWithOnReloadError_PanicRecovery(t *testing.T) {
	path := writeYAML(t, "name: valid\n")

	fired := make(chan struct{}, 2)
	r, err := NewReloader[validatedCfg](
		t.Context(),
		[]Option{WithRequiredFile(path)},
		WithOnReloadError(func(string, error) { panic("boom") }),
		WithOnReloadError(func(string, error) { fired <- struct{}{} }),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := os.WriteFile(path, []byte("port: 1\n"), 0o644); err != nil {
		t.Fatalf("writing invalid yaml: %v", err)
	}
	if _, err := r.Reload(context.Background()); err == nil {
		t.Fatal("expected a failure")
	}

	// A panicking callback must not prevent the next one from running.
	select {
	case <-fired:
	default:
		t.Error("the second callback did not run after the first panicked")
	}
}

// --- ErrReloadInProgress ---

func TestReload_InProgressIsDistinctFromFailure(t *testing.T) {
	if errors.Is(ErrReloadInProgress, ErrReload) {
		t.Fatal("ErrReloadInProgress must NOT satisfy errors.Is(_, ErrReload) — " +
			"conflating a benign single-flight skip with a real failure is the bug this split fixes")
	}
	if errors.Is(ErrReload, ErrReloadInProgress) {
		t.Error("ErrReload must not satisfy errors.Is(_, ErrReloadInProgress)")
	}
}

func TestReload_SingleFlightSkipReturnsInProgress(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithSkipValidation(), WithPostLoad(func(_ context.Context, _ *reloaderCfg) error {
			select {
			case entered <- struct{}{}:
				<-release // hold the single-flight slot until the test says go
			default:
			}
			return nil
		})},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Reload(context.Background())
	}()

	<-entered // the first reload now holds the slot

	_, err = r.Reload(context.Background())
	if !errors.Is(err, ErrReloadInProgress) {
		t.Errorf("concurrent Reload err = %v, want ErrReloadInProgress", err)
	}
	if errors.Is(err, ErrReload) {
		t.Error("a single-flight skip must not read as ErrReload")
	}

	close(release)
	<-done

	// The skip is not an attempt: only the reload that ran is counted.
	if s := r.Stats(); s.Attempts != 1 || s.Failures != 0 {
		t.Errorf("stats = %+v, want 1 attempt / 0 failures — a skip runs no pipeline", s)
	}
}

// --- Symlink-aware file watch (Kubernetes ConfigMap shape) ---

// writeConfigMapMount builds the exact layout kubelet materializes for a
// ConfigMap volume and returns the mount dir plus the user-visible key path:
//
//	mount/..2026_01_01_00_00_00/config.yaml   the real file
//	mount/..data -> ..2026_01_01_00_00_00     the version pointer
//	mount/config.yaml -> ..data/config.yaml   what the app opens
func writeConfigMapMount(t *testing.T, mount, version, content string) {
	t.Helper()
	verDir := filepath.Join(mount, version)
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", verDir, err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing versioned config: %v", err)
	}

	// ..data -> <version>, published atomically via rename, exactly as
	// kubelet's atomic writer does on update.
	dataTmp := filepath.Join(mount, "..data_tmp")
	_ = os.Remove(dataTmp)
	if err := os.Symlink(version, dataTmp); err != nil {
		t.Fatalf("symlink ..data_tmp: %v", err)
	}
	if err := os.Rename(dataTmp, filepath.Join(mount, "..data")); err != nil {
		t.Fatalf("rename ..data_tmp -> ..data: %v", err)
	}

	// The user-visible key is created once and never rewritten on update.
	key := filepath.Join(mount, "config.yaml")
	if _, err := os.Lstat(key); os.IsNotExist(err) {
		if err := os.Symlink(filepath.Join("..data", "config.yaml"), key); err != nil {
			t.Fatalf("symlink config.yaml: %v", err)
		}
	}
}

// A ConfigMap update never writes the watched path — it renames `..data`
// underneath it. Matching events by the watched file's own name therefore
// never fires, which is the bug this covers.
func TestReloader_FileWatch_DetectsConfigMapSymlinkSwap(t *testing.T) {
	mount := t.TempDir()
	writeConfigMapMount(t, mount, "..2026_01_01_00_00_00", "host: original\nport: 8080\n")
	cfgFile := filepath.Join(mount, "config.yaml")

	changed := make(chan *reloaderCfg, 1)
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithRequiredFile(cfgFile), WithSkipValidation()},
		WithFileWatch(50*time.Millisecond),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) { neu := ev.New; changed <- neu }),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Host != "original" {
		t.Fatalf("initial Host = %q, want %q", r.Config().Host, "original")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armReloader(t, r)
	go func() { _ = r.Run(ctx) }()
	defer func() { _ = r.Stop(context.Background()) }()

	// Publish a new version the way kubelet does.
	writeConfigMapMount(t, mount, "..2026_01_01_00_00_01", "host: rotated\nport: 9090\n")

	select {
	case cfg := <-changed:
		if cfg.Host != "rotated" {
			t.Errorf("Host = %q, want %q", cfg.Host, "rotated")
		}
		if cfg.Port != 9090 {
			t.Errorf("Port = %d, want %d", cfg.Port, 9090)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: a ConfigMap-style symlink swap did not trigger a reload")
	}
}

// The plain non-Kubernetes form of the same thing: a release directory
// re-pointed by an atomic `symlink tmp && mv -Tf tmp link`.
//
// Linux only, and the reason is a real platform limitation rather than test
// convenience. fsnotify's kqueue backend (macOS, *BSD) detects directory
// changes by diffing directory listings, so it can only report a change that
// alters the set of names present. This idiom's only trace is `link.tmp`,
// which is renamed away microseconds later — if the diff runs after the
// rename, the name set is identical to before and NO event is generated at
// all. Nothing downstream can react to an event that does not exist. On Linux
// inotify reports the rename itself (IN_MOVED_TO) and detection is reliable.
//
// The Kubernetes case above is immune on every platform because kubelet always
// leaves a durable new `..<timestamp>` directory behind, which the diff sees.
// For the atomic-relink idiom on kqueue platforms, WithPollInterval is the
// supported answer — as WithFileWatch's doc comment says.
func TestReloader_FileWatch_DetectsReleaseDirRelink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("kqueue (%s) diffs directory listings and cannot see a rename that "+
			"leaves the name set unchanged; use WithPollInterval there", runtime.GOOS)
	}
	root := t.TempDir()
	mkRelease := func(name, content string) string {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	v1 := mkRelease("v1", "host: v1\nport: 1\n")
	v2 := mkRelease("v2", "host: v2\nport: 2\n")

	link := filepath.Join(root, "current.yaml")
	if err := os.Symlink(v1, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	changed := make(chan *reloaderCfg, 1)
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithRequiredFile(link), WithSkipValidation()},
		WithFileWatch(50*time.Millisecond),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) { neu := ev.New; changed <- neu }),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armReloader(t, r)
	go func() { _ = r.Run(ctx) }()
	defer func() { _ = r.Stop(context.Background()) }()

	tmp := filepath.Join(root, "current.yaml.tmp")
	if err := os.Symlink(v2, tmp); err != nil {
		t.Fatalf("symlink tmp: %v", err)
	}
	if err := os.Rename(tmp, link); err != nil {
		t.Fatalf("rename: %v", err)
	}

	select {
	case cfg := <-changed:
		if cfg.Host != "v2" {
			t.Errorf("Host = %q, want %q", cfg.Host, "v2")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: a release-dir relink did not trigger a reload")
	}
}

// armReloader arms r's triggers, so a test that changes the files after it
// returns cannot race ahead of the watch.
func armReloader[T any](t *testing.T, r *Reloader[T]) {
	t.Helper()
	if err := r.Arm(t.Context()); err != nil {
		t.Fatalf("Arm: %v", err)
	}
}

//go:build unit

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- Test config struct for reloader tests ---

type reloaderCfg struct {
	Host string `koanf:"host" default:"localhost"`
	Port int    `koanf:"port" default:"8080"`
}

type validatedCfg struct {
	Name string `koanf:"name" validate:"required"`
	Port int    `koanf:"port" default:"8080"`
}

// --- NewReloader Tests ---

func TestNewReloader_InitialLoad(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg := r.Config()
	if cfg == nil {
		t.Fatal("Config() returned nil")
	}
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost")
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want %d", cfg.Port, 8080)
	}
}

func TestNewReloader_InitialLoadError(t *testing.T) {
	_, err := NewReloader[validatedCfg](t.Context(), nil)
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
}

func TestNewReloader_WithEnvVars(t *testing.T) {
	t.Setenv("RELOAD_HOST", "envhost")

	r, err := NewReloader[reloaderCfg](t.Context(), nil,
		WithEnvPrefix("RELOAD"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Host != "envhost" {
		t.Errorf("Host = %q, want %q", r.Config().Host, "envhost")
	}
}

// --- Config() Tests ---

func TestReloader_Config_ReturnsCurrentConfig(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	cfg1 := r.Config()
	cfg2 := r.Config()

	// Same pointer until reload.
	if cfg1 != cfg2 {
		t.Error("Config() should return the same pointer between reloads")
	}
}

// --- Reload Tests ---

func TestReloader_ManualReload(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	before := r.Config()
	after, err := r.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	// New pointer after reload (different allocation).
	if before == after {
		t.Error("Reload should return a new config pointer")
	}
	if after.Host != "localhost" {
		t.Errorf("Host = %q, want %q", after.Host, "localhost")
	}
}

func TestReloader_ManualReload_PicksUpEnvChanges(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil,
		WithEnvPrefix("RELENV"),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Host != "localhost" {
		t.Errorf("initial Host = %q, want %q", r.Config().Host, "localhost")
	}

	// Change env var and reload.
	t.Setenv("RELENV_HOST", "changed")
	newCfg, err := r.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	if newCfg.Host != "changed" {
		t.Errorf("reloaded Host = %q, want %q", newCfg.Host, "changed")
	}
	if r.Config().Host != "changed" {
		t.Errorf("Config().Host = %q, want %q", r.Config().Host, "changed")
	}
}

func TestReloader_ValidateBeforeSwap(t *testing.T) {
	// Start with a valid file.
	path := writeYAML(t, "name: valid\nport: 9090\n")

	r, err := NewReloader[validatedCfg](t.Context(), nil,
		WithMustConfigFile(path),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Name != "valid" {
		t.Fatalf("initial Name = %q, want %q", r.Config().Name, "valid")
	}

	// Write invalid config (missing required name field).
	if err := os.WriteFile(path, []byte("port: 1111\n"), 0o644); err != nil {
		t.Fatalf("writing invalid yaml: %v", err)
	}

	_, err = r.Reload(context.Background())
	if err == nil {
		t.Fatal("expected reload error for invalid config")
	}
	if !errors.Is(err, ErrReload) {
		t.Errorf("expected ErrReload, got: %v", err)
	}

	// Old config should be preserved.
	if r.Config().Name != "valid" {
		t.Errorf("Config().Name = %q, want %q (should preserve old config)", r.Config().Name, "valid")
	}
	if r.Config().Port != 9090 {
		t.Errorf("Config().Port = %d, want %d (should preserve old config)", r.Config().Port, 9090)
	}
}

// --- OnChange Callback Tests ---

func TestReloader_OnChange(t *testing.T) {
	var called int
	var lastOld, lastNew *reloaderCfg

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithOnChange(func(old, new *reloaderCfg) {
				called++
				lastOld = old
				lastNew = new
			}),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := r.Reload(context.Background()); err != nil {
		t.Fatalf("Reload failed: %v", err)
	}

	if called != 1 {
		t.Errorf("callback called %d times, want 1", called)
	}
	if lastOld == nil || lastNew == nil {
		t.Fatal("callback should receive old and new config")
	}
	if lastOld.Host != "localhost" || lastNew.Host != "localhost" {
		t.Errorf("old.Host=%q new.Host=%q, want localhost", lastOld.Host, lastNew.Host)
	}
}

func TestReloader_MultipleOnChange(t *testing.T) {
	var calls []string

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithOnChange(func(old, new *reloaderCfg) {
				calls = append(calls, "first")
			}),
			WithOnChange(func(old, new *reloaderCfg) {
				calls = append(calls, "second")
			}),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r.Reload(context.Background())

	if len(calls) != 2 {
		t.Fatalf("expected 2 callbacks, got %d", len(calls))
	}
	if calls[0] != "first" || calls[1] != "second" {
		t.Errorf("callbacks called in wrong order: %v", calls)
	}
}

func TestReloader_OnChange_PanicRecovery(t *testing.T) {
	var secondCalled bool

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithOnChange(func(old, new *reloaderCfg) {
				panic("intentional panic")
			}),
			WithOnChange(func(old, new *reloaderCfg) {
				secondCalled = true
			}),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not panic.
	_, err = r.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload should not fail due to callback panic: %v", err)
	}

	if !secondCalled {
		t.Error("second callback should still be called after first panics")
	}
}

func TestReloader_OnChange_NotCalledOnFailure(t *testing.T) {
	path := writeYAML(t, "name: valid\n")
	var called int

	r, err := NewReloader[validatedCfg](
		t.Context(),
		[]ReloadOption[validatedCfg]{
			WithOnChange(func(old, new *validatedCfg) {
				called++
			}),
		},
		WithMustConfigFile(path),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Write invalid config.
	os.WriteFile(path, []byte("port: 1111\n"), 0o644)

	_, err = r.Reload(context.Background())
	if err == nil {
		t.Fatal("expected reload error")
	}
	if called != 0 {
		t.Errorf("callback should not be called on reload failure, but was called %d times", called)
	}
}

// --- File Watch Tests ---

func TestReloader_FileWatch(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgFile, []byte("host: original\nport: 8080\n"), 0o644)

	changed := make(chan *reloaderCfg, 1)
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithFileWatch[reloaderCfg](50 * time.Millisecond),
			WithOnChange(func(old, new *reloaderCfg) {
				changed <- new
			}),
		},
		WithConfigFile(cfgFile),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Host != "original" {
		t.Fatalf("initial Host = %q, want %q", r.Config().Host, "original")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- r.Start(ctx) }()

	// Give watcher time to initialize.
	time.Sleep(200 * time.Millisecond)

	// Modify the file.
	os.WriteFile(cfgFile, []byte("host: updated\nport: 9090\n"), 0o644)

	select {
	case cfg := <-changed:
		if cfg.Host != "updated" {
			t.Errorf("Host = %q, want %q", cfg.Host, "updated")
		}
		if cfg.Port != 9090 {
			t.Errorf("Port = %d, want %d", cfg.Port, 9090)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for config reload")
	}

	if err := r.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// --- Poll Interval Tests ---

func TestReloader_PollInterval(t *testing.T) {
	var reloadCount atomic.Int32

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithPollInterval[reloaderCfg](50 * time.Millisecond),
			WithOnChange(func(old, new *reloaderCfg) {
				reloadCount.Add(1)
			}),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go r.Start(ctx)

	time.Sleep(300 * time.Millisecond)
	cancel()

	// Should have reloaded at least 3 times in 300ms with 50ms interval.
	count := reloadCount.Load()
	if count < 3 {
		t.Errorf("reload count = %d, want >= 3", count)
	}
}

// --- Start / Stop Tests ---

func TestReloader_Stop(t *testing.T) {
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithPollInterval[reloaderCfg](50 * time.Millisecond),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()

	time.Sleep(100 * time.Millisecond)
	if err := r.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start should return nil on Stop, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
}

func TestReloader_Stop_Idempotent(t *testing.T) {
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithPollInterval[reloaderCfg](50 * time.Millisecond),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	go r.Start(context.Background())
	time.Sleep(50 * time.Millisecond)

	// Multiple stops should not panic.
	if err := r.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestReloader_Start_NoWatchReturnsImmediately(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start with no watch should return nil, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start should return immediately when no watch is configured")
	}
}

func TestReloader_Start_ContextCancel(t *testing.T) {
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithPollInterval[reloaderCfg](50 * time.Millisecond),
		},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Start should return context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after context cancellation")
	}
}

// --- Concurrent Reload Tests ---

func TestReloader_ConcurrentReloads(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), nil, WithSkipValidation())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				_, _ = r.Reload(context.Background())
				_ = r.Config()
			}
		}()
	}
	wg.Wait()

	// If we got here without a race or panic, the test passes.
	cfg := r.Config()
	if cfg.Host != "localhost" {
		t.Errorf("Host = %q, want %q", cfg.Host, "localhost")
	}
}

// --- filePaths Helper Tests ---

func TestFilePaths_Empty(t *testing.T) {
	paths := filePaths(nil)
	if len(paths) != 0 {
		t.Errorf("expected empty paths, got %v", paths)
	}
}

func TestFilePaths_ExtractsAllPaths(t *testing.T) {
	opts := []Option{
		WithConfigFile("/a.yaml"),
		WithMustConfigFile("/b.yaml"),
		WithConfigFiles("/c.yaml", "/d.yaml"),
	}
	paths := filePaths(opts)
	if len(paths) != 4 {
		t.Fatalf("expected 4 paths, got %d: %v", len(paths), paths)
	}
	expected := []string{"/a.yaml", "/b.yaml", "/c.yaml", "/d.yaml"}
	for i, want := range expected {
		if paths[i] != want {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want)
		}
	}
}

// --- Error Sentinel Tests ---

func TestErrReload_IsSentinel(t *testing.T) {
	if ErrReload == nil {
		t.Fatal("ErrReload should not be nil")
	}
	if ErrReload.Error() == "" {
		t.Error("ErrReload should have a message")
	}

	// Wrap and check errors.Is.
	wrapped := fmt.Errorf("%w: something failed", ErrReload)
	if !errors.Is(wrapped, ErrReload) {
		t.Error("wrapped error should match ErrReload via errors.Is")
	}
}

package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
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
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
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

	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithEnvPrefix("RELOAD"), WithSkipValidation()},
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
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
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
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	before := r.Config()
	res, err := r.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	after := res.Snapshot.Config

	// New pointer after reload (different allocation).
	if before == after {
		t.Error("Reload should return a new config pointer")
	}
	if after.Host != "localhost" {
		t.Errorf("Host = %q, want %q", after.Host, "localhost")
	}
}

func TestReloader_ManualReload_PicksUpEnvChanges(t *testing.T) {
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithEnvPrefix("RELENV"), WithSkipValidation()},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Config().Host != "localhost" {
		t.Errorf("initial Host = %q, want %q", r.Config().Host, "localhost")
	}

	// Change env var and reload.
	t.Setenv("RELENV_HOST", "changed")
	res, err := r.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload failed: %v", err)
	}
	newCfg := res.Snapshot.Config
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

	r, err := NewReloader[validatedCfg](
		t.Context(),
		[]Option{WithRequiredFile(path)},
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
		[]Option{WithSkipValidation()},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) {
			old, new := ev.Old, ev.New
			called++
			lastOld = old
			lastNew = new
		}),
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
		[]Option{WithSkipValidation()},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) {
			calls = append(calls, "first")
		}),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) {
			calls = append(calls, "second")
		}),
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
		[]Option{WithSkipValidation()},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) {
			panic("intentional panic")
		}),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) {
			secondCalled = true
		}),
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
		[]Option{WithRequiredFile(path)},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[validatedCfg]) {
			called++
		}),
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
	if err := os.WriteFile(cfgFile, []byte("host: original\nport: 8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	clk := newFakeClock()
	onReload, reloads := reloadEvents[reloaderCfg]()
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithFile(cfgFile), WithSkipValidation()},
		WithFileWatch(50*time.Millisecond),
		withClock(clk),
		onReload,
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	if r.Config().Host != "original" {
		t.Fatalf("initial Host = %q, want %q", r.Config().Host, "original")
	}
	armReloader(t, r)
	runReloader(t, r)

	if err := os.WriteFile(cfgFile, []byte("host: updated\nport: 9090\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ev := receive(t, clk, reloads, "the file-change reload")
	if ev.Trigger != TriggerFileChange || ev.New.Host != "updated" || ev.New.Port != 9090 {
		t.Errorf("reload %s published %+v, want the file change's host updated and port 9090", ev.Trigger, *ev.New)
	}
}

// --- Poll Interval Tests ---

// Each poll reloads exactly one interval after the last.
func TestReloader_PollInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 50 * time.Millisecond
		onReload, reloads := reloadEvents[reloaderCfg]()
		r, err := NewReloader[reloaderCfg](
			t.Context(),
			[]Option{WithSkipValidation()},
			WithPollInterval(interval),
			onReload,
		)
		if err != nil {
			t.Fatalf("NewReloader: %v", err)
		}
		start := time.Now()
		runReloader(t, r)
		for i := 1; i <= 3; i++ {
			ev := <-reloads
			if ev.Trigger != TriggerPoll {
				t.Fatalf("reload %d triggered by %s, want %s", i, ev.Trigger, TriggerPoll)
			}
			if got := time.Since(start); got != time.Duration(i)*interval {
				t.Errorf("poll %d ran %v after Run started, want %v", i, got, time.Duration(i)*interval)
			}
		}
	})
}

// --- Run / Stop Tests ---

func TestReloader_Stop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, err := NewReloader[reloaderCfg](
			t.Context(),
			[]Option{WithSkipValidation()},
			WithPollInterval(50*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("NewReloader: %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- r.Run(context.Background()) }()
		synctest.Wait()
		if err := r.Stop(context.Background()); err != nil {
			t.Errorf("Stop: %v", err)
		}
		if err := <-done; err != nil {
			t.Errorf("Run should return nil on Stop, got: %v", err)
		}
	})
}

func TestReloader_Stop_Idempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, err := NewReloader[reloaderCfg](
			t.Context(),
			[]Option{WithSkipValidation()},
			WithPollInterval(50*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("NewReloader: %v", err)
		}

		done := make(chan error, 1)
		go func() { done <- r.Run(context.Background()) }()
		synctest.Wait()
		for range 3 {
			if err := r.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		}
		if err := <-done; err != nil {
			t.Errorf("Run = %v, want nil after Stop", err)
		}
	})
}

func TestReloader_Start_ContextCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, err := NewReloader[reloaderCfg](
			t.Context(),
			[]Option{WithSkipValidation()},
			WithPollInterval(50*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("NewReloader: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.Run(ctx) }()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Run should return context.Canceled, got: %v", err)
		}
	})
}

// A Run called while another runs is refused and arms nothing: one poll
// interval reloads once, where a second loop's own ticker would reload again.
// After the first Run returns, another may start.
func TestReloader_SecondRunIsRefused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reloads := make(chan ReloadEvent[reloaderCfg], 4)
		r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()},
			WithPollInterval(time.Minute),
			WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) { reloads <- ev }))
		if err != nil {
			t.Fatalf("NewReloader: %v", err)
		}
		t.Cleanup(func() {
			if err := r.Stop(context.Background()); err != nil {
				t.Errorf("Stop: %v", err)
			}
		})
		ctx, cancel := context.WithCancel(context.Background())
		first := make(chan error, 1)
		go func() { first <- r.Run(ctx) }()
		synctest.Wait()

		second := make(chan error, 1)
		go func() { second <- r.Run(context.Background()) }()
		synctest.Wait()
		select {
		case err := <-second:
			if !errors.Is(err, ErrReloaderRunning) {
				t.Fatalf("second Run = %v, want ErrReloaderRunning", err)
			}
		default:
			t.Fatal("a second Run serves alongside the first")
		}
		if errors.Is(ErrReloaderRunning, ErrReload) {
			t.Error("ErrReloaderRunning must not read as a reload failure")
		}
		<-reloads
		synctest.Wait()
		if s := r.Stats(); s.Attempts != 1 {
			t.Fatalf("one poll interval reloaded %d times, want 1", s.Attempts)
		}

		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("first Run = %v, want context.Canceled", err)
		}
		next := make(chan error, 1)
		go func() { next <- r.Run(context.Background()) }()
		if ev := <-reloads; ev.Trigger != TriggerPoll {
			t.Fatalf("the next Run's reload was triggered by %s, want %s", ev.Trigger, TriggerPoll)
		}
		if err := r.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if err := <-next; err != nil {
			t.Fatalf("next Run = %v, want nil after Stop", err)
		}
	})
}

// --- Concurrent Reload Tests ---

func TestReloader_ConcurrentReloads(t *testing.T) {
	r, err := NewReloader[reloaderCfg](t.Context(), []Option{WithSkipValidation()})
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
	paths := buildOptions(nil).filePaths()
	if len(paths) != 0 {
		t.Errorf("expected empty paths, got %v", paths)
	}
}

func TestFilePaths_ExtractsAllPaths(t *testing.T) {
	opts := []Option{
		WithFile("/a.yaml"),
		WithRequiredFile("/b.yaml"),
		WithFiles("/c.yaml", "/d.yaml"),
	}
	paths := buildOptions(opts).filePaths()
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

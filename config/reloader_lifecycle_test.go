//go:build unit

package config

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// logRecorder is a slog.Handler that keeps every message so a test can wait
// for the reloader to reach a state it only reports through its log.
type logRecorder struct {
	mu       sync.Mutex
	messages []string
	changed  chan struct{}
}

func newLogRecorder() *logRecorder { return &logRecorder{changed: make(chan struct{}, 1)} }

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logRecorder) WithGroup(string) slog.Handler            { return l }

func (l *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	var b strings.Builder
	b.WriteString(rec.Message)
	rec.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.String())
		return true
	})
	l.mu.Lock()
	l.messages = append(l.messages, b.String())
	l.mu.Unlock()
	select {
	case l.changed <- struct{}{}:
	default:
	}
	return nil
}

func (l *logRecorder) has(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.messages {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func (l *logRecorder) waitFor(t *testing.T, substr string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for !l.has(substr) {
		select {
		case <-l.changed:
		case <-deadline:
			t.Fatalf("timeout waiting for a log line containing %q", substr)
		}
	}
}

func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startReloader runs r.Start in the background and stops it at cleanup.
func startReloader[T any](t *testing.T, r *Reloader[T]) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return done
}

// A manual reload reads port 9000 and is held; the file then
// changes to 9001. The watcher's reload must wait for the manual one and then
// apply 9001, not skip and leave 9000 live.
func TestReloader_FileChangeDuringManualReloadIsApplied(t *testing.T) {
	path := writeYAML(t, "port: 9000\n")

	var holdNext atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	logs := newLogRecorder()

	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithFileWatch[portCfg](50 * time.Millisecond)},
		WithConfigFile(path),
		WithSkipValidation(),
		WithLogger(slog.New(logs)),
		WithPostLoad(func(context.Context, *portCfg) error {
			if holdNext.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	startReloader(t, r)
	waitForWatcher(t, r)

	holdNext.Store(true)
	manual := make(chan error, 1)
	go func() {
		_, err := r.Reload(context.Background())
		manual <- err
	}()
	<-entered // the manual reload has read port 9000 and holds the slot

	if err := os.WriteFile(path, []byte("port: 9001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logs.waitFor(t, "config reload waiting for the reload in flight trigger=file_change")

	close(release)
	if err := <-manual; err != nil {
		t.Fatalf("manual reload: %v", err)
	}
	eventually(t, func() bool { return r.Config().Port == 9001 }, "the file change to be applied")
	if s := r.Stats(); s.Successes < 2 || s.LastTrigger != TriggerFileChange {
		t.Errorf("stats = %+v, want the manual reload then the file change", s)
	}
}

// A pending debounce timer dies with Stop; nothing reloads after.
func TestReloader_StopDuringDebounceDoesNotReload(t *testing.T) {
	path := writeYAML(t, "port: 9000\n")
	var changes atomic.Int32
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{
			WithFileWatch[portCfg](300 * time.Millisecond),
			WithOnChange(func(_, _ *portCfg) { changes.Add(1) }),
		},
		WithConfigFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()
	waitForWatcher(t, r)

	if err := os.WriteFile(path, []byte("port: 9001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the event is in; the debounce is armed

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(600 * time.Millisecond) // well past the debounce

	if got := r.Config().Port; got != 9000 {
		t.Errorf("Port = %d, want 9000: a reload ran after Stop", got)
	}
	if n := changes.Load(); n != 0 {
		t.Errorf("OnChange fired %d times after Stop", n)
	}
	if s := r.Stats(); s.Attempts != 0 {
		t.Errorf("stats = %+v, want no attempts", s)
	}
}

// Stop must not return while a reload that could still fire callbacks is
// running, and nothing may reload after it returns.
func TestReloader_StopWaitsForManualReload(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var hold atomic.Bool
	var changes atomic.Int32

	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithOnChange(func(_, _ *portCfg) { changes.Add(1) })},
		WithSkipValidation(),
		WithPostLoad(func(context.Context, *portCfg) error {
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}

	hold.Store(true)
	manual := make(chan error, 1)
	go func() {
		_, err := r.Reload(context.Background())
		manual <- err
	}()
	<-entered

	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := r.Stop(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop with a reload in flight = %v, want context.DeadlineExceeded", err)
	}
	if n := changes.Load(); n != 0 {
		t.Fatalf("OnChange fired before the held reload was released")
	}

	close(release)
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-manual; err != nil {
		t.Fatalf("the in-flight reload should complete: %v", err)
	}
	if n := changes.Load(); n != 1 {
		t.Fatalf("OnChange fired %d times, want exactly the in-flight reload's 1", n)
	}

	if _, err := r.Reload(context.Background()); !errors.Is(err, ErrReloaderStopped) {
		t.Errorf("Reload after Stop = %v, want ErrReloaderStopped", err)
	}
	if errors.Is(ErrReloaderStopped, ErrReload) {
		t.Error("ErrReloaderStopped must not read as a reload failure")
	}
	rec := httptest.NewRecorder()
	r.ReloadHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/reload", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("handler status after Stop = %d, want 503", rec.Code)
	}
	if n := changes.Load(); n != 1 {
		t.Errorf("OnChange fired after Stop returned")
	}
}

// Stop cancels an automatic reload in flight; the abandoned reload is neither
// a success nor a failure.
func TestReloader_StopCancelsAutomaticReload(t *testing.T) {
	var arm atomic.Bool
	entered := make(chan struct{})
	var failures atomic.Int32

	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{
			WithPollInterval[portCfg](20 * time.Millisecond),
			WithOnReloadError[portCfg](func(string, error) { failures.Add(1) }),
		},
		WithSkipValidation(),
		WithSecretPopulator(func(ctx context.Context, _ any) error {
			if arm.CompareAndSwap(true, false) {
				close(entered)
				<-ctx.Done() // a secret backend that only returns on cancellation
				return ctx.Err()
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	arm.Store(true)
	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()
	<-entered
	before := r.Stats()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop did not cancel the automatic reload: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if s := r.Stats(); s.Failures != before.Failures || s.Attempts != before.Attempts {
		t.Errorf("stats moved from %+v to %+v: an abandoned reload must not count", before, s)
	}
	if n := failures.Load(); n != 0 {
		t.Errorf("OnReloadError fired %d times for an abandoned reload", n)
	}
}

func TestReload_AbandonedByContextIsNotAFailure(t *testing.T) {
	var arm atomic.Bool
	var failures atomic.Int32
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{
			WithOnReloadError[portCfg](func(string, error) { failures.Add(1) }),
		},
		WithSkipValidation(),
		WithSecretPopulator(func(ctx context.Context, _ any) error {
			if arm.Load() {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	arm.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = r.Reload(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrReload) {
		t.Error("an abandoned reload must not read as ErrReload")
	}
	if s := r.Stats(); s.Attempts != 0 {
		t.Errorf("stats = %+v, want no attempts", s)
	}
	if n := failures.Load(); n != 0 {
		t.Errorf("OnReloadError fired %d times", n)
	}
}

func TestReloader_StartAfterStopReturnsImmediately(t *testing.T) {
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithPollInterval[portCfg](time.Millisecond)},
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start after Stop = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start ran after Stop")
	}
	if s := r.Stats(); s.Attempts != 0 {
		t.Errorf("stats = %+v, want no attempts", s)
	}
}

// portProvider is a koanf provider whose value the test changes, standing in
// for a source the reloader cannot watch (a replicated store, an API).
type portProvider struct{ port atomic.Int64 }

func (p *portProvider) Read() (map[string]any, error) {
	return map[string]any{"port": int(p.port.Load())}, nil
}

func (p *portProvider) ReadBytes() ([]byte, error) {
	return nil, errors.New("portProvider: Read only")
}

func TestReloader_SourceChangedWithoutWatchers(t *testing.T) {
	src := &portProvider{}
	src.port.Store(9000)
	r, err := NewReloader[portCfg](t.Context(), nil, WithProvider(src), WithSkipValidation())
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}

	// A request made before Start is served once Start runs.
	src.port.Store(9001)
	r.SourceChanged()
	done := make(chan error, 1)
	go func() { done <- r.Start(context.Background()) }()

	eventually(t, func() bool { return r.Config().Port == 9001 }, "the pre-Start request to be served")
	if got := r.Stats().LastTrigger; got != TriggerSourceChanged {
		t.Errorf("LastTrigger = %q, want %q", got, TriggerSourceChanged)
	}

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Start = %v, want nil after Stop", err)
	}
	r.SourceChanged() // after Stop: ignored, must not block
}

// The race a cluster-config listener has with Reload: a change
// committed while a reload is in flight — after it read the source — is lost
// when the second trigger is turned away. SourceChanged must apply it.
func TestReloader_SourceChangedDuringReloadIsApplied(t *testing.T) {
	src := &portProvider{}
	src.port.Store(9000)
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})

	r, err := NewReloader[portCfg](t.Context(), nil,
		WithProvider(src),
		WithSkipValidation(),
		WithPostLoad(func(context.Context, *portCfg) error {
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	startReloader(t, r)

	hold.Store(true)
	manual := make(chan error, 1)
	go func() {
		_, err := r.Reload(context.Background())
		manual <- err
	}()
	<-entered // the manual reload has read 9000

	src.port.Store(9001)
	r.SourceChanged()
	close(release)
	if err := <-manual; err != nil {
		t.Fatalf("manual reload: %v", err)
	}
	eventually(t, func() bool { return r.Config().Port == 9001 }, "the change committed mid-reload")
}

// A burst of requests while a reload runs collapses into one more reload.
func TestReloader_SourceChangedCoalesces(t *testing.T) {
	src := &portProvider{}
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := NewReloader[portCfg](t.Context(), nil,
		WithProvider(src),
		WithSkipValidation(),
		WithPostLoad(func(context.Context, *portCfg) error {
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	startReloader(t, r)

	hold.Store(true)
	r.SourceChanged()
	<-entered // the first request's reload is running
	for range 50 {
		r.SourceChanged()
	}
	close(release)

	eventually(t, func() bool { return r.Stats().Attempts == 2 }, "the coalesced follow-up reload")
	time.Sleep(100 * time.Millisecond)
	if got := r.Stats().Attempts; got != 2 {
		t.Errorf("Attempts = %d, want 2: fifty requests during one reload are one follow-up", got)
	}
}

// A file written steadily next to the config file cannot hold the debounce
// open forever: a real config change still applies.
func TestReloader_BusyDirectoryCannotStarveAFileChange(t *testing.T) {
	path := writeYAML(t, "port: 9000\n")
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithFileWatch[portCfg](50 * time.Millisecond)},
		WithConfigFile(path),
		WithSkipValidation(),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	startReloader(t, r)
	waitForWatcher(t, r)

	stopNoise := make(chan struct{})
	noiseDone := make(chan struct{})
	go func() {
		defer close(noiseDone)
		sibling := filepath.Join(filepath.Dir(path), "busy.log")
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stopNoise:
				return
			case <-tick.C:
				_ = os.WriteFile(sibling, []byte(strconv.Itoa(i)), 0o644)
			}
		}
	}()
	defer func() { close(stopNoise); <-noiseDone }()

	if err := os.WriteFile(path, []byte("port: 9001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ten 50ms windows bound the delay at 500ms; allow slack for a loaded host
	// but stay well inside the noise, which runs until the test ends.
	deadline := time.Now().Add(3 * time.Second)
	for r.Config().Port != 9001 {
		if time.Now().After(deadline) {
			t.Fatal("the file change never settled while a sibling file was being written")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

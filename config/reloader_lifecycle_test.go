package config

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
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

// reloadEvents is an option that sends every successful reload's event to
// the channel it returns.
func reloadEvents[T any]() (ReloadOption, <-chan ReloadEvent[T]) {
	ch := make(chan ReloadEvent[T], 64)
	return WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[T]) { ch <- ev }), ch
}

// recv waits for a value on ch without moving any clock.
func recv[V any](t *testing.T, ch <-chan V, what string) V {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
		panic("unreachable")
	}
}

// awaitLog waits for a log line containing substr, firing each debounce the
// file watch arms meanwhile, as receive does.
func awaitLog(t *testing.T, clk *fakeClock, logs *logRecorder, substr string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		changed := clk.changes()
		if logs.has(substr) {
			return
		}
		if due, ok := clk.debounce(); ok {
			clk.Advance(due.Sub(clk.Now()))
			continue
		}
		select {
		case <-logs.changed:
		case <-changed:
		case <-deadline:
			t.Fatalf("timeout waiting for a log line containing %q", substr)
		}
	}
}

// runReloader runs r.Run in the background and stops it at cleanup.
func runReloader[T any](t *testing.T, r *Reloader[T]) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
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
	clk := newFakeClock()
	onReload, reloads := reloadEvents[portCfg]()

	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithFile(path), WithSkipValidation(), WithLogger(slog.New(logs)), WithPostLoad(func(context.Context, *portCfg) error {
			if holdNext.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		})},
		WithFileWatch(50*time.Millisecond),
		withClock(clk),
		onReload,
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	armReloader(t, r)
	runReloader(t, r)

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
	awaitLog(t, clk, logs, "config reload waiting for the reload in flight trigger=file_change")

	close(release)
	if err := <-manual; err != nil {
		t.Fatalf("manual reload: %v", err)
	}
	first := recv(t, reloads, "the manual reload")
	assert.Equal(t, TriggerManual, first.Trigger)
	assert.Equal(t, 9000, first.New.Port, "the manual reload read the file before it changed")
	next := receive(t, clk, reloads, "the file-change reload")
	assert.Equal(t, TriggerFileChange, next.Trigger)
	assert.Equal(t, 9001, next.New.Port)
}

// A pending debounce timer dies with Stop: Run stops it before returning, and
// no reload runs.
func TestReloader_StopDuringDebounceDoesNotReload(t *testing.T) {
	path := writeYAML(t, "port: 9000\n")
	var changes atomic.Int32
	clk := newFakeClock()
	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithFile(path), WithSkipValidation()},
		WithFileWatch(300*time.Millisecond),
		withClock(clk),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[portCfg]) { changes.Add(1) }),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	done := make(chan error, 1)
	armReloader(t, r)
	go func() { done <- r.Run(context.Background()) }()

	if err := os.WriteFile(path, []byte("port: 9001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clk.awaitDebounce(t)

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, armed := clk.debounce(); armed {
		t.Fatal("Run returned with its debounce still armed")
	}

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
	synctest.Test(t, testStopWaitsForManualReload)
}

func testStopWaitsForManualReload(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var hold atomic.Bool
	var changes atomic.Int32

	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithSkipValidation(), WithPostLoad(func(context.Context, *portCfg) error {
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		})},
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[portCfg]) { changes.Add(1) }),
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
	if n := changes.Load(); n != 1 {
		t.Errorf("OnChange fired after Stop returned")
	}
}

// Stop cancels an automatic reload in flight; the abandoned reload is neither
// a success nor a failure.
func TestReloader_StopCancelsAutomaticReload(t *testing.T) {
	synctest.Test(t, testStopCancelsAutomaticReload)
}

func testStopCancelsAutomaticReload(t *testing.T) {
	var arm atomic.Bool
	entered := make(chan struct{})
	var failures atomic.Int32

	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithSkipValidation(), WithSecretPopulator(func(ctx context.Context, _ any) error {
			if arm.CompareAndSwap(true, false) {
				close(entered)
				<-ctx.Done() // a secret backend that only returns on cancellation
				return ctx.Err()
			}
			return nil
		})},
		WithPollInterval(20*time.Millisecond),
		WithOnReloadError(func(string, error) { failures.Add(1) }),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	arm.Store(true)
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	<-entered
	before := r.Stats()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Stop(ctx); err != nil {
		t.Fatalf("Stop did not cancel the automatic reload: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s := r.Stats(); s.Failures != before.Failures || s.Attempts != before.Attempts {
		t.Errorf("stats moved from %+v to %+v: an abandoned reload must not count", before, s)
	}
	if n := failures.Load(); n != 0 {
		t.Errorf("OnReloadError fired %d times for an abandoned reload", n)
	}
}

func TestReload_AbandonedByContextIsNotAFailure(t *testing.T) {
	synctest.Test(t, testAbandonedByContextIsNotAFailure)
}

func testAbandonedByContextIsNotAFailure(t *testing.T) {
	var arm atomic.Bool
	var failures atomic.Int32
	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithSkipValidation(), WithSecretPopulator(func(ctx context.Context, _ any) error {
			if arm.Load() {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		})},
		WithOnReloadError(func(string, error) { failures.Add(1) }),
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
	synctest.Test(t, testRunAfterStopReturnsImmediately)
}

func testRunAfterStopReturnsImmediately(t *testing.T) {
	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithSkipValidation()},
		WithPollInterval(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run after Stop = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run ran after Stop")
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
	synctest.Test(t, testSourceChangedWithoutWatchers)
}

func testSourceChangedWithoutWatchers(t *testing.T) {
	src := &portProvider{}
	src.port.Store(9000)
	r, err := NewReloader[portCfg](t.Context(), []Option{WithOverrideProvider(src, nil), WithSkipValidation()})
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}

	// A request made before Run is served once Run runs.
	src.port.Store(9001)
	r.SourceChanged()
	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()

	synctest.Wait()
	if got := r.Config().Port; got != 9001 {
		t.Errorf("Port = %d, want the pre-Run request served", got)
	}
	if s := r.Stats(); s.Attempts != 1 || s.LastTrigger != TriggerSourceChanged {
		t.Errorf("stats = %+v, want one %s reload", s, TriggerSourceChanged)
	}

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Errorf("Run = %v, want nil after Stop", err)
	}
	r.SourceChanged() // after Stop: ignored, must not block
}

// The race a cluster-config listener has with Reload: a change
// committed while a reload is in flight — after it read the source — is lost
// when the second trigger is turned away. SourceChanged must apply it.
func TestReloader_SourceChangedDuringReloadIsApplied(t *testing.T) {
	synctest.Test(t, testSourceChangedDuringReloadIsApplied)
}

func testSourceChangedDuringReloadIsApplied(t *testing.T) {
	src := &portProvider{}
	src.port.Store(9000)
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})

	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithOverrideProvider(src, nil), WithSkipValidation(), WithPostLoad(func(context.Context, *portCfg) error {
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		})},
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	runReloader(t, r)

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
	synctest.Wait()
	if got := r.Config().Port; got != 9001 {
		t.Errorf("Port = %d, want the change committed mid-reload", got)
	}
	if s := r.Stats(); s.Attempts != 2 || s.LastTrigger != TriggerSourceChanged {
		t.Errorf("stats = %+v, want the manual reload then one %s reload", s, TriggerSourceChanged)
	}
}

// A burst of requests while a reload runs collapses into one more reload.
func TestReloader_SourceChangedCoalesces(t *testing.T) {
	synctest.Test(t, testSourceChangedCoalesces)
}

func testSourceChangedCoalesces(t *testing.T) {
	src := &portProvider{}
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithOverrideProvider(src, nil), WithSkipValidation(), WithPostLoad(func(context.Context, *portCfg) error {
			if hold.CompareAndSwap(true, false) {
				close(entered)
				<-release
			}
			return nil
		})},
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	runReloader(t, r)

	hold.Store(true)
	r.SourceChanged()
	<-entered // the first request's reload is running
	for range 50 {
		r.SourceChanged()
	}
	close(release)

	synctest.Wait()
	if got := r.Stats().Attempts; got != 2 {
		t.Errorf("Attempts = %d, want 2: fifty requests during one reload are one follow-up", got)
	}
}

// Files created steadily next to the config file cannot hold the debounce
// open forever: with one every half window, the change still applies no later
// than ten windows after the burst began. Each is a new name, which every
// backend reports; kqueue can miss the first write to a file it has only just
// begun to watch. That it fires at the bound and not before is asserted on
// the file watch itself, where no late event can end a burst early.
func TestReloader_BusyDirectoryCannotStarveAFileChange(t *testing.T) {
	const debounce = 50 * time.Millisecond
	path := writeYAML(t, "port: 9000\n")
	clk := newFakeClock()
	onReload, reloads := reloadEvents[portCfg]()
	r, err := NewReloader[portCfg](
		t.Context(),
		[]Option{WithFile(path), WithSkipValidation()},
		WithFileWatch(debounce),
		withClock(clk),
		onReload,
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	armReloader(t, r)
	runReloader(t, r)

	if err := os.WriteFile(path, []byte("port: 9001\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clk.awaitDebounce(t)
	began := clk.Now()
	for i := range 2*maxDebounceWindows - 1 {
		armed := clk.resetCount()
		sibling := filepath.Join(filepath.Dir(path), "busy-"+strconv.Itoa(i)+".log")
		if err := os.WriteFile(sibling, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		clk.await(t, "the sibling file to re-arm the debounce", func() bool { return clk.resetCount() > armed })
		clk.Advance(debounce / 2)
	}
	clk.Advance(debounce / 2)

	ev := recv(t, reloads, "the file change")
	assert.Equal(t, TriggerFileChange, ev.Trigger)
	assert.Equal(t, 9001, ev.New.Port)
	bound := began.Add(maxDebounceWindows * debounce).UTC()
	if at := ev.Result.Snapshot.Stats.LastSuccess; at.After(bound) {
		t.Errorf("the change applied %v after the burst began, past its %v bound", at.Sub(began), bound.Sub(began))
	}
}

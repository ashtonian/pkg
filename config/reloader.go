package config

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Reloader holds the current configuration and replaces it when a source
// changes, only ever with one that passed the whole load pipeline. Config
// returns the current one with a single atomic load.
//
// A reload is triggered by [Reloader.Reload], or by what [Reloader.Run]
// serves: the file watch, a poll tick, a signal, or [Reloader.SourceChanged].
// On failure the previous config stays live and the error is reported through
// [WithOnReloadError], the log, and — for Reload — the return value.
//
// Reloads are serialized. A manual Reload that finds another reload in flight
// returns [ErrReloadInProgress]; an automatic trigger instead waits for it and
// then reloads, so a change that lands while another reload is reading the
// sources is never lost.
//
// The configs a Reloader publishes are shared by every reader and must be
// treated as immutable, nested maps and slices included: atomic publication
// makes the swap safe, not mutation.
type Reloader[T any] struct {
	current atomic.Pointer[T]
	// load runs one load and reports the digests of the config files it
	// parsed, nil when the loader is the caller's.
	load   func(ctx context.Context) (*T, fileDigests, error)
	logger *slog.Logger

	onSuccess    []successHook
	onError      []func(trigger string, err error)
	fileWatch    bool
	debounce     time.Duration
	pollInterval time.Duration
	signals      []os.Signal
	// paths are the files the watch covers; extra are those of them the
	// loader does not report on, whose digests the reloader takes itself.
	paths []string
	extra []string

	// slot is the single-flight token: holding it is the right to run the
	// load pipeline, publish, and fire callbacks. Stop takes it and never
	// gives it back.
	slot chan struct{}
	// loaded is what each watched file held at the last successful load.
	// Guarded by slot.
	loaded fileDigests
	stats  atomic.Pointer[ReloadStats] // written while holding slot, read lock-free

	// armSem guards armed. It is taken before the slot, never while holding
	// it, and waited for under a context, so an Arm queued behind another
	// gives up when its own context ends.
	armSem  chan struct{}
	armed   *triggers    // set up by Arm for the next Run to serve
	serving atomic.Int32 // Run calls serving their triggers

	stopOnce sync.Once
	stopCh   chan struct{}
	quiesced chan struct{} // closed once Stop holds the slot
	disarmed chan struct{} // closed once Stop has released triggers no Run took
	// changed holds at most one pending SourceChanged request; further
	// requests coalesce into it.
	changed chan struct{}
}

// Reload trigger names. These form a closed vocabulary so consumers can use
// them directly as a bounded metric label.
const (
	TriggerManual        = "manual"         // an explicit Reload call
	TriggerFileChange    = "file_change"    // WithFileWatch saw a watched file or its symlink target change
	TriggerPoll          = "poll"           // WithPollInterval ticked
	TriggerSignal        = "signal"         // WithSignalReload received one of its signals
	TriggerSourceChanged = "source_changed" // the caller reported a change via SourceChanged
)

// ReloadStats is an immutable point-in-time snapshot of a Reloader's reload
// history, returned by [Reloader.Stats].
//
// Only reloads that ran to a verdict are counted. A manual Reload turned away
// by [ErrReloadInProgress] or [ErrReloaderStopped] ran no pipeline, and a
// reload abandoned because its context ended (including one cancelled by
// Stop) reached no verdict about the sources.
type ReloadStats struct {
	// Attempts is Successes+Failures — every reload that ran the pipeline.
	Attempts uint64
	// Successes counts reloads that produced a new config and swapped it in.
	Successes uint64
	// Failures counts reloads that left the previous config in place.
	Failures uint64
	// LastTrigger names what drove the most recent attempt: one of the
	// Trigger* constants. Empty before the first attempt.
	LastTrigger string
	// LastSuccess is when the most recent successful reload swapped. Zero if
	// no reload has succeeded since construction; the initial load is not a
	// reload and does not set it.
	LastSuccess time.Time
	// LastFailure is when the most recent failed reload was rejected. Zero if
	// none has failed.
	LastFailure time.Time
	// LastError is the error from the most recent attempt, or nil when that
	// attempt succeeded. A success clears it; a failure sets it without
	// touching LastSuccess, so a non-nil LastError beside an earlier
	// LastSuccess means the config from that success is still live.
	LastError error
}

// NewReloader loads the initial config with [Load]'s pipeline and loadOpts,
// and returns a Reloader that re-runs that pipeline, with the same options,
// on every reload. Each reload uses the ctx passed to Reload, or Run's for
// automatic triggers. The [WithLogger] logger in loadOpts also receives
// reload events unless [WithReloadLogger] sets another. The file watch covers
// the config files loadOpts name, and any [WithWatchedFiles] adds.
func NewReloader[T any](ctx context.Context, loadOpts []Option, opts ...ReloadOption) (*Reloader[T], error) {
	loadOpts = slices.Clone(loadOpts)
	o := buildOptions(loadOpts)
	r, err := newReloader[T](buildReloadOptions(opts), o.logger, o.filePaths())
	if err != nil {
		return nil, err
	}
	r.load = func(ctx context.Context) (*T, fileDigests, error) {
		o := buildOptions(loadOpts)
		o.requireFiles(r.loaded)
		return load[T](ctx, o)
	}
	if err := r.loadInitial(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// NewReloaderFunc returns a Reloader whose loader is the caller's: load
// builds a complete, validated config, and its error keeps the previous one
// live. Use it to give an existing loading path the reload lifecycle. The
// file watch covers the files given to [WithWatchedFiles]; each reload
// compares them with what they held just before the previous load.
func NewReloaderFunc[T any](ctx context.Context, load func(ctx context.Context) (*T, error), opts ...ReloadOption) (*Reloader[T], error) {
	r, err := newReloader[T](buildReloadOptions(opts), slog.Default(), nil)
	if err != nil {
		return nil, err
	}
	r.load = func(ctx context.Context) (*T, fileDigests, error) {
		cfg, err := load(ctx)
		if err == nil && cfg == nil {
			err = errors.New("config: the loader returned a nil config")
		}
		return cfg, nil, err
	}
	if err := r.loadInitial(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func newReloader[T any](ro *reloadOptions, logger *slog.Logger, configFiles []string) (*Reloader[T], error) {
	if err := ro.checkHookTypes(reflect.TypeFor[*T]()); err != nil {
		return nil, err
	}
	paths := slices.Clone(configFiles)
	var extra []string
	for _, p := range ro.watched {
		if !slices.Contains(paths, p) {
			paths = append(paths, p)
			extra = append(extra, p)
		}
	}
	if ro.fileWatch && len(paths) == 0 {
		return nil, errors.New("config: WithFileWatch has no files to watch; name them with WithFile or WithWatchedFiles")
	}
	return &Reloader[T]{
		logger:       cmp.Or(ro.logger, logger),
		onSuccess:    ro.onSuccess,
		onError:      ro.onError,
		fileWatch:    ro.fileWatch,
		debounce:     ro.debounce,
		pollInterval: ro.pollInterval,
		signals:      ro.signals,
		paths:        paths,
		extra:        extra,
		slot:         make(chan struct{}, 1),
		armSem:       make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
		changed:      make(chan struct{}, 1),
		quiesced:     make(chan struct{}),
		disarmed:     make(chan struct{}),
	}, nil
}

func (r *Reloader[T]) loadInitial(ctx context.Context) error {
	cfg, files, err := r.runLoad(ctx)
	if err != nil {
		return err
	}
	r.loaded = files
	r.current.Store(cfg)
	return nil
}

// runLoad runs the loader and returns what every watched file held for this
// load: the loader's own record of the config files it parsed, and for the
// rest what they held just before the load, so a change after that still
// reads as a change.
func (r *Reloader[T]) runLoad(ctx context.Context) (*T, fileDigests, error) {
	before := digestFiles(r.extra)
	cfg, files, err := r.load(ctx)
	if err != nil {
		return nil, nil, err
	}
	if files == nil {
		files = fileDigests{}
	}
	maps.Copy(files, before)
	return cfg, files, nil
}

// digestFiles hashes each of paths that can be read; one that cannot is left
// out, as an absent config file is.
func digestFiles(paths []string) fileDigests {
	out := make(fileDigests, len(paths))
	for _, p := range paths {
		if data, err := os.ReadFile(p); err == nil {
			out[p] = sha256.Sum256(data)
		}
	}
	return out
}

// Config returns the current configuration. This is an atomic load
// and safe to call from any goroutine without locking.
func (r *Reloader[T]) Config() *T {
	return r.current.Load()
}

// Reload triggers an immediate config reload. Returns the new config on
// success. On failure the previous config is preserved and an error wrapping
// both [ErrReload] and the underlying cause is returned.
//
// If another reload is in flight this call does not wait: it returns
// [ErrReloadInProgress], which deliberately does NOT wrap ErrReload — no
// pipeline ran and the config is unchanged. After Stop it returns
// [ErrReloaderStopped]. If ctx ends while the reload runs, the reload is
// abandoned: the error carries ctx.Err(), not ErrReload, and nothing is
// recorded. Callers that alert on breakage check ErrReload alone.
func (r *Reloader[T]) Reload(ctx context.Context) (*T, error) {
	if err := r.acquire(ctx, TriggerManual, false); err != nil {
		return nil, err
	}
	defer r.release()
	return r.reloadHeld(ctx, TriggerManual)
}

// SourceChanged requests a reload because a source changed that the reloader
// does not watch itself — a provider source, or anything else the caller
// learns about through its own events. It never blocks: requests made while
// one is pending coalesce into it, and the reload they trigger starts after
// the latest of them, so it reads every change made before the call. Like the
// watchers, it waits for a reload already in flight instead of being dropped.
//
// Run serves the requests; one made before Run starts is served once it
// does, and requests after Stop are ignored. Use Reload instead when the
// caller needs the outcome.
func (r *Reloader[T]) SourceChanged() {
	select {
	case r.changed <- struct{}{}:
	default: // a request is already pending; it will read this change too
	}
}

// Stats returns a snapshot of this reloader's reload history. Safe to call
// from any goroutine; the snapshot is internally consistent (every field
// describes the same moment).
func (r *Reloader[T]) Stats() ReloadStats {
	if s := r.stats.Load(); s != nil {
		return *s
	}
	return ReloadStats{}
}

// acquire takes the single-flight slot for trigger. With wait false it fails
// fast with ErrReloadInProgress; with wait true it blocks until the slot
// frees, Stop is called, or ctx ends.
func (r *Reloader[T]) acquire(ctx context.Context, trigger string, wait bool) error {
	select {
	case <-r.stopCh:
		return ErrReloaderStopped
	default:
	}

	select {
	case r.slot <- struct{}{}:
	default:
		if !wait {
			return ErrReloadInProgress
		}
		r.logger.DebugContext(ctx, "config reload waiting for the reload in flight", "trigger", trigger)
		select {
		case r.slot <- struct{}{}:
		case <-r.stopCh:
			return ErrReloaderStopped
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Stop may have been called while this caller waited; its own wait for the
	// slot proceeds as soon as the slot is handed back.
	select {
	case <-r.stopCh:
		r.release()
		return ErrReloaderStopped
	default:
		return nil
	}
}

func (r *Reloader[T]) release() { <-r.slot }

// reloadHeld runs one attempt attributed to trigger. The caller holds the
// slot. Every reload path funnels through here so stats, callbacks, and
// logging stay uniform across manual, file-watch, poll, and signal triggers.
//
// A reload whose context has ended is abandoned, not refuted: it publishes
// nothing, counts as neither success nor failure and fires no callback — even
// when the context ends after the sources were read, say inside a post-load
// hook that went on to return nil.
func (r *Reloader[T]) reloadHeld(ctx context.Context, trigger string) (*T, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("config reload abandoned: %w", err)
	}
	cfg, files, err := r.runLoad(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Say nothing about the sources, which may be fine.
		if err != nil && !errors.Is(err, ctxErr) {
			ctxErr = errors.Join(ctxErr, err)
		}
		return nil, fmt.Errorf("config reload abandoned: %w", ctxErr)
	}
	if err != nil {
		reloadErr := fmt.Errorf("%w: %w", ErrReload, err)
		r.recordAttempt(trigger, reloadErr)
		r.notifyReloadError(ctx, trigger, reloadErr)
		return nil, reloadErr
	}

	r.loaded = files
	old := r.current.Swap(cfg)
	r.recordAttempt(trigger, nil)
	r.notifySuccess(ctx, old, cfg)
	return cfg, nil
}

// recordAttempt folds one completed attempt into the stats snapshot. Called
// only with the slot held, so the read-modify-write needs no further
// synchronisation; readers see the new snapshot atomically.
func (r *Reloader[T]) recordAttempt(trigger string, err error) {
	next := r.Stats()
	next.Attempts++
	next.LastTrigger = trigger
	next.LastError = err
	if err != nil {
		next.Failures++
		next.LastFailure = time.Now()
	} else {
		next.Successes++
		next.LastSuccess = time.Now()
	}
	r.stats.Store(&next)
}

// Arm sets up the reloader's triggers — the file watch and the signal
// registration — and reports whether they could be: nil once every configured
// trigger is armed, the setup error otherwise, with nothing left armed. A
// caller that must not report itself ready on a reloader that cannot deliver
// what it was configured for calls Arm before Run:
//
//	if err := reloader.Arm(ctx); err != nil {
//		return err // e.g. a config directory that cannot be watched
//	}
//	go reloader.Run(ctx)
//
// Run serves the triggers Arm set up, so nothing is missed in between: a
// change to a config file after Arm returns, or one made after NewReloader
// loaded and before the watch existed, is reloaded once Run starts, and a
// signal received in between is served then instead of taking the signal's
// default action. Poll ticks begin with Run. Run arms the triggers itself
// when Arm was not called.
//
// Arm returns nil without arming again when the triggers are already armed —
// by an earlier Arm, or by a Run serving them — and [ErrReloaderStopped]
// after Stop. Comparing the files with the last load waits for a reload in
// flight, and a concurrent Arm waits for this one; ctx bounds both waits, and
// an Arm whose ctx ends while it waits returns ctx's error. Triggers Arm sets
// up are released when the Run serving them returns, or by Stop. Like
// Stop, Arm must not be called from a WithOnReloadSuccess or WithOnReloadError
// callback.
func (r *Reloader[T]) Arm(ctx context.Context) error {
	if err := r.lockArm(ctx); err != nil {
		return err
	}
	defer r.unlockArm()
	if r.stopped() {
		return ErrReloaderStopped
	}
	if r.armed != nil || r.serving.Load() > 0 {
		return nil
	}
	t, err := r.arm(ctx)
	if err != nil {
		return err
	}
	r.armed = t
	return nil
}

// lockArm takes armSem, or returns ctx's error if ctx ends first — including
// when it has already ended, so an expired caller never proceeds.
func (r *Reloader[T]) lockArm(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.armSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		r.unlockArm()
		return err
	}
	return nil
}

func (r *Reloader[T]) unlockArm() { <-r.armSem }

// triggers are what arm sets up and Run serves.
type triggers struct {
	watch   *fileWatch     // nil without a file watch
	signals chan os.Signal // nil without signal reload
	// stale reports that a config file differed from the last load once the
	// watch was live: a change no watch event will report.
	stale bool
}

func (t *triggers) close() error {
	if t.signals != nil {
		signal.Stop(t.signals)
	}
	if t.watch != nil {
		return t.watch.close()
	}
	return nil
}

// arm sets up the triggers. The caller holds armSem. The files are compared
// with the last load only once the watch is live, so every change is either
// found by the comparison or reported by the watch.
func (r *Reloader[T]) arm(ctx context.Context) (*triggers, error) {
	t := &triggers{}
	if r.fileWatch {
		watch, err := newFileWatch(r.paths, cmp.Or(max(r.debounce, 0), DefaultFileWatchDebounce), r.logger)
		if err != nil {
			return nil, fmt.Errorf("setting up file watch: %w", err)
		}
		if err := r.acquire(ctx, TriggerFileChange, true); err != nil {
			return nil, errors.Join(err, watch.close())
		}
		_, t.stale = r.changedSinceLoad()
		r.release()
		t.watch = watch
	}
	if len(r.signals) > 0 {
		t.signals = make(chan os.Signal, 1)
		signal.Notify(t.signals, r.signals...)
	}
	return t, nil
}

// serve hands Run the triggers Arm set up, or arms them.
func (r *Reloader[T]) serve(ctx context.Context) (*triggers, error) {
	if err := r.lockArm(ctx); err != nil {
		return nil, err
	}
	defer r.unlockArm()
	if r.stopped() {
		return nil, ErrReloaderStopped
	}
	t := r.armed
	r.armed = nil
	if t == nil {
		var err error
		if t, err = r.arm(ctx); err != nil {
			return nil, err
		}
	}
	r.serving.Add(1)
	return t, nil
}

// unserve releases the triggers a returning Run served.
func (r *Reloader[T]) unserve(ctx context.Context, t *triggers) {
	r.serving.Add(-1)
	if err := t.close(); err != nil {
		r.logger.WarnContext(ctx, "closing config file watcher", "error", err)
	}
}

func (r *Reloader[T]) stopped() bool {
	select {
	case <-r.stopCh:
		return true
	default:
		return false
	}
}

// Run runs the reload loop until Stop is called or ctx ends: the configured
// watchers — file watch, poll interval, signals — and [Reloader.SourceChanged]
// requests. It returns nil after Stop (immediately if already stopped) and
// ctx.Err() when ctx ends. Typically called in a goroutine:
// go reloader.Run(ctx), after [Reloader.Arm] when the caller needs to know
// the triggers armed. Without Arm, Run arms them and returns the error when
// it cannot.
//
// Every automatic reload runs on Start's goroutine with a context that Stop
// also cancels, and the file-watch debounce timer is owned by the same loop,
// so no automatic reload can begin once Run has returned.
func (r *Reloader[T]) Run(ctx context.Context) error {
	if r.stopped() {
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Stop cancels runCtx so an automatic reload in flight abandons its
	// secret lookups instead of holding Stop up. Exits when Run returns.
	go func() {
		select {
		case <-r.stopCh:
			cancel()
		case <-runCtx.Done():
		}
	}()

	t, err := r.serve(runCtx)
	if err != nil {
		if runCtx.Err() != nil || errors.Is(err, ErrReloaderStopped) {
			return r.exit(ctx)
		}
		return err
	}
	defer r.unserve(ctx, t)

	var (
		events    <-chan fsnotify.Event
		watchErrs <-chan error
		settled   <-chan time.Time
	)
	if t.watch != nil {
		events, watchErrs, settled = t.watch.watcher.Events, t.watch.watcher.Errors, t.watch.timer.C
		if t.stale {
			r.catchUp(runCtx)
		}
	}

	var tickCh <-chan time.Time
	if r.pollInterval > 0 {
		ticker := time.NewTicker(r.pollInterval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	r.logger.InfoContext(ctx, "config reloader started",
		"file_watch", t.watch != nil,
		"poll_interval", r.pollInterval,
		"signal_count", len(r.signals),
	)

	for {
		select {
		case <-runCtx.Done():
			return r.exit(ctx)

		case <-tickCh:
			r.autoReload(runCtx, TriggerPoll)

		case <-r.changed:
			r.autoReload(runCtx, TriggerSourceChanged)

		case sig := <-t.signals:
			r.logger.InfoContext(ctx, "config reload triggered by signal", "signal", sig)
			r.autoReload(runCtx, TriggerSignal)

		case ev, ok := <-events:
			if !ok {
				r.logger.ErrorContext(ctx, "config file watcher closed unexpectedly; file-change reloads are off")
				events, watchErrs, settled = nil, nil, nil
				continue
			}
			t.watch.observe(ev)

		case err, ok := <-watchErrs:
			if !ok {
				watchErrs = nil
				continue
			}
			r.logger.ErrorContext(ctx, "config file watcher error", "error", err)

		case <-settled:
			path, changed, err := t.watch.settle()
			if err != nil {
				r.logger.ErrorContext(ctx, "config file watch is missing directories; changes there will not reload", "error", err)
			}
			if changed {
				r.autoReload(runCtx, TriggerFileChange, "path", path)
			}
		}
	}
}

// exit is Start's return once its run context has ended: nil after Stop,
// ctx.Err() when the caller's ctx ended.
func (r *Reloader[T]) exit(ctx context.Context) error {
	if r.stopped() {
		r.logger.InfoContext(ctx, "config reloader stopped")
		return nil
	}
	r.logger.InfoContext(ctx, "config reloader context cancelled")
	return ctx.Err()
}

// autoReload runs one watcher-driven reload, waiting for any reload already in
// flight so the change that triggered this one is read after it lands.
func (r *Reloader[T]) autoReload(ctx context.Context, trigger string, attrs ...any) {
	if err := r.acquire(ctx, trigger, true); err != nil {
		return // stopping: Stop or ctx ended, and Start's loop exits next
	}
	defer r.release()
	r.reloadAndLog(ctx, trigger, attrs...)
}

// catchUp reloads if a config file still differs from what the last load
// read; a reload since arming may already have read it.
func (r *Reloader[T]) catchUp(ctx context.Context) {
	if err := r.acquire(ctx, TriggerFileChange, true); err != nil {
		return
	}
	defer r.release()
	if path, changed := r.changedSinceLoad(); changed {
		r.reloadAndLog(ctx, TriggerFileChange, "path", path, "changed", "before the watch started")
	}
}

// changedSinceLoad compares every watched file with what it held at the last
// load: changed, newly present or gone. The caller holds the slot.
func (r *Reloader[T]) changedSinceLoad() (string, bool) {
	for _, path := range r.paths {
		was, loaded := r.loaded[path]
		data, err := os.ReadFile(path)
		switch {
		case err != nil && loaded, err == nil && !loaded:
			return path, true
		case err == nil && sha256.Sum256(data) != was:
			return path, true
		}
	}
	return "", false
}

// reloadAndLog runs one automatic reload and logs its outcome. The caller
// holds the slot.
func (r *Reloader[T]) reloadAndLog(ctx context.Context, trigger string, attrs ...any) {
	args := append([]any{"trigger", trigger}, attrs...)
	if _, err := r.reloadHeld(ctx, trigger); err != nil {
		if ctx.Err() != nil {
			return
		}
		r.logger.ErrorContext(ctx, "config reload failed", append(args, "error", err)...)
		return
	}
	level := slog.LevelInfo
	if trigger == TriggerPoll {
		level = slog.LevelDebug // a poll reloads on every tick; Info would flood
	}
	r.logger.Log(ctx, level, "config reloaded", args...)
}

// Stop stops the reloader. From the moment it is called, Reload returns
// [ErrReloaderStopped], no new reload starts, an automatic reload in flight
// is cancelled, and Run returns.
//
// Stop then waits for the reload in flight, if any, and for the release of
// triggers Arm set up that no Run took. When it returns nil both are done:
// no reload is running or will run, and no WithOnReloadSuccess or WithOnReloadError
// callback will fire, so it is safe to tear down whatever the callbacks
// touch. A manual Reload in flight runs to completion under its caller's
// context; if ctx ends first, Stop returns ctx's error and that guarantee is
// not yet given: the reloader finishes stopping in the background once the
// reload returns, and a later Stop call waits for it again.
//
// Stop is idempotent and safe for concurrent use, but must not be called from
// a WithOnReloadSuccess or WithOnReloadError callback: those run holding the slot
// Stop waits for.
func (r *Reloader[T]) Stop(ctx context.Context) error {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		go func() {
			// Taking the slot waits out whichever reload holds it; never
			// giving it back is what keeps later reloads from starting.
			r.slot <- struct{}{}
			close(r.quiesced)
		}()
		logCtx := context.WithoutCancel(ctx)
		go func() {
			// An Arm or serve holding armSem sees the closed stopCh and
			// returns; none sets triggers up after this.
			r.armSem <- struct{}{}
			if r.armed != nil {
				if err := r.armed.close(); err != nil {
					r.logger.WarnContext(logCtx, "closing config file watcher", "error", err)
				}
				r.armed = nil
			}
			<-r.armSem
			close(r.disarmed)
		}()
	})
	for _, done := range []chan struct{}{r.quiesced, r.disarmed} {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("stopping config reloader: waiting for the reload in flight: %w", ctx.Err())
		}
	}
	return nil
}

// notifySuccess invokes the WithOnReloadSuccess callbacks, recovering a
// panic so a misbehaving callback cannot crash the process.
func (r *Reloader[T]) notifySuccess(ctx context.Context, old, new *T) {
	for _, h := range r.onSuccess {
		func() {
			defer func() {
				if p := recover(); p != nil {
					r.logger.ErrorContext(ctx, "config reload success callback panicked", "panic", p)
				}
			}()
			h.run(old, new)
		}()
	}
}

// notifyReloadError invokes the WithOnReloadError callbacks, with the same
// panic isolation.
func (r *Reloader[T]) notifyReloadError(ctx context.Context, trigger string, err error) {
	for _, fn := range r.onError {
		func() {
			defer func() {
				if p := recover(); p != nil {
					r.logger.ErrorContext(ctx, "config reload error callback panicked", "panic", p)
				}
			}()
			fn(trigger, err)
		}()
	}
}

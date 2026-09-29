package config

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Reloader watches configuration sources and atomically reloads the config
// struct when changes are detected. Access the current config via Config(),
// which uses atomic.Pointer for zero read contention.
//
// On reload failure (parse error, validation failure), the previous config
// is preserved and the error is reported through [WithOnReloadError], the
// log, and — for [Reloader.Reload] — the return value.
//
// Reloads are serialized. A manual Reload that finds another reload in flight
// returns [ErrReloadInProgress]; an automatic trigger (file change, poll,
// signal) instead waits for it and then reloads, so a change that lands while
// another reload is reading the sources is never lost.
type Reloader[T any] struct {
	current    atomic.Pointer[T]
	configOpts []Option
	logger     *slog.Logger

	// Reload options.
	onChange      []func(old, new *T)
	onReloadError []func(trigger string, err error)
	fileWatch     bool
	debounce      time.Duration
	pollInterval  time.Duration
	signals       []os.Signal

	// slot is the single-flight token: holding it is the right to run the
	// load pipeline, publish, and fire callbacks. Stop takes it and never
	// gives it back.
	slot chan struct{}
	// loadedFiles are the config files the last successful load read.
	// Guarded by slot.
	loadedFiles []string
	stats       atomic.Pointer[ReloadStats] // written while holding slot, read lock-free

	watchArmed atomic.Bool // set once Start has registered the watcher's dirs
	stopOnce   sync.Once
	stopCh     chan struct{}
	quiesced   chan struct{} // closed once Stop holds the slot
}

// watcherReady reports whether Start has finished arming the fsnotify watcher.
// Start is asynchronous by design (callers run it in a goroutine), so tests
// that mutate the filesystem need a way to know the watch is live before they
// do. Deliberately unexported: production code that polls this is a smell —
// Start already returns an error when arming fails.
func (r *Reloader[T]) watcherReady() bool { return r.watchArmed.Load() }

// Reload trigger names. These form a closed vocabulary so consumers can use
// them directly as a bounded metric label.
const (
	TriggerManual     = "manual"      // an explicit Reload call (including ReloadHandler)
	TriggerFileChange = "file_change" // WithFileWatch saw the file or its symlink target change
	TriggerPoll       = "poll"        // WithPollInterval ticked
	TriggerSignal     = "signal"      // WithSignalReload received one of its signals
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
	// no reload has succeeded since construction. Note that the initial load
	// performed by NewReloader is not a reload and does not set this.
	LastSuccess time.Time
	// LastFailure is when the most recent failed reload was rejected. Zero if
	// none has failed.
	LastFailure time.Time
	// LastError is the error from the most recent attempt, or nil when that
	// attempt succeeded. A non-nil LastError alongside a recent LastSuccess
	// cannot happen: this field is cleared on every success.
	LastError error
}

// ReloadOption configures a Reloader.
type ReloadOption[T any] func(*Reloader[T])

// DefaultFileWatchDebounce is the [WithFileWatch] debounce window applied
// when the option's argument is zero or negative. 500ms is large enough to
// coalesce editor rename+write sequences yet small enough that operators
// see fresh config within one human reaction time.
const DefaultFileWatchDebounce = 500 * time.Millisecond

// WithOnChange registers a callback invoked after a successful reload.
// Multiple callbacks are invoked in registration order. The callback
// receives the old and new config pointers. Callbacks run while the reload
// holds the single-flight slot: long-running work should be handed off, and a
// callback must not call [Reloader.Stop], which waits for that slot.
func WithOnChange[T any](fn func(old, new *T)) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.onChange = append(r.onChange, fn)
	}
}

// WithFileWatch enables fsnotify-based watching of config files specified
// via WithConfigFile / WithMustConfigFile. When a watched file changes,
// the full GetConfig[T] pipeline is re-run after debounce elapses.
//
// debounce coalesces bursts of rapid filesystem events (editors that issue
// rename+write, atomic-write toolchains) into a single reload. Pass 0 to
// select [DefaultFileWatchDebounce] (500ms), or a positive value to override.
// Negative values are treated as the default.
//
// Detected: direct writes, write-temp-then-rename saves, and symlink swaps —
// including Kubernetes ConfigMap and Secret volume updates, where the watched
// path is a symlink into `..data` and the leaf file is never written.
//
// NOT detected, all three requiring [WithPollInterval] instead:
//
//   - ConfigMap/Secret volumes mounted with subPath. kubelet cannot swap a
//     symlink through a bind mount, so these never update in the container at
//     all — no watcher can help.
//   - Sources that change without a filesystem event: environment variables,
//     and any provider wired via [WithProvider].
//   - On kqueue platforms (macOS, *BSD) only: a symlink relink whose sole
//     trace is a temporary name that is renamed away, e.g.
//     `ln -s new link.tmp && mv -Tf link.tmp link`. fsnotify detects directory
//     changes there by diffing directory listings, so a change that leaves the
//     set of names unchanged produces no event at all. Kubernetes volumes are
//     unaffected — kubelet leaves a durable new `..<timestamp>` directory —
//     and Linux inotify reports the rename directly, so this is macOS/BSD
//     hand-rolled-deploy only.
func WithFileWatch[T any](debounce time.Duration) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.fileWatch = true
		r.debounce = debounce
	}
}

// WithPollInterval enables periodic reload at the given interval.
// On each tick the full GetConfig[T] pipeline is re-run. This is useful
// as a fallback for environments where fsnotify is unreliable (NFS, some
// container runtimes) or when config sources change without file events
// (e.g., environment variables).
func WithPollInterval[T any](interval time.Duration) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.pollInterval = interval
	}
}

// WithSignalReload triggers a reload when the specified OS signals are
// received (typically syscall.SIGHUP).
func WithSignalReload[T any](signals ...os.Signal) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.signals = append(r.signals, signals...)
	}
}

// WithOnReloadError registers a callback invoked after every FAILED reload,
// including the ones driven by the internal watchers in [Reloader.Start],
// where the error would otherwise reach nothing but the log. It fires in
// addition to — not instead of — the error [Reloader.Reload] returns, so a
// single registration covers every trigger.
//
// trigger is one of the Trigger* constants. Callbacks run in registration
// order and panics are recovered, matching [WithOnChange], including the rule
// that a callback must not call [Reloader.Stop]. Keep them cheap: they run on
// the reload path.
//
// Reloads that reach no verdict — [ErrReloadInProgress], [ErrReloaderStopped],
// or a reload abandoned because its context ended — do NOT invoke it.
func WithOnReloadError[T any](fn func(trigger string, err error)) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.onReloadError = append(r.onReloadError, fn)
	}
}

// NewReloader creates a Reloader that loads initial config via
// GetConfig[T](ctx, configOpts...) and optionally watches for changes.
// The configOpts are stored and re-used on every reload (each reload uses
// the ctx passed to Reload, or to Start for automatic triggers). The logger
// set by [WithLogger] in configOpts also receives every reload event.
func NewReloader[T any](ctx context.Context, reloadOpts []ReloadOption[T], configOpts ...Option) (*Reloader[T], error) {
	o := buildOptions(configOpts)
	cfg, files, err := load[T](ctx, o)
	if err != nil {
		return nil, err
	}

	r := &Reloader[T]{
		configOpts:  configOpts,
		logger:      o.logger,
		slot:        make(chan struct{}, 1),
		loadedFiles: files,
		stopCh:      make(chan struct{}),
		quiesced:    make(chan struct{}),
	}
	r.current.Store(cfg)

	for _, opt := range reloadOpts {
		opt(r)
	}

	return r, nil
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
func (r *Reloader[T]) reloadHeld(ctx context.Context, trigger string) (*T, error) {
	o := buildOptions(r.configOpts)
	o.requireFiles(r.loadedFiles)

	cfg, files, err := load[T](ctx, o)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The reload was abandoned, not refuted: say nothing about the
			// sources, which may be fine.
			if !errors.Is(err, ctxErr) {
				err = errors.Join(ctxErr, err)
			}
			return nil, fmt.Errorf("config reload abandoned: %w", err)
		}
		reloadErr := fmt.Errorf("%w: %w", ErrReload, err)
		r.recordAttempt(trigger, reloadErr)
		r.notifyReloadError(trigger, reloadErr)
		return nil, reloadErr
	}

	r.loadedFiles = files
	old := r.current.Swap(cfg)
	r.recordAttempt(trigger, nil)
	r.notifyCallbacks(old, cfg)
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

// Start runs the configured watchers — file watch, poll interval, signals —
// until Stop is called or ctx ends. It returns nil immediately when none is
// configured or the reloader is already stopped, nil after Stop, and ctx.Err()
// when ctx ends. Typically called in a goroutine: go reloader.Start(ctx).
//
// Every automatic reload runs on Start's goroutine with a context that Stop
// also cancels, and the file-watch debounce timer is owned by the same loop,
// so no automatic reload can begin once Start has returned.
func (r *Reloader[T]) Start(ctx context.Context) error {
	paths := filePaths(r.configOpts)
	hasFileWatch := r.fileWatch && len(paths) > 0
	hasPoll := r.pollInterval > 0
	hasSignal := len(r.signals) > 0

	if !hasFileWatch && !hasPoll && !hasSignal {
		return nil
	}
	select {
	case <-r.stopCh:
		return nil
	default:
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Stop cancels runCtx so an automatic reload in flight abandons its
	// secret lookups instead of holding Stop up. Exits when Start returns.
	go func() {
		select {
		case <-r.stopCh:
			cancel()
		case <-runCtx.Done():
		}
	}()

	var (
		fw        *fileWatch
		events    <-chan fsnotify.Event
		watchErrs <-chan error
		settled   <-chan time.Time
	)
	if hasFileWatch {
		var err error
		fw, err = newFileWatch(paths, cmp.Or(max(r.debounce, 0), DefaultFileWatchDebounce), r.logger)
		if err != nil {
			return fmt.Errorf("setting up file watch: %w", err)
		}
		defer func() {
			if err := fw.close(); err != nil {
				r.logger.WarnContext(ctx, "closing config file watcher", "error", err)
			}
		}()
		events, watchErrs, settled = fw.watcher.Events, fw.watcher.Errors, fw.timer.C
		r.watchArmed.Store(true)
	}

	var sigCh <-chan os.Signal
	if hasSignal {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, r.signals...)
		sigCh = ch
		defer signal.Stop(ch)
	}

	var tickCh <-chan time.Time
	if hasPoll {
		ticker := time.NewTicker(r.pollInterval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	r.logger.InfoContext(ctx, "config reloader started",
		"file_watch", hasFileWatch,
		"poll_interval", r.pollInterval,
		"signal_count", len(r.signals),
	)

	for {
		select {
		case <-runCtx.Done():
			select {
			case <-r.stopCh:
				r.logger.InfoContext(ctx, "config reloader stopped")
				return nil
			default:
				r.logger.InfoContext(ctx, "config reloader context cancelled")
				return ctx.Err()
			}

		case <-tickCh:
			r.autoReload(runCtx, TriggerPoll)

		case sig := <-sigCh:
			r.logger.InfoContext(ctx, "config reload triggered by signal", "signal", sig)
			r.autoReload(runCtx, TriggerSignal)

		case ev, ok := <-events:
			if !ok {
				r.logger.ErrorContext(ctx, "config file watcher closed unexpectedly; file-change reloads are off")
				events, watchErrs, settled = nil, nil, nil
				continue
			}
			fw.observe(ev)

		case err, ok := <-watchErrs:
			if !ok {
				watchErrs = nil
				continue
			}
			r.logger.ErrorContext(ctx, "config file watcher error", "error", err)

		case <-settled:
			if path, changed := fw.settle(); changed {
				r.autoReload(runCtx, TriggerFileChange, "path", path)
			}
		}
	}
}

// autoReload runs one watcher-driven reload, waiting for any reload already in
// flight so the change that triggered this one is read after it lands.
func (r *Reloader[T]) autoReload(ctx context.Context, trigger string, attrs ...any) {
	if err := r.acquire(ctx, trigger, true); err != nil {
		return // stopping: Stop or ctx ended, and Start's loop exits next
	}
	defer r.release()

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

// Stop stops the reloader and waits until no reload is running. After Stop
// returns nil, no reload — automatic or manual — will run again, and no
// WithOnChange or WithOnReloadError callback will fire: it is safe to tear
// down whatever the callbacks touch. Reload returns [ErrReloaderStopped] from
// then on, and Start returns.
//
// An automatic reload in flight is cancelled. A manual Reload in flight runs
// to completion under its caller's context; if ctx ends first, Stop returns
// ctx's error and the reloader finishes stopping in the background once that
// reload returns. Stop is idempotent and safe for concurrent use, but must
// not be called from a WithOnChange or WithOnReloadError callback: those run
// holding the slot Stop waits for.
func (r *Reloader[T]) Stop(ctx context.Context) error {
	r.stopOnce.Do(func() {
		close(r.stopCh)
		go func() {
			// Taking the slot waits out whichever reload holds it; never
			// giving it back is what keeps later reloads from starting.
			r.slot <- struct{}{}
			close(r.quiesced)
		}()
	})
	select {
	case <-r.quiesced:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("stopping config reloader: waiting for the reload in flight: %w", ctx.Err())
	}
}

// notifyCallbacks invokes all registered onChange callbacks, recovering
// from panics to prevent a misbehaving callback from crashing the
// application.
func (r *Reloader[T]) notifyCallbacks(old, new *T) {
	for _, fn := range r.onChange {
		func() {
			defer func() {
				if p := recover(); p != nil {
					r.logger.Error("config change callback panicked", "panic", p)
				}
			}()
			fn(old, new)
		}()
	}
}

// notifyReloadError invokes all registered onReloadError callbacks, with the
// same panic isolation notifyCallbacks applies.
func (r *Reloader[T]) notifyReloadError(trigger string, err error) {
	for _, fn := range r.onReloadError {
		func() {
			defer func() {
				if p := recover(); p != nil {
					r.logger.Error("config reload-error callback panicked", "panic", p)
				}
			}()
			fn(trigger, err)
		}()
	}
}

// filePaths extracts config file paths from the config options.
func filePaths(opts []Option) []string {
	o := buildOptions(opts)
	paths := make([]string, 0, len(o.configFiles))
	for _, cf := range o.configFiles {
		paths = append(paths, cf.path)
	}
	return paths
}

// fileWatch is the file-watch state behind Start. It is driven entirely by
// Start's loop — events in through observe, the debounce timer out through
// settle — so it needs no locking, and closing it stops the timer for good.
//
// Two detection paths, because config reaches a process two different ways:
//
//   - Direct write to the file, including the write-temp-then-rename pattern
//     editors and atomic-write toolchains use. Caught by watching the file's
//     PARENT DIRECTORY and matching events by name.
//   - Symlink swap, where the watched path is a link whose target moves and
//     the leaf path itself is never written. This is how Kubernetes publishes
//     ConfigMap and Secret volumes (kubelet writes a new timestamped dir, then
//     renames `..data` over the old one; every key is a symlink into `..data`)
//     and how release-directory deploys work (`ln -sfn`). fsnotify does not
//     follow symlinks, so a name match never fires. Caught by resolving each
//     path up front, additionally watching the resolved target's directory,
//     and re-resolving on any unmatched event in a watched directory.
type fileWatch struct {
	watcher *fsnotify.Watcher
	logger  *slog.Logger

	// watchedFiles is the set of absolute paths the operator declared.
	watchedFiles map[string]bool
	// resolved maps each watched path to its symlink target as last observed.
	// A path that is not a symlink maps to itself and never changes, so it
	// costs one comparison per settle and never triggers the symlink branch.
	resolved map[string]string
	dirs     map[string]bool

	// timer is the debounce: every event resets it, so a burst collapses into
	// one settle. Created stopped; fires only once armed by observe.
	timer    *time.Timer
	debounce time.Duration
	// pendingDirect names the watched file a Write/Create landed on since the
	// last settle, or "" if the pending events were all indirect.
	pendingDirect string
}

func newFileWatch(paths []string, debounce time.Duration, logger *slog.Logger) (*fileWatch, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating fsnotify watcher: %w", err)
	}

	timer := time.NewTimer(debounce)
	timer.Stop()
	w := &fileWatch{
		watcher:      watcher,
		logger:       logger,
		watchedFiles: make(map[string]bool, len(paths)),
		resolved:     make(map[string]string, len(paths)),
		dirs:         make(map[string]bool),
		timer:        timer,
		debounce:     debounce,
	}

	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			logger.Warn("failed to resolve config file path", "path", p, "error", err)
			continue
		}
		w.watchedFiles[absPath] = true
		w.addDir(filepath.Dir(absPath))

		// Record the path's current symlink target and watch that directory
		// too, so an in-place edit of the target file is seen as well.
		target := resolveTarget(absPath)
		w.resolved[absPath] = target
		if target != absPath {
			w.addDir(filepath.Dir(target))
		}
	}
	return w, nil
}

func (w *fileWatch) addDir(dir string) {
	if w.dirs[dir] {
		return
	}
	if err := w.watcher.Add(dir); err != nil {
		w.logger.Warn("failed to watch directory", "dir", dir, "error", err)
		return
	}
	w.dirs[dir] = true
}

// observe records one filesystem event and (re)arms the debounce. A
// Write/Create on a watched file is a definite change. Anything else in a
// watched directory is only a hint — it may be the first step of a symlink
// swap, or an unrelated sibling file — decided after the debounce, in settle.
func (w *fileWatch) observe(ev fsnotify.Event) {
	absName, err := filepath.Abs(ev.Name)
	if err != nil {
		return
	}
	if w.watchedFiles[absName] && ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
		w.pendingDirect = absName
	}
	w.timer.Reset(w.debounce)
}

// settle decides, once the filesystem has stopped moving, whether anything the
// reloader reads actually changed, and names the path that did.
//
// Deferring the symlink check to here rather than doing it per event is
// load-bearing. A Kubernetes ConfigMap update emits its events BEFORE the swap
// lands: the new `..2026_…` directory and `..data_tmp` are both created, and
// only then is `..data` renamed — an operation that on some platforms produces
// no further event at all. Re-resolving at event time therefore always reads
// the pre-swap target and concludes nothing changed. Re-resolving after the
// debounce reads the settled state.
func (w *fileWatch) settle() (string, bool) {
	path := w.pendingDirect
	w.pendingDirect = ""
	movedFrom, movedTo, moved := w.refreshResolved()

	if moved {
		w.logger.Debug("config file symlink target changed",
			"old_target", movedFrom, "new_target", movedTo)
	}
	switch {
	case path != "":
		return path, true
	case moved:
		return movedTo, true
	default:
		// Events fired but nothing we read moved — a sibling file in a
		// shared directory, or the first half of a swap we already handled.
		return "", false
	}
}

// refreshResolved re-resolves every watched path, records any new targets,
// and subscribes to a new target's directory so a later in-place edit there
// is still seen. Reports the last move observed, if any.
func (w *fileWatch) refreshResolved() (from, to string, moved bool) {
	for path, was := range w.resolved {
		now := resolveTarget(path)
		if now == was {
			continue
		}
		w.resolved[path] = now
		if now != path {
			w.addDir(filepath.Dir(now))
		}
		from, to, moved = was, now, true
	}
	return from, to, moved
}

func (w *fileWatch) close() error {
	w.timer.Stop()
	return w.watcher.Close()
}

// resolveTarget returns path with symlinks resolved, or path itself when it
// cannot be resolved. Returning the input on failure is deliberate: a path
// that is briefly dangling mid-swap must not read as "the target changed" on
// the way out and again on the way back, which would reload twice for one swap.
func resolveTarget(path string) string {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return target
}

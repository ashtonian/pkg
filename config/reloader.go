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
// is preserved and the error is logged.
type Reloader[T any] struct {
	current    atomic.Pointer[T]
	configOpts []Option

	// Reload options.
	onChange      []func(old, new *T)
	onReloadError []func(trigger string, err error)
	logger        *slog.Logger
	fileWatch     bool
	debounce      time.Duration
	pollInterval  time.Duration
	signals       []os.Signal

	// Runtime state.
	reloading  atomic.Bool                 // prevents concurrent reloads
	reloadMu   sync.Mutex                  // single-flight reload
	stats      atomic.Pointer[ReloadStats] // written under reloadMu, read lock-free
	watchArmed atomic.Bool                 // set once setupFileWatch has registered its dirs
	stopOnce   sync.Once
	stopCh     chan struct{}
	watcher    *fsnotify.Watcher // nil if no file watch; owned by Start's goroutine
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
// Single-flight skips are not counted: no pipeline ran, so nothing was
// attempted and nothing went stale.
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
// receives the old and new config pointers. Long-running callbacks
// should spawn goroutines to avoid blocking the reload loop.
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
// order and panics are recovered, matching [WithOnChange]. Keep them cheap:
// they run on the reload path.
//
// Single-flight skips ([ErrReloadInProgress]) do NOT invoke it — no reload was
// attempted and no config went stale as a result.
func WithOnReloadError[T any](fn func(trigger string, err error)) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.onReloadError = append(r.onReloadError, fn)
	}
}

// WithReloadLogger sets the logger for reload events.
func WithReloadLogger[T any](logger *slog.Logger) ReloadOption[T] {
	return func(r *Reloader[T]) {
		r.logger = logger
	}
}

// NewReloader creates a Reloader that loads initial config via
// GetConfig[T](ctx, configOpts...) and optionally watches for changes.
// The configOpts are stored and re-used on every reload (each reload uses
// the ctx passed to Reload).
func NewReloader[T any](ctx context.Context, reloadOpts []ReloadOption[T], configOpts ...Option) (*Reloader[T], error) {
	cfg, err := GetConfig[T](ctx, configOpts...)
	if err != nil {
		return nil, err
	}

	r := &Reloader[T]{
		configOpts: configOpts,
		logger:     slog.Default(),
		stopCh:     make(chan struct{}),
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
// [ErrReload] is returned.
//
// Concurrent calls are serialized. If a reload is already in flight this call
// is skipped and returns [ErrReloadInProgress], which deliberately does NOT
// wrap ErrReload: the in-flight reload reads the same source state this call
// would have, so nothing failed and nothing is stale. Callers that want "any
// non-success" check both sentinels; callers that alert on breakage check
// ErrReload alone.
func (r *Reloader[T]) Reload(ctx context.Context) (*T, error) {
	return r.reload(ctx, TriggerManual)
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

// reload runs one attempt attributed to trigger. Every reload path funnels
// through here so stats, callbacks, and logging stay uniform across manual,
// file-watch, poll, and signal triggers.
func (r *Reloader[T]) reload(ctx context.Context, trigger string) (*T, error) {
	if !r.reloading.CompareAndSwap(false, true) {
		return nil, ErrReloadInProgress
	}
	defer r.reloading.Store(false)

	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	cfg, err := GetConfig[T](ctx, r.configOpts...)
	if err != nil {
		reloadErr := fmt.Errorf("%w: %v", ErrReload, err)
		r.recordAttempt(trigger, reloadErr)
		r.notifyReloadError(trigger, reloadErr)
		return nil, reloadErr
	}

	old := r.current.Swap(cfg)
	r.recordAttempt(trigger, nil)
	r.notifyCallbacks(old, cfg)
	return cfg, nil
}

// recordAttempt folds one completed attempt into the stats snapshot. Called
// only with reloadMu held, so the read-modify-write needs no further
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

// Start begins watching for config changes. It blocks until Stop is
// called or the context is cancelled. If no watch sources are configured
// (no file watch, no poll interval, no signals), Start returns nil
// immediately. Typically called in a goroutine: go reloader.Start(ctx).
func (r *Reloader[T]) Start(ctx context.Context) error {
	paths := filePaths(r.configOpts)
	hasFileWatch := r.fileWatch && len(paths) > 0
	hasPoll := r.pollInterval > 0
	hasSignal := len(r.signals) > 0

	if !hasFileWatch && !hasPoll && !hasSignal {
		return nil
	}

	// File watch setup.
	if hasFileWatch {
		if err := r.setupFileWatch(ctx, paths); err != nil {
			return fmt.Errorf("setting up file watch: %w", err)
		}
		defer r.watcher.Close()
	}

	// Signal handler.
	var sigCh <-chan os.Signal
	if hasSignal {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, r.signals...)
		sigCh = ch
		defer signal.Stop(ch)
	}

	// Poll ticker.
	var tickCh <-chan time.Time
	if hasPoll {
		ticker := time.NewTicker(r.pollInterval)
		tickCh = ticker.C
		defer ticker.Stop()
	}

	r.logger.Info("config reloader started",
		"file_watch", hasFileWatch,
		"poll_interval", r.pollInterval,
		"signal_count", len(r.signals),
	)

	for {
		select {
		case <-tickCh:
			if _, err := r.reload(ctx, TriggerPoll); err != nil {
				r.logReloadFailure(TriggerPoll, err)
			} else {
				r.logger.Debug("config reloaded", "trigger", TriggerPoll)
			}

		case sig := <-sigCh:
			r.logger.Info("config reload triggered by signal", "signal", sig)
			if _, err := r.reload(ctx, TriggerSignal); err != nil {
				r.logReloadFailure(TriggerSignal, err)
			}

		case <-r.stopCh:
			r.logger.Info("config reloader stopped")
			return nil

		case <-ctx.Done():
			r.logger.Info("config reloader context cancelled")
			return ctx.Err()
		}
	}
}

// Stop signals the reloader to stop. Safe to call multiple times.
func (r *Reloader[T]) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
}

// setupFileWatch creates an fsnotify watcher for the configured config file
// paths and starts a goroutine that triggers debounced reloads on change.
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
func (r *Reloader[T]) setupFileWatch(ctx context.Context, paths []string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	r.watcher = watcher

	w := &fileWatch[T]{
		r:            r,
		watcher:      watcher,
		watchedFiles: make(map[string]bool, len(paths)),
		resolved:     make(map[string]string, len(paths)),
		dirs:         make(map[string]bool),
		debounce:     cmp.Or(max(r.debounce, 0), DefaultFileWatchDebounce),
	}

	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			r.logger.Warn("failed to resolve config file path", "path", p, "error", err)
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

	go w.run(ctx)

	r.watchArmed.Store(true)
	return nil
}

// fileWatch is the state behind one Reloader's file-watch goroutine. It exists
// as a type rather than a pile of closures because the debounce timer fires on
// its own goroutine and shares the resolution map with the event loop.
type fileWatch[T any] struct {
	r       *Reloader[T]
	watcher *fsnotify.Watcher

	// watchedFiles is the set of absolute paths the operator declared.
	watchedFiles map[string]bool

	mu sync.Mutex
	// resolved maps each watched path to its symlink target as last observed.
	// A path that is not a symlink maps to itself and never changes, so it
	// costs one comparison per settle and never triggers the symlink branch.
	resolved map[string]string
	dirs     map[string]bool
	timer    *time.Timer
	// pendingDirect names the watched file a Write/Create landed on since the
	// last settle, or "" if the pending events were all indirect.
	pendingDirect string
	debounce      time.Duration
}

func (w *fileWatch[T]) addDir(dir string) {
	if w.dirs[dir] {
		return
	}
	if err := w.watcher.Add(dir); err != nil {
		w.r.logger.Warn("failed to watch directory", "dir", dir, "error", err)
		return
	}
	w.dirs[dir] = true
}

func (w *fileWatch[T]) run(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			w.r.logger.Error("file watcher goroutine panicked", "panic", p)
		}
	}()
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			absName, err := filepath.Abs(event.Name)
			if err != nil {
				continue
			}
			// A Write/Create on a watched file is a definite change. Anything
			// else in a watched directory is only a hint — it may be the first
			// step of a symlink swap, or an unrelated sibling file. Both cases
			// are decided after the debounce, in settle().
			direct := w.watchedFiles[absName] && event.Op&(fsnotify.Write|fsnotify.Create) != 0
			w.schedule(ctx, absName, direct)

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.r.logger.Error("file watcher error", "error", err)
		}
	}
}

// schedule (re)arms the debounce timer. Every event resets it, so a burst
// collapses into one settle().
func (w *fileWatch[T]) schedule(ctx context.Context, path string, direct bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if direct {
		w.pendingDirect = path
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() { w.settle(ctx) })
}

// settle decides, once the filesystem has stopped moving, whether anything the
// reloader reads actually changed.
//
// Deferring the symlink check to here rather than doing it per event is
// load-bearing. A Kubernetes ConfigMap update emits its events BEFORE the swap
// lands: the new `..2026_…` directory and `..data_tmp` are both created, and
// only then is `..data` renamed — an operation that on some platforms produces
// no further event at all. Re-resolving at event time therefore always reads
// the pre-swap target and concludes nothing changed. Re-resolving after the
// debounce reads the settled state.
func (w *fileWatch[T]) settle(ctx context.Context) {
	w.mu.Lock()
	path := w.pendingDirect
	w.pendingDirect = ""
	movedFrom, movedTo, moved := w.refreshResolvedLocked()
	w.mu.Unlock()

	if path == "" {
		if !moved {
			// Events fired but nothing we read moved — a sibling file in a
			// shared directory, or the first half of a swap we already handled.
			return
		}
		path = movedTo
	}
	if moved {
		w.r.logger.Debug("config file symlink target changed",
			"old_target", movedFrom, "new_target", movedTo)
	}

	if _, err := w.r.reload(ctx, TriggerFileChange); err != nil {
		w.r.logReloadFailure(TriggerFileChange, err, "path", path)
		return
	}
	w.r.logger.Info("config reloaded", "trigger", TriggerFileChange, "path", path)
}

// refreshResolvedLocked re-resolves every watched path, records any new
// targets, and subscribes to a new target's directory so a later in-place edit
// there is still seen. Reports the last move observed, if any.
func (w *fileWatch[T]) refreshResolvedLocked() (from, to string, moved bool) {
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

// logReloadFailure reports a watcher-driven reload failure. A single-flight
// skip is logged at Debug, not Error: another goroutine is loading the same
// source state, so the outcome the operator cares about is still on its way.
func (r *Reloader[T]) logReloadFailure(trigger string, err error, attrs ...any) {
	args := append([]any{"trigger", trigger, "error", err}, attrs...)
	if errors.Is(err, ErrReloadInProgress) {
		r.logger.Debug("config reload skipped, another reload in flight", args...)
		return
	}
	r.logger.Error("config reload failed", args...)
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

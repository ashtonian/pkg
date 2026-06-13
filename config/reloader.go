package config

import (
	"context"
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
	onChange     []func(old, new *T)
	logger       *slog.Logger
	fileWatch    bool
	debounce     time.Duration
	pollInterval time.Duration
	signals      []os.Signal

	// Runtime state.
	reloading atomic.Bool // prevents concurrent reloads
	reloadMu  sync.Mutex  // single-flight reload
	stopOnce  sync.Once
	stopCh    chan struct{}
	watcher   *fsnotify.Watcher // nil if no file watch
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
// success. On failure the previous config is preserved and an error
// wrapping ErrReload is returned. Concurrent calls are serialized; if a
// reload is already in progress the call is skipped and ErrReload is returned.
func (r *Reloader[T]) Reload(ctx context.Context) (*T, error) {
	if !r.reloading.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("%w: reload already in progress", ErrReload)
	}
	defer r.reloading.Store(false)

	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	cfg, err := GetConfig[T](ctx, r.configOpts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrReload, err)
	}

	old := r.current.Swap(cfg)
	r.notifyCallbacks(old, cfg)
	return cfg, nil
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
			if _, err := r.Reload(ctx); err != nil {
				r.logger.Error("config reload failed", "trigger", "poll", "error", err)
			} else {
				r.logger.Debug("config reloaded", "trigger", "poll")
			}

		case sig := <-sigCh:
			r.logger.Info("config reload triggered by signal", "signal", sig)
			if _, err := r.Reload(ctx); err != nil {
				r.logger.Error("config reload failed", "trigger", "signal", "error", err)
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

// setupFileWatch creates an fsnotify watcher for the configured config
// file paths and starts a goroutine that triggers debounced reloads on
// file change events.
func (r *Reloader[T]) setupFileWatch(ctx context.Context, paths []string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating fsnotify watcher: %w", err)
	}
	r.watcher = watcher

	// Track which absolute file paths we care about.
	watchedFiles := make(map[string]bool, len(paths))
	dirs := make(map[string]bool)

	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			r.logger.Warn("failed to resolve config file path", "path", p, "error", err)
			continue
		}
		watchedFiles[absPath] = true

		// Watch the parent directory (fsnotify works better with directories
		// and this handles editor atomic save patterns: write tmp → rename).
		dir := filepath.Dir(absPath)
		if dirs[dir] {
			continue
		}
		if err := watcher.Add(dir); err != nil {
			r.logger.Warn("failed to watch directory", "dir", dir, "error", err)
			continue
		}
		dirs[dir] = true
	}

	debounce := r.debounce
	if debounce <= 0 {
		debounce = DefaultFileWatchDebounce
	}

	var timerMu sync.Mutex
	var timer *time.Timer

	go func() {
		defer func() {
			if p := recover(); p != nil {
				r.logger.Error("file watcher goroutine panicked", "panic", p)
			}
		}()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				absName, err := filepath.Abs(event.Name)
				if err != nil || !watchedFiles[absName] {
					continue
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}

				timerMu.Lock()
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(debounce, func() {
					if _, err := r.Reload(ctx); err != nil {
						r.logger.Error("config reload failed",
							"trigger", "file_change",
							"path", absName,
							"error", err,
						)
					} else {
						r.logger.Info("config reloaded",
							"trigger", "file_change",
							"path", absName,
						)
					}
				})
				timerMu.Unlock()

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				r.logger.Error("file watcher error", "error", err)
			}
		}
	}()

	return nil
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

// filePaths extracts config file paths from the config options.
func filePaths(opts []Option) []string {
	o := buildOptions(opts)
	paths := make([]string, 0, len(o.configFiles))
	for _, cf := range o.configFiles {
		paths = append(paths, cf.path)
	}
	return paths
}

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// fileWatch is the file-watch state behind Run. It is driven entirely by
// Run's loop — events in through observe, the debounce timer and the recheck
// ticker out through settle — so it needs no locking, and closing it stops
// the timer for good.
//
// Config reaches a process three ways, and each is detected:
//
//   - Direct write to the file, including the write-temp-then-rename pattern
//     editors and atomic-write toolchains use, and removal. Caught by
//     watching the file's directory and matching events by name.
//   - Symlink swap, where the watched path is a link whose target moves and
//     the leaf path itself is never written. This is how Kubernetes publishes
//     ConfigMap and Secret volumes (kubelet writes a new timestamped dir, then
//     renames `..data` over the old one; every key is a symlink into `..data`)
//     and how release-directory deploys work (`ln -sfn`). fsnotify does not
//     follow symlinks, so a name match never fires. Caught by watching every
//     directory holding a symlink the path resolves through, and re-resolving
//     after every burst of events.
//   - An edit of a symlink's current target, matched by the target's name.
//
// Paths resolve as the kernel resolves them (resolvePath): a ".." after a
// symlink climbs from the link's target, not from the link, so the watch
// follows the file the loader opens. The subscriptions are reconciled after
// every burst to exactly the directories resolution needs — and, while a
// component is missing, the deepest one that exists, so a directory created
// later is noticed. A watched directory removed, replaced or renamed away has
// lost its watch, or kept one on the old directory; reconciling notices by
// the directory's identity. A replacement further up — an ancestor renamed
// away and recreated — sends no event to any watched directory, so Run also
// reconciles every recheck interval with no event to prompt it.
type fileWatch struct {
	watcher *fsnotify.Watcher
	logger  *slog.Logger

	// declared is every path the operator declared, absolute but not
	// lexically cleaned: cleaning "link/../x" before resolving link would
	// name a different file.
	declared []string
	// paths holds each declared path's resolution as last observed.
	paths map[string]resolution
	// needed is the set of directories the last reconcile subscribed.
	needed map[string]bool
	// watching maps each directory subscribed to the directory it was when
	// subscribed, so a replacement at the same path is told apart.
	watching map[string]os.FileInfo

	// timer is the debounce: every event resets it, so a burst collapses into
	// one settle. Created stopped; fires only once armed by observe.
	timer    *time.Timer
	debounce time.Duration
	// burstStart is when the pending burst's first event arrived, zero when
	// idle. A burst may postpone settle by at most maxDebounceWindows
	// debounce windows, so a steadily written sibling file in a watched
	// directory cannot hold a real change off forever.
	burstStart time.Time
	// pendingDirect names the declared path a write, create, removal or
	// rename of it or its target landed on since the last settle, or "" if
	// the pending events were all indirect.
	pendingDirect string
	// recheckEvery is how often Run reconciles with no event to prompt it.
	recheckEvery time.Duration
}

// recheckWindows sets the recheck interval in debounce windows; it is never
// shorter than minRecheck.
const (
	recheckWindows = 4
	minRecheck     = time.Second
)

// newFileWatch subscribes to every directory paths need. A subscription that
// cannot be made fails construction: a watch that silently covers less than
// it was asked to would never report the changes it misses.
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
		paths:        make(map[string]resolution, len(paths)),
		watching:     make(map[string]os.FileInfo),
		timer:        timer,
		debounce:     debounce,
		recheckEvery: max(recheckWindows*debounce, minRecheck),
	}
	for _, p := range paths {
		abs, err := absolute(p)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("resolving config file path %q: %w", p, err), w.close())
		}
		w.declared = append(w.declared, abs)
		w.paths[abs] = resolvePath(abs)
	}
	if _, err := w.reconcile(); err != nil {
		return nil, errors.Join(err, w.close())
	}
	return w, nil
}

// absolute makes p absolute without cleaning it.
func absolute(p string) (string, error) {
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + string(filepath.Separator) + p, nil
}

// resolution is how the kernel resolves one path.
type resolution struct {
	// target is the file the path names, every symlink resolved; "" while a
	// component is missing.
	target string
	// entry is where the path's last name sits: that name in its resolved
	// directory. It is the target unless the path ends in a symlink.
	entry string
	// dirs are the directories the resolution depends on: each holding a
	// symlink it passes through, the target's, or — while a component is
	// missing — the deepest one that exists.
	dirs []string
}

// resolvePath resolves path component by component as the kernel does: a
// symlink is replaced in place by its target, ".." climbs from the directory
// resolved so far, and every component a name is looked up in must be a
// directory — "file/../x", "file/." and "file/" do not resolve.
func resolvePath(path string) resolution {
	type part struct {
		name string
		last bool // the path's own last name, not one from a link target
	}
	var r resolution
	vol := filepath.VolumeName(path)
	dir := vol + string(filepath.Separator)
	names := splitPath(path[len(vol):])
	if strings.HasSuffix(path, string(filepath.Separator)) {
		names = append(names, ".") // a trailing separator requires a directory
	}
	rest := make([]part, len(names))
	for i, n := range names {
		rest[i] = part{name: n, last: i == len(names)-1}
	}
	for hops := 0; len(rest) > 0; {
		p := rest[0]
		rest = rest[1:]
		switch p.name {
		case ".":
			continue
		case "..":
			dir = filepath.Dir(dir)
			continue
		}
		next := filepath.Join(dir, p.name)
		if p.last {
			r.entry = next
		}
		info, err := os.Lstat(next)
		if err != nil {
			r.dirs = append(r.dirs, dir)
			return r
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			if len(rest) > 0 && !info.IsDir() {
				r.dirs = append(r.dirs, dir) // a name looked up in a file: ENOTDIR
				return r
			}
			dir = next
			continue
		}
		r.dirs = append(r.dirs, dir)
		hops++
		target, err := os.Readlink(next)
		if err != nil || hops > maxLinkHops {
			return r
		}
		if filepath.IsAbs(target) {
			tvol := filepath.VolumeName(target)
			dir = tvol + string(filepath.Separator)
			target = target[len(tvol):]
		}
		expanded := splitPath(target)
		parts := make([]part, 0, len(expanded)+len(rest))
		for _, n := range expanded {
			parts = append(parts, part{name: n})
		}
		rest = append(parts, rest...)
	}
	r.target = dir
	r.dirs = append(r.dirs, filepath.Dir(dir))
	return r
}

// maxLinkHops bounds resolution like the kernel's own symlink limit, so a
// link loop ends.
const maxLinkHops = 40

func splitPath(p string) []string {
	var parts []string
	for part := range strings.SplitSeq(p, string(filepath.Separator)) {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// resolveTarget is the file path names, or path itself while it does not
// resolve.
func resolveTarget(path string) string {
	if r := resolvePath(path); r.target != "" {
		return r.target
	}
	return path
}

// reconcile subscribes to the directories the declared paths need now and
// drops every other subscription. shifted reports that the set of
// directories changed — one appeared, vanished, or was replaced at its path —
// since the last reconcile: a file the reloader reads may have changed with
// it, before any watch could see it.
func (w *fileWatch) reconcile() (shifted bool, err error) {
	live := make(map[string]bool)
	for _, dir := range w.watcher.WatchList() {
		live[dir] = true
	}
	needed := make(map[string]bool)
	var errs []error
	for _, p := range w.declared {
		for _, dir := range w.paths[p].dirs {
			watched, replaced, err := w.subscribe(dir, live)
			if err != nil {
				errs = append(errs, err)
			}
			needed[watched] = true
			shifted = shifted || replaced
		}
	}
	shifted = shifted || w.needed != nil && !maps.Equal(w.needed, needed)
	w.needed = needed

	for dir := range w.watching {
		if needed[dir] {
			continue
		}
		// A directory that was deleted has already lost its watch;
		// removing it again fails harmlessly.
		_ = w.watcher.Remove(dir)
		delete(w.watching, dir)
	}
	return shifted, errors.Join(errs...)
}

// subscribe makes sure dir — or, should it vanish meanwhile, its nearest
// existing ancestor — is watched, and returns the directory watched. replaced
// reports that the watch on it had been lost or was pinned to a directory no
// longer at that path, and was made afresh. live is the watcher's own list of
// watches; subscribe adds to it what it subscribes.
func (w *fileWatch) subscribe(dir string, live map[string]bool) (watched string, replaced bool, err error) {
	for {
		watched = nearestExistingDir(dir)
		info, statErr := os.Stat(watched)
		prev, tracked := w.watching[watched]
		if tracked && live[watched] && statErr == nil && os.SameFile(prev, info) {
			return watched, replaced, nil
		}
		if tracked {
			_ = w.watcher.Remove(watched) // drop a watch still pinned to the old directory
			delete(w.watching, watched)
			delete(live, watched)
			replaced = true
		}
		if statErr == nil {
			statErr = w.watcher.Add(watched)
		}
		switch {
		case statErr == nil:
			w.watching[watched] = info
			live[watched] = true
			return watched, replaced, nil
		case errors.Is(statErr, fs.ErrNotExist) && watched != filepath.Dir(watched):
			continue // removed since it was found; watch what is there now
		default:
			return watched, replaced, fmt.Errorf("watching config directory %q: %w", watched, statErr)
		}
	}
}

// nearestExistingDir returns dir, or its closest ancestor that exists.
func nearestExistingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

// owner returns the declared path an event on name affects directly: name is
// where that path's last name sits, or its current target.
func (w *fileWatch) owner(name string) (string, bool) {
	for _, p := range w.declared {
		if r := w.paths[p]; name == r.entry || name == r.target {
			return p, true
		}
	}
	return "", false
}

// observe records one filesystem event and (re)arms the debounce. A write,
// create, removal or rename of a declared file or its target is a definite
// change. Anything else in a watched directory is only a hint — it may be
// the first step of a symlink swap, a directory on the way to a declared
// path, or an unrelated sibling file — decided after the debounce, in settle.
func (w *fileWatch) observe(ev fsnotify.Event) {
	if p, ok := w.owner(filepath.Clean(ev.Name)); ok && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
		w.pendingDirect = p
	}
	now := time.Now()
	if w.burstStart.IsZero() {
		w.burstStart = now
	}
	deadline := w.burstStart.Add(maxDebounceWindows * w.debounce)
	w.timer.Reset(max(min(w.debounce, deadline.Sub(now)), 0))
}

// maxDebounceWindows bounds how long a continuous stream of events can delay
// a file-change reload, in debounce windows.
const maxDebounceWindows = 10

// settle decides, once the filesystem has stopped moving, whether anything the
// reloader reads actually changed, and names the path that did. A
// subscription that can no longer be made is returned as err; the change is
// still reported.
//
// Deferring the symlink check to here rather than doing it per event is
// load-bearing. A Kubernetes ConfigMap update emits its events BEFORE the swap
// lands: the new `..2026_…` directory and `..data_tmp` are both created, and
// only then is `..data` renamed — an operation that on some platforms produces
// no further event at all. Re-resolving at event time therefore always reads
// the pre-swap target and concludes nothing changed. Re-resolving after the
// debounce reads the settled state.
func (w *fileWatch) settle() (path string, changed bool, err error) {
	path = w.pendingDirect
	w.pendingDirect = ""
	w.burstStart = time.Time{}
	movedFrom, movedTo, moved := w.refresh()
	shifted, err := w.reconcile()

	if moved {
		w.logger.Debug("config file symlink target changed",
			"old_target", movedFrom, "new_target", movedTo)
	}
	switch {
	case path != "":
		return path, true, err
	case moved && movedTo != "":
		return movedTo, true, err
	case moved:
		return movedFrom, true, err
	case shifted:
		return "", true, err
	default:
		// Events fired but nothing we read moved — a sibling file in a
		// shared directory, or the first half of a swap we already handled.
		return "", false, err
	}
}

// recheck is settle with no event to prompt it, for a change no watched
// directory reports. It does nothing while a burst is pending: that burst's
// settle is due anyway.
func (w *fileWatch) recheck() (path string, changed bool, err error) {
	if !w.burstStart.IsZero() {
		return "", false, nil
	}
	return w.settle()
}

// refresh re-resolves every declared path and reports the last move
// observed: a new target, or a path that stopped or started resolving — a
// directory on its way removed, a link it passes through deleted. A path
// caught dangling in the middle of a swap reloads then, failing and keeping
// the live config, and again once the swap lands.
func (w *fileWatch) refresh() (from, to string, moved bool) {
	for _, p := range w.declared {
		was, now := w.paths[p], resolvePath(p)
		w.paths[p] = now
		if now.target != was.target {
			from, to, moved = was.target, now.target, true
		}
	}
	return from, to, moved
}

func (w *fileWatch) close() error {
	w.timer.Stop()
	return w.watcher.Close()
}

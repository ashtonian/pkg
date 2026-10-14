package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

// fileWatch is the file-watch state behind Run. It is driven entirely by
// Start's loop — events in through observe, the debounce timer out through
// settle — so it needs no locking, and closing it stops the timer for good.
//
// Config reaches a process three ways, and each is detected:
//
//   - Direct write to the file, including the write-temp-then-rename pattern
//     editors and atomic-write toolchains use, and removal. Caught by
//     watching the file's PARENT DIRECTORY and matching events by name.
//   - Symlink swap, where the watched path is a link whose target moves and
//     the leaf path itself is never written. This is how Kubernetes publishes
//     ConfigMap and Secret volumes (kubelet writes a new timestamped dir, then
//     renames `..data` over the old one; every key is a symlink into `..data`)
//     and how release-directory deploys work (`ln -sfn`). fsnotify does not
//     follow symlinks, so a name match never fires. Caught by resolving each
//     path, watching the resolved target's directory too, and re-resolving
//     after every burst of events in a watched directory.
//   - An edit of a symlink's current target, matched by the target's name.
//
// The subscriptions are reconciled after every burst: the parent of each
// declared path and of each current target, and every directory holding a
// symlink along a declared path, so relinking a directory component
// (`current -> releases/v2`) is noticed like relinking the file. While one of
// them does not exist, its nearest existing ancestor is watched instead, so a
// directory created later is still noticed. Nothing else is watched, so
// retired release directories are dropped. A watched directory that was
// removed, replaced or renamed away has lost its watch, or kept one on the
// old directory; reconciling notices by the directory's identity and watches
// what is at the path now.
type fileWatch struct {
	watcher *fsnotify.Watcher
	logger  *slog.Logger

	// declared is every path the operator declared, absolute.
	declared []string
	// resolved maps each declared path to its symlink target as last
	// observed; a path that is not a symlink maps to itself.
	resolved map[string]string
	// reach maps each directory a declared path needs to the directory
	// watched for it: itself, or its nearest existing ancestor.
	reach map[string]string
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
}

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
		watcher:  watcher,
		logger:   logger,
		resolved: make(map[string]string, len(paths)),
		reach:    make(map[string]string),
		watching: make(map[string]os.FileInfo),
		timer:    timer,
		debounce: debounce,
	}
	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("resolving config file path %q: %w", p, err), w.close())
		}
		w.declared = append(w.declared, absPath)
		w.resolved[absPath] = resolveTarget(absPath)
	}
	if _, err := w.reconcile(); err != nil {
		return nil, errors.Join(err, w.close())
	}
	return w, nil
}

// reconcile subscribes to the directories the declared paths need now and
// drops every other subscription. appeared reports a needed directory that
// came into existence, or was replaced, since the last reconcile: a file
// inside it may have changed with it, before any watch could see it.
func (w *fileWatch) reconcile() (appeared bool, err error) {
	want := make(map[string]bool, 2*len(w.declared))
	for _, p := range w.declared {
		want[filepath.Dir(p)] = true
		want[filepath.Dir(w.resolved[p])] = true
		for _, dir := range linkDirs(p) {
			want[dir] = true
		}
	}
	live := make(map[string]bool)
	for _, dir := range w.watcher.WatchList() {
		live[dir] = true
	}

	desired := make(map[string]bool, len(want))
	reach := make(map[string]string, len(want))
	var errs []error
	for dir := range want {
		watched, replaced, err := w.subscribe(dir, live)
		if err != nil {
			errs = append(errs, err)
		}
		if was, ok := w.reach[dir]; replaced || ok && was != dir && watched == dir {
			appeared = true
		}
		reach[dir] = watched
		desired[watched] = true
	}
	w.reach = reach

	for dir := range w.watching {
		if desired[dir] {
			continue
		}
		// A directory that was deleted has already lost its watch;
		// removing it again fails harmlessly.
		_ = w.watcher.Remove(dir)
		delete(w.watching, dir)
	}
	return appeared, errors.Join(errs...)
}

// subscribe makes sure dir — or, while it does not exist, its nearest
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

// linkDirs returns every directory holding a symlink that resolving path
// passes through — the directory components and the file itself — following
// each link's target as the kernel does. A component that does not exist
// ends the walk; nearestExistingDir covers what lies beyond it.
func linkDirs(path string) []string {
	var dirs []string
	root := filepath.VolumeName(path) + string(filepath.Separator)
	dir, rest := root, splitPath(strings.TrimPrefix(path, root))
	for hops := 0; len(rest) > 0 && hops <= maxLinkHops; {
		next := filepath.Join(dir, rest[0])
		info, err := os.Lstat(next)
		if err != nil {
			break
		}
		rest = rest[1:]
		if info.Mode()&fs.ModeSymlink == 0 {
			dir = next
			continue
		}
		hops++
		dirs = append(dirs, dir)
		target, err := os.Readlink(next)
		if err != nil {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		root = filepath.VolumeName(target) + string(filepath.Separator)
		dir, rest = root, append(splitPath(strings.TrimPrefix(filepath.Clean(target), root)), rest...)
	}
	return dirs
}

// maxLinkHops bounds linkDirs like the kernel's own symlink limit, so a link
// loop ends.
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
// that path or its current symlink target.
func (w *fileWatch) owner(name string) (string, bool) {
	for _, p := range w.declared {
		if name == p || name == w.resolved[p] {
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
	absName, err := filepath.Abs(ev.Name)
	if err != nil {
		return
	}
	if p, ok := w.owner(absName); ok && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 {
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
	movedFrom, movedTo, moved := w.refreshResolved()
	appeared, err := w.reconcile()

	if moved {
		w.logger.Debug("config file symlink target changed",
			"old_target", movedFrom, "new_target", movedTo)
	}
	switch {
	case path != "":
		return path, true, err
	case moved:
		return movedTo, true, err
	case appeared:
		return "", true, err
	default:
		// Events fired but nothing we read moved — a sibling file in a
		// shared directory, or the first half of a swap we already handled.
		return "", false, err
	}
}

// refreshResolved re-resolves every declared path and records new targets.
// Reports the last move observed, if any.
func (w *fileWatch) refreshResolved() (from, to string, moved bool) {
	for _, path := range w.declared {
		was, now := w.resolved[path], resolveTarget(path)
		if now == was {
			continue
		}
		w.resolved[path] = now
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

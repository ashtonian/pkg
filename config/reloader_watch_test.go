//go:build unit

package config

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// watchedReloader starts a file-watching reloader over opts and returns the
// channel its successful reloads arrive on.
func watchedReloader(t *testing.T, opts ...Option) (*Reloader[reloaderCfg], <-chan *reloaderCfg) {
	t.Helper()
	changed := make(chan *reloaderCfg, 4)
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		append(opts, WithSkipValidation()),
		WithFileWatch(50*time.Millisecond),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) { neu := ev.New; changed <- neu }),
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	armReloader(t, r)
	go func() { _ = r.Run(ctx) }()
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	return r, changed
}

func awaitHost(t *testing.T, changed <-chan *reloaderCfg, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case cfg := <-changed:
			if cfg.Host == want {
				return
			}
		case <-deadline:
			t.Fatalf("timeout: no reload produced host %q", want)
		}
	}
}

// Editing the file a symlink points at changes what the reloader reads, though
// the declared path is never written.
func TestFileWatch_EditOfTheSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "releases", "target.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("host: before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	_, changed := watchedReloader(t, WithRequiredFile(link))
	if err := os.WriteFile(target, []byte("host: after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	awaitHost(t, changed, "after")
}

// Removing a file the running config was read from is a change: the reload
// it triggers fails, because a file read once stays required, and the
// failure is reported rather than waiting for some later trigger.
func TestFileWatch_RemovalIsReported(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("host: from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	r, err := NewReloader[reloaderCfg](
		t.Context(),
		[]Option{WithFile(file), WithSkipValidation()},
		WithFileWatch(50*time.Millisecond),
		WithOnReloadError(func(_ string, err error) {
			select {
			case failed <- err:
			default:
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armReloader(t, r)
	go func() { _ = r.Run(ctx) }()
	defer func() { _ = r.Stop(context.Background()) }()

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failed:
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("reload error %v does not identify the missing file", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: removing the config file triggered no reload")
	}
	if r.Config().Host != "from-file" {
		t.Fatalf("a failed reload replaced the config: host %q", r.Config().Host)
	}
}

// An optional file inside a directory that does not exist yet is picked up
// once the directory and the file appear.
func TestFileWatch_DirectoryCreatedLater(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "later", "deeper", "config.yaml")
	_, changed := watchedReloader(t, WithFile(file))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("host: appeared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	awaitHost(t, changed, "appeared")
}

// Relinking a path to new release directories keeps exactly the
// subscriptions the current layout needs.
func TestFileWatch_RelinksDropRetiredDirectories(t *testing.T) {
	root := realTempDir(t)
	link := filepath.Join(root, "current.yaml")
	release := func(name string) string {
		p := filepath.Join(root, name, "config.yaml")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("host: "+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := os.Symlink(release("v1"), link); err != nil {
		t.Fatal(err)
	}
	w, err := newFileWatch([]string{link}, time.Millisecond, discardLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	for _, name := range []string{"v2", "v3", "v4"} {
		target := release(name)
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, changed, err := w.settle(); err != nil || !changed {
			t.Fatalf("relink to %s: changed=%v err=%v", name, changed, err)
		}
	}
	_, watchesRoot := w.watching[root]
	_, watchesV4 := w.watching[filepath.Join(root, "v4")]
	if len(w.watching) != 2 || !watchesRoot || !watchesV4 {
		t.Fatalf("watching %v; want only the link's directory and the current release", slices.Collect(maps.Keys(w.watching)))
	}
	if got := len(w.watcher.WatchList()); got != 2 {
		t.Fatalf("%d kernel watches after three relinks, want 2", got)
	}
}

// The decision itself, independent of what a platform's backend reports: an
// event on a symlink's target, or a removal or rename of a declared file, is
// a change; a chmod is not. Events carry the resolved names of the watched
// directories, so the test's directory is named without symlinks.
func TestFileWatch_EventsOnTargetsAndRemovals(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "target.yaml")
	plain := filepath.Join(dir, "plain.yaml")
	link := filepath.Join(dir, "link.yaml")
	for _, p := range []string{target, plain} {
		if err := os.WriteFile(p, []byte("host: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	w, err := newFileWatch([]string{link, plain}, time.Millisecond, discardLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	for _, tc := range []struct {
		event   fsnotify.Event
		changed bool
		path    string
	}{
		{fsnotify.Event{Name: resolveTarget(link), Op: fsnotify.Write}, true, link},
		{fsnotify.Event{Name: plain, Op: fsnotify.Remove}, true, plain},
		{fsnotify.Event{Name: plain, Op: fsnotify.Rename}, true, plain},
		{fsnotify.Event{Name: plain, Op: fsnotify.Chmod}, false, ""},
	} {
		w.observe(tc.event)
		path, changed, err := w.settle()
		if err != nil || changed != tc.changed || path != tc.path {
			t.Errorf("%s: settle = (%q, %v, %v), want (%q, %v)", tc.event, path, changed, err, tc.path, tc.changed)
		}
	}
}

// A change made after NewReloader loaded and before the watch existed
// produces no event: Arm finds it by comparing the files with what was loaded,
// and Start reloads it. A change after Arm and before Start is reported by the
// watch Arm set up. Unchanged files cause no reload.
func TestFileWatch_ChangeBeforeStartIsNotMissed(t *testing.T) {
	newReloader := func(t *testing.T) (*Reloader[reloaderCfg], string, <-chan *reloaderCfg, *logRecorder) {
		file := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(file, []byte("host: at-load\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		changed := make(chan *reloaderCfg, 1)
		logs := newLogRecorder()
		r, err := NewReloader[reloaderCfg](
			t.Context(),
			[]Option{WithFile(file), WithSkipValidation(), WithLogger(slog.New(logs))},
			WithFileWatch(50*time.Millisecond),
			WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[reloaderCfg]) { neu := ev.New; changed <- neu }),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r, file, changed, logs
	}
	write := func(t *testing.T, file, content string) {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("unchanged", func(t *testing.T) {
		r, _, _, logs := newReloader(t)
		armReloader(t, r)
		runReloader(t, r)
		logs.waitFor(t, "config reloader started")
		if s := r.Stats(); s.Attempts != 0 {
			t.Fatalf("Start reloaded unchanged files (%d attempts)", s.Attempts)
		}
	})
	t.Run("before Arm", func(t *testing.T) {
		r, file, changed, _ := newReloader(t)
		write(t, file, "host: before-arm\n")
		armReloader(t, r)
		runReloader(t, r)
		awaitHost(t, changed, "before-arm")
	})
	t.Run("between Arm and Start", func(t *testing.T) {
		r, file, changed, _ := newReloader(t)
		armReloader(t, r)
		write(t, file, "host: after-arm\n")
		runReloader(t, r)
		awaitHost(t, changed, "after-arm")
	})
}

// realTempDir is a temporary directory named without symlinks, so a test of
// symlink handling is not also watching the links above the temp root
// (macOS's /var -> /private/var).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// waitForEvent waits for the watcher to report an event on name, feeding
// every event it sees to the watch as Start's loop would.
func waitForEvent(t *testing.T, w *fileWatch, name string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-w.watcher.Events:
			w.observe(ev)
			if ev.Name == name {
				return
			}
		case err := <-w.watcher.Errors:
			t.Fatalf("watcher error: %v", err)
		case <-deadline:
			t.Fatalf("no event on %s", name)
		}
	}
}

// A watched directory removed and recreated at the same path has lost its
// watch, or kept one on the directory that is gone. Reconciling watches the
// new directory, reports the change, and later edits are seen.
func TestFileWatch_RecreatedDirectoryIsWatchedAgain(t *testing.T) {
	dir := filepath.Join(realTempDir(t), "config")
	file := filepath.Join(dir, "config.yaml")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, "port: 9000\n")
	w, err := newFileWatch([]string{file}, time.Hour, discardLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, file, "port: 9001\n")
	if _, changed, err := w.settle(); err != nil || !changed {
		t.Fatalf("settle after the directory was replaced: changed=%v err=%v", changed, err)
	}
	if !slices.Contains(w.watcher.WatchList(), dir) {
		t.Fatalf("watches %v after the directory was replaced; want %s", w.watcher.WatchList(), dir)
	}

	writeFile(t, file, "port: 9002\n")
	waitForEvent(t, w, file)
}

// A symlink among the directory components is watched where it lives, so
// relinking it is seen and the watch follows the new target.
func TestFileWatch_DirectoryComponentSymlink(t *testing.T) {
	root := realTempDir(t)
	for _, name := range []string{"v1", "v2"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(root, name, "config.yaml"), "release: "+name+"\n")
	}
	link := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Join(root, "v1"), link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(link, "config.yaml")
	w, err := newFileWatch([]string{file}, time.Hour, discardLogger)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	if !slices.Contains(w.watcher.WatchList(), root) {
		t.Fatalf("watches %v; want the directory holding the link, %s", w.watcher.WatchList(), root)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "v2"), link); err != nil {
		t.Fatal(err)
	}
	// A durable new name: kqueue reports a directory change by the names it
	// gained, and relinking under the same name gains none.
	marker := filepath.Join(root, "deployed")
	writeFile(t, marker, "v2")
	waitForEvent(t, w, marker)

	path, changed, err := w.settle()
	if err != nil || !changed {
		t.Fatalf("settle after relinking a directory component: changed=%v err=%v", changed, err)
	}
	want := filepath.Join(root, "v2", "config.yaml")
	if path != want || w.paths[file].target != want {
		t.Fatalf("settle reported %q, resolved %q; want %q", path, w.paths[file].target, want)
	}
	if !slices.Contains(w.watcher.WatchList(), filepath.Join(root, "v2")) {
		t.Fatalf("watches %v; want the new target directory", w.watcher.WatchList())
	}
}

// resolvePath resolves as the kernel does: links in place, ".." from the
// resolved directory, and every directory the resolution looks a name up in
// that it depends on.
func TestResolvePath(t *testing.T) {
	root := realTempDir(t)
	for _, dir := range []string{"real", "b", "actual/child"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "real", "c.yaml"), "x: 1\n")
	writeFile(t, filepath.Join(root, "actual", "c.yaml"), "x: 2\n")
	// a -> b (relative directory link); b/c.yaml -> ../real/c.yaml (file link).
	if err := os.Symlink("b", filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "real", "c.yaml"), filepath.Join(root, "b", "c.yaml")); err != nil {
		t.Fatal(err)
	}
	r := resolvePath(filepath.Join(root, "a", "c.yaml"))
	assert.Equal(t, filepath.Join(root, "real", "c.yaml"), r.target)
	assert.Equal(t, filepath.Join(root, "b", "c.yaml"), r.entry, "the path's last name sits in b, through a")
	assert.Equal(t, []string{root, filepath.Join(root, "b"), filepath.Join(root, "real")}, r.dirs)

	// ".." after a link climbs from the link's target, as the kernel does.
	if err := os.Symlink(filepath.Join(root, "actual", "child"), filepath.Join(root, "jump")); err != nil {
		t.Fatal(err)
	}
	dotdot := root + "/jump/../c.yaml"
	want, err := filepath.EvalSymlinks(dotdot)
	require.NoError(t, err)
	r = resolvePath(dotdot)
	assert.Equal(t, want, r.target)
	assert.Equal(t, filepath.Join(root, "actual", "c.yaml"), r.target)

	r = resolvePath(filepath.Join(root, "real", "c.yaml"))
	assert.Equal(t, []string{filepath.Join(root, "real")}, r.dirs, "no links: only the target's directory")

	r = resolvePath(filepath.Join(root, "missing", "deeper", "c.yaml"))
	assert.Empty(t, r.target)
	assert.Equal(t, []string{root}, r.dirs, "a missing component: the deepest directory that exists")

	// A loop ends.
	if err := os.Symlink("y", filepath.Join(root, "x")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("x", filepath.Join(root, "y")); err != nil {
		t.Fatal(err)
	}
	r = resolvePath(filepath.Join(root, "x", "c.yaml"))
	assert.Empty(t, r.target)
	assert.LessOrEqual(t, len(r.dirs), maxLinkHops+1)
}

// A ".." after a symlinked directory climbs from the link's target, so the
// watch follows the file the loader opens, not the one lexical cleaning names.
func TestFileWatch_DotDotAfterSymlink(t *testing.T) {
	root := realTempDir(t)
	for _, dir := range []string{"actual/child", "declared"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "actual", "child"), filepath.Join(root, "declared", "jump")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "declared", "jump") + "/../config.yaml"
	writeFile(t, file, "value: first\n")
	w, err := newFileWatch([]string{file}, time.Hour, discardLogger)
	require.NoError(t, err)
	defer w.close()

	opened, err := filepath.EvalSymlinks(file)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "actual", "config.yaml"), opened)
	assert.Equal(t, opened, w.paths[file].target)
	assert.Contains(t, w.watcher.WatchList(), filepath.Join(root, "actual"))
	assert.Contains(t, w.watcher.WatchList(), filepath.Join(root, "declared"), "the directory holding the link")
}

// A link whose target climbs out of another link (config.yaml ->
// branch/jump/../config.yaml) depends on branch too: relinking jump moves the
// target, and the watch sees it.
func TestFileWatch_RelinkInsideALinkTarget(t *testing.T) {
	root := realTempDir(t)
	for _, dir := range []string{"branch", "v1/child", "v2/child"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(root, "v1", "config.yaml"), "value: first\n")
	writeFile(t, filepath.Join(root, "v2", "config.yaml"), "value: second\n")
	jump := filepath.Join(root, "branch", "jump")
	if err := os.Symlink(filepath.Join(root, "v1", "child"), jump); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "config.yaml")
	if err := os.Symlink("branch/jump/../config.yaml", file); err != nil {
		t.Fatal(err)
	}
	w, err := newFileWatch([]string{file}, time.Hour, discardLogger)
	require.NoError(t, err)
	defer w.close()
	require.Contains(t, w.watcher.WatchList(), filepath.Join(root, "branch"))

	require.NoError(t, os.Remove(jump))
	require.NoError(t, os.Symlink(filepath.Join(root, "v2", "child"), jump))
	marker := filepath.Join(root, "branch", "deployed") // a durable name kqueue reports
	writeFile(t, marker, "v2")
	waitForEvent(t, w, marker)

	path, changed, err := w.settle()
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, filepath.Join(root, "v2", "config.yaml"), path)
}

// A watched directory moved away is a change: the reload that follows finds
// the file gone and fails, instead of the reloader serving it as if present.
func TestFileWatch_DirectoryMovedAwayIsAChange(t *testing.T) {
	root := realTempDir(t)
	dir := filepath.Join(root, "config")
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeFile(t, file, "value: first\n")
	w, err := newFileWatch([]string{file}, time.Hour, discardLogger)
	require.NoError(t, err)
	defer w.close()

	require.NoError(t, os.Rename(dir, filepath.Join(root, "old")))
	w.observe(fsnotify.Event{Name: dir, Op: fsnotify.Rename})
	_, changed, err := w.settle()
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Contains(t, w.watcher.WatchList(), root, "the deepest directory that still exists")
}

// An ancestor renamed away and recreated sends no event to the directory the
// watch holds, which follows the old tree; Run's recheck notices the
// replacement and reloads the new file.
func TestReloader_AncestorReplacedIsReloaded(t *testing.T) {
	root := realTempDir(t)
	parent := filepath.Join(root, "config")
	dir := filepath.Join(parent, "nested")
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeFile(t, file, "port: 1\n")
	r, err := NewReloader[portCfg](t.Context(),
		[]Option{WithFile(file), WithSkipValidation(), WithLogger(discardLogger())},
		WithFileWatch(10*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	armReloader(t, r)
	runReloader(t, r)

	require.NoError(t, os.Rename(parent, filepath.Join(root, "old")))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeFile(t, file, "port: 2\n")
	eventually(t, func() bool { return r.Config().Port == 2 }, "the recheck to reload the replaced tree")
}

// testWatch watches paths with a millisecond debounce, closed at cleanup.
func testWatch(t *testing.T, paths ...string) *fileWatch {
	t.Helper()
	w, err := newFileWatch(paths, time.Millisecond, discardLogger)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, w.close()) })
	return w
}

// A path that stops resolving is a change even when the directories watched
// keep their names: a removed directory the path climbs out of with "..", or
// a removed directory link it passes through.
func TestFileWatch_PathThatStopsResolvingIsAChange(t *testing.T) {
	t.Run("directory before ..", func(t *testing.T) {
		root := realTempDir(t)
		branch := filepath.Join(root, "branch")
		require.NoError(t, os.Mkdir(branch, 0o700))
		writeFile(t, filepath.Join(root, "config.yaml"), "value: one\n")
		file := branch + "/../config.yaml"
		w := testWatch(t, file)

		require.NoError(t, os.Remove(branch))
		w.observe(fsnotify.Event{Name: branch, Op: fsnotify.Remove})
		_, changed, err := w.settle()
		require.NoError(t, err)
		assert.True(t, changed)
	})
	t.Run("directory link", func(t *testing.T) {
		root := realTempDir(t)
		writeFile(t, filepath.Join(root, "config.yaml"), "value: one\n")
		link := filepath.Join(root, "current")
		require.NoError(t, os.Symlink(".", link))
		file := filepath.Join(link, "config.yaml")
		w := testWatch(t, file)

		require.NoError(t, os.Remove(link))
		w.observe(fsnotify.Event{Name: link, Op: fsnotify.Remove})
		_, changed, err := w.settle()
		require.NoError(t, err)
		assert.True(t, changed)
	})
}

// Every component before the last must be a directory, as the kernel
// requires; a trailing separator requires one too.
func TestResolvePath_NonDirectoryComponentResolvesNothing(t *testing.T) {
	root := realTempDir(t)
	regular := filepath.Join(root, "regular")
	writeFile(t, regular, "ordinary file")
	writeFile(t, filepath.Join(root, "config.yaml"), "value: one\n")
	for _, p := range []string{regular + "/../config.yaml", regular + "/.", regular + "/"} {
		_, statErr := os.Stat(p)
		require.Error(t, statErr, "the kernel refuses %q", p)
		assert.Empty(t, resolvePath(p).target, p)
	}
}

// resolvePath agrees with the kernel on the shapes a path can take.
func TestResolvePath_AgreesWithTheKernel(t *testing.T) {
	root := realTempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "child"), 0o700))
	file := filepath.Join(root, "config.yaml")
	writeFile(t, file, "value: one\n")
	require.NoError(t, os.Symlink("config.yaml", filepath.Join(root, "relative")))
	require.NoError(t, os.Symlink(file, filepath.Join(root, "absolute")))
	t.Chdir(filepath.Join(root, "child"))
	fromWD, err := absolute("../config.yaml")
	require.NoError(t, err)

	for _, p := range []string{
		root + "/./child/../config.yaml",
		filepath.Join(root, "relative"),
		filepath.Join(root, "absolute"),
		"/../../" + strings.TrimPrefix(file, "/"),
		fromWD,
	} {
		want, err := filepath.EvalSymlinks(p)
		require.NoError(t, err)
		assert.Equal(t, want, resolvePath(p).target, p)
	}
}

// A write to the file a path names lexically, which is not the file it
// resolves to, is not a change.
func TestFileWatch_LexicalTwinIsNotAChange(t *testing.T) {
	root := realTempDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "actual", "child"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "declared"), 0o700))
	link := filepath.Join(root, "declared", "jump")
	require.NoError(t, os.Symlink(filepath.Join(root, "actual", "child"), link))
	file := link + "/../config.yaml"
	writeFile(t, file, "value: current\n")
	twin := filepath.Clean(file)
	writeFile(t, twin, "value: unrelated\n")
	w := testWatch(t, file)

	w.observe(fsnotify.Event{Name: twin, Op: fsnotify.Write})
	_, changed, err := w.settle()
	require.NoError(t, err)
	assert.False(t, changed)
}

// The periodic recheck reports nothing while idle, leaves a pending burst to
// settle, and does not report a change settle already reported.
func TestFileWatch_RecheckLeavesPendingBurstsToSettle(t *testing.T) {
	root := realTempDir(t)
	parent := filepath.Join(root, "parent")
	dir := filepath.Join(parent, "nested")
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeFile(t, file, "value: first\n")
	w := testWatch(t, file)

	recheckIsQuiet := func(when string) {
		t.Helper()
		_, changed, err := w.recheck()
		require.NoError(t, err)
		assert.False(t, changed, when)
	}
	recheckIsQuiet("idle")
	recheckIsQuiet("idle, again")

	w.observe(fsnotify.Event{Name: filepath.Join(dir, "sibling"), Op: fsnotify.Write})
	require.NoError(t, os.Rename(parent, filepath.Join(root, "old")))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	writeFile(t, file, "value: second\n")
	recheckIsQuiet("a burst is pending")

	_, changed, err := w.settle()
	require.NoError(t, err)
	assert.True(t, changed, "settle reports the replaced ancestor")
	recheckIsQuiet("after settle")
	recheckIsQuiet("after settle, again")
}

// Events that keep arriving for other files delay a pending reload by a
// bounded number of debounce windows; they never starve it.
func TestFileWatch_ContinuousEventsDoNotStarveAReload(t *testing.T) {
	root := realTempDir(t)
	file := filepath.Join(root, "config.yaml")
	writeFile(t, file, "value: first\n")
	w := testWatch(t, file)
	w.debounce = 2 * time.Millisecond
	w.observe(fsnotify.Event{Name: file, Op: fsnotify.Write})

	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	deadline := time.After(time.Second)
	for {
		select {
		case <-tick.C:
			w.observe(fsnotify.Event{Name: filepath.Join(root, "sibling"), Op: fsnotify.Write})
		case <-w.timer.C:
			_, changed, err := w.settle()
			require.NoError(t, err)
			assert.True(t, changed)
			return
		case <-deadline:
			t.Fatal("continuous events starved the pending reload")
		}
	}
}

// A directory link removed from the path makes the file unreadable; Run
// reports the failed reload instead of keeping quiet.
func TestReloader_LostDirectoryLinkIsReported(t *testing.T) {
	root := realTempDir(t)
	writeFile(t, filepath.Join(root, "config.yaml"), "port: 1\n")
	link := filepath.Join(root, "current")
	require.NoError(t, os.Symlink(".", link))
	r, err := NewReloader[portCfg](t.Context(),
		[]Option{WithRequiredFile(filepath.Join(link, "config.yaml")), WithSkipValidation(), WithLogger(discardLogger())},
		WithFileWatch(5*time.Millisecond),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	armReloader(t, r)
	runReloader(t, r)

	require.NoError(t, os.Remove(link))
	eventually(t, func() bool { return r.Stats().Failures > 0 }, "the failed reload to be reported")
	assert.Equal(t, 1, r.Config().Port, "the previous config stays live")
}

// A relink whose only trace is a temporary name renamed over the link
// (`ln -s new link.tmp && mv -Tf link.tmp link`) leaves kqueue's directory
// listing unchanged, so it may send no event; the recheck finds the new
// target anyway.
func TestFileWatch_RecheckCatchesASilentRelink(t *testing.T) {
	root := realTempDir(t)
	for _, v := range []string{"v1", "v2"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, v), 0o700))
		writeFile(t, filepath.Join(root, v, "config.yaml"), "value: "+v+"\n")
	}
	link := filepath.Join(root, "current")
	require.NoError(t, os.Symlink("v1", link))
	w := testWatch(t, filepath.Join(link, "config.yaml"))

	tmp := filepath.Join(root, "current.tmp")
	require.NoError(t, os.Symlink("v2", tmp))
	require.NoError(t, os.Rename(tmp, link))
	_, changed, err := w.recheck()
	require.NoError(t, err)
	assert.True(t, changed)
}

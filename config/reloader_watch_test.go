//go:build unit

package config

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// watchedReloader starts a file-watching reloader over opts and returns the
// channel its successful reloads arrive on.
func watchedReloader(t *testing.T, opts ...Option) (*Reloader[reloaderCfg], <-chan *reloaderCfg) {
	t.Helper()
	changed := make(chan *reloaderCfg, 4)
	r, err := NewReloader[reloaderCfg](t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithFileWatch[reloaderCfg](50 * time.Millisecond),
			WithOnChange(func(_, neu *reloaderCfg) { changed <- neu }),
		},
		append(opts, WithSkipValidation())...,
	)
	if err != nil {
		t.Fatalf("NewReloader: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = r.Start(ctx) }()
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	waitForWatcher(t, r)
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
	_, changed := watchedReloader(t, WithMustConfigFile(link))
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
	r, err := NewReloader[reloaderCfg](t.Context(),
		[]ReloadOption[reloaderCfg]{
			WithFileWatch[reloaderCfg](50 * time.Millisecond),
			WithOnReloadError[reloaderCfg](func(_ string, err error) {
				select {
				case failed <- err:
				default:
				}
			}),
		},
		WithConfigFile(file), WithSkipValidation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Start(ctx) }()
	defer func() { _ = r.Stop(context.Background()) }()
	waitForWatcher(t, r)

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
	_, changed := watchedReloader(t, WithConfigFile(file))
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
	root := t.TempDir()
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
	w, err := newFileWatch([]string{link}, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	rootDir, _ := filepath.EvalSymlinks(root)
	v4Dir, _ := filepath.EvalSymlinks(filepath.Join(root, "v4"))
	if len(w.watching) != 2 || !w.watching[root] && !w.watching[rootDir] || !w.watching[v4Dir] {
		t.Fatalf("watching %v; want only the link's directory and the current release", w.watching)
	}
	if got := len(w.watcher.WatchList()); got != 2 {
		t.Fatalf("%d kernel watches after three relinks, want 2", got)
	}
}

// The decision itself, independent of what a platform's backend reports: an
// event on a symlink's target, or a removal or rename of a declared file, is
// a change; a chmod is not.
func TestFileWatch_EventsOnTargetsAndRemovals(t *testing.T) {
	dir := t.TempDir()
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
	w, err := newFileWatch([]string{link, plain}, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
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

// A change made after NewReloader loaded but before Start armed the watch
// produces no event; Start compares the files with what was loaded and
// reloads. Unchanged files cause no reload.
func TestFileWatch_ChangeBeforeStartIsNotMissed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("host: at-load\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newReloader := func() (*Reloader[reloaderCfg], <-chan *reloaderCfg) {
		changed := make(chan *reloaderCfg, 1)
		r, err := NewReloader[reloaderCfg](t.Context(),
			[]ReloadOption[reloaderCfg]{
				WithFileWatch[reloaderCfg](50 * time.Millisecond),
				WithOnChange(func(_, neu *reloaderCfg) { changed <- neu }),
			},
			WithConfigFile(file), WithSkipValidation(),
		)
		if err != nil {
			t.Fatal(err)
		}
		return r, changed
	}
	start := func(r *Reloader[reloaderCfg]) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go func() { _ = r.Start(ctx) }()
		t.Cleanup(func() { _ = r.Stop(context.Background()) })
		waitForWatcher(t, r)
	}

	unchanged, _ := newReloader()
	start(unchanged)
	if s := unchanged.Stats(); s.Attempts != 0 {
		t.Fatalf("Start reloaded unchanged files (%d attempts)", s.Attempts)
	}

	r, changed := newReloader()
	if err := os.WriteFile(file, []byte("host: before-start\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	start(r)
	awaitHost(t, changed, "before-start")
}

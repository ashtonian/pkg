package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"
)

// Candidate archives include current source edits, while omitting the ignored
// build output a Git-backed release would never publish. Go's zip package still
// owns module boundaries, vendoring, symlinks and archive validity.
func createWorkingTreeArchive(ctx context.Context, w io.Writer, m module.Version, dir string) error {
	probe := gitCommand(ctx, dir, "rev-parse", "--is-inside-work-tree")
	inside, err := probe.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 128 &&
			strings.HasPrefix(string(exit.Stderr), "fatal: not a git repository") {
			return zip.CreateFromDir(w, m, dir)
		}
		return fmt.Errorf("identify archive working tree: %w", err)
	}
	if strings.TrimSpace(string(inside)) != "true" {
		return fmt.Errorf("archive source is not a Git working tree: %s", dir)
	}
	listed, err := gitCommand(ctx, dir, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", ".").Output()
	if err != nil {
		return fmt.Errorf("list archive source: %w", err)
	}
	paths := strings.Split(strings.TrimSuffix(string(listed), "\x00"), "\x00")
	slices.Sort(paths)
	paths = slices.Compact(paths)
	files := make([]zip.File, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		linked, err := hasSymlinkParent(dir, path)
		if err != nil {
			return fmt.Errorf("inspect archive source %s: %w", path, err)
		}
		if linked {
			continue
		}
		file := workingTreeFile{root: dir, name: path}
		if _, err := file.Lstat(); os.IsNotExist(err) {
			continue // A deleted tracked file is absent from the candidate too.
		} else if err != nil {
			return fmt.Errorf("stat archive source %s: %w", path, err)
		}
		files = append(files, file)
	}
	return zip.Create(w, m, files)
}

// Git can still list a tracked descendant after its directory is replaced by
// a symlink. Omit that subtree just as CreateFromDir does; never read through
// an ancestor link into a different source tree.
func hasSymlinkParent(root, name string) (bool, error) {
	for parent := filepath.Dir(filepath.FromSlash(name)); parent != "."; parent = filepath.Dir(parent) {
		info, err := os.Lstat(filepath.Join(root, parent))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
	}
	return false, nil
}

func gitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// Git's non-repository diagnostic distinguishes source exports from real
	// failures. Keep that diagnostic independent of the invoking user's locale.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd
}

type workingTreeFile struct{ root, name string }

func (f workingTreeFile) Path() string { return f.name }
func (f workingTreeFile) Lstat() (os.FileInfo, error) {
	return os.Lstat(filepath.Join(f.root, filepath.FromSlash(f.name)))
}
func (f workingTreeFile) Open() (io.ReadCloser, error) {
	return os.Open(filepath.Join(f.root, filepath.FromSlash(f.name)))
}

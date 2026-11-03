// Package gitx lists a repository's files as git records them, for a repository's own
// checks. A check that reads what git tracks gives the same answer in every clone, a
// sparse one included, whatever else sits in one working tree.
//
// Three lists cover what such checks need: every tracked path ([Tracked]), the files a
// commit could add ([Untracked]), and the tracked paths that hold content of their own
// ([TrackedRegular]), which leaves out symbolic links and submodules and lists a path
// with an unresolved merge once. The conflictmarkers and mergeinserts packages below it,
// and their commands, are the checks this module ships.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Tracked returns the paths git tracks under root, slash-separated, relative to root and
// in git's order. Paths a sparse checkout leaves out of the working tree are listed.
func Tracked(ctx context.Context, root string) ([]string, error) {
	return list(ctx, root)
}

// Untracked returns the files under root that git neither tracks nor ignores. Together
// with Tracked, that is what a commit can hold.
func Untracked(ctx context.Context, root string) ([]string, error) {
	return list(ctx, root, "--others", "--exclude-standard")
}

// TrackedRegular returns the tracked paths that hold content of their own: symbolic links
// and submodules are left out. A path the index holds at several stages is an unresolved
// merge, and it is listed once.
func TrackedRegular(ctx context.Context, root string) ([]string, error) {
	entries, err := list(ctx, root, "--stage")
	if err != nil {
		return nil, err
	}
	var files []string
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		// Each entry is "<mode> <object> <stage>\t<path>".
		meta, path, ok := strings.Cut(entry, "\t")
		if !ok {
			return nil, fmt.Errorf("git ls-files --stage in %s: an entry without a path: %q", root, entry)
		}
		mode, _, _ := strings.Cut(meta, " ")
		if mode == modeSymlink || mode == modeSubmodule || seen[path] {
			continue
		}
		seen[path] = true
		files = append(files, path)
	}
	return files, nil
}

const (
	modeSymlink   = "120000"
	modeSubmodule = "160000"
)

// list runs git ls-files with -z, so a path holding a newline or a quote arrives as
// written rather than quoted.
func list(ctx context.Context, root string, args ...string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root, "ls-files", "-z"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files %s in %s: %w: %s",
			strings.Join(args, " "), root, err, strings.TrimSpace(stderr.String()))
	}
	var entries []string
	for e := range strings.SplitSeq(string(out), "\x00") {
		if e != "" {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

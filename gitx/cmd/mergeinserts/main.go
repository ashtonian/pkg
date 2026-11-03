// Command mergeinserts is a git merge driver for hand-edited markdown. It merges the
// way git does, then resolves each conflict whose common-ancestor section is empty:
// both sides inserted lines at the same point and neither changed a line the other
// kept. It keeps the current side's lines, then the other side's. During a rebase that
// is upstream's text first, then the replayed commit's. Every other conflict is left
// as git leaves it, with git's markers and labels in the configured conflict style.
//
// Several people or agents append notes, rows and log entries at the same place in a
// shared file, and git refuses to order two insertions at one point. Without the driver
// each such stop needs a hand resolution, and a hand resolution can leave a stray
// marker. An edit to a line the other side also changed still stops the merge, because
// only a person can decide between two versions of the same text.
//
// git runs it from the top of the working tree as
//
//	mergeinserts %O %A %B %L %P %S %X %Y
//
// and reads the result back from the %A file. Exit status 0 means merged, 1 means
// conflicts remain. On any error of its own it falls back to git merge-file, so a
// failure never leaves the current side in place without markers.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/gitx/mergeinserts"
)

const usage = `usage: mergeinserts BASE OURS THEIRS MARKER-SIZE PATH BASE-LABEL OURS-LABEL THEIRS-LABEL
a git merge driver, configured as: mergeinserts %O %A %B %L %P %S %X %Y
`

// driverCommand is the merge.keep-inserts.driver value a repository installs: the
// built tool when the clone has it, and git's own merge otherwise, so a fresh clone or
// a linked worktree conflicts as git does rather than leaving one side's text unmarked.
const driverCommand = "if [ -x .tools/mergeinserts ]; then exec .tools/mergeinserts %O %A %B %L %P %S %X %Y; else exec git merge-file --marker-size=%L -L %X -L %S -L %Y %A %O %B; fi"

func main() {
	args := os.Args[1:]
	if len(args) != 8 {
		_, _ = os.Stderr.WriteString(usage)
		os.Exit(2)
	}
	base, ours, theirs, path := args[0], args[1], args[2], args[4]
	l := mergeinserts.Labels{Base: args[5], Ours: args[6], Theirs: args[7]}
	size, err := strconv.Atoi(args[3])
	if err == nil && size < 1 {
		err = fmt.Errorf("marker size %d", size)
	}
	if err != nil {
		size = 7
		os.Exit(fallBack(path, ours, base, theirs, size, l, err))
	}
	r, err := mergeinserts.Merge(ours, base, theirs, size, conflictStyle(), l)
	if err == nil {
		err = os.WriteFile(ours, []byte(r.Text), 0o644)
	}
	if err != nil {
		os.Exit(fallBack(path, ours, base, theirs, size, l, err))
	}
	if r.Kept > 0 {
		left := ""
		if r.Conflicts > 0 {
			left = fmt.Sprintf("; %d conflict(s) left", r.Conflicts)
		}
		fmt.Fprintf(os.Stderr, "mergeinserts: %s: kept both sides' insertions at %d point(s)%s\n", path, r.Kept, left)
	}
	if r.Conflicts > 0 {
		os.Exit(1)
	}
}

// conflictStyle reads merge.conflictStyle as git would for this merge.
func conflictStyle() mergeinserts.Style {
	out, err := exec.Command("git", "config", "--get", "merge.conflictStyle").Output()
	if err != nil {
		return mergeinserts.StyleMerge
	}
	switch strings.TrimSpace(string(out)) {
	case "diff3", "zdiff3":
		return mergeinserts.StyleDiff3
	}
	return mergeinserts.StyleMerge
}

// fallBack merges as git does without a driver, so an error here never leaves the
// current side in place without markers. It returns the exit status for git.
func fallBack(path, ours, base, theirs string, size int, l mergeinserts.Labels, cause error) int {
	fmt.Fprintf(os.Stderr, "mergeinserts: %s: %v; merging as git does\n", path, cause)
	cmd := exec.Command("git", "merge-file", fmt.Sprintf("--marker-size=%d", size), "-L", l.Ours, "-L", l.Base, "-L", l.Theirs, ours, base, theirs)
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exit) && exit.ExitCode() >= 1 && exit.ExitCode() <= 127:
		return 1
	}
	fmt.Fprintf(os.Stderr, "mergeinserts: %s: git merge-file: %v\n", path, err)
	return 2
}

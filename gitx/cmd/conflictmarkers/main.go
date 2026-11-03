// Command conflictmarkers fails on a merge-conflict marker left in a tracked file. A
// marker is a line git itself counts as one: seven of `<`, `|`, `=` or `>` at the start
// of the line, then whitespace or the end of the line. Each kind is a finding on its
// own, because a resolution that deletes some marker lines and misses others leaves
// exactly that.
//
// It reads every tracked text file of the repository, less the paths -skip names and
// plus the files under them whose names end with a -keep suffix: a repository whose
// machines write records into one directory, where people still edit the markdown
// beside them, runs it as
//
//	conflictmarkers -root . -skip records/ -keep .md
//
// Binary files (a NUL byte in the first 8000 bytes, as git decides), symbolic links
// and submodules are not read. A file a sparse checkout leaves out is skipped; a path
// with an unresolved merge is read once, from the working tree. Exit status 0 means no
// findings, 1 means findings, 2 means the command could not run.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ashtonian/pkg/gitx"
	"github.com/ashtonian/pkg/gitx/conflictmarkers"
)

func main() {
	var skip, keep []string
	root := flag.String("root", ".", "repository root")
	flag.Func("skip", "a path prefix whose files are not read; repeatable", func(s string) error {
		skip = append(skip, s)
		return nil
	})
	flag.Func("keep", "a file-name suffix read even under a skipped prefix; repeatable", func(s string) error {
		keep = append(keep, s)
		return nil
	})
	flag.Parse()
	tracked, err := gitx.TrackedRegular(context.Background(), *root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "conflictmarkers:", err)
		os.Exit(2)
	}
	res, err := conflictmarkers.Check(os.DirFS(*root), tracked, scope(skip, keep))
	if err != nil {
		fmt.Fprintln(os.Stderr, "conflictmarkers:", err)
		os.Exit(2)
	}
	for _, f := range res.Findings {
		fmt.Println(f)
	}
	fmt.Printf("conflict markers: %d files checked, %d findings\n", res.Files, len(res.Findings))
	if len(res.Findings) > 0 {
		os.Exit(1)
	}
}

// scope reports whether a path is read: every path outside the skipped prefixes, and
// under them only the files with a kept suffix.
func scope(skip, keep []string) func(string) bool {
	return func(path string) bool {
		for _, prefix := range skip {
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			for _, suffix := range keep {
				if strings.HasSuffix(path, suffix) {
					return true
				}
			}
			return false
		}
		return true
	}
}

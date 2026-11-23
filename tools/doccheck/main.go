// Command doccheck holds the Markdown of every module of the repository to
// the code it documents: those go.work uses, and every other go.mod outside
// hidden, vendored, test-fixture and node_modules directories, such as an
// example or a tool kept out of the workspace. The Go identifiers its code spans name resolve
// against the module's exported API; its relative links and their anchors
// resolve; the files its code maps name exist. A module that opts in, in the
// allow list, is also held the other way: every non-test Go file is in a
// code map, every exported name is named by some Markdown and every option
// has a table row. Every module's Go comments are held to whole sentences:
// no line opens on a stray full stop, comma, semicolon or colon, or ends in
// a lone full stop.
//
//	cd tools/doccheck && GOWORK=off go run . -root ../..
//
// It prints one finding per line and exits 1 when there is any.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	root := flag.String("root", ".", "repository root: the directory holding go.work")
	allow := flag.String("allow", "", "allow list (default: tools/doccheck/allow.json under the root)")
	timeout := flag.Duration("timeout", 2*time.Minute, "bound on the whole check")
	flag.Parse()
	if *allow == "" {
		*allow = filepath.Join(*root, "tools", "doccheck", "allow.json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	findings, err := check(ctx, *root, *allow)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck:", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		fmt.Fprintf(os.Stderr, "doccheck: %d findings\n", len(findings))
		os.Exit(1)
	}
}

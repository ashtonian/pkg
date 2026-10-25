package main

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitFixture(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := gitCommand(t.Context(), dir, args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestArchiveUsesCurrentGitSourceWithoutIgnoredBuildOutput(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/pkg/parse\n\ngo 1.26.0\n")
	writeFixture(t, root, "LICENSE", "license\n")
	writeFixture(t, root, ".gitignore", "node_modules/\ndist/\n")
	writeFixture(t, root, "parse.go", "package parse\n// Original.\n")
	writeFixture(t, root, "deleted.go", "package parse\n")
	writeFixture(t, root, "linked/child.go", "package child\n")
	writeFixture(t, root, "dist/intentional.txt", "tracked output\n")
	gitFixture(t, root, "init", "--quiet")
	gitFixture(t, root, "add", "go.mod", "LICENSE", ".gitignore", "parse.go", "deleted.go", "linked/child.go")
	gitFixture(t, root, "add", "--force", "dist/intentional.txt")
	writeFixture(t, root, "parse.go", "package parse\n// Current.\n")
	writeFixture(t, root, "new.go", "package parse\n")
	writeFixture(t, root, "node_modules/dependency/README.md", "See internal RFC-0001.\n")
	writeFixture(t, root, "dist/generated.js", "generated build output\n")
	writeFixture(t, root, "nested/go.mod", "module example.com/pkg/parse/nested\n\ngo 1.26.0\n")
	writeFixture(t, root, "nested/docs/reports/review.md", "internal review\n")
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	writeFixture(t, external, "child.go", "package child\n// Private RFC-0001.\n")
	if err := os.Symlink(external, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := archive(t.Context(), out, "v0.1.0", root); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(filepath.Join(out, "example.com/pkg/parse/@v/v0.1.0.zip"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := z.Close(); err != nil {
			t.Error(err)
		}
	})
	prefix := "example.com/pkg/parse@v0.1.0/"
	files := make(map[string]string)
	for _, f := range z.File {
		data, err := readArchiveFile(f)
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimPrefix(f.Name, prefix)] = string(data)
	}
	for _, name := range []string{"go.mod", "LICENSE", ".gitignore", "parse.go", "new.go", "dist/intentional.txt"} {
		if _, ok := files[name]; !ok {
			t.Errorf("candidate omits source file %q", name)
		}
	}
	if files["parse.go"] != "package parse\n// Current.\n" {
		t.Errorf("candidate does not contain current tracked source: %q", files["parse.go"])
	}
	for _, name := range []string{"deleted.go", "linked/child.go", "node_modules/dependency/README.md", "dist/generated.js", "nested/go.mod", "nested/docs/reports/review.md"} {
		if _, ok := files[name]; ok {
			t.Errorf("candidate includes excluded file %q", name)
		}
	}
	writeFixture(t, root, "parse.go", "package parse\n// Internal RFC-0001.\n")
	if _, err := archive(t.Context(), t.TempDir(), "v0.1.0", root); err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("private current source was not rejected: %v", err)
	}
}

func TestArchiveDoesNotFallBackOnGitFailure(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/pkg/parse\n\ngo 1.26.0\n")
	writeFixture(t, root, "LICENSE", "license\n")
	gitFixture(t, root, "init", "--quiet")
	writeFixture(t, root, ".git/index", "broken index\n")
	if _, err := archive(t.Context(), t.TempDir(), "v0.1.0", root); err == nil || !strings.Contains(err.Error(), "list archive source") {
		t.Fatalf("invalid Git state was not rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := archive(ctx, t.TempDir(), "v0.1.0", root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled archive did not preserve cancellation: %v", err)
	}
}

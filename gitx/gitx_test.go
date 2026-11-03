package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repo returns a git repository in a temporary directory, with no system or global
// configuration, and a function that runs git there with stdin.
func repo(t *testing.T) (string, func(stdin string, args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	git := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "-q")
	return dir, git
}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The index holds a plain file, a symbolic link, a submodule and a path at three merge
// stages. A path with a newline in it arrives as written, not quoted.
func TestTrackedAndTrackedRegular(t *testing.T) {
	dir, git := repo(t)
	object := git("x\n", "hash-object", "-w", "--stdin")
	git(strings.Join([]string{
		"100644 " + object + " 0\tplain.md",
		"100644 " + object + " 0\tdocs/two\nlines.md",
		"120000 " + object + " 0\tlink",
		"160000 " + object + " 0\tsub",
		"100644 " + object + " 1\tmerged.md",
		"100644 " + object + " 2\tmerged.md",
		"100644 " + object + " 3\tmerged.md",
	}, "\x00")+"\x00", "update-index", "-z", "--index-info")

	ctx := context.Background()
	got, err := TrackedRegular(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs/two\nlines.md", "merged.md", "plain.md"}; !slices.Equal(got, want) {
		t.Errorf("TrackedRegular = %q, want %q", got, want)
	}
	got, err = Tracked(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/two\nlines.md", "link", "merged.md", "merged.md", "merged.md", "plain.md", "sub"}
	if !slices.Equal(got, want) {
		t.Errorf("Tracked = %q, want %q: every index entry, as git lists it", got, want)
	}
}

func TestUntrackedHonoursTheIgnoreRules(t *testing.T) {
	dir, git := repo(t)
	write(t, dir, map[string]string{
		".gitignore":   "/build/\n*.log\n",
		"tracked.go":   "package x\n",
		"new.go":       "package x\n",
		"build/out":    "binary",
		"run.log":      "log",
		"docs/note.md": "note\n",
	})
	git("", "add", "tracked.go")
	got, err := Untracked(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{".gitignore", "docs/note.md", "new.go"}; !slices.Equal(got, want) {
		t.Errorf("Untracked = %q, want %q", got, want)
	}
}

func TestAnErrorNamesTheRootAndGitsMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir() // not a repository
	_, err := Tracked(context.Background(), root)
	if err == nil {
		t.Fatal("Tracked outside a repository returned no error")
	}
	if msg := err.Error(); !strings.Contains(msg, root) || !strings.Contains(msg, "not a git repository") {
		t.Errorf("error %q does not name the root and git's reason", msg)
	}
}

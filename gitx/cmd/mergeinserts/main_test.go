package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// driverEnv makes the test binary act as the driver, so the end-to-end test runs the
// code under test through git.
const driverEnv = "MERGEINSERTS_AS_DRIVER"

func TestMain(m *testing.M) {
	if os.Getenv(driverEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// isolateGit keeps the user's and the system's git configuration out of a test.
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// The README documents the exact driver command the end-to-end test installs.
func TestReadmeDocumentsTheDriverCommand(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), driverCommand) {
		t.Fatalf("README.md does not carry the driver command:\n%s", driverCommand)
	}
}

// A rebase through the installed driver: same-point insertions merge, an edit
// conflict stops with markers, and without the built tool the fallback stops with
// markers too rather than leaving one side's text unmarked.
func TestRebaseThroughTheInstalledDriver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the driver command is a POSIX shell command")
	}
	isolateGit(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_EDITOR=true", driverEnv+"=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) {
		t.Helper()
		if out, err := git(args...); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	must("init", "-q", "-b", "main")
	must("config", "user.name", "test")
	must("config", "user.email", "test@example.com")
	must("config", "commit.gpgsign", "false")
	must("config", "merge.keep-inserts.driver", driverCommand)
	// The tool is this test binary, which acts as the driver under driverEnv. A link
	// rather than a new script: macOS checks a newly written executable on its first
	// launch, which costs seconds.
	if err := os.Mkdir(filepath.Join(repo, ".tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(self, filepath.Join(repo, ".tools", "mergeinserts")); err != nil {
		t.Fatal(err)
	}
	write(".gitignore", "/.tools/\n")
	write(".gitattributes", "*.md merge=keep-inserts\n")
	write("notes.md", "# Notes\n\nbase\n")
	write("table.md", "| a |\n| b |\n")
	must("add", ".")
	must("commit", "-qm", "base")

	// Two lines of work, each touching both files, rebased onto each other.
	diverge := func(tag, mainTable, sideTable string) {
		t.Helper()
		must("checkout", "-q", "-B", "side-"+tag, "main")
		write("notes.md", read("notes.md")+"side "+tag+"\n")
		write("table.md", sideTable)
		must("commit", "-qam", "side "+tag)
		must("checkout", "-q", "main")
		write("notes.md", read("notes.md")+"main "+tag+"\n")
		write("table.md", mainTable)
		must("commit", "-qam", "main "+tag)
		must("checkout", "-q", "side-"+tag)
	}

	t.Run("insertions merge", func(t *testing.T) {
		diverge("1", "| a |\n| b |\n| main |\n", "| a |\n| b |\n| side |\n")
		if out, err := git("rebase", "main"); err != nil {
			t.Fatalf("rebase stopped: %v\n%s", err, out)
		}
		if got, want := read("notes.md"), "# Notes\n\nbase\nmain 1\nside 1\n"; got != want {
			t.Errorf("notes.md:\n%s\nwant:\n%s", got, want)
		}
		if got, want := read("table.md"), "| a |\n| b |\n| main |\n| side |\n"; got != want {
			t.Errorf("table.md:\n%s\nwant:\n%s", got, want)
		}
		must("checkout", "-q", "main")
		must("reset", "-q", "--hard", "side-1")
	})

	t.Run("an edit conflict stops with markers", func(t *testing.T) {
		diverge("2", "| a |\n| main edit |\n| main |\n| side |\n", "| a |\n| side edit |\n| main |\n| side |\n")
		if out, err := git("rebase", "main"); err == nil {
			t.Fatalf("rebase completed over an edit conflict:\n%s", out)
		}
		got := read("table.md")
		for _, want := range []string{"<<<<<<< HEAD\n", "| main edit |\n", "=======\n", "| side edit |\n", ">>>>>>> "} {
			if !strings.Contains(got, want) {
				t.Errorf("table.md lacks %q:\n%s", want, got)
			}
		}
		if got, want := read("notes.md"), "# Notes\n\nbase\nmain 1\nside 1\nmain 2\nside 2\n"; got != want {
			t.Errorf("notes.md, merged beside the conflict:\n%s\nwant:\n%s", got, want)
		}
		must("rebase", "--abort")
		must("checkout", "-q", "main")
	})

	t.Run("without the tool the fallback stops with markers", func(t *testing.T) {
		if err := os.Remove(filepath.Join(repo, ".tools", "mergeinserts")); err != nil {
			t.Fatal(err)
		}
		diverge("3", read("table.md"), read("table.md"))
		if out, err := git("rebase", "main"); err == nil {
			t.Fatalf("rebase completed without the tool:\n%s", out)
		}
		got := read("notes.md")
		for _, want := range []string{"<<<<<<< HEAD\n", "main 3\n", "=======\n", "side 3\n", ">>>>>>> "} {
			if !strings.Contains(got, want) {
				t.Errorf("notes.md lacks %q:\n%s", want, got)
			}
		}
		must("rebase", "--abort")
	})
}

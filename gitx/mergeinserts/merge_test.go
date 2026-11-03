package mergeinserts

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// isolateGit keeps the user's and the system's git configuration out of a test.
func isolateGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestMerge(t *testing.T) {
	isolateGit(t)
	l := Labels{Ours: "HEAD", Base: "base", Theirs: "theirs"}
	tests := []struct {
		name               string
		base, ours, theirs string
		st                 Style
		want               string
		kept, conflicts    int
	}{
		{
			name: "both append at the end",
			base: "# Notes\n\nfirst\n", ours: "# Notes\n\nfirst\nours\n", theirs: "# Notes\n\nfirst\ntheirs\n",
			want: "# Notes\n\nfirst\nours\ntheirs\n", kept: 1,
		},
		{
			name: "both insert rows at one point inside a table",
			base: "| a |\n| b |\n", ours: "| a |\n| o1 |\n| o2 |\n| b |\n", theirs: "| a |\n| t |\n| b |\n",
			want: "| a |\n| o1 |\n| o2 |\n| t |\n| b |\n", kept: 1,
		},
		{
			name: "both edit one line",
			base: "x\ny\nz\n", ours: "x\nO\nz\n", theirs: "x\nT\nz\n",
			want: "x\n<<<<<<< HEAD\nO\n=======\nT\n>>>>>>> theirs\nz\n", conflicts: 1,
		},
		{
			name: "an edit conflict in the diff3 style keeps the base section",
			base: "x\ny\nz\n", ours: "x\nO\nz\n", theirs: "x\nT\nz\n", st: StyleDiff3,
			want: "x\n<<<<<<< HEAD\nO\n||||||| base\ny\n=======\nT\n>>>>>>> theirs\nz\n", conflicts: 1,
		},
		{
			name: "an insertion is kept while an edit conflict is left",
			base: "a\n\nmid\n\nb\n", ours: "A1\n\nmid\n\nb\no\n", theirs: "A2\n\nmid\n\nb\nt\n",
			want: "<<<<<<< HEAD\nA1\n=======\nA2\n>>>>>>> theirs\n\nmid\n\nb\no\nt\n", kept: 1, conflicts: 1,
		},
		{
			name: "a line that looks like a marker is text",
			base: "Title\n=======\n<<<<<<< not a conflict\n", ours: "Title\n=======\n<<<<<<< not a conflict\no\n",
			theirs: "Title\n=======\n<<<<<<< not a conflict\nt\n",
			want:   "Title\n=======\n<<<<<<< not a conflict\no\nt\n", kept: 1,
		},
		{
			name: "an insertion holding a run as long as the internal marker",
			base: "a\n", ours: "a\nTitle\n========\n", theirs: "a\nt\n",
			want: "a\nTitle\n========\nt\n", kept: 1,
		},
		{
			name: "a clean merge passes through",
			base: "a\nb\nc\nd\ne\n", ours: "A\nb\nc\nd\ne\n", theirs: "a\nb\nc\nd\nE\n",
			want: "A\nb\nc\nd\nE\n",
		},
		{
			name: "insertions without a final newline stay on lines of their own",
			base: "a\n", ours: "a\no", theirs: "a\nt",
			want: "a\no\nt\n", kept: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, text string) string {
				p := filepath.Join(dir, name)
				if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			}
			ours, base, theirs := write("ours", tt.ours), write("base", tt.base), write("theirs", tt.theirs)
			r, err := Merge(ours, base, theirs, 7, tt.st, l)
			if err != nil {
				t.Fatal(err)
			}
			if r.Text != tt.want {
				t.Errorf("merged:\n%s\nwant:\n%s", r.Text, tt.want)
			}
			if r.Kept != tt.kept || r.Conflicts != tt.conflicts {
				t.Errorf("kept %d, conflicts %d; want %d, %d", r.Kept, r.Conflicts, tt.kept, tt.conflicts)
			}
		})
	}
}

func TestMergeReportsAMissingSide(t *testing.T) {
	isolateGit(t)
	dir := t.TempDir()
	ours := filepath.Join(dir, "ours")
	if err := os.WriteFile(ours, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Merge(ours, filepath.Join(dir, "missing"), ours, 7, StyleMerge, Labels{}); err == nil {
		t.Fatal("a merge against a missing base returned no error")
	}
}

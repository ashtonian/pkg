package conflictmarkers

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func all(string) bool { return true }

func checkTree(t *testing.T, tree fstest.MapFS, read func(string) bool) Result {
	t.Helper()
	var paths []string
	for name := range tree {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	res, err := Check(tree, paths, read)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func lines(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.String())
	}
	return out
}

func TestCheckReportsEachMarkerKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"a lone base-section marker, as a two-sided resolution of a diff3 conflict leaves it",
			"# Notes\n\n||||||| parent of 33111f1708 (a subject)\n### Next note\n",
			[]string{"a.md:3: base-section conflict marker"}},
		{"a lone opening marker", "text\n<<<<<<< HEAD\n", []string{"a.md:2: opening conflict marker"}},
		{"a lone separator", "ours\n=======\ntheirs\n", []string{"a.md:2: conflict separator"}},
		{"a lone closing marker", ">>>>>>> 33111f1708 (subject)\n", []string{"a.md:1: closing conflict marker"}},
		{"a whole diff3 conflict",
			"<<<<<<< HEAD\nours\n||||||| base\n=======\ntheirs\n>>>>>>> feature\n",
			[]string{"a.md:1: opening conflict marker", "a.md:3: base-section conflict marker",
				"a.md:4: conflict separator", "a.md:6: closing conflict marker"}},
		{"a bare base marker with no label", "x\n|||||||\n", []string{"a.md:2: base-section conflict marker"}},
		{"a tab after the run", "<<<<<<<\tHEAD\n", []string{"a.md:1: opening conflict marker"}},
		{"a CRLF line", "=======\r\n", []string{"a.md:1: conflict separator"}},
		{"the last line without a newline", "x\n>>>>>>>", []string{"a.md:2: closing conflict marker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := checkTree(t, fstest.MapFS{"a.md": file(tc.text)}, all)
			if got := lines(res.Findings); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// What git does not count as a marker is not one here either: a longer or shorter run,
// a run with text joined to it, a run of six with another character seventh, one that
// does not start the line.
func TestCheckPassesOrdinaryText(t *testing.T) {
	text := "Title\n========\n\n| a | b |\n|---|---|\n" +
		"<<<<<<<< eight\n<<<<<< six\n|||||||x\n>>>>>>>>\n=======x\n" +
		"  =======\nx <<<<<<< y\n\t>>>>>>> indented\n<<<<<<= x\n" +
		"<<<<<<"
	res := checkTree(t, fstest.MapFS{"a.md": file(text)}, all)
	if len(res.Findings) != 0 || res.Files != 1 {
		t.Fatalf("checked %d files with findings %q, want 1 file and none", res.Files, lines(res.Findings))
	}
}

// The caller's predicate decides what is read; binary files are not.
func TestCheckReadsWhatThePredicateAdmits(t *testing.T) {
	marked := "=======\n"
	tree := fstest.MapFS{
		"records/LEDGER.md":       file(marked),
		"records/run-1/README.md": file(marked),
		"records/run-1/sut.log":   file(marked),
		"records/run-1/plan.json": file(marked),
		"docs/guide.md":           file(marked),
		"scripts/run.sh":          file(marked),
		"testdata/fixture.bin":    {Data: []byte("\x00" + marked)},
	}
	handEdited := func(p string) bool {
		return !strings.HasPrefix(p, "records/") || strings.HasSuffix(p, ".md")
	}
	res := checkTree(t, tree, handEdited)
	want := []string{
		"docs/guide.md:1: conflict separator",
		"records/LEDGER.md:1: conflict separator",
		"records/run-1/README.md:1: conflict separator",
		"scripts/run.sh:1: conflict separator",
	}
	if got := lines(res.Findings); !reflect.DeepEqual(got, want) {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if res.Files != 4 {
		t.Fatalf("checked %d text files, want the 4 with findings", res.Files)
	}
}

// A listed path missing from the tree (a sparse checkout, an unstaged deletion) has
// nothing to report.
func TestCheckSkipsAbsentFiles(t *testing.T) {
	res, err := Check(fstest.MapFS{"a.md": file("ok\n")}, []string{"a.md", "gone.md"}, all)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || len(res.Findings) != 0 {
		t.Fatalf("checked %d files with findings %q, want 1 and none", res.Files, lines(res.Findings))
	}
}

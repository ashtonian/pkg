// Package mergeinserts merges a hand-edited text file the way git does, then resolves
// each conflict whose common-ancestor section is empty: both sides inserted lines at
// the same point and neither changed a line the other kept. It keeps the current
// side's lines, then the other side's. Every other conflict is left as git leaves it,
// with git's markers and labels in the requested conflict style, because only a person
// can decide between two versions of the same text.
package mergeinserts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Style is how an unresolved conflict is written: git's two-sided style, or with the
// common ancestor's lines between the sides. zdiff3 is written as diff3.
type Style int

const (
	StyleMerge Style = iota
	StyleDiff3
)

// Labels name the three sides in conflict markers.
type Labels struct{ Ours, Base, Theirs string }

// Result is a merged file and what happened to its conflicts.
type Result struct {
	Text      string
	Kept      int // conflicts resolved by keeping both sides' insertions
	Conflicts int // conflicts left for resolution
}

const markerChars = "<|=>"

// Merge merges the file theirs into the file ours against the file base. Conflicts
// with an empty base section are resolved by keeping ours's lines, then theirs's;
// the rest are written with markers markerSize long, in st, labelled by l. It runs
// git merge-file for the three-way merge, so git must be on PATH.
func Merge(ours, base, theirs string, markerSize int, st Style, l Labels) (Result, error) {
	inner := 1 + markerSize
	for _, p := range []string{ours, base, theirs} {
		b, err := os.ReadFile(p)
		if err != nil {
			return Result{}, err
		}
		inner = max(inner, 1+longestMarkerRun(b))
	}

	// The internal merge marks conflicts with markers longer than any run of marker
	// characters starting a line of the inputs, so no text line parses as one.
	cmd := exec.Command("git", "merge-file", "-p", "--diff3", fmt.Sprintf("--marker-size=%d", inner), ours, base, theirs)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return Result{Text: string(out)}, nil
	case errors.As(err, &exit) && exit.ExitCode() >= 1 && exit.ExitCode() <= 127:
		// The exit status is the number of conflicts.
	default:
		return Result{}, fmt.Errorf("git merge-file: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return resolve(string(out), inner, markerSize, st, l)
}

// longestMarkerRun is the longest run of one marker character starting a line of b.
func longestMarkerRun(b []byte) int {
	longest := 0
	for line := range bytes.Lines(b) {
		if len(line) == 0 || !strings.ContainsRune(markerChars, rune(line[0])) {
			continue
		}
		n := 1
		for n < len(line) && line[n] == line[0] {
			n++
		}
		longest = max(longest, n)
	}
	return longest
}

// resolve rewrites merge-file's diff3 output, whose markers are inner long.
func resolve(merged string, inner, markerSize int, st Style, l Labels) (Result, error) {
	const (
		outside = iota
		inOurs
		inBase
		inTheirs
	)
	isMarker := func(line string, c byte) bool {
		rest, ok := strings.CutPrefix(line, strings.Repeat(string(c), inner))
		return ok && (rest == "" || rest == "\n" || rest[0] == ' ')
	}
	var (
		r            Result
		out          strings.Builder
		o, base, thr []string
		state        = outside
	)
	for line := range strings.Lines(merged) {
		switch state {
		case outside:
			if isMarker(line, '<') {
				o, base, thr = nil, nil, nil
				state = inOurs
				continue
			}
			out.WriteString(line)
		case inOurs:
			if isMarker(line, '|') {
				state = inBase
				continue
			}
			if isMarker(line, '=') {
				return Result{}, errors.New("a conflict without its common-ancestor section")
			}
			o = append(o, line)
		case inBase:
			if isMarker(line, '=') {
				state = inTheirs
				continue
			}
			base = append(base, line)
		case inTheirs:
			if isMarker(line, '>') {
				r.write(&out, o, base, thr, markerSize, st, l)
				state = outside
				continue
			}
			thr = append(thr, line)
		}
	}
	if state != outside {
		return Result{}, errors.New("an unterminated conflict")
	}
	r.Text = out.String()
	return r, nil
}

// write emits one conflict: both insertions when the base section is empty, and git's
// markers around the sides otherwise.
func (r *Result) write(out *strings.Builder, ours, base, theirs []string, size int, st Style, l Labels) {
	if len(base) == 0 {
		r.Kept++
		writeLines(out, ours)
		writeLines(out, theirs)
		return
	}
	r.Conflicts++
	marker := func(c byte, label string) {
		out.WriteString(strings.Repeat(string(c), size))
		if label != "" {
			out.WriteString(" ")
			out.WriteString(label)
		}
		out.WriteString("\n")
	}
	marker('<', l.Ours)
	writeLines(out, ours)
	if st == StyleDiff3 {
		marker('|', l.Base)
		writeLines(out, base)
	}
	marker('=', "")
	writeLines(out, theirs)
	marker('>', l.Theirs)
}

// writeLines writes a conflict section's lines. merge-file ends every line it copies
// into a conflict, the last one included, so a section never runs into what follows.
func writeLines(out *strings.Builder, lines []string) {
	for _, line := range lines {
		out.WriteString(line)
	}
}

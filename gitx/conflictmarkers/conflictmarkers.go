// Package conflictmarkers finds merge-conflict markers left in files. A marker is a
// line git itself counts as one: seven of `<`, `|`, `=` or `>` at the start of the
// line, then whitespace or the end of the line. Each kind is a finding on its own,
// because a resolution that deletes some marker lines and misses others leaves exactly
// that. The base-section line of the diff3 conflict style is the one a resolution
// written for the two-sided style misses.
package conflictmarkers

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
)

// Marker is one marker line of a file.
type Marker struct {
	Line int
	Kind string
}

// Finding is a marker line of a named file. Its text form is file:line: kind.
type Finding struct {
	Path string
	Line int
	Kind string
}

func (f Finding) String() string { return fmt.Sprintf("%s:%d: %s", f.Path, f.Line, f.Kind) }

// Result is what a check read and found.
type Result struct {
	Findings []Finding
	Files    int // text files read
}

// Check reads each of paths that read reports true for from fsys, and returns every
// marker line, in the order of paths. A path absent from fsys is skipped: a sparse
// checkout leaves it out, or it is deleted and not yet staged, and nothing in this
// tree was merged into it. A binary file is skipped too, by git's own test: a NUL byte
// in its first 8000 bytes.
func Check(fsys fs.FS, paths []string, read func(path string) bool) (Result, error) {
	var res Result
	for _, name := range paths {
		if !read(name) {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return res, err
		}
		if binary(data) {
			continue
		}
		res.Files++
		for _, m := range Markers(data) {
			res.Findings = append(res.Findings, Finding{Path: name, Line: m.Line, Kind: m.Kind})
		}
	}
	return res, nil
}

func binary(data []byte) bool {
	return bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
}

// markerSize is git's default conflict-marker size.
const markerSize = 7

// kinds names the lines a merge leaves, by their character: the opening and closing
// ones, the separator between the two sides, and the base section's, which the diff3
// and zdiff3 conflict styles add.
var kinds = map[byte]string{
	'<': "opening conflict marker",
	'|': "base-section conflict marker",
	'=': "conflict separator",
	'>': "closing conflict marker",
}

// Markers finds every line git itself counts as a conflict marker: seven of one marker
// character at the start of the line, then whitespace or the end of the line. A longer
// run is ordinary text, as it is to git.
func Markers(data []byte) []Marker {
	var found []Marker
	n := 0
	for line := range bytes.Lines(data) {
		n++
		if len(line) < markerSize {
			continue
		}
		kind, ok := kinds[line[0]]
		if !ok || bytes.Count(line[:markerSize], line[:1]) != markerSize {
			continue
		}
		if len(line) > markerSize && !isSpace(line[markerSize]) {
			continue
		}
		found = append(found, Marker{Line: n, Kind: kind})
	}
	return found
}

func isSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

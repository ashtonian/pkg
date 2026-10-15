package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Internal design documents use zero-padded RFC-0xxx identifiers. IETF
// standards such as RFC 9110, RFC 9111, and RFC-9110 remain valid public docs.
var internalRFC = regexp.MustCompile(`(?i)\bRFC-0[0-9]{3}\b`)

// checkPublicArchive inspects the actual module ZIP, after Go's module
// boundary and file-selection rules have excluded nested modules. Internal
// planning artifacts belong outside published modules, rather than silently
// filtered from a release that would differ from its source tree.
func checkPublicArchive(filename, prefix string) (err error) {
	z, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, z.Close()) }()
	for _, f := range z.File {
		name, ok := strings.CutPrefix(f.Name, prefix)
		if !ok {
			return fmt.Errorf("unexpected archive path %q", f.Name)
		}
		if internalArtifactPath(name) {
			return fmt.Errorf("%s: internal planning artifact in published module", name)
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".go", ".md", ".txt":
			data, err := readArchiveFile(f)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if internalRFC.Match(data) {
				return fmt.Errorf("%s: internal RFC reference in published module", name)
			}
		}
	}
	return nil
}

// These are reserved internal-document locations, not broad substring
// matches: a public guide like docs/status-reports.md remains allowed.
func internalArtifactPath(name string) bool {
	name = strings.ToLower(name)
	for component := range strings.SplitSeq(name, "/") {
		switch component {
		case ".llm", ".claude", ".codex", "_review":
			return true
		}
	}
	for _, dir := range []string{"docs/spec/", "docs/plans/", "docs/reports/", "docs/audits/", "docs/promotion/"} {
		if strings.HasPrefix(name, dir) {
			return true
		}
	}
	switch path.Base(name) {
	case "audit.md", "audit-report.md", "report.md", "promotion.md", "promotion-checklist.md":
		return true
	}
	return internalRFC.MatchString(name)
}

func readArchiveFile(f *zip.File) (data []byte, err error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	return io.ReadAll(r)
}

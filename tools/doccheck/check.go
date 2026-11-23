package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// finding is one way a document disagrees with the code or with itself.
type finding struct {
	path string
	line int // 0 for a finding about the file or package as a whole
	msg  string
}

func (f finding) String() string {
	if f.line == 0 {
		return f.path + ": " + f.msg
	}
	return fmt.Sprintf("%s:%d: %s", f.path, f.line, f.msg)
}

// moduleAllow is one module's allow list. Every entry carries the reason it
// is allowed; an entry that no longer matches anything is itself a finding,
// so the list only shrinks as the documentation catches up.
type moduleAllow struct {
	// Reverse opts the module into the reverse checks: every non-test Go
	// file in a code map, every exported name in some Markdown, every option
	// in a table row.
	Reverse bool `json:"reverse"`
	// Identifiers are references the Markdown may make that resolve to
	// nothing in the module, as written in the code span.
	Identifiers map[string]string `json:"identifiers,omitempty"`
	// UnlistedFiles are path patterns, relative to the repository root, of
	// files no code map lists.
	UnlistedFiles map[string]string `json:"unlisted_files,omitempty"`
	// UnnamedExports are "package.Name" patterns of exported names no
	// Markdown names.
	UnnamedExports map[string]string `json:"unnamed_exports,omitempty"`
	// UntabledOptions are "package.Name" patterns of options no table row
	// names.
	UntabledOptions map[string]string `json:"untabled_options,omitempty"`
}

type module struct {
	dir     string // relative to the repository root
	dirs    []string
	goFiles []string // every Go file, tests included, relative to the root
	docs    []*mdDoc
	api     *moduleAPI
	allow   moduleAllow
	used    map[string]bool // allow entries that matched, by "kind\x00key"
}

// check runs every check over every module of the repository at root.
func check(ctx context.Context, root, allowPath string) ([]finding, error) {
	allow := map[string]moduleAllow{}
	if data, err := os.ReadFile(allowPath); err == nil {
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&allow); err != nil {
			return nil, fmt.Errorf("reading allow list %s: %w", allowPath, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("reading allow list %s: %w", allowPath, err)
	}
	roots, err := moduleRoots(root)
	if err != nil {
		return nil, err
	}
	mods, err := checkedModules(ctx, root, roots)
	if err != nil {
		return nil, err
	}
	docs := newDocCache(root)
	// Findings name the allow list as they name documents: from the root.
	if rel, err := filepath.Rel(root, allowPath); err == nil && !strings.HasPrefix(rel, "..") {
		allowPath = filepath.ToSlash(rel)
	}
	var findings []finding
	for name := range allow {
		if !slices.Contains(mods, name) {
			findings = append(findings, finding{path: allowPath, msg: fmt.Sprintf("allow list names %s, which is no module of the repository", name)})
		}
	}
	modules := make([]*module, 0, len(mods))
	for _, dir := range mods {
		m, err := loadModule(root, dir, roots, docs)
		if err != nil {
			return nil, err
		}
		m.allow = allow[dir]
		modules = append(modules, m)
	}
	// An option named on its own may be another module's: a provider's
	// README names the options of the module it plugs into.
	option := func(name string) bool {
		return slices.ContainsFunc(modules, func(m *module) bool { return m.api.declaredAnywhere(name) })
	}
	for _, m := range modules {
		findings = append(findings, m.checkReasons(allowPath)...)
		findings = append(findings, m.checkIdentifiers(option)...)
		findings = append(findings, m.checkLinks(root, docs)...)
		commentFindings, err := m.checkComments(root)
		if err != nil {
			return nil, err
		}
		findings = append(findings, commentFindings...)
		maps, mapFindings := m.codeMaps(root)
		findings = append(findings, mapFindings...)
		if m.allow.Reverse {
			findings = append(findings, m.checkListed(maps)...)
			findings = append(findings, m.checkNamed()...)
			findings = append(findings, m.checkTabled()...)
		}
		findings = append(findings, m.checkStale(allowPath)...)
	}
	slices.SortFunc(findings, func(a, b finding) int {
		return cmp.Or(strings.Compare(a.path, b.path), cmp.Compare(a.line, b.line), strings.Compare(a.msg, b.msg))
	})
	return findings, nil
}

// checkedModules are the modules the check reads, relative to root: those
// go.work uses, then, sorted, every other module of the repository (roots).
// A module kept out of the workspace, an example or a tool with its own
// dependency graph, still documents the code beside it.
func checkedModules(ctx context.Context, root string, roots map[string]bool) ([]string, error) {
	mods, err := workspaceModules(ctx, root)
	if err != nil {
		return nil, err
	}
	var outside []string
	for dir := range roots {
		if !slices.Contains(mods, dir) {
			outside = append(outside, dir)
		}
	}
	slices.Sort(outside)
	return append(mods, outside...), nil
}

// workspaceModules are the directories go.work uses, relative to root.
func workspaceModules(ctx context.Context, root string) ([]string, error) {
	work, err := filepath.Abs(filepath.Join(root, "go.work"))
	if err != nil {
		return nil, fmt.Errorf("locating go.work in %s: %w", root, err)
	}
	cmd := exec.CommandContext(ctx, "go", "work", "edit", "-json")
	cmd.Dir = root
	// The tool itself runs outside the workspace (GOWORK=off); the workspace
	// it reads is the root's.
	cmd.Env = append(os.Environ(), "GOWORK="+work)
	out, err := cmd.Output()
	if err != nil {
		exit, ok := errors.AsType[*exec.ExitError](err)
		if ok {
			return nil, fmt.Errorf("reading %s: %w: %s", work, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("reading %s: %w", work, err)
	}
	var parsed struct{ Use []struct{ DiskPath string } }
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("decoding go work edit -json: %w", err)
	}
	var mods []string
	for _, u := range parsed.Use {
		mods = append(mods, path.Clean(filepath.ToSlash(u.DiskPath)))
	}
	return mods, nil
}

// skipDir reports whether a walk leaves a directory out: hidden, vendored,
// test fixtures and JavaScript dependencies are no module's documentation
// or API.
func skipDir(name string) bool {
	return name != "." && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
		name == "testdata" || name == "vendor" || name == "node_modules")
}

// moduleRoots are every directory holding a go.mod, relative to root, but
// those skipDir leaves out: a test fixture's or a dependency's module is
// not the repository's.
func moduleRoots(root string) (map[string]bool, error) {
	roots := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && skipDir(d.Name()) && p != root {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "go.mod" {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			roots[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("finding modules under %s: %w", root, err)
	}
	return roots, nil
}

// loadModule reads a module's tree: its directories but those of modules
// nested in it.
func loadModule(root, dir string, roots map[string]bool, docs *docCache) (*module, error) {
	m := &module{dir: dir, used: map[string]bool{}}
	err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != dir && (skipDir(d.Name()) || roots[rel]) {
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(rel, ".go"):
			m.goFiles = append(m.goFiles, rel)
			if pd := path.Dir(rel); !strings.HasSuffix(rel, "_test.go") && !slices.Contains(m.dirs, pd) {
				m.dirs = append(m.dirs, pd)
			}
		case strings.HasSuffix(rel, ".md") && path.Base(rel) != "CHANGELOG.md":
			doc, err := docs.get(rel)
			if err != nil {
				return err
			}
			m.docs = append(m.docs, doc)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading module %s: %w", dir, err)
	}
	if m.api, err = loadAPI(root, m.dirs); err != nil {
		return nil, err
	}
	return m, nil
}

// docCache parses each Markdown file once, for the module that holds it and
// for every link into it.
type docCache struct {
	root string
	docs map[string]*mdDoc
}

func newDocCache(root string) *docCache { return &docCache{root: root, docs: map[string]*mdDoc{}} }

func (c *docCache) get(rel string) (*mdDoc, error) {
	if doc, ok := c.docs[rel]; ok {
		return doc, nil
	}
	data, err := os.ReadFile(filepath.Join(c.root, rel))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rel, err)
	}
	doc := parseMarkdown(rel, string(data))
	c.docs[rel] = doc
	return doc, nil
}

// allowed reports whether an allow list entry of kind matches key, and
// records that it was used.
func (m *module) allowed(kind string, entries map[string]string, key string) bool {
	for pattern := range entries {
		if ok, _ := path.Match(pattern, key); ok || pattern == key {
			m.used[kind+"\x00"+pattern] = true
			return true
		}
	}
	return false
}

func (m *module) checkReasons(allowPath string) []finding {
	var out []finding
	for kind, entries := range m.allowKinds() {
		for key, reason := range entries {
			if strings.TrimSpace(reason) == "" {
				out = append(out, finding{path: allowPath, msg: fmt.Sprintf("%s: %s entry %q gives no reason", m.dir, kind, key)})
			}
		}
	}
	return out
}

func (m *module) allowKinds() map[string]map[string]string {
	return map[string]map[string]string{
		"identifiers": m.allow.Identifiers, "unlisted_files": m.allow.UnlistedFiles, "unnamed_exports": m.allow.UnnamedExports,
		"untabled_options": m.allow.UntabledOptions,
	}
}

func (m *module) checkStale(allowPath string) []finding {
	var out []finding
	for kind, entries := range m.allowKinds() {
		for key := range entries {
			if !m.used[kind+"\x00"+key] {
				out = append(out, finding{path: allowPath, msg: fmt.Sprintf("%s: %s entry %q matches nothing; remove it", m.dir, kind, key)})
			}
		}
	}
	return out
}

var (
	// qualifiedRef is pkg.Name, pkg.Name*, pkg.Type[T].Member.
	qualifiedRef = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])([a-z][a-z0-9]*)\.([A-Z][A-Za-z0-9_]*)(\*?)(?:\[[^\]]*\])?(?:\.([A-Z][A-Za-z0-9_]*)(\*?))?`)
	// memberRef is Type.Member, Type[T].Member.
	memberRef = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])([A-Z][A-Za-z0-9_]*)(?:\[[^\]]*\])?\.([A-Z][A-Za-z0-9_]*)(\*?)`)
	// optionRef is a functional option named on its own.
	optionRef = regexp.MustCompile(`(?:^|[^A-Za-z0-9_.])(With[A-Z][A-Za-z0-9_]*)`)
)

// checkIdentifiers resolves the Go identifiers a module's code spans name
// against its exported API: a package of the module qualified by its name,
// a member of one of its types, and a functional option. A qualifier that
// names no package of the module, or a type it does not declare, belongs to
// another module or the standard library and is not checked.
func (m *module) checkIdentifiers(option func(string) bool) []finding {
	var out []finding
	for _, doc := range m.docs {
		for _, span := range doc.spans {
			for _, ref := range m.unresolved(span.text, option) {
				if !m.allowed("identifiers", m.allow.Identifiers, ref) {
					out = append(out, finding{path: doc.path, line: span.line, msg: fmt.Sprintf("%s names nothing %s declares", ref, m.dir)})
				}
			}
		}
	}
	return out
}

func (m *module) unresolved(text string, option func(string) bool) []string {
	var out []string
	for _, g := range qualifiedRef.FindAllStringSubmatch(text, -1) {
		pkg, name, prefix, member, memberPrefix := g[1], g[2], g[3] == "*", g[4], g[5] == "*"
		if !slices.ContainsFunc(m.api.packages[pkg], func(p *packageAPI) bool { return p.importable }) || m.api.shadowed(pkg) {
			continue
		}
		switch {
		case !m.api.hasDecl(pkg, name, prefix):
			out = append(out, pkg+"."+name+g[3])
		case member != "" && !m.api.hasMember(pkg, name, member, memberPrefix):
			out = append(out, pkg+"."+name+"."+member+g[5])
		}
	}
	for _, g := range memberRef.FindAllStringSubmatch(text, -1) {
		typ, member, prefix := g[1], g[2], g[3] == "*"
		if m.api.typeNamed(typ) && !m.api.hasMember("", typ, member, prefix) {
			out = append(out, typ+"."+member+g[3])
		}
	}
	for _, g := range optionRef.FindAllStringSubmatch(text, -1) {
		if !option(g[1]) {
			out = append(out, g[1])
		}
	}
	return out
}

// checkLinks requires every relative link of the module's Markdown to name
// a file or directory in the repository, and its fragment to name a heading
// or an HTML anchor when it points into Markdown.
func (m *module) checkLinks(root string, docs *docCache) []finding {
	var out []finding
	for _, doc := range m.docs {
		for _, l := range doc.links {
			if strings.Contains(l.target, "://") || strings.HasPrefix(l.target, "mailto:") {
				continue
			}
			p, fragment := splitTarget(l.target)
			target := doc.path
			switch {
			case strings.HasPrefix(p, "/"):
				target = path.Clean(strings.TrimPrefix(p, "/"))
			case p != "":
				target = path.Join(path.Dir(doc.path), p)
			}
			if target == ".." || strings.HasPrefix(target, "../") {
				out = append(out, finding{path: doc.path, line: l.line, msg: fmt.Sprintf("link %s leaves the repository", l.target)})
				continue
			}
			if _, err := os.Stat(filepath.Join(root, target)); err != nil {
				out = append(out, finding{path: doc.path, line: l.line, msg: fmt.Sprintf("link %s: %s does not exist", l.target, target)})
				continue
			}
			if fragment == "" || !strings.HasSuffix(target, ".md") {
				continue
			}
			into, err := docs.get(target)
			if err != nil {
				out = append(out, finding{path: doc.path, line: l.line, msg: fmt.Sprintf("link %s: %v", l.target, err)})
				continue
			}
			if !into.anchors[fragment] {
				out = append(out, finding{path: doc.path, line: l.line, msg: fmt.Sprintf("link %s: %s has no heading or anchor %q", l.target, target, fragment)})
			}
		}
	}
	return out
}

// codeMap is the entries one README's code map names, resolved against the
// README's directory.
type codeMap struct {
	dir     string
	entries []string // files, directories ending in "/", or glob patterns
}

func (c codeMap) lists(file string) bool {
	for _, e := range c.entries {
		switch {
		case strings.HasSuffix(e, "/"):
			if strings.HasPrefix(file, e) {
				return true
			}
		case strings.ContainsAny(e, "*?["):
			if ok, _ := path.Match(e, file); ok {
				return true
			}
		case e == file:
			return true
		}
	}
	return false
}

var (
	codeMapHeading = regexp.MustCompile(`(?i)\b(code map|key files)\b`)
	pathLike       = regexp.MustCompile(`^[A-Za-z0-9_.\-/*]+$`)
	fileExt        = regexp.MustCompile(`\.(go|md|json|ts|mjs|js|sh|sql|ya?ml|mod|sum|txt|proto|py)$`)
)

// codeMaps reads the code map of each Markdown file that has one: the first
// cell of each row of a table under a "Code map" or "Key files" heading. An
// entry with a file extension or a slash is a path: it must name a file or
// directory that exists, or be a pattern that matches one.
func (m *module) codeMaps(root string) (map[string]codeMap, []finding) {
	maps := map[string]codeMap{}
	var out []finding
	for _, doc := range m.docs {
		dir := path.Dir(doc.path)
		for _, row := range doc.rows {
			if len(row.cells) == 0 || !slices.ContainsFunc(row.sections, codeMapHeading.MatchString) {
				continue
			}
			c := maps[doc.path]
			c.dir = dir
			for _, entry := range row.cells[0] {
				if !pathLike.MatchString(entry) || !fileExt.MatchString(entry) && !strings.Contains(entry, "/") {
					continue
				}
				resolved, ok := m.resolveEntry(root, dir, entry)
				if !ok {
					out = append(out, finding{path: doc.path, line: row.line, msg: fmt.Sprintf("code map entry %s: no such file under %s or %s", entry, dir, m.dir)})
				}
				c.entries = append(c.entries, resolved)
			}
			maps[doc.path] = c
		}
	}
	return maps, out
}

// checkListed requires each non-test Go file to be listed by its package's
// code map: the nearest README, from the file's directory up to the
// module's, that has one.
func (m *module) checkListed(maps map[string]codeMap) []finding {
	var out []finding
	for _, p := range m.api.all() {
		for _, file := range p.files {
			c, ok := m.nearestCodeMap(path.Dir(file), maps)
			switch {
			case ok && c.lists(file):
			case m.allowed("unlisted_files", m.allow.UnlistedFiles, file):
			case ok:
				out = append(out, finding{path: file, msg: fmt.Sprintf("%s/README.md's code map does not list it", c.dir)})
			default:
				out = append(out, finding{path: file, msg: "no README code map covers its directory"})
			}
		}
	}
	return out
}

// resolveEntry finds what a code map entry names: a path relative to the
// Markdown file's directory, or, for a guide that maps the module, to the
// module's.
func (m *module) resolveEntry(root, dir, entry string) (string, bool) {
	for _, base := range []string{dir, m.dir} {
		resolved := path.Join(base, entry)
		if strings.ContainsAny(entry, "*?[") {
			if matches, _ := filepath.Glob(filepath.Join(root, resolved)); len(matches) > 0 {
				return resolved, true
			}
			continue
		}
		info, err := os.Stat(filepath.Join(root, resolved))
		if err != nil {
			continue
		}
		if info.IsDir() {
			// A directory entry covers every file under it.
			resolved += "/"
		}
		return resolved, true
	}
	return path.Join(dir, entry), false
}

func (m *module) nearestCodeMap(dir string, maps map[string]codeMap) (codeMap, bool) {
	for {
		if c, ok := maps[pathJoin(dir, "README.md")]; ok {
			return c, true
		}
		if dir == m.dir || dir == "." || dir == "" {
			return codeMap{}, false
		}
		dir = path.Dir(dir)
	}
}

var (
	word = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	// wordPrefix is a family named by its prefix, ECode* or Param*.
	wordPrefix = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\*`)
)

// checkNamed requires each exported top-level name of an importable package
// to appear in some code span or code block of the module's Markdown, or to
// belong to a family a code span names by its prefix.
func (m *module) checkNamed() []finding {
	named := map[string]bool{}
	var prefixes []string
	for _, doc := range m.docs {
		for _, s := range doc.spans {
			for _, w := range word.FindAllString(s.text, -1) {
				named[w] = true
			}
			for _, g := range wordPrefix.FindAllStringSubmatch(s.text, -1) {
				prefixes = append(prefixes, g[1])
			}
		}
		for _, line := range doc.fenced {
			for _, w := range word.FindAllString(line, -1) {
				named[w] = true
			}
		}
	}
	var out []finding
	for _, p := range m.api.all() {
		if !p.importable {
			continue
		}
		for _, name := range slices.Sorted(mapsKeys(p.decls)) {
			inFamily := slices.ContainsFunc(prefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
			if named[name] || inFamily || m.allowed("unnamed_exports", m.allow.UnnamedExports, p.name+"."+name) {
				continue
			}
			out = append(out, finding{path: p.dir, msg: fmt.Sprintf("%s.%s is exported and no Markdown names it", p.name, name)})
		}
	}
	return out
}

// checkTabled requires each option of an importable package to be named in
// a table row of the module's Markdown, where options are documented: an
// option table, or a row linking to the guide that holds one, not a code
// map. A code span
// naming a family by its prefix (`With*`) names each of its members.
func (m *module) checkTabled() []finding {
	named := map[string]bool{}
	var prefixes []string
	for _, doc := range m.docs {
		for _, row := range doc.rows {
			// A code map lists what a file declares; it documents no option.
			if slices.ContainsFunc(row.sections, codeMapHeading.MatchString) {
				continue
			}
			for _, cell := range row.cells {
				for _, span := range cell {
					for _, w := range word.FindAllString(span, -1) {
						named[w] = true
					}
					for _, g := range wordPrefix.FindAllStringSubmatch(span, -1) {
						prefixes = append(prefixes, g[1])
					}
				}
			}
		}
	}
	var out []finding
	for _, p := range m.api.all() {
		if !p.importable {
			continue
		}
		for _, name := range slices.Sorted(mapsKeys(p.options)) {
			inFamily := slices.ContainsFunc(prefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
			if named[name] || inFamily || m.allowed("untabled_options", m.allow.UntabledOptions, p.name+"."+name) {
				continue
			}
			out = append(out, finding{path: p.dir, msg: fmt.Sprintf("%s.%s is an option no table row names", p.name, name)})
		}
	}
	return out
}

func mapsKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

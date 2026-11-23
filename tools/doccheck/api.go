package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// packageAPI is what one package directory declares: its exported top-level
// names and, per type, its exported methods and fields and the types it
// embeds, through which members are promoted.
type packageAPI struct {
	name  string
	dir   string   // relative to the repository root, slash-separated
	files []string // non-test Go files that declare something, relative to the repository root
	// importable is false for a main package and for one under internal/.
	importable bool
	decls      map[string]bool
	types      map[string]*typeAPI
	// imports are the names the package's files import other packages
	// under: the last element of the path without a major version suffix,
	// and any alias.
	imports map[string]string
}

type typeAPI struct {
	members  map[string]bool
	embedded []typeRef
}

// typeRef names a type: pkg is the qualifier as written, "" for the
// declaring package.
type typeRef struct{ pkg, name string }

// moduleAPI is the exported API of a module's packages, by package name:
// several directories may declare packages of one name.
type moduleAPI struct {
	packages map[string][]*packageAPI
}

// shadowed reports whether a package of the module imports another package
// it could be confused with in prose: a module package named like the
// library it wraps (a msgpack codec over a msgpack library). A qualifier of
// that name in Markdown may mean either, so it is not checked.
func (m *moduleAPI) shadowed(name string) bool {
	for _, pkgs := range m.packages {
		for _, p := range pkgs {
			if path, ok := p.imports[name]; ok && !slices.ContainsFunc(m.packages[name], func(q *packageAPI) bool { return strings.HasSuffix(path, "/"+q.dir) }) {
				return true
			}
		}
	}
	return false
}

func (m *moduleAPI) all() []*packageAPI {
	var out []*packageAPI
	for _, pkgs := range m.packages {
		out = append(out, pkgs...)
	}
	slices.SortFunc(out, func(a, b *packageAPI) int { return strings.Compare(a.dir, b.dir) })
	return out
}

// loadAPI parses every non-test Go file of the module's directories,
// whatever its build constraints: a name declared for any platform or tag
// is one the documentation may name.
func loadAPI(root string, dirs []string) (*moduleAPI, error) {
	api := &moduleAPI{packages: map[string][]*packageAPI{}}
	fset := token.NewFileSet()
	for _, dir := range dirs {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
		var pkg *packageAPI
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			rel := pathJoin(dir, name)
			file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.SkipObjectResolution)
			if err != nil {
				return nil, fmt.Errorf("parsing %s: %w", rel, err)
			}
			if pkg == nil {
				pkg = &packageAPI{
					name:       file.Name.Name,
					dir:        dir,
					importable: file.Name.Name != "main" && !isInternal(dir),
					decls:      map[string]bool{},
					types:      map[string]*typeAPI{},
					imports:    map[string]string{},
				}
			}
			// A file that declares nothing (doc.go) is the package's
			// documentation, not code a code map describes.
			if len(file.Decls) > 0 {
				pkg.files = append(pkg.files, rel)
			}
			pkg.add(file)
		}
		if pkg != nil {
			api.packages[pkg.name] = append(api.packages[pkg.name], pkg)
		}
	}
	return api, nil
}

func isInternal(dir string) bool {
	return slices.Contains(strings.Split(dir, "/"), "internal")
}

func (p *packageAPI) typ(name string) *typeAPI {
	t := p.types[name]
	if t == nil {
		t = &typeAPI{members: map[string]bool{}}
		p.types[name] = t
	}
	return t
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

func (p *packageAPI) add(file *ast.File) {
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		name := pathpkg.Base(path)
		if majorVersion.MatchString(name) {
			name = pathpkg.Base(pathpkg.Dir(path))
		}
		// Prose names a library as its own documentation does, whatever the
		// alias this file gives it.
		p.imports[name] = path
		if imp.Name != nil {
			p.imports[imp.Name.Name] = path
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				if d.Name.IsExported() {
					p.decls[d.Name.Name] = true
				}
				continue
			}
			if recv := receiverName(d.Recv.List[0].Type); recv != "" && d.Name.IsExported() {
				p.typ(recv).members[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						p.decls[s.Name.Name] = true
					}
					p.addType(p.typ(s.Name.Name), s)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							p.decls[n.Name] = true
						}
					}
				}
			}
		}
	}
}

func (p *packageAPI) addType(t *typeAPI, s *ast.TypeSpec) {
	if s.Assign.IsValid() {
		// An alias has the members of the type it names.
		if ref, ok := refOf(s.Type); ok {
			t.embedded = append(t.embedded, ref)
		}
		return
	}
	switch typ := s.Type.(type) {
	case *ast.StructType:
		for _, f := range typ.Fields.List {
			if len(f.Names) == 0 {
				if ref, ok := refOf(f.Type); ok {
					t.embedded = append(t.embedded, ref)
					t.members[ref.name] = ast.IsExported(ref.name)
				}
				continue
			}
			for _, n := range f.Names {
				if n.IsExported() {
					t.members[n.Name] = true
				}
			}
		}
	case *ast.InterfaceType:
		for _, m := range typ.Methods.List {
			if len(m.Names) == 0 {
				if ref, ok := refOf(m.Type); ok {
					t.embedded = append(t.embedded, ref)
				}
				continue
			}
			for _, n := range m.Names {
				if n.IsExported() {
					t.members[n.Name] = true
				}
			}
		}
	}
}

// receiverName is the type a method's receiver names, without pointer or
// type parameters.
func receiverName(expr ast.Expr) string {
	ref, ok := refOf(expr)
	if !ok || ref.pkg != "" {
		return ""
	}
	return ref.name
}

func refOf(expr ast.Expr) (typeRef, bool) {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return refOf(e.X)
	case *ast.IndexExpr:
		return refOf(e.X)
	case *ast.IndexListExpr:
		return refOf(e.X)
	case *ast.Ident:
		return typeRef{name: e.Name}, true
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return typeRef{pkg: x.Name, name: e.Sel.Name}, true
		}
	}
	return typeRef{}, false
}

// hasDecl reports whether a package named pkg declares name, or, with
// prefix, a name starting with it.
func (m *moduleAPI) hasDecl(pkg, name string, prefix bool) bool {
	for _, p := range m.packages[pkg] {
		if !p.importable {
			continue
		}
		for decl := range p.decls {
			if decl == name || prefix && strings.HasPrefix(decl, name) {
				return true
			}
		}
	}
	return false
}

// typeNamed reports whether any package declares an exported type name.
func (m *moduleAPI) typeNamed(name string) bool {
	for _, pkgs := range m.packages {
		for _, p := range pkgs {
			if t := p.types[name]; t != nil && p.decls[name] {
				return true
			}
		}
	}
	return false
}

// hasMember reports whether a type named typ, in the package named pkg or,
// when pkg is "", in any package, has member, declared or promoted.
func (m *moduleAPI) hasMember(pkg, typ, member string, prefix bool) bool {
	for name, pkgs := range m.packages {
		if pkg != "" && name != pkg {
			continue
		}
		for _, p := range pkgs {
			if m.memberOf(p, typ, member, prefix, map[*typeAPI]bool{}) {
				return true
			}
		}
	}
	return false
}

func (m *moduleAPI) memberOf(p *packageAPI, typ, member string, prefix bool, seen map[*typeAPI]bool) bool {
	t := p.types[typ]
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	for name, exported := range t.members {
		if exported && (name == member || prefix && strings.HasPrefix(name, member)) {
			return true
		}
	}
	for _, ref := range t.embedded {
		if ref.pkg == "" {
			if m.memberOf(p, ref.name, member, prefix, seen) {
				return true
			}
			continue
		}
		for _, q := range m.packages[ref.pkg] {
			if m.memberOf(q, ref.name, member, prefix, seen) {
				return true
			}
		}
	}
	return false
}

// declaredAnywhere reports whether any package declares name at top level
// or as a member of a type.
func (m *moduleAPI) declaredAnywhere(name string) bool {
	for _, pkgs := range m.packages {
		for _, p := range pkgs {
			if p.decls[name] {
				return true
			}
			for _, t := range p.types {
				if t.members[name] {
					return true
				}
			}
		}
	}
	return false
}

func pathJoin(dir, name string) string {
	if dir == "" || dir == "." {
		return name
	}
	return dir + "/" + name
}

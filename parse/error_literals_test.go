//go:build unit

package parse

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The import paths a ParseError is named through.
const (
	rootPackagePath = "github.com/ashtonian/pkg/parse"
	corePackagePath = rootPackagePath + "/core"
)

// Every request error is built by core.NewError, so none picks its own
// shape: the module's Go files, tests and every build tag included, hold no
// composite literal of core.ParseError outside core itself. Other modules
// (client, the example server) are their own.
func TestSource_BuildsParseErrorsThroughNewError(t *testing.T) {
	fset := token.NewFileSet()
	var literals []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return moduleDir(path, entry)
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		root := filepath.Dir(path) == "." && file.Name.Name == "parse"
		for _, pos := range parseErrorLiterals(file, root) {
			literals = append(literals, fset.Position(pos).String())
		}
		return nil
	})
	require.NoError(t, err)
	assert.Empty(t, literals, "build each ParseError with core.NewError")
}

// moduleDir is the walk's decision on a directory: core, which defines the
// type, and what ./... skips (hidden and _-prefixed directories, testdata,
// node_modules and other modules) are left out.
func moduleDir(path string, entry fs.DirEntry) error {
	if path == "." {
		return nil
	}
	name := entry.Name()
	if path == "core" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules" {
		return fs.SkipDir
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	if err == nil {
		return fs.SkipDir
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// parseErrorLiterals are the positions of file's composite literals of
// core.ParseError: through core's or the root package's name for it (the
// root's ParseError is an alias), whatever the import is named, unqualified
// in the root package or under a dot import, and as the elided element of a
// slice, array or map literal.
func parseErrorLiterals(file *ast.File, rootPackage bool) []token.Pos {
	qualifiers := map[string]bool{}
	unqualified := rootPackage
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || (path != corePackagePath && path != rootPackagePath) {
			continue
		}
		name := pathpkg.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "." {
			unqualified = true
		}
		qualifiers[name] = true
	}
	isParseError := func(t ast.Expr) bool {
		star, ok := t.(*ast.StarExpr)
		if ok {
			t = star.X
		}
		switch t := t.(type) {
		case *ast.SelectorExpr:
			x, ok := t.X.(*ast.Ident)
			return ok && qualifiers[x.Name] && t.Sel.Name == "ParseError"
		case *ast.Ident:
			return unqualified && t.Name == "ParseError"
		}
		return false
	}
	var found []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if lit.Type != nil && isParseError(lit.Type) {
			found = append(found, lit.Pos())
			return true
		}
		var elem ast.Expr
		switch t := lit.Type.(type) {
		case *ast.ArrayType:
			elem = t.Elt
		case *ast.MapType:
			elem = t.Value
		}
		if elem == nil || !isParseError(elem) {
			return true
		}
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if ok {
				e = kv.Value
			}
			u, ok := e.(*ast.UnaryExpr)
			if ok && u.Op == token.AND {
				e = u.X
			}
			inner, ok := e.(*ast.CompositeLit)
			if ok && inner.Type == nil {
				found = append(found, inner.Pos())
			}
		}
		return true
	})
	return found
}

// The guard finds each spelling of a literal, so a passing walk means there
// is none, and finds none in what builds or names the type.
func TestParseErrorLiterals_FindsEverySpelling(t *testing.T) {
	tests := []struct {
		name string
		root bool
		src  string
		want int
	}{
		{"pointer", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = &core.ParseError{Code: "E2071"}`, 1},
		{"value", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = core.ParseError{}`, 1},
		{"renamed import", false, `import c "github.com/ashtonian/pkg/parse/core"; var _ = &c.ParseError{}`, 1},
		{"root alias", false, `import "github.com/ashtonian/pkg/parse"; var _ = &parse.ParseError{}`, 1},
		{"unqualified in the root package", true, `var _ = &ParseError{}`, 1},
		{"dot import", false, `import . "github.com/ashtonian/pkg/parse/core"; var _ = ParseError{}`, 1},
		{"elided slice elements", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = []*core.ParseError{{}, {Code: "E2071"}}`, 2},
		{"elided map values", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = map[string]core.ParseError{"a": {}}`, 1},
		{"constructor", false, `import "github.com/ashtonian/pkg/parse/core"; var _ = core.NewError(core.CodeUnknownField, "m")`, 0},
		{"slice of errors", false, `import "github.com/ashtonian/pkg/parse/core"; var pe *core.ParseError; var _ = []*core.ParseError{pe}`, 0},
		{"another package's ParseError", false, `import "example.com/other"; var _ = &other.ParseError{}`, 0},
		{"unqualified outside the root package", false, `var _ = &ParseError{}`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "x.go", "package x; "+tt.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			assert.Len(t, parseErrorLiterals(file, tt.root), tt.want)
		})
	}
}

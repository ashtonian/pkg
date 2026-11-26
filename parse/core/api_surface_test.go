//go:build unit

package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gowalkerImport = "github.com/ashtonian/pkg/gowalker"

// core's exported API names no gowalker type, so a gowalker change is no
// break for core's callers; parse's packages that walk gowalker shapes
// share them through parse/internal/shapes.
func TestExportedAPINamesNoGowalkerType(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var named []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		require.NoError(t, err)
		local := gowalkerName(file)
		if local == "" {
			continue
		}
		for _, node := range exportedSignatures(file) {
			if mentions(node, local) {
				named = append(named, fset.Position(node.Pos()).String())
			}
		}
	}
	assert.Empty(t, named, "exported declarations naming a gowalker type")
}

// gowalkerName is the name file imports gowalker as, empty when it does not.
func gowalkerName(file *ast.File) string {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != gowalkerImport {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "gowalker"
	}
	return ""
}

// exportedSignatures are the parts of file's exported declarations a
// caller sees: function and method signatures, exported types with their
// exported fields and methods, and exported variables and constants. Bodies
// and unexported fields are not.
func exportedSignatures(file *ast.File) []ast.Node {
	var nodes []ast.Node
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Name.IsExported() && (d.Recv == nil || exportedReceiver(d.Recv)) {
				nodes = append(nodes, d.Type)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				nodes = append(nodes, exportedSpec(spec)...)
			}
		}
	}
	return nodes
}

func exportedReceiver(recv *ast.FieldList) bool {
	typ := recv.List[0].Type
	for {
		switch r := typ.(type) {
		case *ast.StarExpr:
			typ = r.X
		case *ast.IndexExpr:
			typ = r.X
		case *ast.IndexListExpr:
			typ = r.X
		case *ast.Ident:
			return r.IsExported()
		default:
			return false
		}
	}
}

func exportedSpec(spec ast.Spec) []ast.Node {
	var nodes []ast.Node
	switch s := spec.(type) {
	case *ast.TypeSpec:
		if !s.Name.IsExported() {
			return nil
		}
		if s.TypeParams != nil {
			nodes = append(nodes, s.TypeParams)
		}
		strct, ok := s.Type.(*ast.StructType)
		if !ok {
			nodes = append(nodes, s.Type)
			return nodes
		}
		for _, field := range strct.Fields.List {
			if exportedField(field) {
				nodes = append(nodes, field.Type)
			}
		}
	case *ast.ValueSpec:
		for i, name := range s.Names {
			if !name.IsExported() {
				continue
			}
			if s.Type != nil {
				nodes = append(nodes, s.Type)
			}
			if i < len(s.Values) {
				nodes = append(nodes, s.Values[i])
			}
		}
	}
	return nodes
}

// exportedField reports a struct field a caller reaches: a named exported
// field, or an embedded exported type.
func exportedField(field *ast.Field) bool {
	if len(field.Names) == 0 {
		typ := field.Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		switch e := typ.(type) {
		case *ast.Ident:
			return e.IsExported()
		case *ast.SelectorExpr:
			return e.Sel.IsExported()
		}
		return false
	}
	for _, name := range field.Names {
		if name.IsExported() {
			return true
		}
	}
	return false
}

// mentions reports node naming a member of the package imported as pkg.
func mentions(node ast.Node, pkg string) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return !found
		}
		ident, ok := sel.X.(*ast.Ident)
		if ok && ident.Name == pkg {
			found = true
		}
		return !found
	})
	return found
}

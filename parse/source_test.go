//go:build unit

package parse_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	// Linked so that a change to what any of them imports changes the test
	// binary: go test's cache does not see what the go list subprocess reads.
	_ "github.com/ashtonian/pkg/parse"
	_ "github.com/ashtonian/pkg/parse/core"
	_ "github.com/ashtonian/pkg/parse/filter"
	_ "github.com/ashtonian/pkg/parse/internal/backend"
	_ "github.com/ashtonian/pkg/parse/internal/sqlexec"
	_ "github.com/ashtonian/pkg/parse/internal/sqlplan"
	_ "github.com/ashtonian/pkg/parse/patch"
)

// The packages a server compiles whatever its transport link no HTTP stack:
// net/http, and crypto/tls with it, belong to parsehttp.
func TestTransportFreePackages_ImportNoNetHTTP(t *testing.T) {
	for _, pkg := range []string{
		"github.com/ashtonian/pkg/parse",
		"github.com/ashtonian/pkg/parse/core",
		"github.com/ashtonian/pkg/parse/filter",
		"github.com/ashtonian/pkg/parse/patch",
	} {
		out, err := exec.CommandContext(t.Context(), "go", "list", "-deps", pkg).Output()
		require.NoError(t, err, pkg)
		deps := strings.Fields(string(out))
		require.Contains(t, deps, pkg, "go list named the package")
		require.False(t, slices.Contains(deps, "net/http"), "%s imports net/http", pkg)
	}
}

// The packages around the provider contract import across it in one
// direction. internal/backend, the contract every provider runs on, imports
// filter, patch, core and the standard library alone, and reaches neither
// the SQL compiler nor the SQL executor, so a provider that runs no SQL
// links neither. The root package composes requests and compiles the SQL
// builders' statements through internal/sqlplan, and never reaches the
// executor; the executor compiles through internal/sqlplan, and neither
// reaches the root package.
func TestLayout_PackagesImportAcrossTheContract(t *testing.T) {
	const (
		root     = "github.com/ashtonian/pkg/parse"
		backend  = root + "/internal/backend"
		sqlplan  = root + "/internal/sqlplan"
		sqlexec  = root + "/internal/sqlexec"
		contract = "filter, patch, core and the standard library"
	)
	listed := packageImports(t, root, backend, sqlplan, sqlexec)

	for _, imported := range listed[backend].Imports {
		standard := !strings.Contains(strings.Split(imported, "/")[0], ".")
		allowed := []string{root + "/filter", root + "/patch", root + "/core"}
		assert.True(t, standard || slices.Contains(allowed, imported), "internal/backend imports %s; it may import %s only", imported, contract)
	}
	for pkg, forbidden := range map[string][]string{
		backend: {root, sqlplan, sqlexec},
		root:    {sqlexec},
		sqlplan: {root, sqlexec},
		sqlexec: {root},
	} {
		for _, f := range forbidden {
			assert.NotContains(t, listed[pkg].Deps, f, "%s reaches %s", pkg, f)
		}
	}
	assert.Subset(t, listed[root].Imports, []string{backend, sqlplan}, "the root package composes through internal/backend and compiles through internal/sqlplan")
	assert.Contains(t, listed[sqlexec].Imports, sqlplan, "the SQL executor compiles through internal/sqlplan")
}

// importedPackage is what go list reports of a package's imports: its
// direct imports and every package it imports, transitively.
type importedPackage struct {
	ImportPath string
	Imports    []string
	Deps       []string
}

// packageImports is what go list reports of pkgs' imports, by import path.
func packageImports(t *testing.T, pkgs ...string) map[string]importedPackage {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", append([]string{"list", "-json"}, pkgs...)...).Output()
	require.NoError(t, err)
	listed := make(map[string]importedPackage, len(pkgs))
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p importedPackage
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		listed[p.ImportPath] = p
	}
	for _, pkg := range pkgs {
		require.Contains(t, listed, pkg, "go list named the package")
	}
	return listed
}

// The module's text never formats a type through fmt or reflect.Type's
// String: for a struct type with no name, which a row, a document or one of
// their fields may be, those write every field's tags, values included.
// gowalker.TypeText writes the same type without them. The rule is checked
// on the module's type-checked source rather than on error values, so a site
// no test reaches is held to it too: a reflect.Type operand of a fmt call, a
// %T verb in a constant fmt format, or a call of reflect.Type's String.
func TestSource_FormatsTypesThroughTypeText(t *testing.T) {
	packages := modulePackages(t)
	exports := make(map[string]string, len(packages))
	for _, pkg := range packages {
		if pkg.Export != "" {
			exports[pkg.ImportPath] = pkg.Export
		}
	}
	fset := token.NewFileSet()
	imports := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		export, ok := exports[path]
		if !ok {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(export)
	})
	var violations []string
	for _, pkg := range packages {
		if pkg.DepOnly || pkg.Module == nil || pkg.Module.Path != modulePath {
			continue
		}
		var files []*ast.File
		for _, name := range pkg.GoFiles {
			file, err := parser.ParseFile(fset, filepath.Join(pkg.Dir, name), nil, parser.SkipObjectResolution)
			require.NoError(t, err)
			files = append(files, file)
		}
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
		conf := types.Config{Importer: imports}
		_, err := conf.Check(pkg.ImportPath, fset, files, info)
		require.NoError(t, err, pkg.ImportPath)
		for _, file := range files {
			ast.Inspect(file, func(n ast.Node) bool {
				why := typeFormatting(n, info)
				if why != "" {
					violations = append(violations, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), why))
				}
				return true
			})
		}
	}
	assert.Empty(t, violations, "format each type through gowalker.TypeText")
}

const modulePath = "github.com/ashtonian/pkg/parse"

// listedPackage is the part of `go list -json` the check reads.
type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Export     string
	DepOnly    bool
	Module     *struct{ Path string }
}

// modulePackages lists the module's packages and their dependencies, each
// with its compiled export data, so the module type-checks against what the
// build compiled.
func modulePackages(t *testing.T) []listedPackage {
	t.Helper()
	readModuleTree(t)
	cmd := exec.CommandContext(t.Context(), "go", "list", "-e", "-export", "-deps", "-json=ImportPath,Dir,GoFiles,Export,DepOnly,Module", "./...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	var packages []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg listedPackage
		err := dec.Decode(&pkg)
		if errors.Is(err, io.EOF) {
			return packages
		}
		require.NoError(t, err)
		packages = append(packages, pkg)
	}
}

// readModuleTree reads every directory in which go list's ./... can find a
// package. go test keys a cached result on what the test process opens, not
// on what the go list subprocess reads: without this, a package that no test
// links would leave the key unchanged and replay the last pass.
func readModuleTree(t *testing.T) {
	t.Helper()
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() || path == "." {
			return nil
		}
		// What ./... skips: hidden and _-prefixed directories, testdata,
		// node_modules (go.mod ignores it) and other modules.
		name := entry.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "node_modules" {
			return fs.SkipDir
		}
		_, err = os.Stat(filepath.Join(path, "go.mod"))
		if err == nil {
			return fs.SkipDir
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	})
	require.NoError(t, err)
}

// typeFormatting says how n formats a type, or "" when it does not.
func typeFormatting(n ast.Node, info *types.Info) string {
	switch n := n.(type) {
	case *ast.CallExpr:
		fn := calledFunc(n, info)
		if fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != "fmt" {
			return ""
		}
		sig, _ := fn.Type().(*types.Signature)
		for i, arg := range n.Args {
			if isReflectType(info.TypeOf(arg)) {
				return fmt.Sprintf("fmt.%s formats a reflect.Type", fn.Name())
			}
			if sig == nil || i >= sig.Params().Len() || sig.Params().At(i).Name() != "format" {
				continue
			}
			value := info.Types[arg].Value
			if value != nil && value.Kind() == constant.String && hasTypeVerb(constant.StringVal(value)) {
				return fmt.Sprintf("fmt.%s formats with %%T", fn.Name())
			}
		}
	case *ast.SelectorExpr:
		sel := info.Selections[n]
		if sel != nil && sel.Kind() != types.FieldVal && sel.Obj().Name() == "String" && isReflectType(sel.Recv()) {
			return "reflect.Type.String"
		}
	}
	return ""
}

func calledFunc(call *ast.CallExpr, info *types.Info) *types.Func {
	var id *ast.Ident
	switch fun := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		id = fun
	case *ast.SelectorExpr:
		id = fun.Sel
	default:
		return nil
	}
	fn, _ := info.Uses[id].(*types.Func)
	return fn
}

func isReflectType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "reflect" && obj.Name() == "Type"
}

// hasTypeVerb reports a %T verb in a fmt format.
func hasTypeVerb(format string) bool {
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		for i < len(format) && strings.IndexByte("+-# 0123456789.*[]", format[i]) >= 0 {
			i++
		}
		if i < len(format) && format[i] == 'T' {
			return true
		}
	}
	return false
}

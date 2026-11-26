//go:build unit

package client

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
)

// The parse core package's code declarations, beside this module in the
// repository.
const coreCodesPath = "../core/code.go"

// codeConstants are the constants of type Code that file declares, by name.
func codeConstants(t *testing.T, file string, src []byte) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	codes := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			typ, ok := value.Type.(*ast.Ident)
			if !ok || typ.Name != "Code" {
				continue
			}
			for i, name := range value.Names {
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s: %s is no string literal", file, name.Name)
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %s: %v", file, name.Name, err)
				}
				codes[name.Name] = text
			}
		}
	}
	return codes
}

// The codes are the server's catalog, under the names parse's core package
// gives them.
func TestCodes_AreTheServersCatalog(t *testing.T) {
	var catalog []struct {
		Code string `json:"code"`
	}
	decodeCorpus(t, errorCodesPath, &catalog)
	served := make([]string, 0, len(catalog))
	for _, c := range catalog {
		served = append(served, c.Code)
	}
	src, err := os.ReadFile("error.go")
	must(t, err)
	ours := codeConstants(t, "error.go", src)
	checkDeep(t, slices.Sorted(slices.Values(served)), slices.Sorted(maps.Values(ours)), "the codes against", errorCodesPath)
	checkDeep(t, codeConstants(t, coreCodesPath, readCorpus(t, coreCodesPath)), ours, "the names against", coreCodesPath)
}

func TestDecodeError(t *testing.T) {
	zero := 0
	tests := []struct {
		name   string
		status int
		body   string
		want   *Error
		text   string
	}{
		{
			name: "a patch error", status: http.StatusUnprocessableEntity,
			body: `{"code":"E2077","message":"value does not match the field type","path":"/count","index":0,"details":{"field":"count"}}`,
			want: &Error{Status: 422, Code: CodePatchValueInvalid, Message: "value does not match the field type", Path: "/count", Index: &zero, Details: map[string]any{"field": "count"}},
			text: "client: E2077 (HTTP 422): value does not match the field type at /count in operation 0",
		},
		{
			name: "an application's code", status: http.StatusNotFound,
			body: `{"code":"E4004","message":"device d1 not found"}`,
			want: &Error{Status: 404, Code: "E4004", Message: "device d1 not found"},
			text: "client: E4004 (HTTP 404): device d1 not found",
		},
		{
			name: "a proxy's page", status: http.StatusBadGateway, body: `<html>bad gateway</html>`,
			want: &Error{Status: 502}, text: "client: HTTP 502",
		},
		{name: "no body", status: http.StatusServiceUnavailable, want: &Error{Status: 503}, text: "client: HTTP 503"},
		{
			name: "another JSON document", status: http.StatusBadRequest, body: `{"error":"bad request"}`,
			want: &Error{Status: 400}, text: "client: HTTP 400",
		},
		{
			name: "a code that is no string", status: http.StatusBadRequest, body: `{"code":2077,"message":"m"}`,
			want: &Error{Status: 400}, text: "client: HTTP 400",
		},
		{
			name: "a message that is null", status: http.StatusBadRequest, body: `{"code":"E2071","message":null}`,
			want: &Error{Status: 400}, text: "client: HTTP 400",
		},
		{
			name: "an index of another type keeps the code", status: http.StatusBadRequest,
			body: `{"code":"E2071","message":"m","index":"3","path":"/a"}`,
			want: &Error{Status: 400, Code: CodeUnknownField, Message: "m", Path: "/a"}, text: "client: E2071 (HTTP 400): m at /a",
		},
		{
			name: "details of another type keep the code", status: http.StatusBadRequest,
			body: `{"code":"E2071","message":"m","details":["x"],"index":1}`,
			want: &Error{Status: 400, Code: CodeUnknownField, Message: "m", Index: new(1)}, text: "client: E2071 (HTTP 400): m in operation 1",
		},
		{
			name: "a path of another type keeps the code", status: http.StatusBadRequest,
			body: `{"code":"E2071","message":"m","path":5,"index":null}`,
			want: &Error{Status: 400, Code: CodeUnknownField, Message: "m"}, text: "client: E2071 (HTTP 400): m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeError(tt.status, []byte(tt.body))
			checkDeep(t, tt.want, got)
			check(t, tt.text, got.Error())
		})
	}
}

func TestError_IsMatchesItsCodeOnly(t *testing.T) {
	err := error(&Error{Status: 422, Code: CodeUnknownField})
	checkErrorIs(t, err, CodeUnknownField)
	if errors.Is(err, CodeInvalidOperator) {
		t.Error("an E2071 matches E2072")
	}
	if errors.Is(&Error{Status: 502}, Code("")) {
		t.Error("an answer with no code matches the empty code")
	}
	check(t, "E2071", CodeUnknownField.Error())
}

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workspace writes files under a new directory, with a go.work using mods,
// and returns the directory.
func workspace(t *testing.T, mods []string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	work := "go 1.27.0\n\nuse (\n"
	for _, m := range mods {
		work += "\t./" + m + "\n"
		files[m+"/go.mod"] = "module example.com/" + m + "\n\ngo 1.27.0\n"
	}
	files["go.work"] = work + ")\n"
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return root
}

func run(t *testing.T, root, allow string) []string {
	t.Helper()
	allowPath := filepath.Join(root, "allow.json")
	if allow != "" {
		require.NoError(t, os.WriteFile(allowPath, []byte(allow), 0o644))
	}
	findings, err := check(t.Context(), root, allowPath)
	require.NoError(t, err)
	out := make([]string, len(findings))
	for i, f := range findings {
		out[i] = f.String()
	}
	return out
}

const apiSource = `package a

import "example.com/mod/b"

// T is a type.
type T struct {
	b.Embedded
	Field int
}

func (T) Method() {}

type G[V any] struct{}

func (G[V]) Get() {}

func WithLimit(n int) int { return n }

const (
	CodeOne = 1
	CodeTwo = 2
)
`

const embeddedSource = `package b

type Embedded struct{ Promoted int }

func (Embedded) Inherited() {}
`

func TestCheck_IdentifiersResolveAgainstTheModule(t *testing.T) {
	root := workspace(t, []string{"mod"}, map[string]string{
		"mod/a/a.go": apiSource,
		"mod/b/b.go": embeddedSource,
		"mod/README.md": "# mod\n\n" +
			"`a.T`, `a.T.Method`, `T.Field`, `T.Promoted`, `T.Inherited`, `a.G[int].Get`, `a.Code*`, `WithLimit`, `*a.T`\n\n" +
			"`fmt.Println`, `Other.Thing` and `http.Request` belong elsewhere.\n\n" +
			"`a.Missing`, `a.T.Nope`, `T.Gone`, `a.Prefix*`, `WithNothing`\n\n" +
			"```go\na.InFence()\n```\n",
	})
	assert.Equal(t, []string{
		"mod/README.md:7: T.Gone names nothing mod declares",
		"mod/README.md:7: WithNothing names nothing mod declares",
		"mod/README.md:7: a.Missing names nothing mod declares",
		"mod/README.md:7: a.Prefix* names nothing mod declares",
		"mod/README.md:7: a.T.Nope names nothing mod declares",
	}, run(t, root, ""))
}

func TestCheck_OptionsResolveAcrossTheWorkspace(t *testing.T) {
	root := workspace(t, []string{"core", "plugin"}, map[string]string{
		"core/core.go":     "package core\n\nfunc WithCore() {}\n",
		"plugin/plugin.go": "package plugin\n\nfunc WithPlugin() {}\n",
		"core/README.md":   "# core\n\n`WithPlugin` plugs `WithCore` in.\n",
	})
	assert.Empty(t, run(t, root, ""))
}

func TestCheck_AQualifierShadowedByAnImportIsNotChecked(t *testing.T) {
	root := workspace(t, []string{"codec"}, map[string]string{
		"codec/msgpack/msgpack.go": "package msgpack\n\nimport lib \"example.com/lib/msgpack/v5\"\n\nvar _ = lib.X\n\nfunc New() {}\n",
		"codec/README.md":          "# codec\n\n`msgpack.Marshal` is the library's; `msgpack.New` is ours.\n",
	})
	assert.Empty(t, run(t, root, ""))
}

func TestCheck_LinksAndAnchorsResolve(t *testing.T) {
	root := workspace(t, []string{"mod"}, map[string]string{
		"mod/a.go": "package mod\n",
		"mod/README.md": "# mod\n\n## Code map\n\n## Code map\n\n## The `Foo` type — and more\n\n" +
			"[ok](docs/guide.md#usage) [dir](docs/) [self](#code-map-1) [code](#the-foo-type--and-more) [html](docs/guide.md#pinned)\n\n" +
			"[web](https://example.com/x) [mail](mailto:a@example.com) `[span](missing.md)`\n\n" +
			"[gone](docs/missing.md) [noanchor](docs/guide.md#nope) [selfmiss](#code-map-2) [out](../../x.md)\n\n" +
			"[ref]: docs/also-missing.md\n",
		"mod/docs/guide.md": "# Guide\n\n## Usage\n\n<a id=\"pinned\"></a>\n\n```md\n## Fenced heading\n```\n",
	})
	assert.Equal(t, []string{
		"mod/README.md:13: link #code-map-2: mod/README.md has no heading or anchor \"code-map-2\"",
		"mod/README.md:13: link ../../x.md leaves the repository",
		"mod/README.md:13: link docs/guide.md#nope: mod/docs/guide.md has no heading or anchor \"nope\"",
		"mod/README.md:13: link docs/missing.md: mod/docs/missing.md does not exist",
		"mod/README.md:15: link docs/also-missing.md: mod/docs/also-missing.md does not exist",
	}, run(t, root, ""))
}

func TestCheck_CodeMapEntriesExist(t *testing.T) {
	root := workspace(t, []string{"mod"}, map[string]string{
		"mod/a.go":          "package mod\n\nfunc A() {}\n",
		"mod/sub/sub.go":    "package sub\n",
		"mod/docs/dev.md":   "# Development\n\n## Key files\n\n| File | Role |\n|---|---|\n| `a.go` | module root, from a guide |\n",
		"mod/internal/x.go": "package internal\n",
		"mod/README.md": "# mod\n\n## Code map\n\n### Files\n\n| File | Role |\n|---|---|\n" +
			"| `a.go` | here |\n| `sub/` | a directory |\n| `internal/x.go`, `*.go` | a path and a pattern |\n| `Resource.Method` | not a path |\n" +
			"| `gone.go` | missing |\n| `nothing/*.go` | matches nothing |\n\n## Other\n\n| File | Role |\n|---|---|\n| `ignored.go` | not a code map |\n",
	})
	assert.Equal(t, []string{
		"mod/README.md:13: code map entry gone.go: no such file under mod or mod",
		"mod/README.md:14: code map entry nothing/*.go: no such file under mod or mod",
	}, run(t, root, ""))
}

func TestCheck_ReverseChecksAModuleThatOptsIn(t *testing.T) {
	files := map[string]string{
		"mod/a/a.go":          "package a\n\nfunc Named() {}\n\nfunc Unnamed() {}\n\nfunc Allowed() {}\n\nconst FamilyOne, FamilyTwo = 1, 2\n",
		"mod/a/extra.go":      "package a\n\nfunc unexported() {}\n",
		"mod/a/doc.go":        "// Package a is documented here.\npackage a\n",
		"mod/a/README.md":     "# a\n\n`Named`, the `Family*` constants.\n\n## Code map\n\n| File | Role |\n|---|---|\n| `a.go` | the API |\n",
		"mod/internal/i/i.go": "package i\n\nfunc Internal() {}\n",
		"mod/cmd/main.go":     "package main\n\nfunc Main() {}\n",
		"mod/README.md":       "# mod\n\n## Code map\n\n| File | Role |\n|---|---|\n| `internal/` | internals |\n",
	}
	root := workspace(t, []string{"mod"}, files)
	assert.Empty(t, run(t, root, `{"mod": {"reverse": false}}`), "reverse checks are opt-in")

	assert.Equal(t, []string{
		"allow.json: mod: identifiers entry \"x.Y\" gives no reason",
		"allow.json: mod: identifiers entry \"x.Y\" matches nothing; remove it",
		"allow.json: mod: unnamed_exports entry \"a.Stale\" matches nothing; remove it",
		"mod/a: a.Unnamed is exported and no Markdown names it",
		"mod/a/extra.go: mod/a/README.md's code map does not list it",
		"mod/cmd/main.go: mod/README.md's code map does not list it",
	}, run(t, root, `{"mod": {
		"reverse": true,
		"identifiers": {"x.Y": " "},
		"unnamed_exports": {"a.Allowed": "named by its family elsewhere", "a.Stale": "was once"}
	}}`))
}

func TestCheck_AnAllowListForNoModuleIsAFinding(t *testing.T) {
	root := workspace(t, []string{"mod"}, map[string]string{"mod/a.go": "package mod\n"})
	assert.Equal(t, []string{"allow.json: allow list names gone, which is no workspace module"}, run(t, root, `{"gone": {}}`))
}

func TestCheck_ANestedModuleIsNotTheOuterModules(t *testing.T) {
	root := workspace(t, []string{"outer"}, map[string]string{
		"outer/a.go":            "package outer\n\nfunc A() {}\n",
		"outer/README.md":       "# outer\n\n`outer.A`\n\n## Code map\n\n| File | Role |\n|---|---|\n| `a.go` | the API |\n",
		"outer/inner/go.mod":    "module example.com/outer/inner\n\ngo 1.27.0\n",
		"outer/inner/b.go":      "package inner\n\nfunc B() {}\n",
		"outer/inner/README.md": "# inner\n\n`outer.Missing` is not outer's to check here.\n",
	})
	assert.Empty(t, run(t, root, `{"outer": {"reverse": true}}`))
}

func TestSlug(t *testing.T) {
	tests := []struct{ heading, want string }{
		{"Code map", "code-map"},
		{"The `Foo` type", "the-foo-type"},
		{"Errors — and status", "errors--and-status"},
		{"[Linked](x.md) heading", "linked-heading"},
		{"snake_case and 1.2.3", "snake_case-and-123"},
		{"Ünïcode Straße", "ünïcode-straße"},
	}
	for _, tt := range tests {
		t.Run(tt.heading, func(t *testing.T) {
			assert.Equal(t, tt.want, slug(tt.heading))
		})
	}
}

func TestCodeSpans(t *testing.T) {
	spans, rest := codeSpans("a `one` b ``two ` with`` c `unclosed")
	assert.Equal(t, []string{"one", "two ` with"}, spans)
	assert.Equal(t, "a       b                c `unclosed", rest)
}

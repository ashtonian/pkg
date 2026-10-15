package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestArchivesUseModuleBoundariesAndManifestVersions(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, ".release-please-manifest.json", `{"httpx":"0.0.0","httpx/codec/proto":"0.2.3"}`)
	writeFixture(t, root, "httpx/go.mod", "module example.com/pkg/httpx\n\ngo 1.26.0\n")
	writeFixture(t, root, "httpx/LICENSE", "parent license\n")
	writeFixture(t, root, "httpx/client.go", "package httpx\n")
	writeFixture(t, root, "httpx/codec/proto/go.mod", "module example.com/pkg/httpx/codec/proto\n\ngo 1.26.0\n")
	writeFixture(t, root, "httpx/codec/proto/LICENSE", "optional license\n")
	writeFixture(t, root, "httpx/codec/proto/codec.go", "package proto\n")
	proxy := t.TempDir()
	var index bytes.Buffer
	if err := run(proxy, filepath.Join(root, ".release-please-manifest.json"), "v0.1.0",
		[]string{filepath.Join(root, "httpx"), filepath.Join(root, "httpx/codec/proto")}, &index); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index.String(), "example.com/pkg/httpx\tv0.1.0\t") ||
		!strings.Contains(index.String(), "example.com/pkg/httpx/codec/proto\tv0.2.3\t") {
		t.Fatalf("wrong consumer index: %s", index.String())
	}
	for _, tc := range []struct{ path, version string }{
		{"example.com/pkg/httpx", "v0.1.0"},
		{"example.com/pkg/httpx/codec/proto", "v0.2.3"},
	} {
		z, err := zip.OpenReader(filepath.Join(proxy, tc.path, "@v", tc.version+".zip"))
		if err != nil {
			t.Fatal(err)
		}
		prefix := tc.path + "@" + tc.version + "/"
		licensed := false
		for _, f := range z.File {
			licensed = licensed || f.Name == prefix+"LICENSE"
			if tc.path == "example.com/pkg/httpx" && strings.HasPrefix(f.Name, prefix+"codec/proto/") {
				t.Errorf("parent archive includes nested module: %s", f.Name)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		if !licensed {
			t.Errorf("%s archive has no LICENSE", tc.path)
		}
	}
}

func TestArchiveRejectsMissingLicenseAndLocalReplacements(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module example.com/pkg/httpx\n\ngo 1.26.0\n")
	if _, err := archive(t.TempDir(), "v0.1.0", root); err == nil || !strings.Contains(err.Error(), "LICENSE") {
		t.Fatalf("missing license: %v", err)
	}
	writeFixture(t, root, "LICENSE", "license\n")
	writeFixture(t, root, "go.mod", "module example.com/pkg/httpx\n\ngo 1.26.0\n\nreplace example.com/private => ../private\n")
	if _, err := archive(t.TempDir(), "v0.1.0", root); err == nil || !strings.Contains(err.Error(), "replace") {
		t.Fatalf("local replacement: %v", err)
	}
}

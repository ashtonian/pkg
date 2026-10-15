//go:build integration

package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestExampleClientExecutable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "httpx-example-client")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-race", "-o", binary, "../client")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build example client: %v\n%s", err, output)
	}
	srv := httptest.NewServer(newRouter(demoToken))
	defer srv.Close()
	cmd := exec.CommandContext(ctx, binary, "-base-url", srv.URL, "-deadline", "15s")
	cmd.Env = append(os.Environ(), "HTTPX_EXAMPLE_TOKEN="+demoToken)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("client demonstration: %v\n%s", err, output)
	}
	for _, marker := range []string{"Multiple Resource Types", "fwd-status=304", "Created:", "Deleted: status 204", "No auth: status 401", "20 concurrent requests completed", "Metrics Summary"} {
		if !strings.Contains(string(output), marker) {
			t.Errorf("client did not demonstrate %q\n%s", marker, output)
		}
	}

	// A status failure must make the command fail, not report zero-value data
	// and a successful completion as the original example did.
	cmd = exec.CommandContext(ctx, binary, "-base-url", srv.URL, "-deadline", "15s")
	cmd.Env = append(os.Environ(), "HTTPX_EXAMPLE_TOKEN=incorrect-demo-token")
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "unexpected HTTP 401") {
		t.Fatalf("wrong-token command must fail clearly: %v\n%s", err, output)
	}
}

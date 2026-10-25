// Command modarchive writes module directories as a file-based Go module
// proxy using golang.org/x/mod/zip, with the same file-selection rules as the
// go command. Each module uses its release-please manifest version; a 0.0.0
// bootstrap entry uses -initial-version instead.
//
// The output is one tab-separated module path, version and source directory
// per archive, suitable for a fresh-consumer build outside the workspace.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

func main() {
	out := flag.String("out", "", "proxy directory to write")
	manifest := flag.String("manifest", "", "release-please manifest in the repository root")
	initial := flag.String("initial-version", "v0.1.0", "candidate version for a 0.0.0 bootstrap entry")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *out, *manifest, *initial, flag.Args(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "modarchive:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out, manifest, initial string, dirs []string, index io.Writer) error {
	if out == "" || manifest == "" || len(dirs) == 0 {
		return errors.New("usage: modarchive -out DIR -manifest FILE MODULE_DIR [MODULE_DIR]")
	}
	if !semver.IsValid(initial) || semver.Canonical(initial) != initial {
		return fmt.Errorf("initial version %q is not a canonical semantic version", initial)
	}
	manifest, err := filepath.Abs(manifest)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	var versions map[string]string
	if err := json.Unmarshal(data, &versions); err != nil {
		return err
	}
	for _, dir := range dirs {
		dir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if strings.ContainsAny(dir, "\t\r\n") {
			return fmt.Errorf("module directory %q contains a line delimiter", dir)
		}
		rel, err := filepath.Rel(filepath.Dir(manifest), dir)
		if err != nil {
			return err
		}
		version, ok := versions[filepath.ToSlash(rel)]
		if !ok {
			return fmt.Errorf("%s: no release-please manifest entry", dir)
		}
		version = "v" + version
		if version == "v0.0.0" {
			version = initial
		}
		path, err := archive(ctx, out, version, dir)
		if err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		if _, err := fmt.Fprintf(index, "%s\t%s\t%s\n", path, version, dir); err != nil {
			return err
		}
	}
	return nil
}

// archive writes <escaped module>/@v/{list,version.info,version.mod,version.zip}.
// Published modules must carry their own license and resolve without replace.
func archive(ctx context.Context, out, version, dir string) (string, error) {
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}
	f, err := modfile.Parse("go.mod", gomod, nil)
	if err != nil {
		return "", err
	}
	if f.Module == nil {
		return "", errors.New("go.mod declares no module path")
	}
	if len(f.Replace) != 0 {
		return "", errors.New("published module must not contain replace directives")
	}
	license, err := os.ReadFile(filepath.Join(dir, "LICENSE"))
	if err != nil {
		return "", fmt.Errorf("module-local LICENSE: %w", err)
	}
	if strings.TrimSpace(string(license)) == "" {
		return "", errors.New("module-local LICENSE is empty")
	}
	path := f.Module.Mod.Path
	if err := module.Check(path, version); err != nil {
		return "", err
	}
	if semver.Canonical(version) != version {
		return "", fmt.Errorf("version %q is not canonical", version)
	}
	escaped, err := module.EscapePath(path)
	if err != nil {
		return "", err
	}
	vdir := filepath.Join(out, filepath.FromSlash(escaped), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return "", err
	}
	zipPath := filepath.Join(vdir, version+".zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		return "", err
	}
	if err := createWorkingTreeArchive(ctx, zf, module.Version{Path: path, Version: version}, dir); err != nil {
		return "", errors.Join(err, zf.Close())
	}
	if err := zf.Close(); err != nil {
		return "", err
	}
	if err := checkPublicArchive(zipPath, path+"@"+version+"/"); err != nil {
		return "", errors.Join(err, os.Remove(zipPath))
	}
	if err := os.WriteFile(filepath.Join(vdir, version+".mod"), gomod, 0o644); err != nil {
		return "", err
	}
	info, err := json.Marshal(struct {
		Version string
		Time    time.Time
	}{version, time.Now().UTC()})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(vdir, version+".info"), info, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(vdir, "list"), []byte(version+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

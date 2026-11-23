//go:build unit

package client

// The module requires nothing, so its tests use the standard library alone:
// a test dependency would be listed in every consumer's module graph.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	flag.Bool("update", false, "rewrite parse's testdata/client_requests.json from the requests this client builds")
	flag.Parse()
	os.Exit(m.Run())
}

// updating reports whether the run rewrites the fixtures it checks (-update).
func updating() bool { return flag.Lookup("update").Value.String() == "true" }

// The corpora parse records from its handlers and registry, and the
// requests this client builds for parse's tests to read. They are parse's
// testdata, beside this module in the repository; the module's archive does
// not carry them.
const (
	sdkWirePath           = "../testdata/sdk_wire.json"
	errorCodesPath        = "../testdata/error_codes.json"
	schemaDocumentPath    = "../testdata/schema_document.json"
	filterConformancePath = "../testdata/filter_conformance.json"
	patchConformancePath  = "../testdata/patch_conformance.json"
	clientRequestsPath    = "../testdata/client_requests.json"
)

// readCorpus reads one of parse's corpora. Outside the repository, where a
// downloaded module has no parse testdata beside it, the test skips; in it,
// a missing corpus fails.
func readCorpus(t *testing.T, path string) []byte {
	t.Helper()
	_, err := os.Stat(filepath.Dir(path))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is parse's testdata, in the repository beside this module; a module archive does not carry it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return data
}

// decodeCorpus decodes one of parse's corpora into v.
func decodeCorpus(t *testing.T, path string, v any) {
	t.Helper()
	err := json.Unmarshal(readCorpus(t, path), v)
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
}

func encode(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encoding %T: %v", v, err)
	}
	return string(b)
}

// jsonValue is data's JSON value, its numbers kept as their text.
func jsonValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// checkJSON fails unless got and want are the same JSON value: the same
// members in any order, the same elements in order, numbers spelt alike.
func checkJSON(t *testing.T, want, got string, context ...any) {
	t.Helper()
	w, err := jsonValue([]byte(want))
	if err != nil {
		t.Fatalf("%sthe wanted JSON %s: %v", prefix(context), want, err)
	}
	g, err := jsonValue([]byte(got))
	if err != nil {
		t.Fatalf("%sthe JSON %s: %v", prefix(context), got, err)
	}
	if !reflect.DeepEqual(w, g) {
		t.Errorf("%sgot JSON\n\t%s\nwant\n\t%s", prefix(context), got, want)
	}
}

// check fails unless got equals want.
func check[T comparable](t *testing.T, want, got T, context ...any) {
	t.Helper()
	if got != want {
		t.Errorf("%sgot %#v, want %#v", prefix(context), got, want)
	}
}

// checkDeep fails unless got and want are deeply equal.
func checkDeep(t *testing.T, want, got any, context ...any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%sgot %#v, want %#v", prefix(context), got, want)
	}
}

// checkErrorIs fails unless err matches target.
func checkErrorIs(t *testing.T, err, target error, context ...any) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%sgot error %v, want one matching %v", prefix(context), err, target)
	}
}

// must fails the test on err.
func must(t *testing.T, err error, context ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s%v", prefix(context), err)
	}
}

func prefix(context []any) string {
	if len(context) == 0 {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintln(context...)) + ": "
}

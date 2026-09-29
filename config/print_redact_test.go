//go:build unit

package config

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type redactInner struct {
	Value string `koanf:"value"`
}

type redactContainers struct {
	Block  redactInner            `koanf:"block" sensitive:"true"`
	Ptr    *redactInner           `koanf:"ptr" sensitive:"true"`
	Blocks []redactInner          `koanf:"blocks" sensitive:"true"`
	ByName map[string]redactInner `koanf:"by_name" sensitive:"true"`
	Plain  redactInner            `koanf:"plain"`
}

func newRedactContainers() redactContainers {
	return redactContainers{
		Block:  redactInner{Value: "SECRET_STRUCT"},
		Ptr:    &redactInner{Value: "SECRET_PTR"},
		Blocks: []redactInner{{Value: "SECRET_SLICE"}},
		ByName: map[string]redactInner{"a": {Value: "SECRET_MAP"}},
		Plain:  redactInner{Value: "visible"},
	}
}

// Sensitive:"true" on a struct, slice or
// map field was only consulted at leaves, so the walker descended past it and
// printed the contents.
func TestRedaction_SensitiveContainerTag(t *testing.T) {
	cfg := newRedactContainers()

	m := ToMap(cfg, true)
	for _, key := range []string{"block.value", "ptr.value", "blocks[0].value", "by_name[a].value"} {
		if m[key] != redactedValue {
			t.Errorf("ToMap[%s] = %v, want %s", key, m[key], redactedValue)
		}
	}
	if m["plain.value"] != "visible" {
		t.Errorf("plain.value = %v, want visible: redaction must not spread to untagged siblings", m["plain.value"])
	}

	var buf bytes.Buffer
	if err := PrintRedacted(cfg, WithWriter(&buf), WithFormat(FormatJSON)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
	if strings.Contains(buf.String(), "SECRET_") {
		t.Errorf("a secret reached PrintRedacted output:\n%s", buf.String())
	}

	shown := ToMap(cfg, false)
	if shown["blocks[0].value"] != "SECRET_SLICE" {
		t.Errorf("unredacted blocks[0].value = %v, want SECRET_SLICE", shown["blocks[0].value"])
	}
}

type redactLoop struct {
	Name     string      `koanf:"name"`
	Password string      `koanf:"password"`
	Next     *redactLoop `koanf:"next"`
}

func TestRedaction_PointerCycleTerminates(t *testing.T) {
	a := &redactLoop{Name: "a", Password: "SECRET_CYCLE"}
	a.Next = a

	m := ToMap(a, true)
	if m["next"] != cycleValue {
		t.Errorf("next = %v, want %s", m["next"], cycleValue)
	}
	if m["password"] != redactedValue {
		t.Errorf("password = %v, want %s", m["password"], redactedValue)
	}
}

func TestPrintRedacted_PointerLeafPrintsValue(t *testing.T) {
	port := 8443
	cfg := struct {
		Port  *int `koanf:"port"`
		Unset *int `koanf:"unset"`
	}{Port: &port}

	m := ToMap(cfg, true)
	if m["port"] != "8443" {
		t.Errorf("port = %v, want 8443 (the value, not the pointer)", m["port"])
	}
	if m["unset"] != "<nil>" {
		t.Errorf("unset = %v, want <nil>", m["unset"])
	}
}

type failingWriter struct{}

var errWriteFailed = errors.New("disk full")

func (failingWriter) Write([]byte) (int, error) { return 0, errWriteFailed }

// Every writer error was discarded, so a truncated
// export looked successful.
func TestPrintRedacted_ReturnsWriterError(t *testing.T) {
	cfg := struct {
		Host string `koanf:"host" desc:"server host"`
	}{Host: "h"}

	for _, format := range []string{FormatTable, FormatJSON, FormatEnv} {
		for _, desc := range []bool{false, true} {
			opts := []PrintOption{WithWriter(failingWriter{}), WithFormat(format)}
			if desc {
				opts = append(opts, WithIncludeDescriptions())
			}
			if err := PrintRedacted(cfg, opts...); !errors.Is(err, errWriteFailed) {
				t.Errorf("format %s, descriptions %v: err = %v, want the writer's error", format, desc, err)
			}
		}
	}
}

func TestPrintRedacted_UnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	err := PrintRedacted(struct{}{}, WithWriter(&buf), WithFormat("yaml"))
	if err == nil || !strings.Contains(err.Error(), `"yaml"`) {
		t.Errorf("err = %v, want an unknown-format error naming yaml", err)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q for an unknown format", buf.String())
	}
}

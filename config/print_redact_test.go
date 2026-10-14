//go:build unit

package config

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	m := RedactedMap(cfg)
	for _, key := range []string{"block.value", "ptr.value", "blocks[0].value", "by_name[a].value"} {
		if m[key] != redactedValue {
			t.Errorf("RedactedMap[%s] = %v, want %s", key, m[key], redactedValue)
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
}

type redactLoop struct {
	Name     string      `koanf:"name"`
	Password string      `koanf:"password"`
	Next     *redactLoop `koanf:"next"`
}

func TestRedaction_PointerCycleTerminates(t *testing.T) {
	a := &redactLoop{Name: "a", Password: "SECRET_CYCLE"}
	a.Next = a

	m := RedactedMap(a)
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

	m := RedactedMap(cfg)
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

// A credential under a sensitive key
// of a dynamic map was printed, because only field names were judged and a
// map was printed whole.
func TestRedaction_DynamicMapKeys(t *testing.T) {
	cfg := struct {
		Settings map[string]any `koanf:"settings"`
	}{Settings: map[string]any{
		"password": "SECRET_TOP",
		"region":   "us",
		"nested":   map[string]any{"api_key": "SECRET_NESTED", "zone": "a"},
		"list":     []any{map[string]any{"token": "SECRET_LIST"}},
	}}

	m := RedactedMap(cfg)
	want := map[string]string{
		"settings[password]":        redactedValue,
		"settings[region]":          "us",
		"settings[nested][api_key]": redactedValue,
		"settings[nested][zone]":    "a",
		"settings[list][0][token]":  redactedValue,
	}
	for key, value := range want {
		if m[key] != value {
			t.Errorf("RedactedMap[%s] = %v, want %v", key, m[key], value)
		}
	}
	if strings.Contains(fmt.Sprint(m), "SECRET_") {
		t.Errorf("a secret survived redaction: %v", m)
	}
}

// keyPair decodes from text, so the printers show it whole; a field of it
// named like a credential keeps it out of every output.
type keyPair struct {
	Public  string
	Private string
}

func (k *keyPair) UnmarshalText(b []byte) error {
	k.Public, k.Private, _ = strings.Cut(string(b), ":")
	return nil
}

func TestRedaction_SelfDecodedStructHoldingACredential(t *testing.T) {
	type cfg struct {
		Signing keyPair   `koanf:"signing"`
		Rotated []keyPair `koanf:"rotated"`
		Empty   []keyPair `koanf:"empty"`
		Started time.Time `koanf:"started"`
		DSN     string    `koanf:"dsn"`
	}
	c := &cfg{
		Signing: keyPair{Public: "pub", Private: "SECRET_PRIVATE"},
		Rotated: []keyPair{{Public: "old", Private: "SECRET_ROTATED"}},
		DSN:     "postgres://u:SECRET_DSN@db/x",
	}
	m := RedactedMap(c)
	assert.Equal(t, redactedValue, m["signing"])
	assert.Equal(t, redactedValue, m["rotated"])
	assert.Equal(t, "[]", m["empty"])
	assert.Equal(t, redactedValue, m["dsn"])
	assert.NotEqual(t, redactedValue, m["started"], "a struct with no exported field that could hold a credential prints")

	var buf bytes.Buffer
	require.NoError(t, PrintRedacted(c, WithWriter(&buf), WithFormat(FormatJSON)))
	assert.NotContains(t, buf.String(), "SECRET_")
}

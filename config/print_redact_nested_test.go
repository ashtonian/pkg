//go:build unit

package config

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestRedaction_NestedCompositeSecrets pins the fix for secrets nested inside a
// slice-of-structs or map-of-structs must be redacted, not leaked by
// stringifying the whole composite. Both the explicit `sensitive:"true"` tag
// and the sensitive-name heuristic are exercised, through PrintRedacted and ToMap.
func TestRedaction_NestedCompositeSecrets(t *testing.T) {
	type Upstream struct {
		URL      string `koanf:"url"`
		APIKey   string `koanf:"api_key" sensitive:"true"` // tag-sensitive
		Password string `koanf:"password"`                 // name-sensitive
	}
	type Cfg struct {
		Upstreams []Upstream          `koanf:"upstreams"`
		ByName    map[string]Upstream `koanf:"by_name"`
	}
	cfg := Cfg{
		Upstreams: []Upstream{{URL: "https://a", APIKey: "SECRET_A", Password: "PW_A"}},
		ByName:    map[string]Upstream{"x": {URL: "https://x", APIKey: "SECRET_X", Password: "PW_X"}},
	}
	secrets := []string{"SECRET_A", "PW_A", "SECRET_X", "PW_X"}

	// PrintRedacted (operator-facing dump).
	var buf bytes.Buffer
	PrintRedacted(cfg, WithWriter(&buf))
	out := buf.String()
	for _, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("PrintRedacted leaked secret %q in nested composite:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "https://a") {
		t.Errorf("PrintRedacted dropped a non-sensitive url (recursion should show it):\n%s", out)
	}

	// ToMap (diagnostics map).
	var flat strings.Builder
	for k, v := range ToMap(cfg, true) {
		fmt.Fprintf(&flat, "%s=%v ", k, v)
	}
	for _, s := range secrets {
		if strings.Contains(flat.String(), s) {
			t.Errorf("ToMap leaked secret %q in nested composite: %s", s, flat.String())
		}
	}
}

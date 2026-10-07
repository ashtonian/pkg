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
	if err := PrintRedacted(cfg, WithWriter(&buf)); err != nil {
		t.Fatalf("PrintRedacted: %v", err)
	}
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

// A sensitive name covers every container beneath it — a map field, a map
// key, slices of slices — while an ordinary struct field named like a secret
// (Auth) keeps its non-secret fields printable.
func TestRedaction_SensitivityCrossesContainers(t *testing.T) {
	type S struct {
		Value string `sensitive:"true"`
	}
	type Plain struct{ Value string }
	type Auth struct {
		Issuer string
		Secret string
	}
	cases := map[string]any{
		"sensitive map name":    struct{ Passwords map[string]string }{map[string]string{"primary": "FAKE_SECRET"}},
		"nested slices":         struct{ Rows [][]S }{[][]S{{{Value: "FAKE_SECRET"}}}},
		"dynamic sensitive key": struct{ Settings map[string]any }{map[string]any{"password": map[string]any{"value": "FAKE_SECRET"}}},
		"struct under a key":    struct{ Settings map[string]Plain }{map[string]Plain{"token": {Value: "FAKE_SECRET"}}},
		"slice under a key":     struct{ Settings map[string]any }{map[string]any{"credentials": []any{map[string]any{"v": "FAKE_SECRET"}}}},
		"slice of slices":       struct{ Grid [][]map[string]string }{[][]map[string]string{{{"api_key": "FAKE_SECRET"}}}},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			if out := fmt.Sprint(ToMap(v, true)); strings.Contains(out, "FAKE_SECRET") {
				t.Fatalf("redacted output leaked the secret: %s", out)
			}
		})
	}

	m := ToMap(struct{ Auth Auth }{Auth{Issuer: "https://issuer", Secret: "FAKE_SECRET"}}, true)
	if m["auth.issuer"] != "https://issuer" || m["auth.secret"] != redactedValue {
		t.Fatalf("an Auth struct's fields must be judged by their own names: %v", m)
	}
}

// A map or slice that contains itself prints a cycle marker instead of
// recursing until the stack overflows.
func TestRedaction_DynamicCycles(t *testing.T) {
	m := map[string]any{"name": "n"}
	m["self"] = m
	s := make([]any, 2)
	s[0] = map[string]any{"k": "v"}
	s[1] = s
	out := ToMap(struct {
		Settings map[string]any
		List     []any
	}{m, s}, true)
	if out["settings[self]"] != cycleValue || out["list[1]"] != cycleValue {
		t.Fatalf("cycles = %v, want %q at settings[self] and list[1]", out, cycleValue)
	}
	if out["settings[name]"] != "n" || out["list[0][k]"] != "v" {
		t.Fatalf("the acyclic parts must still print: %v", out)
	}
}

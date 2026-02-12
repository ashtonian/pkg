//go:build unit

package config

import (
	"fmt"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	kenv "github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

func TestDebug_KoanfSliceFromEnv(t *testing.T) {
	t.Setenv("APP_TAGS", "a,b,c")

	k := koanf.New(".")
	err := k.Load(kenv.Provider("APP_", ".", func(s string) string {
		return "tags"
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	
	raw := k.Get("tags")
	t.Logf("raw type=%T value=%v", raw, raw)

	type cfg struct {
		Tags []string `koanf:"tags"`
	}
	var c cfg
	err = k.UnmarshalWithConf("", &c, koanf.UnmarshalConf{Tag: "koanf"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Tags = %v len=%d", c.Tags, len(c.Tags))
}

func TestDebug_KoanfSliceFromConfmap(t *testing.T) {
	k := koanf.New(".")
	// Store as string (like DefaultsFromTags does)
	err := k.Load(confmap.Provider(map[string]any{
		"tags": "a,b,c",
	}, "."), nil)
	if err != nil {
		t.Fatal(err)
	}

	raw := k.Get("tags")
	t.Logf("string: raw type=%T value=%v", raw, raw)

	type cfg struct {
		Tags []string `koanf:"tags"`
	}
	var c cfg
	err = k.UnmarshalWithConf("", &c, koanf.UnmarshalConf{Tag: "koanf"})
	t.Logf("string: Tags = %v len=%d err=%v", c.Tags, len(c.Tags), err)

	// Now store as actual slice
	k2 := koanf.New(".")
	err = k2.Load(confmap.Provider(map[string]any{
		"tags": []string{"a", "b", "c"},
	}, "."), nil)
	if err != nil {
		t.Fatal(err)
	}
	var c2 cfg
	err = k2.UnmarshalWithConf("", &c2, koanf.UnmarshalConf{Tag: "koanf"})
	t.Logf("slice: Tags = %v len=%d err=%v", c2.Tags, len(c2.Tags), err)
	
	// Check what koanf stores internally
	fmt.Printf("All keys: %v\n", k.Keys())
	fmt.Printf("All: %v\n", k.All())
}

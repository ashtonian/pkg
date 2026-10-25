//go:build unit

package parse_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// bsonDevice is tagged with bson only — the schema generator reads bson names
// via WithFieldNameTag; the direct-apply path must use the same tag.
type bsonDevice struct {
	ID     string `json:"id,omitempty" db:"id"`
	Name   string `bson:"name"   parse:"filter,patch" patch_ops:"replace"`
	Status string `bson:"status" parse:"filter,patch" patch_ops:"replace"`
}

// TestFieldNameTagThreadsToApply: with a non-json FieldNameTag, applying a
// validated patch previously no-op'd silently — fieldIndexMap hardcoded the
// json tag, found no entry, and continued.
func TestFieldNameTagThreadsToApply(t *testing.T) {
	tp, err := parse.NewResource[bsonDevice]("bson_devices", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"renamed"}]`),
		bsonDevice{Name: "orig", Status: "active"},
	)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if out.Name != "renamed" {
		t.Fatalf("Name = %q; want renamed (patch silently no-op'd)", out.Name)
	}
	if out.Status != "active" {
		t.Errorf("Status = %q; want active untouched", out.Status)
	}
}

// TestSchemaValidatedPathMissMustError: a path the schema validated but the
// struct map can't resolve is an internal invariant violation, not a skip.
type mixedTagDevice struct {
	ID   string `json:"id,omitempty" db:"id"`
	Name string `bson:"name" json:"-" parse:"filter,patch" patch_ops:"replace"`
}

func TestSchemaValidatedPathMissMustError(t *testing.T) {
	// Schema reads bson ("name" patchable); the json tag hides the field from
	// a json-keyed map. With bson threading the apply works; this pins that
	// the two layers agree on the SAME tag rather than silently dropping ops.
	tp, err := parse.NewResource[mixedTagDevice]("mixed_tag_devices", parse.ParserOptions(core.WithGeneratorOptions(core.WithFieldNameTag("bson"))))
	if err != nil {
		t.Fatal(err)
	}
	out, err := tp.ApplyPatch(
		[]byte(`[{"op":"replace","path":"/name","value":"x"}]`),
		mixedTagDevice{Name: "orig"},
	)
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if out.Name != "x" {
		t.Fatalf("Name = %q; want x", out.Name)
	}
}

// uintDevice exercises P-5's numeric coercion guards on the direct-apply path.
type uintDevice struct {
	ID     string  `json:"id,omitempty" db:"id"`
	Count  uint    `json:"count"  parse:"filter,patch" patch_ops:"replace"`
	Level  int     `json:"level"  parse:"filter,patch" patch_ops:"replace"`
	Weight float64 `json:"weight" parse:"filter,patch" patch_ops:"replace"`
}

// TestNumericCoercionGuards: -1 wrapped into uint 18446744073709551615 and
// 3.7 silently truncated into int 3. Both must error; integral floats and
// real floats still work.
func TestNumericCoercionGuards(t *testing.T) {
	tp, err := parse.NewResource[uintDevice]("uint_devices")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("negative into uint errors", func(t *testing.T) {
		_, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/count","value":-1}]`), uintDevice{})
		if err == nil {
			t.Fatal("-1 accepted into uint; want error (wrapped to 2^64-1 before)")
		}
		if !strings.Contains(err.Error(), "E2077") && !strings.Contains(strings.ToLower(err.Error()), "negative") {
			t.Logf("error text: %v", err)
		}
	})

	t.Run("fractional into int errors", func(t *testing.T) {
		_, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/level","value":3.7}]`), uintDevice{})
		if err == nil {
			t.Fatal("3.7 accepted into int; want error (silent truncation before)")
		}
	})

	t.Run("fractional into uint errors", func(t *testing.T) {
		_, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/count","value":2.5}]`), uintDevice{})
		if err == nil {
			t.Fatal("2.5 accepted into uint; want error")
		}
	})

	t.Run("integral float into int ok", func(t *testing.T) {
		out, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/level","value":3}]`), uintDevice{})
		if err != nil {
			t.Fatalf("3 rejected: %v", err)
		}
		if out.Level != 3 {
			t.Errorf("Level = %d; want 3", out.Level)
		}
	})

	t.Run("integral float into uint ok", func(t *testing.T) {
		out, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/count","value":7}]`), uintDevice{})
		if err != nil {
			t.Fatalf("7 rejected: %v", err)
		}
		if out.Count != 7 {
			t.Errorf("Count = %d; want 7", out.Count)
		}
	})

	t.Run("float field unaffected", func(t *testing.T) {
		out, err := tp.ApplyPatch([]byte(`[{"op":"replace","path":"/weight","value":2.5}]`), uintDevice{})
		if err != nil {
			t.Fatalf("2.5 into float rejected: %v", err)
		}
		if out.Weight != 2.5 {
			t.Errorf("Weight = %v; want 2.5", out.Weight)
		}
	})
}

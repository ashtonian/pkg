package config

import (
	"bytes"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

var inspectionDecodeCalls atomic.Int32

type inspectionText string

func (v *inspectionText) UnmarshalText(input []byte) error {
	inspectionDecodeCalls.Add(1)
	*v = inspectionText(input)
	return nil
}

func TestInspectionDoesNotDecodeTags(t *testing.T) {
	type cfg struct {
		Value inspectionText `koanf:"value" example:"example"`
	}
	before := inspectionDecodeCalls.Load()
	for _, format := range []string{FormatTable, FormatJSON, FormatYAML} {
		var out bytes.Buffer
		if err := PrintRedacted(cfg{Value: "live"}, WithWriter(&out), WithFormat(format)); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out.Bytes(), []byte("live")) {
			t.Fatal(out.String())
		}
	}
	if _, err := RedactedMapChecked(cfg{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RedactedTreeChecked(cfg{}); err != nil {
		t.Fatal(err)
	}
	if _, err := FieldsChecked[cfg](); err != nil {
		t.Fatal(err)
	}
	if got := inspectionDecodeCalls.Load() - before; got != 0 {
		t.Fatalf("inspection decoded tags %d times", got)
	}
	schemaCache.RLock()
	_, cached := schemaCache.entries[reflect.TypeFor[cfg]()]
	schemaCache.RUnlock()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			_, err := rootSchema(reflect.TypeFor[cfg]())
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	want := int32(12)
	if cached {
		want = 1
	}
	if got := inspectionDecodeCalls.Load() - before; got != want {
		t.Fatalf("load-time validation decoded the example %d times; want %d", got, want)
	}
}

type conflictingInspection struct {
	Port  int `koanf:"Port"`
	Other int `koanf:"PORT"`
}

func TestInspectionReportsStructuralErrorsWithoutPartialOutput(t *testing.T) {
	for _, cfg := range []any{
		conflictingInspection{80, 90},
		struct {
			Good    int
			Dynamic any
		}{Good: 42, Dynamic: conflictingInspection{80, 90}},
	} {
		for _, format := range []string{FormatTable, FormatJSON, FormatYAML} {
			var out bytes.Buffer
			err := PrintRedacted(cfg, WithWriter(&out), WithFormat(format))
			if !errors.Is(err, ErrSchema) || out.Len() != 0 {
				t.Fatalf("format=%s err=%v output=%q", format, err, out.String())
			}
		}
		if got, err := RedactedMapChecked(cfg); !errors.Is(err, ErrSchema) || got != nil {
			t.Fatalf("map=%v err=%v", got, err)
		}
		if got, err := RedactedTreeChecked(cfg); !errors.Is(err, ErrSchema) || got != nil {
			t.Fatalf("tree=%v err=%v", got, err)
		}
		if RedactedMap(cfg) != nil || RedactedTree(cfg) != nil {
			t.Fatal("legacy diagnostics returned partial values")
		}
	}
	if got, err := FieldsChecked[conflictingInspection](); !errors.Is(err, ErrSchema) || got != nil {
		t.Fatalf("fields=%v err=%v", got, err)
	}
	if got := Fields[conflictingInspection](); got != nil {
		t.Fatal(got)
	}
	if _, err := Load[conflictingInspection](t.Context()); !errors.Is(err, ErrSchema) {
		t.Fatal(err)
	}
	if _, err := FieldsChecked[int](); !errors.Is(err, ErrSchema) {
		t.Fatal(err)
	}
}

func TestInspectionKeepsConservativeRedactionWithoutTagValidation(t *testing.T) {
	type cfg struct {
		Value string `koanf:"value" sensitive:"yes" example:"a"`
	}
	for _, format := range []string{FormatTable, FormatJSON, FormatYAML} {
		var out bytes.Buffer
		if err := PrintRedacted(cfg{"never-print-me"}, WithWriter(&out), WithFormat(format)); err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(out.Bytes(), []byte("never-print-me")) || !bytes.Contains(out.Bytes(), []byte(redactedValue)) {
			t.Fatal(out.String())
		}
	}
	if _, err := Load[cfg](t.Context()); !errors.Is(err, ErrSchema) {
		t.Fatal(err)
	}
}

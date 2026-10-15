//go:build unit

package httpx

import (
	"maps"
	"slices"
	"testing"
)

type namedCodec string

func (c namedCodec) ContentType() string        { return string(c) }
func (namedCodec) Encode(any) ([]byte, error)   { return nil, nil }
func (namedCodec) Decode(_ []byte, _ any) error { return nil }

// isolateCodecs restores the codec registry when the test ends.
func isolateCodecs(t *testing.T) {
	t.Helper()
	codecs.mu.Lock()
	saved := maps.Clone(codecs.m)
	codecs.mu.Unlock()
	t.Cleanup(func() {
		codecs.mu.Lock()
		codecs.m = saved
		codecs.mu.Unlock()
	})
}

func TestRegisteredCodecsAreOrderedByContentType(t *testing.T) {
	isolateCodecs(t)
	RegisterCodec(namedCodec("application/zz-test"))
	RegisterCodec(namedCodec("application/aa-test"))

	var types []string
	for _, c := range RegisteredCodecs() {
		types = append(types, c.ContentType())
	}
	if !slices.IsSorted(types) || !slices.Contains(types, "application/aa-test") {
		t.Fatalf("content types = %v, want sorted and complete", types)
	}
	first := registeredAccept()
	for range 20 {
		if got := registeredAccept(); got != first {
			t.Fatalf("Accept changed between requests: %q vs %q", first, got)
		}
	}
	if !slices.IsSorted(RegisteredEncodings()) {
		t.Fatalf("encodings = %v, want sorted", RegisteredEncodings())
	}
}

func TestRegisterRejectsNil(t *testing.T) {
	for name, register := range map[string]func(){
		"codec":      func() { RegisterCodec(nil) },
		"compressor": func() { RegisterCompressor("x-test", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("a nil registration must panic at init time")
				}
			}()
			register()
		})
	}
}

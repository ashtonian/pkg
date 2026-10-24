//go:build unit

package cursor_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/internal/cursor"
)

// samplePayload exercises exact integer cursor values.
type samplePayload struct {
	Field string `msgpack:"f"`
	Value int64  `msgpack:"v"`
}

// The envelope retains exact integers and checks generation, expiry and tenant.
func TestEnvelopeRoundTrip(t *testing.T) {
	// int64 above 2^53 survives (msgpack payload, not JSON float64) — the
	// round-trip bug the JSON cursor had.
	want := samplePayload{Field: "created_at", Value: 9_007_199_254_740_993}

	fixed := time.Unix(1_700_000_000, 0).UTC()
	clock := func() time.Time { return fixed }

	cur, err := cursor.Encode(want,
		cursor.WithGeneration(3),
		cursor.WithTTL(time.Hour),
		cursor.WithTenantHash(0xABCD),
		cursor.WithClock(clock),
	)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if cur == "" {
		t.Fatal("Encode returned an empty cursor")
	}

	got, err := cursor.Decode[samplePayload](cur,
		cursor.ExpectGeneration(3),
		cursor.ExpectTenantHash(0xABCD),
		cursor.WithDecodeClock(clock),
	)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestEnvelopeGenerationMismatch(t *testing.T) {
	cur, err := cursor.Encode(samplePayload{Field: "x"}, cursor.WithGeneration(1))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := cursor.Decode[samplePayload](cur, cursor.ExpectGeneration(2)); !errors.Is(err, cursor.ErrCursorGeneration) {
		t.Fatalf("gen mismatch: err = %v, want ErrCursorGeneration", err)
	}
}

func TestEnvelopeExpiry(t *testing.T) {
	issued := time.Unix(1_700_000_000, 0).UTC()
	cur, err := cursor.Encode(samplePayload{Field: "x"},
		cursor.WithTTL(time.Minute),
		cursor.WithClock(func() time.Time { return issued }),
	)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	// Decode two minutes later — expired.
	later := issued.Add(2 * time.Minute)
	if _, err := cursor.Decode[samplePayload](cur, cursor.WithDecodeClock(func() time.Time { return later })); !errors.Is(err, cursor.ErrCursorExpired) {
		t.Fatalf("expiry: err = %v, want ErrCursorExpired", err)
	}
}

func TestEnvelopeTenantMismatch(t *testing.T) {
	cur, err := cursor.Encode(samplePayload{Field: "x"}, cursor.WithTenantHash(111))
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := cursor.Decode[samplePayload](cur, cursor.ExpectTenantHash(222)); !errors.Is(err, cursor.ErrCursorTenantMismatch) {
		t.Fatalf("tenant mismatch: err = %v, want ErrCursorTenantMismatch", err)
	}
}

func TestEnvelopeMalformed(t *testing.T) {
	if _, err := cursor.Decode[samplePayload]("!!!not-base64!!!"); !errors.Is(err, cursor.ErrInvalidCursor) {
		t.Fatalf("malformed: err = %v, want ErrInvalidCursor", err)
	}
}

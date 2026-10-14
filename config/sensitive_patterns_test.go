package config

import (
	"reflect"
	"testing"
)

// TestSensitivePatternsIsArray guards the immutability invariant for
// the package-level `sensitivePatterns` variable. It must remain a
// fixed-size array (`[N]string{...}`) — converting to a slice would
// allow runtime mutation via reslice or append, which would let a
// downstream package change the auto-redaction set out from under
// other call sites.
func TestSensitivePatternsIsArray(t *testing.T) {
	k := reflect.TypeOf(sensitivePatterns).Kind()
	if k != reflect.Array {
		t.Errorf("sensitivePatterns must be a fixed-size array (Kind=Array); got Kind=%v. "+
			"Converting to slice would allow runtime mutation of the redaction set.",
			k)
	}
}

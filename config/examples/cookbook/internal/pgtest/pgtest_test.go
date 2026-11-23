package pgtest

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stopped ends a recordingTB's test the way FailNow and SkipNow end a real one.
type stopped struct{}

// recordingTB records how the fixture ended a test instead of ending it.
type recordingTB struct {
	testing.TB
	fatal, skip string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(stopped{})
}

func (r *recordingTB) Skipf(format string, args ...any) {
	r.skip = fmt.Sprintf(format, args...)
	panic(stopped{})
}

// Without a DSN the fixture skips its test, unless PostgreSQL is required:
// then it fails the test and names the variable that is missing.
func TestMissingDSN(t *testing.T) {
	tests := []struct {
		name     string
		require  string
		wantFail bool
	}{
		{"skips by default", "", false},
		{"skips unless the requirement is 1", "true", false},
		{"fails when required", "1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(DSNEnv, "")
			t.Setenv(RequireEnv, tt.require)
			r := &recordingTB{TB: t}
			func() {
				defer func() {
					if p := recover(); p != nil {
						if _, ok := p.(stopped); !ok {
							panic(p)
						}
					}
				}()
				DSN(r)
			}()
			if tt.wantFail {
				assert.Contains(t, r.fatal, DSNEnv)
				assert.Empty(t, r.skip)
			} else {
				assert.Contains(t, r.skip, DSNEnv)
				assert.Empty(t, r.fatal)
			}
		})
	}
}

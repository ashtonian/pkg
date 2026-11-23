//go:build integration

package dbtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// stopped ends a recordingTB's test the way FailNow and SkipNow end a real one.
type stopped struct{}

// recordingTB records how a fixture ended a test instead of ending it.
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

// Without a DSN a fixture skips its test, unless PostgreSQL is required: then
// it fails the test and names the variable that is missing.
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
			t.Setenv(PostgresDSNEnv, "")
			t.Setenv(RequirePostgresEnv, tt.require)
			r := record(t, func(r *recordingTB) { Postgres(r) })
			if tt.wantFail {
				assert.Contains(t, r.fatal, PostgresDSNEnv)
				assert.Empty(t, r.skip)
			} else {
				assert.Contains(t, r.skip, PostgresDSNEnv)
				assert.Empty(t, r.fatal)
			}
		})
	}
}

// A test's deadline is TimeoutEnv's duration, 30 seconds when it is unset;
// a value that is no positive duration fails the test and names the
// variable.
func TestTimeout(t *testing.T) {
	tests := []struct {
		value    string
		want     time.Duration
		wantFail bool
	}{
		{"", 30 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"90", 0, true},
		{"0s", 0, true},
		{"-1s", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(TimeoutEnv, tt.value)
			var got time.Duration
			r := record(t, func(r *recordingTB) { got = Timeout(r) })
			if tt.wantFail {
				assert.Contains(t, r.fatal, TimeoutEnv)
				return
			}
			assert.Empty(t, r.fatal)
			assert.Equal(t, tt.want, got)
		})
	}
}

// record runs f on a recordingTB over t and returns it, f stopped where a
// real test would have ended.
func record(t *testing.T, f func(*recordingTB)) (r *recordingTB) {
	r = &recordingTB{TB: t}
	defer func() {
		p := recover()
		if p != nil {
			_, ok := p.(stopped)
			if !ok {
				panic(p)
			}
		}
	}()
	f(r)
	return r
}

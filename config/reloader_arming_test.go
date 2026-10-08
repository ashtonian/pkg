//go:build unit

package config

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Stop releases triggers Arm set up for a Start that never ran; after Stop,
// Arm refuses and Start returns at once.
func TestArm_StopReleasesUnservedTriggers(t *testing.T) {
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithFileWatch[portCfg](0)},
		WithConfigFile(writeYAML(t, "port: 9000\n")), WithSkipValidation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	armReloader(t, r)
	r.armMu.Lock()
	watch := r.armed.watch
	r.armMu.Unlock()

	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case _, open := <-watch.watcher.Events:
		if open {
			t.Fatal("the armed file watch delivered an event after Stop")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop left the armed file watch open")
	}
	if err := r.Arm(t.Context()); !errors.Is(err, ErrReloaderStopped) {
		t.Fatalf("Arm after Stop = %v, want ErrReloaderStopped", err)
	}
	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start after Stop = %v, want nil", err)
	}
}

// Start serves the triggers Arm set up, and Arm while they are served, or a
// second Arm before Start, sets up nothing more.
func TestArm_StartServesWhatArmSetUp(t *testing.T) {
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithFileWatch[portCfg](0)},
		WithConfigFile(writeYAML(t, "port: 9000\n")), WithSkipValidation(),
	)
	if err != nil {
		t.Fatal(err)
	}
	armed := func() (*triggers, int) {
		r.armMu.Lock()
		defer r.armMu.Unlock()
		return r.armed, r.serving
	}

	armReloader(t, r)
	first, _ := armed()
	armReloader(t, r)
	if again, _ := armed(); again != first {
		t.Fatal("a second Arm set up a second set of triggers")
	}

	startReloader(t, r)
	eventually(t, func() bool { tr, serving := armed(); return tr == nil && serving == 1 }, "Start to take the armed triggers")
	armReloader(t, r)
	if tr, serving := armed(); tr != nil || serving != 1 {
		t.Fatalf("Arm while Start serves set up triggers no Start serves (armed %v, serving %d)", tr != nil, serving)
	}
}

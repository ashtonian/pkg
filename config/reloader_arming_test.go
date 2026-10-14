//go:build unit

package config

import (
	"context"
	"errors"
	"sync/atomic"
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
	r.armSem <- struct{}{}
	watch := r.armed.watch
	<-r.armSem

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
	armed := func() (*triggers, int32) {
		r.armSem <- struct{}{}
		defer func() { <-r.armSem }()
		return r.armed, r.serving.Load()
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

// An Arm queued behind another — itself waiting out a manual reload — gives
// up when its own context ends, instead of waiting past it and then
// reporting success.
func TestArm_QueuedArmHonoursItsContext(t *testing.T) {
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithFileWatch[portCfg](time.Hour)},
		WithConfigFile(writeYAML(t, "port: 9000\n")), WithSkipValidation(),
		WithLogger(discardLogger()),
		WithPostLoad(func(context.Context, *portCfg) error {
			if hold.Load() {
				close(entered)
				<-release
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	hold.Store(true)
	manual := make(chan error, 1)
	go func() { _, err := r.Reload(context.Background()); manual <- err }()
	<-entered

	first := make(chan error, 1)
	go func() { first <- r.Arm(context.Background()) }()
	eventually(t, func() bool { return len(r.armSem) == 1 }, "the first Arm to hold the arming lock")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.Arm(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued Arm = %v, want its deadline", err)
	}

	close(release)
	if err := <-manual; err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatalf("first Arm: %v", err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Stop does not wait on the arming lock past its context: an Arm holding it
// while it waits out a manual reload holds Stop up only within ctx.
func TestStop_BoundedWhileArmWaits(t *testing.T) {
	var hold atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	r, err := NewReloader[portCfg](t.Context(),
		[]ReloadOption[portCfg]{WithFileWatch[portCfg](time.Hour)},
		WithConfigFile(writeYAML(t, "port: 9000\n")), WithSkipValidation(),
		WithLogger(discardLogger()),
		WithPostLoad(func(context.Context, *portCfg) error {
			if hold.Load() {
				close(entered)
				<-release
			}
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	hold.Store(true)
	manual := make(chan error, 1)
	go func() { _, err := r.Reload(context.Background()); manual <- err }()
	<-entered
	arm := make(chan error, 1)
	go func() { arm <- r.Arm(context.Background()) }()
	eventually(t, func() bool { return len(r.armSem) == 1 }, "Arm to hold the arming lock")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop with a reload in flight = %v, want its deadline", err)
	}
	if err := <-arm; !errors.Is(err, ErrReloaderStopped) {
		t.Fatalf("Arm waiting when Stop was called = %v, want ErrReloaderStopped", err)
	}
	close(release)
	if err := <-manual; err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if err := r.Arm(context.Background()); !errors.Is(err, ErrReloaderStopped) {
		t.Fatalf("Arm after Stop = %v", err)
	}
}

//go:build unit

package httpx

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestRateLimitIsPerOrigin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		rl := must(NewRateLimitTransport(p, 1, 1))

		if err := send(t, rl, originA+"/x"); err != nil {
			t.Fatal(err)
		}
		if err := send(t, rl, originA+"/x"); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("a's bucket is empty, got %v", err)
		}
		if p.count("a.example.com") != 1 {
			t.Errorf("a limited request must not reach the origin, sent %d", p.count("a.example.com"))
		}
		if err := send(t, rl, originB+"/x"); err != nil {
			t.Fatalf("b has its own bucket, got %v", err)
		}

		time.Sleep(time.Second)
		if err := send(t, rl, originA+"/x"); err != nil {
			t.Fatalf("a's bucket refills at 1 rps, got %v", err)
		}
	})
}

func TestRateLimitWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newPathOrigin()
		rl := must(NewRateLimitTransport(p, 1, 1, WithWait()))
		send(t, rl, originA+"/x")

		start := time.Now()
		if err := send(t, rl, originA+"/x"); err != nil {
			t.Fatal(err)
		}
		if waited := time.Since(start); waited != time.Second {
			t.Fatalf("waited %s, want the 1s refill", waited)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		start = time.Now()
		if err := <-sendAsync(ctx, rl, originA+"/x"); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("a wait longer than the deadline fails as rate limited, got %v", err)
		}
		if time.Since(start) != 0 {
			t.Fatal("a wait that cannot fit the deadline must fail at once")
		}

		ctx, cancelNow := context.WithCancel(context.Background())
		res := sendAsync(ctx, rl, originA+"/x")
		synctest.Wait()
		cancelNow()
		if err := <-res; !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled wait returns the cancellation, got %v", err)
		}
	})
}

func TestRateLimitBoundsBuckets(t *testing.T) {
	p := newPathOrigin()
	rl := must(NewRateLimitTransport(p, 1, 1, WithMaxBuckets(1)))
	send(t, rl, originA+"/x")
	send(t, rl, originB+"/x") // a's bucket is dropped
	if n := rl.buckets.len(); n != 1 {
		t.Fatalf("buckets = %d, want 1", n)
	}
	if err := send(t, rl, originA+"/x"); err != nil {
		t.Fatalf("a dropped origin starts with a full bucket, got %v", err)
	}
}

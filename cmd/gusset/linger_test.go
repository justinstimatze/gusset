package main

import (
	"context"
	"testing"
	"time"
)

func TestServedSignal_FireSetsServedAndWakes(t *testing.T) {
	s := newServedSignal()
	if s.served() {
		t.Fatal("served() true before any fire")
	}
	s.fire()
	if !s.served() {
		t.Fatal("served() false after fire")
	}
	select {
	case <-s.ch:
	default:
		t.Fatal("fire did not deliver a wakeup")
	}
}

func TestServedSignal_FireCoalesces(t *testing.T) {
	s := newServedSignal()
	s.fire()
	s.fire()
	s.fire()
	if got := s.n.Load(); got != 3 {
		t.Fatalf("count = %d, want 3", got)
	}
	// The buffered channel coalesces: one wakeup is enough to unblock a waiter.
	<-s.ch
	select {
	case <-s.ch:
		t.Fatal("expected a single coalesced wakeup, got a second")
	default:
	}
}

// waitForPullbackReturns runs waitForPullback in a goroutine and reports whether
// it returned within d.
func waitForPullbackReturns(ctx context.Context, s *servedSignal, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		waitForPullback(ctx, time.Hour, s)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

func TestWaitForPullback_ExitsWhenAlreadyServed(t *testing.T) {
	s := newServedSignal()
	s.fire() // a peer pulled before we reached the linger
	if !waitForPullbackReturns(context.Background(), s, time.Second) {
		t.Fatal("waitForPullback did not exit when already served")
	}
}

func TestWaitForPullback_ExitsWhenPeerPullsDuringWait(t *testing.T) {
	s := newServedSignal()
	done := make(chan struct{})
	go func() {
		waitForPullback(context.Background(), time.Hour, s)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter park
	s.fire()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitForPullback did not exit when the peer pulled during the wait")
	}
}

func TestWaitForPullback_FallsBackToForWindow(t *testing.T) {
	s := newServedSignal()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the --for window elapsed (or Ctrl-C) with no peer pull
	if !waitForPullbackReturns(ctx, s, time.Second) {
		t.Fatal("waitForPullback did not exit when the context ended")
	}
}

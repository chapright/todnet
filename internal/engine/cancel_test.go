package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A connection attempt that is cancelled must not install a tunnel.
//
// The attempt used to carry on after the user pressed Cancel and bring the
// tunnel up a moment later, because the generation counter was not bumped
// and the attempt's own check therefore still passed.
func TestCancelInvalidatesAConnectionAttempt(t *testing.T) {
	e := NewEngine()

	e.mu.Lock()
	e.status = Status{State: StateConnecting}
	e.gen++
	gen := e.gen
	ctx, cancel := context.WithCancel(context.Background())
	e.connectCtx = ctx
	e.connectCancel = cancel
	e.mu.Unlock()

	if !e.Cancelling() {
		t.Fatal("the engine should report that it is connecting")
	}

	if err := e.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// The generation moved, so the in-flight attempt's own check trips.
	e.mu.Lock()
	after := e.gen
	state := e.status.State
	e.mu.Unlock()

	if after == gen {
		t.Fatal("Cancel did not invalidate the attempt")
	}
	if state != StateDisconnected {
		t.Fatalf("want disconnected after a cancel, got %q", state)
	}
	if e.Cancelling() {
		t.Fatal("the engine should not report that it is connecting")
	}

	// The attempt's context was cancelled too, so the network work in
	// flight stopped rather than running to completion.
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the attempt's context was not cancelled")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", ctx.Err())
	}
}

// A disconnect invalidates an attempt too: a user who closes the window
// mid-connect must not come back to a live tunnel.
func TestDisconnectInvalidatesAConnectionAttempt(t *testing.T) {
	e := NewEngine()

	e.mu.Lock()
	e.status = Status{State: StateConnecting}
	e.gen++
	gen := e.gen
	ctx, cancel := context.WithCancel(context.Background())
	e.connectCtx = ctx
	e.connectCancel = cancel
	e.mu.Unlock()

	if err := e.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	e.mu.Lock()
	after := e.gen
	e.mu.Unlock()

	if after == gen {
		t.Fatal("Disconnect did not invalidate the attempt")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("the attempt's context was not cancelled")
	}
}

// Cancelling an idle engine is a no-op rather than an error.
func TestCancelOnAnIdleEngineIsHarmless(t *testing.T) {
	e := NewEngine()
	if err := e.Cancel(); err != nil {
		t.Fatalf("Cancel on an idle engine: %v", err)
	}
	if got := e.Status().State; got != StateDisconnected {
		t.Fatalf("want disconnected, got %q", got)
	}
}

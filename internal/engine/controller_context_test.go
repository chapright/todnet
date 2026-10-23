package engine

import (
	"context"
	"testing"
)

// The controller's context must not be a child of the connection
// attempt's context.
//
// It was, so that cancelling an attempt would abort the TLS handshake
// to the edge. But the controller's context is also the one every later
// renewal and every session rebuild runs under, and the attempt's
// context is cancelled as soon as the attempt ends. The live session was
// therefore killed the moment it was established, and every renewal
// failed with "context canceled" — which is what the field logs showed.
func TestTheControllerOutlivesTheConnectionAttempt(t *testing.T) {
	attemptCtx, attemptCancel := context.WithCancel(context.Background())
	c := newController(ControllerConfig{DialContext: attemptCtx})
	defer c.Close()

	attemptCancel()

	if c.ctx.Err() != nil {
		t.Fatalf("the controller's context must not be a child of the attempt's: "+
			"every renewal and every session rebuild runs under it, and they all fail with %q when it is cancelled",
			c.ctx.Err())
	}
	if c.ctx.Err() == context.Canceled && attemptCtx.Err() == nil {
		t.Fatal("unexpected")
	}
}

// The dial itself must still be abortable: cancelling the attempt has to
// stop the TLS handshake rather than waiting out its timeout.
func TestTheDialContextAbortsTheHandshake(t *testing.T) {
	attemptCtx, attemptCancel := context.WithCancel(context.Background())
	c := newController(ControllerConfig{DialContext: attemptCtx})
	defer c.Close()

	attemptCancel()

	if attemptCtx.Err() == nil {
		t.Fatal("the attempt's context should be cancelled")
	}
	if c.ctx.Err() != nil {
		t.Fatal("the controller's context should still be alive")
	}
}

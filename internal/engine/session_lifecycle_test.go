package engine

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// countingSession is a Session that records how often it was closed and can be
// told to look dead to the liveness check.
type countingSession struct {
	closed  atomic.Int32
	alive   atomic.Bool
	tunnels atomic.Int32
}

func newCountingSession(alive bool) *countingSession {
	s := &countingSession{}
	s.alive.Store(alive)
	return s
}

func (s *countingSession) OpenTunnel(string) (net.Conn, error) {
	s.tunnels.Add(1)
	return nil, errors.New("not implemented")
}

func (s *countingSession) Close() error {
	s.closed.Add(1)
	s.alive.Store(false)
	return nil
}

func (s *countingSession) IsAlive() bool { return s.alive.Load() }

// A session that can no longer serve tunnels has to be closed right away. The
// old behaviour waited for the last tunnel reference to drain, which left the
// upstream connection — and every stream parked on it — waiting for the
// operating system's TCP timer. In the field that was a 25-second stall before
// the client saw an error.
func TestMarkUnusableClosesSessionEvenWithOpenTunnels(t *testing.T) {
	session := newCountingSession(true)
	c := &Controller{done: make(chan struct{})}
	ms := &managedSession{session: session, accepting: true, refs: 3}
	c.current = ms

	c.markUnusable(ms)

	if got := session.closed.Load(); got != 1 {
		t.Fatalf("session closed %d times, want 1 (open tunnel refs must not delay it)", got)
	}
	if ms.accepting {
		t.Error("session is still accepting new tunnels")
	}
	if ms.session != nil {
		t.Error("retired session is still attached to the managed session")
	}
	// Releasing the tunnels that were open must not close it a second time.
	c.release(ms)
	c.release(ms)
	c.release(ms)
	if got := session.closed.Load(); got != 1 {
		t.Fatalf("session closed %d times after its refs drained, want 1", got)
	}
}

// A session that has already been replaced is not ours to close.
func TestMarkUnusableIgnoresStaleSession(t *testing.T) {	stale := newCountingSession(true)
	current := newCountingSession(true)
	c := &Controller{done: make(chan struct{})}
	old := &managedSession{session: stale, accepting: true}
	c.current = &managedSession{session: current, accepting: true}

	c.markUnusable(old)

	if got := stale.closed.Load(); got != 0 {
		t.Fatalf("stale session closed %d times, want 0", got)
	}
	if !old.accepting {
		t.Error("stale session was retired even though it is not the current one")
	}
}

// A session that is merely out of stream capacity is not broken. It must stop
// taking new work so a second connection can be opened, but closing it would
// kill the healthy streams it is carrying — the opposite of the fix above.
func TestMarkSaturatedKeepsTheSessionOpen(t *testing.T) {
	session := newCountingSession(true)
	c := &Controller{done: make(chan struct{})}
	ms := &managedSession{session: session, accepting: true, refs: 2}
	c.current = ms

	c.markSaturated(ms)

	if got := session.closed.Load(); got != 0 {
		t.Fatalf("saturated session closed %d times, want 0 while its streams are live", got)
	}
	if ms.accepting {
		t.Error("saturated session is still accepting new tunnels")
	}
	if ms.session == nil {
		t.Fatal("saturated session was detached; it must stay attached so it can drain")
	}

	// Once the last stream lets go it is closed, without anyone else asking.
	c.release(ms)
	c.release(ms)
	if got := session.closed.Load(); got != 1 {
		t.Fatalf("session closed %d times after draining, want 1", got)
	}
}

func TestMarkUnusableToleratesNil(t *testing.T) {
	c := &Controller{done: make(chan struct{})}
	c.markUnusable(nil)
}

// The keepalive budget has to beat the operating system's TCP retransmit timer,
// otherwise a blackholed connection is noticed by the OS first and every stream
// on it hangs for the full timeout.
func TestKeepAliveBudgetIsTighterThanTheOS(t *testing.T) {
	// Windows gives up on a silent connection after roughly twenty seconds.
	const osTCPTimeout = 20 * time.Second

	worst := upstreamKeepAliveInterval +
		time.Duration(upstreamKeepAliveMisses)*upstreamKeepAlivePingTimeout
	if worst >= osTCPTimeout {
		t.Fatalf("worst-case detection %s does not beat the OS TCP timeout %s", worst, osTCPTimeout)
	}
}

// A dead session is rebuilt with the pass it already holds, so recovery never
// waits on a control-plane request. In TUN mode that request would have to
// travel through the tunnel being rebuilt, which is why the tunnel used to stay
// down until the user reconnected by hand.
func TestReusablePassOnlyWhenItHasTimeLeft(t *testing.T) {
	expiry := time.Now().Add(5 * time.Minute)
	c := &Controller{
		done: make(chan struct{}),
		current: &managedSession{
			session:   newCountingSession(false),
			rawToken:  "pass-token",
			expiresAt: expiry,
			accepting: true,
		},
	}

	token, got, ok := c.reusablePass()
	if !ok || token != "pass-token" || !got.Equal(expiry) {
		t.Fatalf("reusablePass = (%q, %v, %t), want the live pass", token, got, ok)
	}

	// Too close to expiry: the rebuilt session would be refused moments
	// later, so the full renewal has to run instead.
	c.current.expiresAt = time.Now().Add(proxyPassRebuildMargin / 2)
	if _, _, ok := c.reusablePass(); ok {
		t.Fatal("reused a pass that is about to expire")
	}

	// Nothing to reuse.
	c.current = nil
	if _, _, ok := c.reusablePass(); ok {
		t.Fatal("reused a pass with no session")
	}
}

// A rejected pass is the one failure a rebuild with the same pass cannot fix:
// the new session would be refused exactly like the old one.
func TestAuthFailureForcesAFreshPass(t *testing.T) {
	for _, status := range []int{401, 403, 407} {
		err := &proxyConnectHTTPError{StatusCode: status, Status: "refused"}
		if !isAuthFailure(err) {
			t.Errorf("HTTP %d was not treated as an auth failure", status)
		}
	}
	for _, status := range []int{502, 503, 500} {
		err := &proxyConnectHTTPError{StatusCode: status, Status: "refused"}
		if isAuthFailure(err) {
			t.Errorf("HTTP %d was treated as an auth failure", status)
		}
	}
	if isAuthFailure(ErrSessionUnhealthy) {
		t.Error("a dead session was treated as an auth failure")
	}
}

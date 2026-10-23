package engine

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

func TestIsPrivateDestination(t *testing.T) {
	private := []string{
		"10.10.34.35",  // the address from the reported log
		"192.168.1.1",  // the router
		"172.16.5.4",   // docker/private
		"169.254.1.1",  // link-local
		"100.64.0.1",   // carrier NAT
		"198.18.0.1",   // the tunnel adapter itself
		"0.0.0.0",
		"224.0.0.251",  // mDNS
	}
	for _, h := range private {
		if !isPrivateDestination(h) {
			t.Errorf("%s should be refused as a private destination", h)
		}
	}

	allowed := []string{
		"1.1.1.1",
		"43.156.86.196", // a Tencent address from the log
		"2606:4700:4700::1111",
		"example.com", // hostnames resolve on the edge
		"127.0.0.1",   // local tooling and test harnesses
	}
	for _, h := range allowed {
		if isPrivateDestination(h) {
			t.Errorf("%s should be carried by the tunnel", h)
		}
	}
}

// A session that dies must not leave its streams blocked. Before this, a
// stalled upstream connection left every open CONNECT parked until the client
// timed out, which is what surfaced as "network error" in the app.
func TestTunnelConnFailUnblocksRead(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	// Production shares one buffer between writer and request body; the test
	// must do the same or it exercises a shape that never exists.
	body := newTunnelWriteBuffer(1024)
	c := &tunnelConn{
		reader:  pr,
		writer:  body,
		reqBody: body,
		name:    tunnelAddr("test"),
	}

	readErr := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 16)
		_, err := c.Read(buf)
		readErr <- err
	}()

	// Give the read a moment to park, then fail the tunnel the way a dead
	// session does.
	time.Sleep(50 * time.Millisecond)
	c.fail(ErrSessionUnhealthy)

	select {
	case err := <-readErr:
		if !errors.Is(err, ErrSessionUnhealthy) {
			t.Fatalf("read returned %v, want ErrSessionUnhealthy", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read stayed blocked after the tunnel was failed")
	}

	// A subsequent write must fail too, not silently buffer into the void.
	if _, err := c.Write([]byte("payload")); err == nil {
		t.Fatal("write succeeded after the tunnel was failed")
	}
	wg.Wait()
}

// fail must be idempotent and keep the first reason, so the log names the
// cause that actually happened rather than a later teardown.
func TestTunnelConnFailKeepsFirstReason(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	c := &tunnelConn{
		reader:  pr,
		writer:  newTunnelWriteBuffer(64),
		reqBody: newTunnelWriteBuffer(64),
	}
	c.fail(ErrSessionUnhealthy)
	c.fail(ErrNoUsableSession)

	if got := c.failure(); !errors.Is(got, ErrSessionUnhealthy) {
		t.Fatalf("failure = %v, want the first reason (ErrSessionUnhealthy)", got)
	}
}

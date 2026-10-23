package engine

import (
	"context"
	"errors"
	"net"
	"testing"
)

// The bypass list decides what skips the tunnel, so the dialer has to
// honour it before it opens anything.

type recordingOpener struct {
	used bool
}

func (o *recordingOpener) OpenTunnel(string) (net.Conn, error) {
	o.used = true
	return nil, errors.New("the tunnel must not be used")
}

func TestTheBypassListSkipsTheTunnel(t *testing.T) {
	ln := startEchoServer(t)
	addr := ln.Addr().String()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("splitting %q: %v", addr, err)
	}

	opener := &recordingOpener{}
	d := &tunnelDialer{controller: opener, bypass: ParseBypassList([]string{host})}

	conn, err := d.OpenTunnel(addr)
	if err != nil {
		t.Fatalf("the bypassed host should connect: %v", err)
	}
	defer conn.Close()
	if opener.used {
		t.Fatal("the bypassed host went through the tunnel")
	}
}

func TestAHostThatIsNotBypassedGoesThroughTheTunnel(t *testing.T) {
	opener := &recordingOpener{}
	d := &tunnelDialer{controller: opener, bypass: ParseBypassList([]string{"*.ir"})}

	if _, err := d.OpenTunnel("example.com:443"); err == nil {
		t.Fatal("the tunnel should have been used")
	}
	if !opener.used {
		t.Fatal("the host should have gone through the tunnel")
	}
}

func TestTheBypassListIsEmptyByDefault(t *testing.T) {
	opener := &recordingOpener{}
	d := &tunnelDialer{controller: opener, bypass: ParseBypassList(nil)}

	if _, err := d.OpenTunnel("example.com:443"); err == nil {
		t.Fatal("the tunnel should have been used")
	}
	if !opener.used {
		t.Fatal("the host should have gone through the tunnel")
	}
}

// The direct dial has to leave through the physical interface when the
// tunnel's own adapter is up, or the connection is routed straight back
// into the tunnel.
func TestTheDirectDialUsesThePinnedDialer(t *testing.T) {
	ln := startEchoServer(t)
	addr := ln.Addr().String()

	called := false
	d := &tunnelDialer{
		controller: &recordingOpener{},
		bypass:     ParseBypassList([]string{"127.0.0.1"}),
		dialDirect: func(ctx context.Context, network, address string) (net.Conn, error) {
			called = true
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, address)
		},
	}

	conn, err := d.OpenTunnel(addr)
	if err != nil {
		t.Fatalf("the bypassed host should connect: %v", err)
	}
	defer conn.Close()
	if !called {
		t.Fatal("the pinned dialer was not used")
	}
}

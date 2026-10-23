package engine

import (
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// stuckOpener never returns: it stands in for a CONNECT that is waiting on an
// upstream rebuild, which is what used to hold a disconnect hostage.
type stuckOpener struct {
	release chan struct{}
}

func (s *stuckOpener) OpenTunnel(string) (net.Conn, error) {
	<-s.release
	return nil, ErrNoUsableSession
}

// A disconnect must not wait for in-flight CONNECT attempts. The grace period
// is the whole point: the UI shows "Disconnecting…" until Close returns.
func TestSocksCloseReturnsWithStuckOpener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	opener := &stuckOpener{release: make(chan struct{})}
	srv := NewSocksServerOn(ln, opener, &Stats{})

	client, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Complete the greeting and ask for a CONNECT, which parks the handler in
	// OpenTunnel.
	if _, err := client.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	greeting := make([]byte, 2)
	if _, err := client.Read(greeting); err != nil {
		t.Fatalf("greeting reply: %v", err)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x00, 0x50}
	if _, err := client.Write(req); err != nil {
		t.Fatalf("connect request: %v", err)
	}

	// Let the handler reach OpenTunnel.
	time.Sleep(50 * time.Millisecond)

	started := time.Now()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(started)
	close(opener.release)

	if elapsed > closeGrace+1500*time.Millisecond {
		t.Fatalf("Close took %s; it must give up after %s", elapsed, closeGrace)
	}
}

func TestHTTPProxyCloseReturnsWithStuckOpener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	opener := &stuckOpener{release: make(chan struct{})}
	srv := NewHTTPProxyServerOn(ln, opener, &Stats{})

	client, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if _, err := client.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")); err != nil {
		t.Fatalf("CONNECT: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	started := time.Now()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	elapsed := time.Since(started)
	close(opener.release)

	if elapsed > closeGrace+1500*time.Millisecond {
		t.Fatalf("Close took %s; it must give up after %s", elapsed, closeGrace)
	}
}

func TestEdgeRefused(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"gateway error", &proxyConnectHTTPError{StatusCode: http.StatusBadGateway, Status: "502 Bad Gateway"}, true},
		{"wrapped gateway error", errors.Join(ErrSessionUnhealthy,
			&proxyConnectHTTPError{StatusCode: http.StatusBadGateway}), true},
		{"unauthorized is not a refusal", &proxyConnectHTTPError{StatusCode: http.StatusUnauthorized}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := edgeRefused(tc.err); got != tc.want {
			t.Errorf("%s: edgeRefused = %v, want %v", tc.name, got, tc.want)
		}
	}
}

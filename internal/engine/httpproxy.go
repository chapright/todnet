package engine

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTPProxyServer speaks the subset of HTTP proxying Windows and browsers
// use: CONNECT for TLS and absolute-form requests for plain HTTP.
type HTTPProxyServer struct {
	opener           TunnelOpener
	handshakeTimeout time.Duration
	idleTimeout      time.Duration
	stats            *Stats
	ln               net.Listener
	mu               sync.Mutex
	closed           bool
	conns            map[net.Conn]struct{}
	wg               sync.WaitGroup
}

// NewHTTPProxyServer binds addr and starts accepting connections.
func NewHTTPProxyServer(addr string, opener TunnelOpener, stats *Stats) (*HTTPProxyServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return NewHTTPProxyServerOn(ln, opener, stats), nil
}

// NewHTTPProxyServerOn serves on an already-bound listener.
func NewHTTPProxyServerOn(ln net.Listener, opener TunnelOpener, stats *Stats) *HTTPProxyServer {
	s := &HTTPProxyServer{
		opener:           opener,
		handshakeTimeout: 15 * time.Second,
		stats:            stats,
		ln:               ln,
		conns:            make(map[net.Conn]struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

// Addr is the bound address.
func (s *HTTPProxyServer) Addr() net.Addr { return s.ln.Addr() }

func (s *HTTPProxyServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			LogWarn("http proxy accept failed: %v", err)
			time.Sleep(20 * time.Millisecond)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *HTTPProxyServer) handle(conn net.Conn) {
	defer conn.Close()

	if !s.track(conn) {
		return
	}
	defer s.untrack(conn)

	if s.handshakeTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.handshakeTimeout))
	}

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	if req.Method == http.MethodConnect {
		s.handleConnect(conn, br, req)
		return
	}
	s.handlePlain(conn, req)
}

func (s *HTTPProxyServer) handleConnect(conn net.Conn, br *bufio.Reader, req *http.Request) {
	target := req.Host
	if target == "" {
		target = req.URL.Host
	}
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "443")
	}

	// The edge cannot reach private addresses, and asking it to just produces
	// a confusing 502. Say so locally instead. This mirrors the bypass list
	// the system proxy already sets, so it only catches apps that were handed
	// the proxy explicitly.
	if host, _, err := net.SplitHostPort(target); err == nil && isPrivateDestination(host) {
		LogThrottled(LevelWarn, "local:"+target,
			"HTTP CONNECT refused client=%s target=%s: private address, not carried by the tunnel",
			conn.RemoteAddr(), target)
		_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}

	LogDebug("HTTP CONNECT client=%s target=%s", conn.RemoteAddr(), target)

	started := time.Now()
	upstream, err := s.opener.OpenTunnel(target)
	if err != nil {
		// Warn, not debug: a refused destination is the reason a download
		// fails, and it has to be visible without turning on verbose logging.
		LogThrottled(LevelWarn, "connect:"+target+":"+err.Error(),
			"HTTP CONNECT refused client=%s target=%s: %s", conn.RemoteAddr(), target, explainConnectError(err))
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer upstream.Close()
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)

	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	s.stats.ActiveConns.Add(1)
	s.stats.TotalConns.Add(1)
	defer s.stats.ActiveConns.Add(-1)

	// Anything the client already buffered past the CONNECT headers belongs
	// to the tunnel.
	var client net.Conn = conn
	if br.Buffered() > 0 {
		buffered, _ := br.Peek(br.Buffered())
		client = &prefixedConn{Conn: conn, prefix: append([]byte(nil), buffered...)}
	}

	LogDebug("HTTP CONNECT open client=%s target=%s setup=%s",
		conn.RemoteAddr(), target, time.Since(started).Round(time.Millisecond))

	up, down := &countingConn{Conn: client, readC: &s.stats.BytesUp, writeC: &s.stats.BytesDown},
		&countingConn{Conn: upstream, readC: &s.stats.BytesDown, writeC: &s.stats.BytesUp}
	relayErr := proxyBidirectional(up, down, s.idleTimeout)

	ttfb := "never"
	if ns := up.firstWrite.Load(); ns > 0 {
		ttfb = time.Duration(ns - started.UnixNano()).Round(time.Millisecond).String()
	}
	reason := ""
	if relayErr != nil && !errors.Is(relayErr, io.EOF) && !errors.Is(relayErr, net.ErrClosed) {
		reason = " reason=" + relayErr.Error()
	}
	LogDebug("HTTP CONNECT close client=%s target=%s after=%s up=%d down=%d ttfb=%s%s",
		conn.RemoteAddr(), target, time.Since(started).Round(time.Millisecond),
		up.nRead.Load(), up.nWrite.Load(), ttfb, reason)
}

// explainConnectError turns an edge refusal into the reason behind it. The
// edge's own wording ("502 Bad Gateway: Destination IP Prohibited") is
// accurate but reads like a local fault.
func explainConnectError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Destination IP Prohibited"):
		return "the VPN server will not route to that address"
	case strings.Contains(msg, "Destination Port Prohibited"):
		return "the VPN server does not carry that port"
	case strings.Contains(msg, "502"):
		return "the VPN server could not reach it"
	case isTimeout(err):
		return "timed out opening the tunnel"
	default:
		return msg
	}
}

// handlePlain relays a single absolute-form HTTP request. The forwarded
// request is marked Connection: close so the response has a defined end and
// the client connection can be closed deterministically.
func (s *HTTPProxyServer) handlePlain(conn net.Conn, req *http.Request) {
	if req.URL == nil || req.URL.Host == "" {
		_, _ = io.WriteString(conn, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	target := req.URL.Host
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "80")
	}
	LogDebug("HTTP %s client=%s target=%s", req.Method, conn.RemoteAddr(), target)

	upstream, err := s.opener.OpenTunnel(target)
	if err != nil {
		LogWarn("HTTP request failed client=%s target=%s err=%v", conn.RemoteAddr(), target, err)
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer upstream.Close()
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)

	s.stats.ActiveConns.Add(1)
	s.stats.TotalConns.Add(1)
	defer s.stats.ActiveConns.Add(-1)

	_ = conn.SetDeadline(time.Time{})

	outbound := req.Clone(req.Context())
	outbound.RequestURI = ""
	outbound.URL.Scheme = ""
	outbound.URL.Host = ""
	outbound.Host = req.URL.Host
	outbound.Close = true
	outbound.Header.Del("Proxy-Connection")
	outbound.Header.Del("Proxy-Authorization")
	outbound.Header.Set("Connection", "close")

	// Plain HTTP travels in exactly one direction on each side: the request is
	// written up, the response is written down. Only the write side of each
	// wrapper is used, so the read counter stays nil and the byte totals in the
	// logs stay honest.
	uw := &countingConn{Conn: upstream, writeC: &s.stats.BytesUp}
	cw := &countingConn{Conn: conn, writeC: &s.stats.BytesDown}

	if err := outbound.Write(uw); err != nil {
		LogDebug("writing request upstream failed target=%s err=%v", target, err)
		return
	}

	if _, err := io.Copy(cw, upstream); err != nil && !errors.Is(err, io.EOF) {
		LogDebug("relaying response failed target=%s err=%v", target, err)
	}
}

// Close stops accepting, force-closes in-flight client connections and waits
// (briefly) for handlers to unwind. The controller is closed first, so a
// handler parked in OpenTunnel returns instead of holding up the disconnect.
func (s *HTTPProxyServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()

	err := s.ln.Close()
	for _, c := range conns {
		_ = c.Close()
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeGrace):
		s.mu.Lock()
		left := len(s.conns)
		s.mu.Unlock()
		LogWarn("HTTP proxy closed with %d connection(s) still draining", left)
	}
	return err
}

func (s *HTTPProxyServer) track(conn net.Conn) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		return false
	}
	s.conns[conn] = struct{}{}
	s.mu.Unlock()
	return true
}

func (s *HTTPProxyServer) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

// prefixedConn replays bytes that were already buffered before the tunnel
// took over the connection.
type prefixedConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// Half-close has to be forwarded so the tunnel's EOF reaches the upstream.
func (c *prefixedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *prefixedConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// ParseHostPort normalises a proxy target, defaulting the port when absent.
func ParseHostPort(target string, defaultPort int) (string, error) {
	if target == "" {
		return "", fmt.Errorf("empty target")
	}
	if host, port, err := net.SplitHostPort(target); err == nil {
		if _, convErr := strconv.Atoi(port); convErr != nil {
			return "", fmt.Errorf("invalid port %q", port)
		}
		return net.JoinHostPort(host, port), nil
	}
	return net.JoinHostPort(strings.Trim(target, "[]"), strconv.Itoa(defaultPort)), nil
}

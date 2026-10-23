package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	socksVersion5     = 0x05
	socksCmdConnect   = 0x01
	socksCmdUDP       = 0x03
	socksAtypIPv4     = 0x01
	socksAtypDomain   = 0x03
	socksAtypIPv6     = 0x04
	socksAuthNoAuth   = 0x00
	socksAuthNoAccept = 0xff

	socksReplySuccess   = 0x00
	socksReplyFailure   = 0x01
	socksReplyNotAllow  = 0x02
	socksReplyNetUnrch  = 0x03
	socksReplyHostUnrch = 0x04
	socksReplyCmdUnsup  = 0x07
	socksReplyAtypUnsup = 0x08

	// closeGrace bounds how long Close waits for in-flight handlers. A
	// disconnect must never look hung in the UI; stragglers are logged.
	closeGrace = 2 * time.Second
)

var errUnsupportedAddrType = errors.New("unsupported SOCKS address type")

// Stats tracks live proxy counters for the UI.
type Stats struct {
	ActiveConns atomic.Int64
	TotalConns  atomic.Int64
	BytesUp     atomic.Int64
	BytesDown   atomic.Int64
}

// The tun package reports through this small surface, so the counters shown
// in the UI cover both tunnel modes.
func (s *Stats) AddUp(n int64)   { s.BytesUp.Add(n) }
func (s *Stats) AddDown(n int64) { s.BytesDown.Add(n) }

func (s *Stats) ConnOpened() {
	s.ActiveConns.Add(1)
	s.TotalConns.Add(1)
}

func (s *Stats) ConnClosed() { s.ActiveConns.Add(-1) }

// TunnelOpener opens a connection to authority ("host:port") through the
// upstream VPN edge.
type TunnelOpener interface {
	OpenTunnel(authority string) (net.Conn, error)
}

// SocksServer is a minimal SOCKS5 CONNECT proxy. UDP ASSOCIATE is rejected:
// the upstream edge only carries TCP CONNECT streams, so advertising UDP
// would make clients wait for traffic that can never arrive.
type SocksServer struct {
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

// NewSocksServer binds addr and starts accepting connections.
func NewSocksServer(addr string, opener TunnelOpener, stats *Stats) (*SocksServer, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return NewSocksServerOn(ln, opener, stats), nil
}

// NewSocksServerOn serves on an already-bound listener, so the caller can fall
// back to another port when the preferred one is taken.
func NewSocksServerOn(ln net.Listener, opener TunnelOpener, stats *Stats) *SocksServer {
	s := &SocksServer{
		opener:           opener,
		handshakeTimeout: 10 * time.Second,
		stats:            stats,
		ln:               ln,
		conns:            make(map[net.Conn]struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

// Addr is the bound address, useful when the caller asked for port 0.
func (s *SocksServer) Addr() net.Addr { return s.ln.Addr() }

func (s *SocksServer) acceptLoop() {
	defer s.wg.Done()
	var retryDelay time.Duration
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			if retryDelay == 0 {
				retryDelay = 5 * time.Millisecond
			} else {
				retryDelay *= 2
			}
			if retryDelay > time.Second {
				retryDelay = time.Second
			}
			LogWarn("socks accept failed; retrying in %s: %v", retryDelay, err)
			time.Sleep(retryDelay)
			continue
		}
		retryDelay = 0
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *SocksServer) handle(conn net.Conn) {
	defer conn.Close()
	start := time.Now()

	if !s.track(conn) {
		return
	}
	defer s.untrack(conn)

	if s.handshakeTimeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.handshakeTimeout))
	}
	target, replyCode, err := handshakeSOCKS5(conn)
	if err != nil {
		LogDebug("SOCKS5 handshake failed client=%s reply=%d err=%v", conn.RemoteAddr(), replyCode, err)
		if replyCode != 0 {
			_ = writeSOCKSReply(conn, replyCode, nil)
		}
		return
	}
	if s.handshakeTimeout > 0 {
		_ = conn.SetDeadline(time.Time{})
	}
	LogDebug("SOCKS5 CONNECT client=%s target=%s", conn.RemoteAddr(), target)

	upstream, err := s.opener.OpenTunnel(target)
	if err != nil {
		reply := mapUpstreamError(err)
		LogThrottled(LevelWarn, "socks:"+target+":"+err.Error(),
			"SOCKS5 CONNECT refused client=%s target=%s: %s",
			conn.RemoteAddr(), target, explainConnectError(err))
		_ = writeSOCKSReply(conn, reply, nil)
		return
	}
	defer upstream.Close()
	// Track the upstream side too: a handler blocked in a tunnel read must be
	// woken by Close, not by waiting for the peer to go away.
	if !s.track(upstream) {
		return
	}
	defer s.untrack(upstream)

	if err := writeSOCKSReply(conn, socksReplySuccess, conn.LocalAddr()); err != nil {
		return
	}
	LogDebug("SOCKS5 open client=%s target=%s setup=%s",
		conn.RemoteAddr(), target, time.Since(start).Round(time.Millisecond))

	s.stats.ActiveConns.Add(1)
	s.stats.TotalConns.Add(1)
	defer s.stats.ActiveConns.Add(-1)

	// Client side: reads are uploads, writes are downloads.
	cr := &countingConn{Conn: conn, readC: &s.stats.BytesUp, writeC: &s.stats.BytesDown}
	// Upstream side is the mirror image.
	ur := &countingConn{Conn: upstream, readC: &s.stats.BytesDown, writeC: &s.stats.BytesUp}
	proxyBidirectional(cr, ur, s.idleTimeout)
	ttfb := "never"
	if ns := cr.firstWrite.Load(); ns > 0 {
		ttfb = time.Duration(ns - start.UnixNano()).Round(time.Millisecond).String()
	}
	LogDebug("SOCKS5 close client=%s target=%s after=%s up=%d down=%d ttfb=%s",
		conn.RemoteAddr(), target, time.Since(start).Round(time.Millisecond),
		cr.nRead.Load(), cr.nWrite.Load(), ttfb)
}

// countingConn attributes bytes to a direction. Read is whatever the peer
// sends us, Write is whatever we send it — so the client side counts reads as
// upload and writes as download, and the upstream side is the mirror image.
//
// Counting both operations into one counter (as this used to) made "up" and
// "down" identical totals, because every byte is written somewhere and read
// somewhere.
type countingConn struct {
	net.Conn
	readC, writeC *atomic.Int64
	nRead, nWrite atomic.Int64
	// firstWrite records when the first byte left this side, which is the
	// only way to tell "the destination never answered" from "the request
	// was slow".
	firstWrite atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		if c.readC != nil {
			c.readC.Add(int64(n))
		}
		c.nRead.Add(int64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		if c.writeC != nil {
			c.writeC.Add(int64(n))
		}
		c.nWrite.Add(int64(n))
		// Store first, compare later: 0 means "nothing was ever written".
		c.firstWrite.CompareAndSwap(0, time.Now().UnixNano())
	}
	return n, err
}

// CloseWrite and CloseRead must be forwarded explicitly: embedding a net.Conn
// interface hides the concrete type's half-close methods, which would stop
// EOF from propagating through the tunnel and leave connections lingering.
func (c *countingConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

func (c *countingConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

// Close stops accepting, force-closes in-flight client connections and waits
// (briefly) for the handlers to unwind. Without the forced close a half-closed
// tunnel would keep the drain timer alive for minutes and stall shutdown.
func (s *SocksServer) Close() error {
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

	// The controller is closed before the listener, so handlers waiting on a
	// CONNECT return almost immediately. Anything still running after the
	// grace period is a straggler: report it instead of freezing the UI on
	// "Disconnecting…".
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
		LogWarn("SOCKS5 proxy closed with %d connection(s) still draining", left)
	}
	return err
}

func (s *SocksServer) track(conn net.Conn) bool {
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

func (s *SocksServer) untrack(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func handshakeSOCKS5(conn net.Conn) (string, byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", 0, err
	}
	if header[0] != socksVersion5 {
		return "", 0, fmt.Errorf("unexpected SOCKS version %d", header[0])
	}

	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", 0, err
	}

	method := byte(socksAuthNoAccept)
	for _, candidate := range methods {
		if candidate == socksAuthNoAuth {
			method = socksAuthNoAuth
			break
		}
	}
	if _, err := conn.Write([]byte{socksVersion5, method}); err != nil {
		return "", 0, err
	}
	if method == socksAuthNoAccept {
		return "", 0, fmt.Errorf("no supported SOCKS auth methods")
	}

	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return "", 0, err
	}
	if reqHeader[0] != socksVersion5 {
		return "", socksReplyFailure, fmt.Errorf("unexpected request version %d", reqHeader[0])
	}
	if reqHeader[1] != socksCmdConnect {
		if reqHeader[1] == socksCmdUDP {
			return "", socksReplyCmdUnsup, fmt.Errorf("UDP ASSOCIATE is not supported by the upstream tunnel")
		}
		return "", socksReplyCmdUnsup, fmt.Errorf("unsupported SOCKS command %d", reqHeader[1])
	}

	host, err := readSOCKSAddr(conn, reqHeader[3])
	if err != nil {
		if errors.Is(err, errUnsupportedAddrType) {
			return "", socksReplyAtypUnsup, err
		}
		return "", socksReplyFailure, err
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", socksReplyFailure, err
	}
	port := binary.BigEndian.Uint16(portBytes)
	return net.JoinHostPort(host, strconv.Itoa(int(port))), socksReplySuccess, nil
}

func readSOCKSAddr(r io.Reader, atyp byte) (string, error) {
	switch atyp {
	case socksAtypIPv4:
		buf := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case socksAtypIPv6:
		buf := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case socksAtypDomain:
		var size [1]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return "", err
		}
		buf := make([]byte, int(size[0]))
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	default:
		return "", errUnsupportedAddrType
	}
}

func writeSOCKSReply(w io.Writer, rep byte, addr net.Addr) error {
	host := "0.0.0.0"
	port := 0
	if addr != nil {
		if tcpAddr, ok := addr.(*net.TCPAddr); ok {
			host = tcpAddr.IP.String()
			port = tcpAddr.Port
		}
	}

	ip := net.ParseIP(host)
	atyp := byte(socksAtypIPv4)
	addrBytes := []byte{0, 0, 0, 0}
	if ip != nil && ip.To4() != nil {
		addrBytes = ip.To4()
	} else if ip != nil && ip.To16() != nil {
		atyp = socksAtypIPv6
		addrBytes = ip.To16()
	}

	reply := make([]byte, 0, 6+len(addrBytes))
	reply = append(reply, socksVersion5, rep, 0x00, atyp)
	reply = append(reply, addrBytes...)
	reply = append(reply, byte(port>>8), byte(port))
	_, err := w.Write(reply)
	return err
}

func mapUpstreamError(err error) byte {
	if errors.Is(err, ErrNoUsableSession) {
		return socksReplyHostUnrch
	}
	if errors.Is(err, errUnsupportedAddrType) {
		return socksReplyAtypUnsup
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return socksReplyHostUnrch
	}
	return socksReplyFailure
}

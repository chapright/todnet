package engine

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
)

const (
	// A 5s ping timeout declared the session dead on any link with a slow
	// round trip, and every false positive tore down the streams that were
	// still working. The edge is reached over the public internet, so give a
	// ping a realistic budget and require two consecutive misses before the
	// session is considered gone.
	//
	// The budget also bounds how long a blackholed link goes unnoticed, and
	// that matters more than it looks: Windows' own TCP retransmit timer
	// gives up after roughly twenty seconds, and until then every stream
	// riding the dead connection simply hangs. Detecting first, and closing
	// the session so the streams fail at once, is the difference between a
	// client retrying in a second and a user staring at "network error".
	upstreamKeepAliveInterval    = 5 * time.Second
	upstreamKeepAlivePingTimeout = 5 * time.Second
	upstreamKeepAliveMisses      = 2
	upstreamSlowPingWarn         = 1500 * time.Millisecond
	// Roughly where a MASQUE edge starts queueing rather than multiplexing.
	// It is a warning threshold, not a hard limit.
	upstreamQueueWarnAt           = 48
	maxDistinctOpenTimeoutTargets = 3
	maxDistinctBadGatewayTargets  = 3
)

var (
	// ErrHTTP2Unavailable means the edge did not negotiate h2 via ALPN, so the
	// single-TCP-connection requirement of this protocol cannot be met.
	ErrHTTP2Unavailable = errors.New("proxy did not negotiate HTTP/2")
	// ErrHTTP3Unavailable means the edge did not negotiate h3.
	ErrHTTP3Unavailable = errors.New("proxy did not negotiate HTTP/3")
	// ErrSessionUnhealthy means the upstream session failed repeatedly and
	// should be rebuilt before it is used again.
	ErrSessionUnhealthy = errors.New("upstream proxy session became unhealthy")
	// ErrNoUsableSession means there is no upstream session to route through.
	ErrNoUsableSession = errors.New("no usable upstream proxy session")
)

// proxyConnectHTTPError carries the status the edge returned for a CONNECT.
type proxyConnectHTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *proxyConnectHTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("proxy CONNECT failed: %s", e.Status)
	}
	return fmt.Sprintf("proxy CONNECT failed: %s: %s", e.Status, e.Body)
}

// tunnelOpenTimeoutError reports that a CONNECT did not complete in time.
type tunnelOpenTimeoutError struct{ timeout time.Duration }

func (e *tunnelOpenTimeoutError) Error() string {
	return fmt.Sprintf("proxy CONNECT timed out after %s", e.timeout)
}
func (e *tunnelOpenTimeoutError) Timeout() bool   { return true }
func (e *tunnelOpenTimeoutError) Temporary() bool { return true }

// Session is one upstream connection that can open CONNECT tunnels.
type Session interface {
	OpenTunnel(authority string) (net.Conn, error)
	Close() error
}

type tokenUpdater interface {
	UpdateToken(token string) error
}

type livenessChecker interface {
	IsAlive() bool
}

// capacityChecker separates "this connection is dead" from "this connection is
// at the edge's stream limit". They are very different: one has to be closed at
// once, the other must be left alone because the streams it carries are fine.
type capacityChecker interface {
	IsFull() bool
}

func sessionAlive(s Session) bool {
	if s == nil {
		return false
	}
	if c, ok := s.(livenessChecker); ok {
		return c.IsAlive()
	}
	return true
}

// sessionFull reports that a session cannot accept another stream right now,
// even though it may be perfectly healthy for the ones it already carries.
func sessionFull(s Session) bool {
	if s == nil {
		return true
	}
	if c, ok := s.(capacityChecker); ok {
		return c.IsFull()
	}
	return false
}

// sessionFailureTracker watches per-target failures so a session that times
// out against several distinct targets is rebuilt instead of being handed to
// more clients.
type sessionFailureTracker struct {
	mu                 sync.Mutex
	openTimeoutTargets map[string]struct{}
	badGatewayTargets  map[string]struct{}
}

func (t *sessionFailureTracker) observe(authority string, err error) error {
	if err == nil {
		t.mu.Lock()
		t.openTimeoutTargets = nil
		t.badGatewayTargets = nil
		t.mu.Unlock()
		return nil
	}

	var timeoutErr *tunnelOpenTimeoutError
	var connectErr *proxyConnectHTTPError
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case errors.As(err, &timeoutErr):
		if t.openTimeoutTargets == nil {
			t.openTimeoutTargets = make(map[string]struct{})
		}
		t.openTimeoutTargets[authority] = struct{}{}
		t.badGatewayTargets = nil
		if len(t.openTimeoutTargets) >= maxDistinctOpenTimeoutTargets {
			return fmt.Errorf("%w: %w", ErrSessionUnhealthy, err)
		}
	case errors.As(err, &connectErr) && connectErr.StatusCode == http.StatusBadGateway:
		if t.badGatewayTargets == nil {
			t.badGatewayTargets = make(map[string]struct{})
		}
		t.badGatewayTargets[authority] = struct{}{}
		t.openTimeoutTargets = nil
		if len(t.badGatewayTargets) >= maxDistinctBadGatewayTargets {
			return fmt.Errorf("%w: %w", ErrSessionUnhealthy, err)
		}
	default:
		t.openTimeoutTargets = nil
		t.badGatewayTargets = nil
	}
	return err
}

func shouldRebuild(err error) bool {
	if errors.Is(err, ErrSessionUnhealthy) {
		return true
	}
	var connectErr *proxyConnectHTTPError
	if errors.As(err, &connectErr) {
		switch connectErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired:
			return true
		default:
			return false
		}
	}
	var timeoutErr *tunnelOpenTimeoutError
	if errors.As(err, &timeoutErr) {
		return false
	}
	return true
}

func roundTripWithOpenTimeout(timeout time.Duration, do func(context.Context) (*http.Response, error)) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	if timeout <= 0 {
		resp, err := do(ctx)
		if err != nil {
			cancel()
			return nil, nil, err
		}
		return resp, cancel, nil
	}

	resultCh := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, err := do(ctx)
		resultCh <- struct {
			resp *http.Response
			err  error
		}{resp, err}
	}()

	timer := time.NewTimer(timeout)
	select {
	case result := <-resultCh:
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if result.err != nil {
			cancel()
			return nil, nil, result.err
		}
		return result.resp, cancel, nil
	case <-timer.C:
		cancel()
		go func() {
			cleanupTimer := time.NewTimer(timeout)
			defer cleanupTimer.Stop()
			select {
			case result := <-resultCh:
				if result.resp != nil && result.resp.Body != nil {
					_ = result.resp.Body.Close()
				}
			case <-cleanupTimer.C:
			}
		}()
		return nil, nil, &tunnelOpenTimeoutError{timeout: timeout}
	}
}

// ---------------------------------------------------------------------------
// HTTP/2 session
// ---------------------------------------------------------------------------

type h2Session struct {
	raw       *tls.Conn
	cc        *http2.ClientConn
	proxyHost string
	token     string
	timeout   time.Duration
	tokenMu   sync.RWMutex
	health    sessionFailureTracker
	closeOnce sync.Once
	closeErr  error
	alive     atomic.Bool
	stopPing  context.CancelFunc

	// inFlight counts CONNECT streams currently open on this session. The edge
	// caps how many it will multiplex at once; once the cap is reached Go's
	// transport queues the next RoundTrip silently, so without this counter a
	// saturation looks identical to a dead destination.
	inFlight atomic.Int64

	// tunnels lets a session that has been declared dead fail its own streams
	// immediately. Without it a stalled connection leaves every open stream
	// blocked until the client gives up, which is what "the app says network
	// error after 15 seconds" actually was.
	tunnelMu sync.Mutex
	tunnels  map[*tunnelConn]struct{}
}

func newH2Session(parent context.Context, proxyURL *url.URL, token string, timeout time.Duration, edgeIPs []net.IP) (*h2Session, error) {
	proxyHost := proxyURL.Host
	if proxyURL.Port() == "" {
		proxyHost = proxyURL.Hostname() + ":443"
	}

	proxyTLS, err := dialEdgeTLS(parent, proxyURL.Hostname(), proxyURL.Port(), edgeIPs, timeout)
	if err != nil {
		return nil, err
	}

	if proxyTLS.ConnectionState().NegotiatedProtocol != "h2" {
		_ = proxyTLS.Close()
		return nil, ErrHTTP2Unavailable
	}

	transport := &http2.Transport{
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     30 * time.Second,
		// A slow upload used to be killed after 30s of a blocked write, which
		// looked like the destination rejecting the request. Only a truly
		// wedged connection should hit this, and the keepalive pings already
		// catch that.
		WriteByteTimeout: 3 * time.Minute,
	}
	cc, err := transport.NewClientConn(proxyTLS)
	if err != nil {
		_ = proxyTLS.Close()
		return nil, err
	}
	LogInfo("upstream HTTP/2 session established proxy=%s", proxyHost)

	ctx, cancel := context.WithCancel(context.Background())
	session := &h2Session{
		raw:       proxyTLS,
		cc:        cc,
		proxyHost: proxyHost,
		token:     token,
		timeout:   timeout,
		stopPing:  cancel,
		tunnels:   make(map[*tunnelConn]struct{}),
	}
	session.alive.Store(true)
	go session.runKeepAlive(ctx)
	return session, nil
}

// runKeepAlive pings the upstream so a silently dropped session is noticed
// while idle rather than on the next client CONNECT.
func (s *h2Session) runKeepAlive(ctx context.Context) {
	ticker := time.NewTicker(upstreamKeepAliveInterval)
	defer ticker.Stop()

	misses := 0
	var lastRTT time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			pingCtx, cancel := context.WithTimeout(ctx, upstreamKeepAlivePingTimeout)
			err := s.cc.Ping(pingCtx)
			cancel()
			if err == nil {
				lastRTT = time.Since(start)
				misses = 0
				// A slow but successful ping is the early warning that the
				// link is degrading, which is what precedes the failures
				// users actually report.
				if lastRTT > upstreamSlowPingWarn {
					LogThrottled(LevelWarn, "ping-slow:"+s.proxyHost,
						"upstream keepalive is slow proxy=%s rtt=%s inFlight=%d",
						s.proxyHost, lastRTT.Round(time.Millisecond), s.inFlight.Load())
				}
				continue
			}
			if ctx.Err() != nil {
				return
			}
			misses++
			LogWarn("upstream HTTP/2 keepalive missed proxy=%s err=%v miss=%d/%d inFlight=%d lastRTT=%s",
				s.proxyHost, err, misses, upstreamKeepAliveMisses, s.inFlight.Load(),
				lastRTT.Round(time.Millisecond))
			if misses < upstreamKeepAliveMisses {
				// One miss is often a transient stall. Failing every open
				// stream on a single slow ping is worse than waiting.
				continue
			}
			s.alive.Store(false)
			open := s.tunnelCount()
			LogWarn("upstream HTTP/2 session declared dead proxy=%s after %d missed pings; failing %d open stream(s)",
				s.proxyHost, misses, open)
			// Fail the streams now: they are pointing at a connection that
			// will never answer, and the client is waiting on them.
			s.failTunnels(ErrSessionUnhealthy)
			// Then close the connection itself. Failing the tunnels only
			// wakes the read side; a stream parked in the transport's write
			// path stays parked until the OS gives up on the dead socket,
			// which is tens of seconds later. Closing the ClientConn fails
			// every stream in one step. Close is idempotent and does not
			// wait on this goroutine, so running it here is safe.
			go s.Close()
			return
		}
	}
}

// dialEdgeTLS opens the TLS connection to the VPN edge.
//
// When the addresses are already known they are dialled directly and the
// hostname is only used for SNI. That matters in TUN mode: once the adapter
// is up, a name lookup goes through the tunnel, and the tunnel is exactly what
// is being rebuilt. Resolving the edge through the very session that just died
// is a loop that never opens — so the addresses resolved before the tunnel
// came up are reused here, and the hostname is the fallback for the first
// connect and for a machine where the edge moved.
func dialEdgeTLS(parent context.Context, host, port string, edgeIPs []net.IP, timeout time.Duration) (*tls.Conn, error) {
	if port == "" {
		port = "443"
	}
	cfg := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2"},
	}
	dialer := &net.Dialer{Timeout: timeout}

	// A stale first address must not eat the whole budget: each attempt gets
	// a share, so a moved edge still connects on a later address.
	perAttempt := timeout
	if len(edgeIPs) > 1 {
		perAttempt = timeout / time.Duration(len(edgeIPs))
		if perAttempt < 5*time.Second {
			perAttempt = 5 * time.Second
		}
	}

	var lastErr error
	for _, ip := range edgeIPs {
		if ip == nil {
			continue
		}
		// The dial runs under the attempt's context, so a cancelled connection
		// attempt stops waiting on an address that will never answer.
		raw, err := dialer.DialContext(parent, "tcp", net.JoinHostPort(ip.String(), port))
		if err != nil {
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			lastErr = err
			continue
		}
		ctx, cancel := context.WithTimeout(parent, perAttempt)
		tlsConn := tls.Client(raw, cfg)
		err = tlsConn.HandshakeContext(ctx)
		cancel()
		if err != nil {
			_ = raw.Close()
			if parent.Err() != nil {
				return nil, parent.Err()
			}
			lastErr = err
			continue
		}
		return tlsConn, nil
	}

	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(host, port), cfg)
	if err != nil {
		if lastErr != nil {
			return nil, fmt.Errorf("dialling %s: %w (by address: %v)", host, err, lastErr)
		}
		return nil, err
	}
	return conn, nil
}

func (s *h2Session) trackTunnel(t *tunnelConn) {
	s.tunnelMu.Lock()
	if s.tunnels != nil {
		s.tunnels[t] = struct{}{}
	}
	s.tunnelMu.Unlock()
}

func (s *h2Session) untrackTunnel(t *tunnelConn) {
	s.tunnelMu.Lock()
	delete(s.tunnels, t)
	s.tunnelMu.Unlock()
}

// tunnelCount is how many streams this session is currently carrying. It is
// what the "declared dead" log means by "open streams" — inFlight counts
// CONNECTs still being negotiated, which is zero on an idle session that has
// plenty of streams open.
func (s *h2Session) tunnelCount() int {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	return len(s.tunnels)
}

// failTunnels unblocks every stream this session owns, so a dead session
// surfaces as an immediate error instead of a client-side timeout.
func (s *h2Session) failTunnels(err error) {
	s.tunnelMu.Lock()
	list := make([]*tunnelConn, 0, len(s.tunnels))
	for t := range s.tunnels {
		list = append(list, t)
	}
	s.tunnelMu.Unlock()
	for _, t := range list {
		t.fail(err)
	}
}

// IsAlive reports whether this connection is still believed to work. Capacity
// is deliberately not part of it: a session at its stream limit is healthy, and
// treating it as dead would close the streams it is carrying perfectly well.
func (s *h2Session) IsAlive() bool { return s.alive.Load() }

// IsFull reports that the edge will not accept another stream on this
// connection right now.
func (s *h2Session) IsFull() bool { return !s.cc.CanTakeNewRequest() }

func (s *h2Session) OpenTunnel(authority string) (net.Conn, error) {
	inFlight := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)

	// The edge limits how many streams one connection may multiplex. Past
	// that limit Go's transport queues the CONNECT with no visible signal,
	// and the old 20s cap turned that queue into a bogus "timed out" error.
	if inFlight > upstreamQueueWarnAt {
		LogThrottled(LevelWarn, "queue:"+s.proxyHost,
			"upstream has %d CONNECT streams open; new tunnels are likely queued by the edge's concurrency limit (proxy=%s)",
			inFlight, s.proxyHost)
	}

	reqBody := newTunnelWriteBuffer(tunnelWriteBufSize)
	opened := time.Now()
	resp, cancel, err := roundTripWithOpenTimeout(s.timeout, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+authority, reqBody)
		if err != nil {
			return nil, err
		}
		req.Host = authority
		req.URL.Host = s.proxyHost
		req.Header.Set("Proxy-Authorization", "Bearer "+s.bearerToken())
		return s.cc.RoundTrip(req)
	})
	if err != nil {
		reqBody.failRead(io.ErrClosedPipe)
		waited := time.Since(opened)
		if waited >= s.timeout && inFlight > upstreamQueueWarnAt {
			LogWarn("CONNECT %s timed out after %s with %d streams already open; the edge's concurrency limit is the likely cause",
				authority, waited.Round(time.Millisecond), inFlight)
		}
		return nil, s.health.observe(authority, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		cancel()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		reqBody.failRead(io.ErrClosedPipe)
		return nil, s.health.observe(authority, &proxyConnectHTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
		})
	}
	s.health.observe(authority, nil)

	// A CONNECT that took noticeably longer than the others is a queueing
	// symptom worth recording once per destination.
	if waited := time.Since(opened); waited > 3*time.Second {
		LogDebug("CONNECT %s accepted after %s (inFlight=%d)", authority, waited.Round(time.Millisecond), inFlight)
	}
	// One line per tunnel in verbose mode: the status the edge actually
	// answered with, the protocol it negotiated, and whether it advertises
	// HTTP/3 for this host. Without these a "connection opened but no data"
	// report cannot be told apart from a queueing or policy problem.
	LogDebug("CONNECT %s -> %s proto=%s alt-svc=%q in %s",
		authority, resp.Status, resp.Proto, resp.Header.Get("Alt-Svc"),
		time.Since(opened).Round(time.Millisecond))

	conn := &tunnelConn{
		reader:  resp.Body,
		writer:  reqBody,
		reqBody: reqBody,
		name:    "h2-connect-stream",
		cancel:  cancel,
		session: s,
	}
	s.trackTunnel(conn)
	return conn, nil
}

func (s *h2Session) bearerToken() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	return s.token
}

func (s *h2Session) UpdateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("empty proxy session token")
	}
	s.tokenMu.Lock()
	s.token = token
	s.tokenMu.Unlock()
	return nil
}

func (s *h2Session) Close() error {
	s.closeOnce.Do(func() {
		s.alive.Store(false)
		if s.stopPing != nil {
			s.stopPing()
		}
		// Wake any handler parked on a stream before the connection goes
		// away, so teardown does not wait on a client that is not reading.
		s.failTunnels(ErrNoUsableSession)
		if s.cc != nil {
			s.closeErr = s.cc.Close()
		}
		if s.raw != nil {
			if closeErr := s.raw.Close(); s.closeErr == nil {
				s.closeErr = closeErr
			}
		}
	})
	return s.closeErr
}

// ---------------------------------------------------------------------------
// HTTP/3 session
// ---------------------------------------------------------------------------

type h3Session struct {
	conn      *quic.Conn
	udpConn   *net.UDPConn
	rt        *http3.Transport
	proxyHost string
	token     string
	timeout   time.Duration
	tokenMu   sync.RWMutex
	health    sessionFailureTracker
	closeOnce sync.Once
	closeErr  error

	inFlight atomic.Int64
	tunnelMu sync.Mutex
	tunnels  map[*tunnelConn]struct{}
}

func newH3Session(parent context.Context, proxyURL *url.URL, token string, timeout time.Duration, edgeIPs []net.IP) (*h3Session, error) {
	host := proxyURL.Hostname()
	portStr := proxyURL.Port()
	if portStr == "" {
		portStr = "443"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy port %q: %w", portStr, err)
	}
	proxyHost := net.JoinHostPort(host, portStr)

	ctx := parent
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
	}
	defer cancel()

	var dialAddrs []netip.Addr
	for _, ip := range edgeIPs {
		if addr, ok := netip.AddrFromSlice(ip); ok {
			dialAddrs = append(dialAddrs, addr)
		}
	}
	if len(dialAddrs) == 0 {
		// No cached address: resolve once. In TUN mode this path is only
		// taken before the adapter is up (see dialEdgeTLS).
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		dialAddrs = resolved
	}
	if len(dialAddrs) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}

	tlsCfg := &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h3"},
	}
	quicCfg := &quic.Config{
		KeepAlivePeriod: 30 * time.Second,
		// The default 1280-byte QUIC payload exceeds low-MTU paths once IP/UDP
		// headers are added, which silently drops every Initial packet.
		// Start below the RFC 8899 floor; PMTU discovery grows it back up.
		InitialPacketSize: 1200,
	}

	var perAttempt time.Duration
	if timeout > 0 {
		perAttempt = timeout / time.Duration(len(dialAddrs))
		if perAttempt < 3*time.Second {
			perAttempt = 3 * time.Second
		}
	}

	var (
		conn    *quic.Conn
		udpConn *net.UDPConn
		lastErr error
	)
	for _, ip := range dialAddrs {
		uc, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
		if err != nil {
			lastErr = err
			continue
		}
		udpAddr := &net.UDPAddr{IP: ip.AsSlice(), Port: port, Zone: ip.Zone()}
		attemptCtx := ctx
		attemptCancel := func() {}
		if perAttempt > 0 {
			attemptCtx, attemptCancel = context.WithTimeout(ctx, perAttempt)
		}
		c, err := quic.Dial(attemptCtx, uc, udpAddr, tlsCfg, quicCfg)
		attemptCancel()
		if err != nil {
			_ = uc.Close()
			lastErr = fmt.Errorf("dial %s: %w", udpAddr, err)
			continue
		}
		conn = c
		udpConn = uc
		break
	}
	if conn == nil {
		return nil, lastErr
	}

	if conn.ConnectionState().TLS.NegotiatedProtocol != "h3" {
		_ = conn.CloseWithError(0, "")
		_ = udpConn.Close()
		return nil, ErrHTTP3Unavailable
	}
	LogInfo("upstream HTTP/3 session established proxy=%s remote=%s", proxyHost, conn.RemoteAddr())

	rt := &http3.Transport{
		Dial: func(_ context.Context, _ string, _ *tls.Config, _ *quic.Config) (*quic.Conn, error) {
			return conn, nil
		},
	}

	return &h3Session{
		conn:      conn,
		udpConn:   udpConn,
		rt:        rt,
		proxyHost: proxyHost,
		token:     token,
		timeout:   timeout,
		tunnels:   make(map[*tunnelConn]struct{}),
	}, nil
}

func (s *h3Session) IsAlive() bool {
	return s.conn != nil && s.conn.Context().Err() == nil
}

func (s *h3Session) OpenTunnel(authority string) (net.Conn, error) {
	s.inFlight.Add(1)
	defer s.inFlight.Add(-1)

	reqBody := newTunnelWriteBuffer(tunnelWriteBufSize)
	resp, cancel, err := roundTripWithOpenTimeout(s.timeout, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "https://"+authority, reqBody)
		if err != nil {
			return nil, err
		}
		req.Host = authority
		req.URL.Host = s.proxyHost
		req.Header.Set("Proxy-Authorization", "Bearer "+s.bearerToken())
		return s.rt.RoundTrip(req)
	})
	if err != nil {
		reqBody.failRead(io.ErrClosedPipe)
		return nil, s.health.observe(authority, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		cancel()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		reqBody.failRead(io.ErrClosedPipe)
		return nil, s.health.observe(authority, &proxyConnectHTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       strings.TrimSpace(string(body)),
		})
	}
	s.health.observe(authority, nil)

	conn := &tunnelConn{
		reader:  resp.Body,
		writer:  reqBody,
		reqBody: reqBody,
		name:    "h3-connect-stream",
		cancel:  cancel,
		session: s,
	}
	s.trackTunnel(conn)
	return conn, nil
}

func (s *h3Session) trackTunnel(t *tunnelConn) {
	s.tunnelMu.Lock()
	if s.tunnels != nil {
		s.tunnels[t] = struct{}{}
	}
	s.tunnelMu.Unlock()
}

func (s *h3Session) untrackTunnel(t *tunnelConn) {
	s.tunnelMu.Lock()
	delete(s.tunnels, t)
	s.tunnelMu.Unlock()
}

func (s *h3Session) failTunnels(err error) {
	s.tunnelMu.Lock()
	list := make([]*tunnelConn, 0, len(s.tunnels))
	for t := range s.tunnels {
		list = append(list, t)
	}
	s.tunnelMu.Unlock()
	for _, t := range list {
		t.fail(err)
	}
}

func (s *h3Session) bearerToken() string {
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	return s.token
}

func (s *h3Session) UpdateToken(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("empty proxy session token")
	}
	s.tokenMu.Lock()
	s.token = token
	s.tokenMu.Unlock()
	return nil
}

func (s *h3Session) Close() error {
	s.closeOnce.Do(func() {
		s.failTunnels(ErrNoUsableSession)
		if s.rt != nil {
			_ = s.rt.Close()
		}
		if s.conn != nil {
			s.closeErr = s.conn.CloseWithError(0, "")
		}
		if s.udpConn != nil {
			if closeErr := s.udpConn.Close(); s.closeErr == nil {
				s.closeErr = closeErr
			}
		}
	})
	return s.closeErr
}

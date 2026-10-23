package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"todnet/internal/tun"
	"todnet/internal/vpnclient"
	"todnet/internal/win"
)

// State is the high-level connection state shown in the UI.
type State string

const (
	StateDisconnected  State = "disconnected"
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateDisconnecting State = "disconnecting"
	StateError         State = "error"
)

// Location is one selectable VPN location (a city inside a country).
type Location struct {
	CountryName string `json:"countryName"`
	CountryCode string `json:"countryCode"`
	CityName    string `json:"cityName"`
	CityCode    string `json:"cityCode"`
	Host        string `json:"host"`
	// LatencyMs is the measured round-trip time to the edge, in
	// milliseconds. Zero means it has not been measured yet.
	LatencyMs int `json:"latencyMs,omitempty"`
}

// Status is a snapshot of engine state for the UI.
type Status struct {
	State       State      `json:"state"`
	Error       string     `json:"error,omitempty"`
	Location    *Location  `json:"location,omitempty"`
	ExitIP      string     `json:"exitIp,omitempty"`
	ExitCountry string     `json:"exitCountry,omitempty"`
	ExitLatency int64      `json:"exitLatencyMs,omitempty"`
	SocksAddr   string     `json:"socksAddr,omitempty"`
	HTTPAddr    string     `json:"httpAddr,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`
	QuotaMax    int64      `json:"quotaMax,omitempty"`
	QuotaLeft   int64      `json:"quotaLeft,omitempty"`
	QuotaReset  string     `json:"quotaReset,omitempty"`
	Transport   string     `json:"transport,omitempty"`
	// Mode is "proxy" (system proxy / manual proxy settings) or "tun"
	// (a virtual adapter that captures every application).
	Mode        string `json:"mode,omitempty"`
	TunName     string `json:"tunName,omitempty"`
	TunIPv4     string `json:"tunIpv4,omitempty"`
	ActiveConns int64  `json:"activeConns"`
	TotalConns  int64  `json:"totalConns"`
	BytesUp     int64  `json:"bytesUp"`
	BytesDown   int64  `json:"bytesDown"`
	Verifying   bool   `json:"verifying"`
}

// ConnectConfig describes one connection attempt.
type ConnectConfig struct {
	Auth       *Auth
	Location   *Location
	Transport  string // "auto" | "h2" | "h3"
	SocksPort  int
	HTTPPort   int
	VerifyExit bool
	// DirectFallback dials a destination directly when the VPN edge refuses
	// it. Off by default: it is a deliberate leak, but it is the only way to
	// reach services the edge's egress will not carry.
	DirectFallback bool
	// BypassDomains are the host patterns that skip the tunnel entirely. A
	// match is dialled straight out of the machine, so the destination sees
	// the real address and the traffic is not carried by the VPN.
	BypassDomains []string
	// TunMode captures all traffic with a virtual adapter instead of
	// pointing the Windows proxy at the local listener. Needs administrator
	// rights; TUN carries TCP only (see internal/tun).
	TunMode bool
	// Latencies holds the measured edge round-trip times, keyed by edge
	// address. It lets the engine pick the closest edge when no location
	// was requested, which is what makes the automatic choice automatic.
	Latencies map[string]int
}

// Engine owns the live tunnel and the local proxy listeners.
type Engine struct {
	mu         sync.Mutex
	status     Status
	controller *Controller
	socks      *SocksServer
	httpProxy  *HTTPProxyServer
	tun        *tun.Tunnel
	stats      Stats
	gen        int

	// connectCtx is cancelled when the user abandons a connection attempt.
	// Every step of the attempt (the server-list fetch, the Guardian proxy
	// pass, the TLS handshake to the edge) derives its deadline from it, so
	// cancelling really does abort the work rather than merely ignoring its
	// result.
	connectCtx    context.Context
	connectCancel context.CancelFunc
}

// ErrConnectCancelled reports that a connection attempt was abandoned on
// purpose. It is not a failure and the UI asked for it, so callers treat it
// as a normal end rather than as an error.
var ErrConnectCancelled = errors.New("connection attempt cancelled")

// NewEngine returns an idle engine.
func NewEngine() *Engine {
	return &Engine{status: Status{State: StateDisconnected}}
}

// Status returns a snapshot including live counters.
func (e *Engine) Status() Status {
	e.mu.Lock()
	st := e.status
	e.mu.Unlock()
	st.ActiveConns = e.stats.ActiveConns.Load()
	st.TotalConns = e.stats.TotalConns.Load()
	st.BytesUp = e.stats.BytesUp.Load()
	st.BytesDown = e.stats.BytesDown.Load()
	return st
}

func (e *Engine) setState(s State, errMsg string) {
	e.mu.Lock()
	e.status.State = s
	e.status.Error = errMsg
	e.mu.Unlock()
}

// notePassQuota keeps the allowance shown on the home screen current.
//
// Status was assembled once from the first proxy pass, so the ring and the
// reset date froze for the whole session while Guardian's counter kept
// moving. Renewals happen every few minutes and carry fresh X-Quota-*
// headers; copying them here means the card ends up within a renewal of the
// real balance, and follows a monthly rollover instead of showing the old
// reset date.
func (e *Engine) notePassQuota(pass *vpnclient.ProxyPassInfo) {
	if pass == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// A renewal that lands after a disconnect (or during the next attempt)
	// must not write an allowance into a status that no longer describes a
	// live tunnel.
	if e.status.State != StateConnected {
		return
	}
	if pass.QuotaLeft != "" {
		e.status.QuotaLeft = parseQuota(pass.QuotaLeft)
	}
	if max := parseQuota(pass.QuotaMax); max > 0 {
		e.status.QuotaMax = max
	}
	if pass.QuotaReset != "" {
		e.status.QuotaReset = pass.QuotaReset
	}
}

// Disconnect tears down the tunnel and local listeners.
func (e *Engine) Disconnect() error {
	// A disconnect invalidates any connection attempt still in flight. Without
	// this the attempt kept its generation, reached the end of connectInner and
	// installed the tunnel anyway — so pressing "Cancel" produced a connected
	// VPN a moment later.
	e.gen++

	e.mu.Lock()
	if e.connectCancel != nil {
		e.connectCancel()
		e.connectCancel = nil
		e.connectCtx = nil
	}
	if e.controller == nil && e.socks == nil && e.httpProxy == nil && e.tun == nil {
		e.status = Status{State: StateDisconnected}
		e.mu.Unlock()
		return nil
	}
	e.status.State = StateDisconnecting
	controller := e.controller
	socks := e.socks
	httpProxy := e.httpProxy
	tnl := e.tun
	e.controller = nil
	e.socks = nil
	e.httpProxy = nil
	e.tun = nil
	e.mu.Unlock()

	// Order matters. The upstream goes first: closing it makes every
	// in-flight CONNECT fail at once, which is what releases the proxy
	// handlers. Doing the listeners first left them waiting out CONNECT
	// timeouts and rebuild attempts, so the UI sat on "Disconnecting…" for
	// seconds or minutes.
	step := time.Now()
	if tnl != nil {
		_ = tnl.Close()
		LogDebug("disconnect: tunnel down in %s", time.Since(step).Round(time.Millisecond))
		step = time.Now()
	}
	if controller != nil {
		_ = controller.Close()
		LogDebug("disconnect: upstream closed in %s", time.Since(step).Round(time.Millisecond))
		step = time.Now()
	}
	if socks != nil {
		_ = socks.Close()
		LogDebug("disconnect: socks listener closed in %s", time.Since(step).Round(time.Millisecond))
		step = time.Now()
	}
	if httpProxy != nil {
		_ = httpProxy.Close()
		LogDebug("disconnect: http listener closed in %s", time.Since(step).Round(time.Millisecond))
	}

	// Anything the tunnel suppressed belongs in the log once, not never.
	FlushThrottled("while the tunnel was up")

	e.stats.ActiveConns.Store(0)
	e.mu.Lock()
	e.status = Status{State: StateDisconnected}
	e.mu.Unlock()
	LogInfo("disconnected")
	return nil
}

// Connect logs in if needed, fetches a proxy pass, selects the location and
// starts the local proxy listeners.
func (e *Engine) Connect(cfg ConnectConfig) error {
	e.mu.Lock()
	if e.status.State == StateConnected || e.status.State == StateConnecting {
		e.mu.Unlock()
		return fmt.Errorf("already %s", e.status.State)
	}
	e.status = Status{State: StateConnecting, Verifying: cfg.VerifyExit}
	e.gen++
	gen := e.gen
	// The attempt runs under a context the user can cancel, so an abandoned
	// connection stops doing network work instead of finishing in the
	// background and installing a tunnel nobody asked for.
	ctx, cancel := context.WithCancel(context.Background())
	e.connectCtx = ctx
	e.connectCancel = cancel
	e.mu.Unlock()

	LogInfo("connecting")
	if err := e.connectInner(ctx, cfg, gen); err != nil {
		e.clearConnectContext(ctx, cancel)
		if errors.Is(err, ErrConnectCancelled) {
			// Not a failure: the user asked for this. Leave the engine idle
			// and report success so the caller does not raise an error the
			// user would then have to dismiss.
			LogInfo("connection attempt cancelled")
			e.setState(StateDisconnected, "")
			return nil
		}
		LogError("connection failed: %v", err)
		e.setState(StateError, err.Error())
		// Roll back anything that did start.
		e.mu.Lock()
		controller := e.controller
		socks := e.socks
		httpProxy := e.httpProxy
		tnl := e.tun
		e.controller, e.socks, e.httpProxy, e.tun = nil, nil, nil, nil
		e.mu.Unlock()
		if tnl != nil {
			_ = tnl.Close()
		}
		if socks != nil {
			_ = socks.Close()
		}
		if httpProxy != nil {
			_ = httpProxy.Close()
		}
		if controller != nil {
			_ = controller.Close()
		}
		return err
	}
	// The attempt's context is deliberately NOT cancelled here. The
	// controller's context is independent, but the controller's first
	// session was dialed under this context, and cancelling it here would
	// take the live session with it. The context is released when the
	// controller closes.
	e.clearConnectContext(ctx, cancel)
	return nil
}

// clearConnectContext forgets the in-flight attempt's context so a later
// Cancel cannot reach a finished attempt.
//
// cancel is only called on the failure path. On success the attempt's
// context is left alone, because the session that was dialed under it
// has to outlive the attempt.
func (e *Engine) clearConnectContext(ctx context.Context, cancel context.CancelFunc) {
	e.mu.Lock()
	if e.connectCtx == ctx {
		e.connectCancel = nil
		e.connectCtx = nil
	}
	e.mu.Unlock()
	cancel()
}

// Cancel abandons a connection attempt that is still running.
//
// The attempt is aborted in two ways at once: the generation counter is
// bumped so connectInner's cancellation check trips, and the attempt's
// context is cancelled so the network work in flight stops immediately.
// Bumping the generation alone would let the attempt run to completion and
// install the tunnel; cancelling the context alone would let connectInner
// continue past its own check.
func (e *Engine) Cancel() error {
	e.mu.Lock()
	if e.status.State != StateConnecting {
		e.mu.Unlock()
		return nil
	}
	e.gen++
	cancel := e.connectCancel
	e.connectCancel = nil
	e.connectCtx = nil
	e.status = Status{State: StateDisconnected}
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	LogInfo("connection attempt cancelled by the user")
	return nil
}

// Cancelling reports whether a connection attempt is currently in flight.
func (e *Engine) Cancelling() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status.State == StateConnecting
}

func (e *Engine) connectInner(ctx context.Context, cfg ConnectConfig, gen int) error {
	if cfg.Auth == nil || cfg.Auth.AccessToken == "" {
		return errors.New("not signed in")
	}
	started := time.Now()

	// cancelled reports that the user abandoned the attempt. It is checked at
	// every step boundary so the attempt stops as early as possible instead of
	// running to completion and then discovering it was cancelled.
	cancelled := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrConnectCancelled, err)
		}
		return nil
	}

	// Resolve the edge to use.
	location := cfg.Location
	if location == nil || location.Host == "" {
		LogInfo("no location selected; fetching server list")
		listCtx, cancel := context.WithTimeout(ctx, apiRequestTimeout)
		countries, err := vpnclient.FetchServerList(listCtx)
		cancel()
		if err != nil {
			if cerr := cancelled(); cerr != nil {
				return cerr
			}
			return fmt.Errorf("fetching server list: %w", err)
		}
		picked, err := PickLocationWithLatency(countries, "", "", cfg.Latencies)
		if err != nil {
			return err
		}
		location = picked
	}

	proxyURL, err := normalizeProxyURL(location.Host)
	if err != nil {
		return fmt.Errorf("invalid proxy address %q: %w", location.Host, err)
	}
	useH3 := resolveTransport(cfg.Transport, proxyURL)
	LogInfo("selected location country=%s city=%s edge=%s transport=%s mode=%s",
		location.CountryName, location.CityName, location.Host, transportName(useH3), modeName(cfg.TunMode))
	LogInfo("connecting: socks=%d http=%d verifyExit=%t directFallback=%t elevation=%s",
		cfg.SocksPort, cfg.HTTPPort, cfg.VerifyExit, cfg.DirectFallback, elevationText())

	if err := cancelled(); err != nil {
		return err
	}

	// Resolve the edge once, now, while no tunnel exists. The addresses are
	// used both to dial the session (so a rebuild never needs a name lookup)
	// and to pin the edge outside the tunnel.
	edgeIPs := resolveEdgeIPs(proxyURL.Hostname())

	// Fetch a proxy pass.
	passStarted := time.Now()
	passCtx, passCancel := context.WithTimeout(ctx, apiRequestTimeout)
	pass, err := vpnclient.FetchProxyPass(passCtx, vpnclient.GuardianEndpointDefault, cfg.Auth.AccessToken)
	passCancel()
	if errors.Is(err, vpnclient.ErrTokenInvalid) {
		LogInfo("Guardian rejected the access token; activating account")
		actx, acancel := context.WithTimeout(ctx, apiRequestTimeout)
		_, activateErr := vpnclient.ActivateGuardian(actx, vpnclient.GuardianEndpointDefault, cfg.Auth.AccessToken)
		acancel()
		if activateErr != nil {
			if cerr := cancelled(); cerr != nil {
				return cerr
			}
			return fmt.Errorf("activating Firefox VPN on this account: %w", activateErr)
		}
		passCtx, passCancel = context.WithTimeout(ctx, apiRequestTimeout)
		pass, err = vpnclient.FetchProxyPass(passCtx, vpnclient.GuardianEndpointDefault, cfg.Auth.AccessToken)
		passCancel()
	}
	if err != nil {
		if cerr := cancelled(); cerr != nil {
			return cerr
		}
		if errors.Is(err, vpnclient.ErrQuotaExceeded) {
			return errors.New("monthly VPN data allowance is used up; it resets at the start of your billing month")
		}
		return fmt.Errorf("fetching proxy pass: %w", err)
	}
	if cerr := cancelled(); cerr != nil {
		return cerr
	}
	LogInfo("proxy pass obtained in %s (expires %s, quota %s of %s left)",
		time.Since(passStarted).Round(time.Millisecond),
		pass.ExpiresAt().Format(time.RFC3339),
		humanBytes(parseQuota(pass.QuotaLeft)), humanBytes(parseQuota(pass.QuotaMax)))

	controller, err := NewController(ControllerConfig{
		Guardian:      vpnclient.GuardianEndpointDefault,
		ProxyURL:      proxyURL,
		Timeout:       30 * time.Second,
		Auth:          cfg.Auth,
		Pass:          pass,
		UseH3:         useH3,
		AllowFallback: cfg.Transport == "auto",
		EdgeIPs:       edgeIPs,
		DialContext:   ctx,
		OnPass:        e.notePassQuota,
	})
	if err != nil {
		if cerr := cancelled(); cerr != nil {
			return cerr
		}
		// The raw error is what makes a report diagnosable; the message the
		// user sees is a summary of it.
		LogError("connecting to the VPN edge failed after %s: %v",
			time.Since(started).Round(time.Millisecond), err)
		switch {
		case errors.Is(err, ErrHTTP2Unavailable):
			return errors.New("this server only accepts HTTP/3 (QUIC); set Transport to Auto or HTTP/3")
		case errors.Is(err, ErrHTTP3Unavailable):
			return errors.New("this server did not negotiate HTTP/3; try another location")
		case isHandshakeRefused(err):
			return errors.New("the VPN server closed the connection during the TLS handshake. " +
				"Your network may be filtering it, or the server is refusing this client — try another location")
		case isTimeout(err):
			if useH3 {
				return errors.New("the QUIC handshake timed out — your network may be blocking UDP. Try another network or location")
			}
			return errors.New("the connection to the VPN server timed out; try another location")
		default:
			return fmt.Errorf("connecting to the VPN edge: %w", err)
		}
	}
	LogInfo("edge session ready in %s (%s)", time.Since(started).Round(time.Millisecond), transportName(useH3))

	socksLn, err := bindLoopback(cfg.SocksPort, "SOCKS5")
	if err != nil {
		_ = controller.Close()
		return fmt.Errorf("starting SOCKS5 proxy: %w", err)
	}
	dialer := &tunnelDialer{
		controller: controller,
		direct:     cfg.DirectFallback,
		bypass:     ParseBypassList(cfg.BypassDomains),
	}
	// While the tunnel's own adapter is up, a connection that leaves the
	// machine has to be pinned to the physical interface: the adapter
	// holds a default route, so anything unpinned is routed straight back
	// into the tunnel.
	if cfg.TunMode {
		dialer.dialDirect = tun.PhysicalDial()
	}
	socks := NewSocksServerOn(socksLn, dialer, &e.stats)

	httpLn, err := bindLoopback(cfg.HTTPPort, "HTTP")
	if err != nil {
		_ = socks.Close()
		_ = controller.Close()
		return fmt.Errorf("starting HTTP proxy: %w", err)
	}
	httpProxy := NewHTTPProxyServerOn(httpLn, dialer, &e.stats)

	// TUN comes last: once the adapter is up, every packet on the machine
	// depends on this controller being alive.
	var tnl *tun.Tunnel
	if cfg.TunMode {
		tnl, err = e.startTun(controller, dialer, edgeIPs, dialer.bypass)
		if err != nil {
			_ = httpProxy.Close()
			_ = socks.Close()
			_ = controller.Close()
			return err
		}
	}

	expiry := pass.ExpiresAt()
	now := time.Now()

	e.mu.Lock()
	// Two independent things have to be true before the tunnel may be
	// installed: the generation must still match (nothing disconnected or
	// cancelled in the meantime) and the attempt must not have been cancelled.
	if e.gen != gen {
		e.mu.Unlock()
		if tnl != nil {
			_ = tnl.Close()
		}
		_ = socks.Close()
		_ = httpProxy.Close()
		_ = controller.Close()
		if cerr := cancelled(); cerr != nil {
			return cerr
		}
		return errors.New("connection attempt was cancelled")
	}
	if err := ctx.Err(); err != nil {
		e.mu.Unlock()
		if tnl != nil {
			_ = tnl.Close()
		}
		_ = socks.Close()
		_ = httpProxy.Close()
		_ = controller.Close()
		return fmt.Errorf("%w: %v", ErrConnectCancelled, err)
	}
	e.controller = controller
	e.socks = socks
	e.httpProxy = httpProxy
	e.tun = tnl
	mode := "proxy"
	e.status = Status{
		State:       StateConnected,
		Location:    location,
		SocksAddr:   socks.Addr().String(),
		HTTPAddr:    httpProxy.Addr().String(),
		ExpiresAt:   &expiry,
		ConnectedAt: &now,
		QuotaMax:    parseQuota(pass.QuotaMax),
		QuotaLeft:   parseQuota(pass.QuotaLeft),
		QuotaReset:  pass.QuotaReset,
		Transport:   transportName(useH3),
		Verifying:   cfg.VerifyExit,
		Mode:        "proxy",
	}
	if tnl != nil {
		mode = "tun"
		e.status.Mode = mode
		e.status.TunName = tnl.Name()
		e.status.TunIPv4 = tnl.IPv4()
	}
	e.mu.Unlock()

	LogInfo("connected via %s (%s), mode=%s SOCKS5=%s HTTP=%s (setup %s)",
		location.CountryName, location.CityName, mode, socks.Addr(), httpProxy.Addr(),
		time.Since(started).Round(time.Millisecond))

	go controller.RunRenewal()

	if cfg.VerifyExit {
		go e.verifyExit(controller, gen)
	}
	return nil
}

func transportName(useH3 bool) string {
	if useH3 {
		return "HTTP/3"
	}
	return "HTTP/2"
}

func modeName(tunMode bool) string {
	if tunMode {
		return "tun"
	}
	return "proxy"
}

// elevationText is logged with every connect: "TUN mode needs administrator
// rights" is by far the most common reason a connect does nothing, and it is
// invisible in a log that does not say whether the process is elevated.
func elevationText() string {
	if win.IsElevated() {
		return "elevated"
	}
	return "not elevated"
}

// humanBytes keeps quota lines readable in the log.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// startTun brings up the virtual adapter and points every application at the
// existing upstream session.
func (e *Engine) startTun(controller *Controller, opener TunnelOpener, edgeIPs []net.IP, bypass *BypassList) (*tun.Tunnel, error) {
	// Per-query DNS detail is the only way to see "the relay answered, and it
	// answered the wrong thing". It is verbose-only, so it costs nothing when
	// verbose logging is off.
	tun.SetDebugLogger(LogDebug)

	// The edge must stay reachable outside the tunnel, or the tunnel would
	// carry its own packets. These are the same addresses the session dials,
	// resolved before the adapter existed.
	exclude := edgeIPs
	if len(exclude) == 0 {
		exclude = resolveEdgeIPs(controller.EdgeHost())
	}

	opts := tun.Options{
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			switch network {
			case "tcp", "tcp4", "tcp6":
			default:
				return nil, fmt.Errorf("%s is not carried by this tunnel", network)
			}
			return openTunnelContext(ctx, opener, address)
		},
		// A bypassed host leaves the machine without the tunnel. The stack
		// only knows the name for TLS, so a plain HTTP connection there
		// cannot be matched.
		Bypass:     bypass.Matches,
		DialDirect: tun.PhysicalDial(),
		ExcludeIPs: exclude,
		Stats:      &e.stats,
		Logf:       LogInfo,
		Warnf:      LogWarn,
		// Hundreds of connections a minute share one failing destination;
		// without this the log fills with copies of the same line.
		WarnThrottled: func(key, format string, args ...any) {
			LogThrottled(LevelWarn, key, format, args...)
		},
	}
	if len(exclude) > 0 {
		LogInfo("tun: pinning %d edge address(es) outside the tunnel", len(exclude))
	}
	tnl, err := tun.Start(opts)
	if err != nil {
		return nil, err
	}
	return tnl, nil
}

// tunnelOpener is the part of the controller the dialer needs. It is an
// interface so the dialer can be tested without an upstream edge.
type tunnelOpener interface {
	OpenTunnel(authority string) (net.Conn, error)
}

// tunnelDialer is what the proxy servers and the TUN stack dial through.
type tunnelDialer struct {
	controller tunnelOpener
	direct     bool
	bypass     *BypassList
	// dialDirect opens a connection that leaves the machine without the
	// tunnel. It is set when the tunnel's own adapter is up: the adapter
	// holds a default route, so a socket that is not pinned to the
	// physical interface would be routed straight back into the tunnel.
	dialDirect func(ctx context.Context, network, address string) (net.Conn, error)
}

func (d *tunnelDialer) OpenTunnel(authority string) (net.Conn, error) {
	if d.bypass.Matches(authority) {
		return d.openDirect(authority, "bypassed")
	}
	conn, err := d.controller.OpenTunnel(authority)
	if err == nil || !d.direct || !edgeRefused(err) {
		return conn, err
	}
	LogWarn("the VPN server refused %s (%v); connecting directly", authority, err)
	return d.openDirect(authority, "refused")
}

// openDirect dials a destination without the tunnel.
func (d *tunnelDialer) openDirect(authority, why string) (net.Conn, error) {
	LogInfo("not carrying %s through the tunnel (%s): connecting directly", authority, why)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if d.dialDirect != nil {
		return d.dialDirect(ctx, "tcp", authority)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", authority)
}

// edgeRefused reports whether the edge answered CONNECT with a gateway error,
// which is what it returns when its own egress cannot reach the destination.
func edgeRefused(err error) bool {
	var connectErr *proxyConnectHTTPError
	if !errors.As(err, &connectErr) {
		return false
	}
	return connectErr.StatusCode == http.StatusBadGateway
}

// openTunnelContext gives OpenTunnel a context, so a cancelled client request
// does not leave a tunnel opening in the background.
func openTunnelContext(ctx context.Context, opener TunnelOpener, address string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := opener.OpenTunnel(address)
		ch <- result{conn, err}
	}()

	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		// The dial may still complete; make sure the tunnel does not leak.
		go func() {
			if r := <-ch; r.conn != nil {
				_ = r.conn.Close()
			}
		}()
		return nil, ctx.Err()
	}
}

// resolveEdgeIPs turns the edge hostname into the addresses that must keep
// using the physical interface.
//
// IPv4 comes first: the tunnel adapter is IPv4 only, so an IPv6 edge address
// cannot be pinned and would be dialled over a path the tunnel does not
// manage. v4 is tried first, v6 stays as a fallback.
func resolveEdgeIPs(host string) []net.IP {
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		LogWarn("could not resolve the edge host %s; TUN mode will pin no exclusion route: %v", host, err)
		return nil
	}
	sort.SliceStable(ips, func(i, j int) bool {
		return ips[i].To4() != nil && ips[j].To4() == nil
	})
	return ips
}

// isTimeout reports whether err is a network timeout, including the
// "no recent network activity" errors quic-go raises.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "no recent network activity")
}

// isHandshakeRefused reports that TCP connected but the peer closed before
// the TLS handshake finished. That is a different problem from a timeout: the
// server is reachable and is deliberately refusing, which usually means the
// network is filtering the edge hostname or the edge is rejecting this
// client. Reporting it as "timed out" sends people looking for the wrong fix.
func isHandshakeRefused(err error) bool {
	if err == nil {
		return false
	}
	// Deliberately checked before the timeout case: when one transport is
	// refused outright and the other times out, the refusal is the more
	// specific and more actionable fact.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "forcibly closed")
}

// resolveTransport decides which protocol to speak to the edge first.
//
// Mozilla's current server list publishes only MASQUE endpoints
// (*.m1.fastly-masque.net on port 2499). Those ports serve QUIC *and* HTTP/2
// over TCP on the same port, with a valid certificate for the edge hostname —
// verified with a TLS probe that negotiates "h2" on 2499. TCP is the better
// default: it survives the many networks that drop or rate-limit UDP, which is
// what made every connect time out when this defaulted to HTTP/3.
//
// "h3" forces QUIC, "h2" forces TCP. In auto mode the controller falls back to
// the other transport if the first one cannot be established.
func resolveTransport(pref string, proxyURL *url.URL) bool {
	return pref == "h3"
}

// bindLoopback binds the preferred port, or the next free one when it is taken.
// Other proxy software on the machine very often owns 8080 or 1080, and
// failing the whole connect with a raw Windows socket error is a bad answer.
func bindLoopback(port int, label string) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		return ln, nil
	}
	if !isAddrInUse(err) {
		return nil, err
	}
	for p := port + 1; p <= port+20 && p < 65536; p++ {
		retry, retryErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if retryErr == nil {
			LogWarn("%s proxy port %d is already in use; using %d instead", label, port, p)
			return retry, nil
		}
	}
	return nil, fmt.Errorf("port %d is in use by another program and no nearby port was free", port)
}

func isAddrInUse(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "only one usage") ||
		strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "wsaeaddrinuse")
}

func parseQuota(raw string) int64 {
	var v int64
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &v); err != nil {
		return 0
	}
	return v
}

// verifyExit probes the data path to confirm the real public exit.
func (e *Engine) verifyExit(controller *Controller, gen int) {
	const probeURL = "https://www.cloudflare.com/cdn-cgi/trace"

	transport := &http.Transport{
		Proxy:               nil,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, _, authority string) (net.Conn, error) {
			type result struct {
				conn net.Conn
				err  error
			}
			ch := make(chan result, 1)
			go func() {
				c, err := controller.OpenTunnel(authority)
				ch <- result{c, err}
			}()
			select {
			case r := <-ch:
				return r.conn, r.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}
	started := time.Now()
	resp, err := client.Get(probeURL)
	if err != nil {
		LogWarn("exit verification failed: %v", err)
		e.mu.Lock()
		e.status.Verifying = false
		e.mu.Unlock()
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	latency := time.Since(started)

	ip, country := parseTrace(body)
	if ip == "" {
		LogWarn("exit verification returned no IP")
		e.mu.Lock()
		e.status.Verifying = false
		e.mu.Unlock()
		return
	}

	e.mu.Lock()
	if e.gen == gen {
		e.status.ExitIP = ip
		e.status.ExitCountry = country
		e.status.ExitLatency = latency.Milliseconds()
		e.status.Verifying = false
	}
	e.mu.Unlock()
	LogInfo("exit verified ip=%s country=%s latency=%s", ip, country, latency.Round(time.Millisecond))
}

func parseTrace(body []byte) (string, string) {
	fields := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			fields[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
	if ip := fields["ip"]; ip != "" {
		return ip, strings.ToUpper(fields["loc"])
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		ip, _ := payload["ip"].(string)
		if ip == "" {
			ip, _ = payload["query"].(string)
		}
		country, _ := payload["country_code"].(string)
		if country == "" {
			country, _ = payload["countryCode"].(string)
		}
		return ip, strings.ToUpper(country)
	}
	return "", ""
}

func normalizeProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty proxy value")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Host == "" {
		return nil, errors.New("missing proxy host")
	}
	return parsed, nil
}

// BuildLocations flattens the Remote Settings country list into selectable
// city-level entries with their CONNECT edge address.
func BuildLocations(countries []vpnclient.Country) []Location {
	var out []Location
	seenHost := map[string]bool{}
	for _, country := range countries {
		// Mozilla's list carries an anycast catch-all filed under the US. It
		// points at the same edge as the "recommended" entry, so offering it
		// as a US location would promise an exit country it cannot deliver.
		if strings.Contains(strings.ToLower(country.Name), "catchall") {
			continue
		}
		for _, city := range country.Cities {
			host := cityEdge(city)
			if host == "" {
				continue
			}
			// One row per endpoint: duplicates are the same destination.
			if seenHost[host] {
				continue
			}
			seenHost[host] = true
			name, cityName := displayNames(country.Code, country.Name, city.Name)
			out = append(out, Location{
				CountryName: name,
				CountryCode: country.Code,
				CityName:    cityName,
				CityCode:    city.Code,
				Host:        host,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CountryName != out[j].CountryName {
			return out[i].CountryName < out[j].CountryName
		}
		return out[i].CityName < out[j].CityName
	})
	return out
}

// recommendedCode is Mozilla's catch-all "pick the fastest edge" entry. Their
// own server list spells it "Recomended Location"/"Recomended City", which is
// not something to show a user, so it is renamed here.
const recommendedCode = "REC"

func displayNames(code, country, city string) (string, string) {
	if strings.EqualFold(code, recommendedCode) {
		return "Recommended", "Fastest available"
	}
	return country, city
}

// cityEdge returns the first usable CONNECT endpoint for a city.
func cityEdge(city vpnclient.City) string {
	for _, srv := range city.Servers {
		if srv.Quarantined {
			continue
		}
		for _, proto := range srv.Protocols {
			if proto.Name == "connect" && proto.Host != "" && proto.Port > 0 {
				return fmt.Sprintf("%s:%d", proto.Host, proto.Port)
			}
		}
		if len(srv.Protocols) == 0 && srv.Hostname != "" && srv.Port > 0 {
			return fmt.Sprintf("%s:%d", srv.Hostname, srv.Port)
		}
	}
	return ""
}

// PickLocation chooses a location by country code and optional city code.
// An empty filter picks the location with the lowest measured latency, or
// the first available one when nothing has been measured yet.
func PickLocation(countries []vpnclient.Country, countryCode, cityCode string) (*Location, error) {
	return PickLocationWithLatency(countries, countryCode, cityCode, nil)
}

// PickLocationWithLatency is PickLocation with the measured edge latencies
// taken into account.
//
// The "Recommended" entry used to claim the app chose the fastest edge
// while the code simply took the first match in an alphabetical list.
func PickLocationWithLatency(countries []vpnclient.Country, countryCode, cityCode string, latencies map[string]int) (*Location, error) {
	locs := BuildLocations(countries)
	for i := range locs {
		if ms, ok := latencies[locs[i].Host]; ok {
			locs[i].LatencyMs = ms
		}
	}
	if len(locs) == 0 {
		return nil, errors.New("the VPN server list is empty")
	}

	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	cityCode = strings.ToLower(strings.TrimSpace(cityCode))

	var countryMatches []Location
	for _, l := range locs {
		if countryCode != "" && !strings.EqualFold(l.CountryCode, countryCode) {
			continue
		}
		countryMatches = append(countryMatches, l)
	}
	if len(countryMatches) == 0 {
		return nil, fmt.Errorf("no VPN server available for %q", countryCode)
	}
	if cityCode != "" {
		for _, l := range countryMatches {
			if strings.EqualFold(l.CityCode, cityCode) {
				return &l, nil
			}
		}
	}
	// No city was asked for: take the closest edge.
	chosen := FastestLocation(countryMatches)
	return chosen, nil
}

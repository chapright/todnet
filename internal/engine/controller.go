package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"todnet/internal/vpnclient"
)

const (
	proxyPassRenewLead    = 2 * time.Minute
	proxyPassRetryDelay   = 30 * time.Second
	proxyPassRenewTimeout = 2 * time.Minute
	// A pass this close to expiry is not worth rebuilding a connection with:
	// the new session would be refused moments later.
	proxyPassRebuildMargin = 60 * time.Second
	oauthRefreshLead       = 2 * time.Minute
	apiRequestTimeout      = 20 * time.Second
	maxRebuildRetries      = 3
)

// Auth is the OAuth state used for Guardian calls.
type Auth struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	ObtainedAt   time.Time
}

// Valid reports whether the access token can still be used, keeping a safety
// margin so a token never expires mid-request.
func (a *Auth) Valid(now time.Time) bool {
	if a == nil || a.AccessToken == "" {
		return false
	}
	expiry := a.ObtainedAt.Add(time.Duration(a.ExpiresIn) * time.Second)
	return now.Before(expiry.Add(-oauthRefreshLead))
}

func (a *Auth) clone() *Auth {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}

// ControllerConfig configures a tunnel controller.
type ControllerConfig struct {
	Guardian string
	ProxyURL *url.URL
	Timeout  time.Duration
	Auth     *Auth
	Pass     *vpnclient.ProxyPassInfo
	UseH3    bool
	// AllowFallback lets the controller try the other transport when the
	// preferred one cannot be established. Set for "auto"; a user who forces
	// HTTP/2 or HTTP/3 gets exactly that, so a failure is reported honestly.
	AllowFallback bool
	// EdgeIPs are the edge addresses resolved before any tunnel existed.
	// Reusing them keeps a session rebuild from needing a name lookup, which
	// in TUN mode would go through the tunnel that is being rebuilt.
	EdgeIPs []net.IP
	// DialContext is the context the first session is established under.
	// It is the connection attempt's context, so cancelling the attempt
	// aborts the TLS handshake to the edge instead of waiting for its
	// timeout.
	//
	// It is used for that handshake and nothing else. The controller's own
	// context is deliberately independent: the session has to outlive the
	// attempt that created it, and every later renewal and every session
	// rebuild runs under the controller's context.
	DialContext context.Context
	// OnPass, if set, is called after every successful renewal with the new
	// proxy pass. Its X-Quota-* headers are the only live allowance numbers
	// the app receives, so this is what keeps the home screen from freezing
	// the balance at connect time.
	//
	// It runs on the renewal goroutine, after the controller's locks are
	// released, and must not block.
	OnPass func(*vpnclient.ProxyPassInfo)
}

type managedSession struct {
	session   Session
	rawToken  string
	expiresAt time.Time
	refs      int
	accepting bool
}

// Controller owns the live upstream session and renews the proxy pass before
// it expires. It implements Session so it can be handed to the proxy servers.
type Controller struct {
	guardian string
	proxyURL *url.URL
	timeout  time.Duration
	useH3    bool
	fallback bool
	edgeIPs  []net.IP

	renewMu sync.Mutex
	mu      sync.Mutex
	auth    *Auth
	current *managedSession
	closed  bool
	done    chan struct{}
	onPass  func(*vpnclient.ProxyPassInfo)

	// ctx is cancelled by Close. Every network call the controller makes on
	// its own behalf (Guardian, FxA) runs under it, so a disconnect aborts a
	// renewal that is already in flight instead of waiting out its timeouts.
	ctx    context.Context
	cancel context.CancelFunc

	closeOnce sync.Once
	closeErr  error
}

// newController builds a controller without dialling.
//
// It exists so the context wiring can be tested on its own: the whole
// point of the separate context is that it is easy to get wrong and
// impossible to notice until every renewal starts failing.
func newController(cfg ControllerConfig) *Controller {
	// The controller's context is its own. It must not be a child of the
	// connection attempt's context: the attempt ends as soon as the tunnel
	// is up, and cancelling it would take the live session with it.
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		guardian: cfg.Guardian,
		proxyURL: cfg.ProxyURL,
		timeout:  cfg.Timeout,
		useH3:    cfg.UseH3,
		fallback: cfg.AllowFallback,
		edgeIPs:  append([]net.IP(nil), cfg.EdgeIPs...),
		auth:     cfg.Auth.clone(),
		done:     make(chan struct{}),
		onPass:   cfg.OnPass,
		ctx:      ctx,
		cancel:   cancel,
	}
}

// NewController establishes the first upstream session.
func NewController(cfg ControllerConfig) (*Controller, error) {
	c := newController(cfg)

	// The first session is established under the attempt's context, so a
	// cancelled attempt does not wait out the handshake.
	dialCtx := cfg.DialContext
	if dialCtx == nil {
		dialCtx = c.ctx
	}
	session, err := c.newSession(dialCtx, cfg.Pass.RawToken)
	if err != nil {
		c.cancel()
		return nil, err
	}
	c.current = &managedSession{
		session:   session,
		rawToken:  cfg.Pass.RawToken,
		expiresAt: cfg.Pass.ExpiresAt(),
		accepting: true,
	}
	return c, nil
}

// sessionDialError keeps both transport failures reachable through errors.Is
// while still reading as one sentence. The old %v formatting meant a caller
// checking for ErrHTTP2Unavailable or a timeout could never match, so every
// failure fell through to a generic message.
type sessionDialError struct {
	preferredName string
	preferred     error
	fallbackName  string
	fallback      error
}

func (e *sessionDialError) Error() string {
	return fmt.Sprintf("%s failed: %v; %s also failed: %v",
		e.preferredName, e.preferred, e.fallbackName, e.fallback)
}

func (e *sessionDialError) Unwrap() []error { return []error{e.preferred, e.fallback} }

func (c *Controller) newSession(ctx context.Context, token string) (Session, error) {
	session, err := c.dialSession(ctx, c.useH3, token)
	if err == nil {
		return session, nil
	}
	if !c.fallback {
		return nil, err
	}
	// Auto mode: the edge may refuse one transport while accepting the other.
	LogWarn("edge did not accept %s (%v); retrying with %s",
		transportName(c.useH3), err, transportName(!c.useH3))
	session, retryErr := c.dialSession(ctx, !c.useH3, token)
	if retryErr != nil {
		return nil, &sessionDialError{
			preferredName: transportName(c.useH3),
			preferred:     err,
			fallbackName:  transportName(!c.useH3),
			fallback:      retryErr,
		}
	}
	// Remember what worked so renewals do not repeat the failed attempt.
	c.mu.Lock()
	c.useH3 = !c.useH3
	c.mu.Unlock()
	return session, nil
}

func (c *Controller) dialSession(ctx context.Context, h3 bool, token string) (Session, error) {
	if h3 {
		return newH3Session(ctx, c.proxyURL, token, c.timeout, c.edgeIPs)
	}
	return newH2Session(ctx, c.proxyURL, token, c.timeout, c.edgeIPs)
}

// OpenTunnel opens a CONNECT tunnel through the current upstream session,
// rebuilding the session if it turns out to be dead.
func (c *Controller) OpenTunnel(authority string) (net.Conn, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRebuildRetries; attempt++ {
		// A disconnect can arrive while a client is waiting for a CONNECT.
		// Returning here keeps the proxy handler from blocking on a rebuild
		// that has nothing left to rebuild into.
		if c.isClosed() {
			return nil, ErrNoUsableSession
		}
		ms, release, err := c.acquire()
		if err != nil {
			if c.isClosed() {
				return nil, err
			}
			if !errors.Is(err, ErrNoUsableSession) || lastErr == nil {
				lastErr = err
			}
		} else if !sessionAlive(ms.session) {
			lastErr = ErrSessionUnhealthy
			c.markUnusable(ms)
			release()
		} else if sessionFull(ms.session) {
			// At the edge's stream limit, not broken. Retire it from new work
			// so a second connection can be opened, but leave it alone: the
			// streams it is carrying are healthy and closing it would kill
			// them for no reason.
			lastErr = ErrSessionUnhealthy
			c.markSaturated(ms)
			release()
		} else {
			conn, err := ms.session.OpenTunnel(authority)
			if err == nil {
				return &managedConn{Conn: conn, release: release}, nil
			}
			if !shouldRebuild(err) {
				release()
				return nil, err
			}
			lastErr = err
			c.markUnusable(ms)
			release()
		}

		if attempt == maxRebuildRetries {
			break
		}
		LogWarn("upstream tunnel open failed; rebuilding session attempt=%d/%d err=%v", attempt+1, maxRebuildRetries, lastErr)
		if err := c.rebuild(lastErr); err != nil {
			lastErr = fmt.Errorf("rebuilding upstream proxy session: %w", err)
		}
	}
	return nil, lastErr
}

func (c *Controller) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Controller) acquire() (*managedSession, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, nil, ErrNoUsableSession
	}
	if c.current == nil || !c.current.accepting {
		return nil, nil, ErrNoUsableSession
	}
	if time.Now().After(c.current.expiresAt) {
		c.current.accepting = false
		c.maybeCloseLocked(c.current)
		return nil, nil, ErrNoUsableSession
	}

	ms := c.current
	ms.refs++
	return ms, func() { c.release(ms) }, nil
}

func (c *Controller) release(ms *managedSession) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ms.refs--
	c.maybeCloseLocked(ms)
}

// markUnusable retires a session that can no longer serve tunnels.
//
// It is closed immediately rather than left to drain. Draining sounds gentler
// but is what made a broken upstream hang for half a minute: the session stayed
// open while its streams waited for the operating system's TCP timer, and the
// client sat on a socket that was never going to answer. A session that cannot
// serve new tunnels has nothing left worth keeping — its streams are already
// dead or about to be — so closing it turns a 25-second stall into an immediate
// error the client can retry.
//
// Close runs outside c.mu on purpose: closing an h2 connection waits for its
// transport to unwind, and the handler releasing the last tunnel reference
// needs the same lock.
func (c *Controller) markUnusable(ms *managedSession) {
	c.retire(ms, true)
}

// markSaturated retires a session that is merely out of stream capacity. It
// stops taking new work but keeps its existing streams, and is closed once the
// last of them is released.
func (c *Controller) markSaturated(ms *managedSession) {
	c.retire(ms, false)
}

func (c *Controller) retire(ms *managedSession, closeNow bool) {
	c.mu.Lock()
	if ms == nil || c.current != ms {
		c.mu.Unlock()
		return
	}
	ms.accepting = false
	var session Session
	if closeNow {
		session = ms.session
		ms.session = nil
	}
	c.mu.Unlock()

	if session != nil {
		_ = session.Close()
	}
}

func (c *Controller) maybeCloseLocked(ms *managedSession) {
	if ms == nil || ms.accepting || ms.refs > 0 || ms.session == nil {
		return
	}
	_ = ms.session.Close()
	ms.session = nil
}

func (c *Controller) swap(session Session, rawToken string, expiresAt time.Time) bool {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = session.Close()
		return false
	}
	old := c.current
	c.current = &managedSession{
		session:   session,
		rawToken:  rawToken,
		expiresAt: expiresAt,
		accepting: true,
	}
	if old != nil {
		old.accepting = false
		c.maybeCloseLocked(old)
	}
	c.mu.Unlock()
	return true
}

func (c *Controller) currentExpiry() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil {
		return time.Time{}
	}
	return c.current.expiresAt
}

// ExpiresAt reports when the current proxy pass stops being accepted.
func (c *Controller) ExpiresAt() time.Time { return c.currentExpiry() }

// RunRenewal starts the background renewal loop and blocks until the
// controller is closed.
func (c *Controller) RunRenewal() {
	for {
		if c.isClosed() {
			return
		}
		sleep := c.nextRenewalDelay()
		LogInfo("proxy pass renewal scheduled next_expiry=%s delay=%s",
			c.currentExpiry().Format(time.RFC3339), sleep.Round(time.Second))
		if !c.sleepOrDone(sleep) {
			return
		}
		LogInfo("proxy pass renewal starting")
		if err, timedOut := c.renewWithTimeout(proxyPassRenewTimeout); timedOut {
			LogError("proxy pass renewal timed out after %s", proxyPassRenewTimeout)
			c.disableExpired()
			if !c.sleepOrDone(proxyPassRetryDelay) {
				return
			}
			continue
		} else if err != nil {
			LogWarn("proxy pass renewal failed: %v", err)
			c.disableExpired()
			if !c.sleepOrDone(proxyPassRetryDelay) {
				return
			}
			continue
		}
	}
}

func (c *Controller) disableExpired() {
	c.mu.Lock()
	changed := false
	if c.current != nil && c.current.accepting && !time.Now().Before(c.current.expiresAt) {
		c.current.accepting = false
		c.maybeCloseLocked(c.current)
		changed = true
	}
	c.mu.Unlock()
	if changed {
		LogWarn("proxy pass expired before it could be renewed")
	}
}

func (c *Controller) renewWithTimeout(timeout time.Duration) (error, bool) {
	resultCh := make(chan error, 1)
	go func() { resultCh <- c.renew() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-resultCh:
		return err, false
	case <-timer.C:
		return nil, true
	case <-c.done:
		return nil, false
	}
}

func (c *Controller) sleepOrDone(d time.Duration) bool {
	if d <= 0 {
		select {
		case <-c.done:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-c.done:
		return false
	}
}

func (c *Controller) nextRenewalDelay() time.Duration {
	expiry := c.currentExpiry()
	if expiry.IsZero() {
		return proxyPassRetryDelay
	}
	delay := time.Until(expiry.Add(-proxyPassRenewLead))
	if delay < 0 {
		return 0
	}
	return delay
}

func (c *Controller) renew() error {
	c.renewMu.Lock()
	defer c.renewMu.Unlock()
	return c.renewLocked()
}

func (c *Controller) rebuild(cause error) error {
	c.renewMu.Lock()
	defer c.renewMu.Unlock()

	// Rebuild with the pass the session already holds, without asking
	// Guardian for a new one.
	//
	// This is the difference between a tunnel that recovers and one that does
	// not. In TUN mode every request this process makes goes into the tunnel,
	// so fetching a pass needs a working session — the very thing being
	// rebuilt. The fetch would queue behind the rebuild, time out, and the
	// tunnel would stay down until the user reconnected by hand. The token is
	// still valid until the pass expires, so a dead connection can be
	// replaced immediately and the full renewal can happen later, on its own
	// schedule, once traffic flows again.
	//
	// A pass the edge rejected is the one case where reusing it would loop:
	// the new session would be refused exactly like the old one, so that
	// failure goes through the full renewal and gets a fresh pass.
	if !isAuthFailure(cause) {
		if token, expiry, ok := c.reusablePass(); ok {
			session, err := c.newSession(c.ctx, token)
			if err == nil {
				if c.swap(session, token, expiry) {
					LogInfo("upstream session rebuilt with the current proxy pass (no control-plane round trip)")
					return nil
				}
				return ErrNoUsableSession
			}
			LogWarn("rebuilding the upstream session failed: %v", err)
		}
	}
	return c.renewLocked()
}

// isAuthFailure reports that the edge rejected the proxy pass itself, which a
// rebuild with the same pass cannot fix.
func isAuthFailure(err error) bool {
	var connectErr *proxyConnectHTTPError
	if !errors.As(err, &connectErr) {
		return false
	}
	switch connectErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusProxyAuthRequired:
		return true
	}
	return false
}

// reusablePass reports the pass the current session is already using, if it
// stays valid long enough to be worth rebuilding with.
func (c *Controller) reusablePass() (string, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == nil || c.current.rawToken == "" {
		return "", time.Time{}, false
	}
	if time.Until(c.current.expiresAt) < proxyPassRebuildMargin {
		return "", time.Time{}, false
	}
	return c.current.rawToken, c.current.expiresAt, true
}

func (c *Controller) renewLocked() error {
	pass, err := c.acquirePass()
	if err != nil {
		return err
	}
	if err := c.adopt(pass); err != nil {
		return err
	}
	if c.onPass != nil {
		c.onPass(pass)
	}
	return nil
}

// acquirePass refreshes the OAuth token if needed and fetches a fresh proxy
// pass, activating the Guardian account when the API says it is not enabled.
func (c *Controller) acquirePass() (*vpnclient.ProxyPassInfo, error) {
	auth, err := c.ensureAuth()
	if err != nil {
		return nil, err
	}

	pass, err := c.fetchPass(auth.AccessToken)
	if err == nil && !isQuotaExhausted(pass) {
		return pass, nil
	}

	if errors.Is(err, vpnclient.ErrTokenInvalid) {
		LogInfo("OAuth access was rejected by Guardian; refreshing token")
		if refreshed, refreshErr := c.refreshAuth(); refreshErr == nil {
			auth = refreshed
			pass, err = c.fetchPass(auth.AccessToken)
			if err == nil && !isQuotaExhausted(pass) {
				return pass, nil
			}
		}
		if errors.Is(err, vpnclient.ErrTokenInvalid) {
			LogInfo("Guardian account is not activated for VPN access; activating")
			ctx, cancel := context.WithTimeout(c.ctx, apiRequestTimeout)
			_, activateErr := vpnclient.ActivateGuardian(ctx, c.guardian, auth.AccessToken)
			cancel()
			if activateErr != nil {
				LogWarn("activating Guardian account failed: %v", activateErr)
				return nil, activateErr
			}
			LogInfo("Guardian account activated")
			pass, err = c.fetchPass(auth.AccessToken)
			if err == nil && !isQuotaExhausted(pass) {
				return pass, nil
			}
		}
	}

	if err != nil {
		return nil, err
	}
	return pass, nil
}

func (c *Controller) fetchPass(accessToken string) (*vpnclient.ProxyPassInfo, error) {
	ctx, cancel := context.WithTimeout(c.ctx, apiRequestTimeout)
	defer cancel()
	return vpnclient.FetchProxyPass(ctx, c.guardian, accessToken)
}

func (c *Controller) ensureAuth() (*Auth, error) {
	c.mu.Lock()
	auth := c.auth.clone()
	c.mu.Unlock()

	if auth != nil && auth.Valid(time.Now()) {
		return auth, nil
	}
	if auth == nil || auth.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available for background renewal")
	}
	return c.refreshAuth()
}

func (c *Controller) refreshAuth() (*Auth, error) {
	c.mu.Lock()
	current := c.auth.clone()
	c.mu.Unlock()
	if current == nil || current.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	LogInfo("refreshing OAuth token")
	ctx, cancel := context.WithTimeout(c.ctx, apiRequestTimeout)
	token, err := vpnclient.FxaRefreshToken(ctx, current.RefreshToken)
	cancel()
	if err != nil {
		return nil, err
	}

	refreshed := &Auth{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresIn:    token.ExpiresIn,
		ObtainedAt:   time.Now(),
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = current.RefreshToken
	}
	if saveErr := vpnclient.SaveTokens(token); saveErr != nil {
		LogWarn("saving refreshed tokens failed: %v", saveErr)
	}

	c.mu.Lock()
	c.auth = refreshed
	c.mu.Unlock()
	LogInfo("OAuth token refreshed")
	return refreshed, nil
}

// adopt applies a new proxy pass, updating the live session's bearer token in
// place when possible so existing tunnels keep flowing.
func (c *Controller) adopt(pass *vpnclient.ProxyPassInfo) error {
	c.mu.Lock()
	ms := c.current
	usable := !c.closed && ms != nil && ms.accepting && time.Now().Before(ms.expiresAt)
	c.mu.Unlock()

	if usable {
		if updater, ok := ms.session.(tokenUpdater); ok {
			if err := updater.UpdateToken(pass.RawToken); err == nil {
				c.mu.Lock()
				if c.current == ms {
					ms.rawToken = pass.RawToken
					ms.expiresAt = pass.ExpiresAt()
				}
				c.mu.Unlock()
				LogInfo("proxy pass renewed successfully next_expiry=%s session=retained", pass.ExpiresAt().Format(time.RFC3339))
				return nil
			}
		}
	}

	session, err := c.newSession(c.ctx, pass.RawToken)
	if err != nil {
		return err
	}
	if !c.swap(session, pass.RawToken, pass.ExpiresAt()) {
		return ErrNoUsableSession
	}
	LogInfo("proxy pass renewed successfully next_expiry=%s", pass.ExpiresAt().Format(time.RFC3339))
	return nil
}

// isQuotaExhausted reports whether the proxy pass headers show no monthly
// traffic left.
func isQuotaExhausted(pass *vpnclient.ProxyPassInfo) bool {
	if pass == nil || pass.QuotaLeft == "" {
		return false
	}
	left, err := strconv.ParseInt(pass.QuotaLeft, 10, 64)
	if err != nil {
		return false
	}
	return left <= 0
}

// Close tears down the upstream session. It returns as soon as the session is
// closed: pending renewals are cancelled through c.ctx, and in-flight CONNECT
// streams die with the session, which is what unblocks the proxy handlers.
func (c *Controller) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		close(c.done)

		c.mu.Lock()
		c.closed = true
		var session Session
		if c.current != nil {
			c.current.accepting = false
			session = c.current.session
			c.current.session = nil
		}
		c.mu.Unlock()

		// Outside the lock: closing an h2 connection waits for its
		// transport to unwind, and a handler releasing a tunnel ref at the
		// same moment would otherwise sit on the mutex.
		if session != nil {
			c.closeErr = session.Close()
		}
	})
	return c.closeErr
}

// EdgeHost is the hostname of the upstream edge this controller talks to.
// TUN mode needs it to pin a host route through the physical gateway, or the
// tunnel's own packets would be routed back into the tunnel.
func (c *Controller) EdgeHost() string {
	if c.proxyURL == nil {
		return ""
	}
	return c.proxyURL.Hostname()
}

// EdgePort is the port of the upstream edge.
func (c *Controller) EdgePort() string {
	if c.proxyURL == nil {
		return ""
	}
	if p := c.proxyURL.Port(); p != "" {
		return p
	}
	return "443"
}

type managedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *managedConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		if c.release != nil {
			c.release()
		}
	})
	return err
}

func (c *managedConn) CloseRead() error {
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

func (c *managedConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

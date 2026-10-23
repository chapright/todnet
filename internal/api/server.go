package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"todnet/internal/engine"
	"todnet/internal/store"
	"todnet/internal/tun"
	"todnet/internal/vpnclient"
	"todnet/internal/win"
)

// UserInfo describes the signed-in Firefox account.
type UserInfo struct {
	SignedIn   bool   `json:"signedIn"`
	Email      string `json:"email,omitempty"`
	UID        int    `json:"uid,omitempty"`
	Subscribed bool   `json:"subscribed"`
	MaxBytes   int64  `json:"maxBytes,omitempty"`
}

// accountView is one row of the account switcher.
type accountView struct {
	Email      string     `json:"email"`
	Label      string     `json:"label"`
	Active     bool       `json:"active"`
	Subscribed bool       `json:"subscribed"`
	MaxBytes   int64      `json:"maxBytes,omitempty"`
	QuotaLeft  int64      `json:"quotaLeft,omitempty"`
	QuotaMax   int64      `json:"quotaMax,omitempty"`
	QuotaAt    *time.Time `json:"quotaAt,omitempty"`
	// Exhausted is derived so the UI does not have to know that "0 left with a
	// known limit" means out of data.
	Exhausted bool `json:"exhausted"`
}

// Server is the local control plane for the UI.
type Server struct {
	eng      *engine.Engine
	settings *store.Store
	sysproxy *win.SystemProxy
	webFS    fs.FS

	mu          sync.Mutex
	accounts    *vpnclient.AccountStore
	auth        *engine.Auth
	email       string
	countries   []vpnclient.Country
	locations   []engine.Location
	locationsAt time.Time
	user        UserInfo
	pending     string // session token awaiting email verification
	logs        []logRecord
	logSeq      int64
	refreshing  bool      // a server-list fetch is in flight
	quotaSaved  time.Time // last time live allowance was written to disk

	// latency holds the measured round-trip time to each edge.
	latency *engine.LatencyCache

	// windowHandle is the window the interface's own title bar drives.
	windowHandle uintptr

	// trayNoticeTitle and trayNoticeText are the balloon the window shows
	// when it is closed to the notification area. The text is sent by the
	// interface, so the balloon speaks the language the user picked.
	trayNoticeTitle string
	trayNoticeText  string
}

// SetWindow records the window the interface's title bar drives.
func (s *Server) SetWindow(hwnd uintptr) { s.windowHandle = hwnd }

// SetTrayNotice records the text of the balloon the window shows when it
// is closed to the notification area.
func (s *Server) SetTrayNotice(title, text string) {
	s.mu.Lock()
	s.trayNoticeTitle = title
	s.trayNoticeText = text
	s.mu.Unlock()
}

// TrayNotice returns the balloon text the interface sent.
func (s *Server) TrayNotice() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trayNoticeTitle, s.trayNoticeText
}

// locationCacheTTL is how long a fetched server list is served from memory.
// The list changes rarely; the UI's Refresh button forces a re-fetch.
const locationCacheTTL = 15 * time.Minute

type logRecord struct {
	Seq   int64           `json:"seq"`
	Entry engine.LogEntry `json:"entry"`
}

// New builds the control server.
func New(eng *engine.Engine, settings *store.Store, webFS fs.FS) *Server {
	s := &Server{
		eng:      eng,
		settings: settings,
		sysproxy: win.NewSystemProxy(),
		webFS:    webFS,
		latency:  engine.NewLatencyCache(engine.DefaultLatencyTTL),
	}
	engine.SetLogSink(s.appendLog)

	// If a previous run died while the tunnel owned the system proxy, put the
	// user's settings back before anything else happens.
	if restored, err := win.RecoverProxyState(); err != nil {
		engine.LogWarn("recovering proxy settings failed: %v", err)
	} else if restored {
		engine.LogInfo("restored Windows proxy settings left over from a previous run")
		win.BroadcastSettingsChange()
	}

	// Populate the location list in the background so it is ready by the time
	// the window finishes loading.
	go s.refreshLocations()
	return s
}

func (s *Server) appendLog(e engine.LogEntry) {
	s.mu.Lock()
	s.logSeq++
	s.logs = append(s.logs, logRecord{Seq: s.logSeq, Entry: e})
	if len(s.logs) > 800 {
		s.logs = s.logs[len(s.logs)-800:]
	}
	s.mu.Unlock()
}

// RestoreProxy puts Windows' proxy settings back. Safe to call repeatedly.
func (s *Server) RestoreProxy() {
	if s.sysproxy.Applied() {
		if err := s.sysproxy.Restore(); err != nil {
			engine.LogWarn("restoring system proxy failed: %v", err)
		}
		win.BroadcastSettingsChange()
	}
}

// LoadSavedSession restores the saved account list so the user stays signed in
// across restarts, with the account that was active last time still active.
func (s *Server) LoadSavedSession() {
	store, err := vpnclient.LoadAccounts()
	if err != nil {
		engine.LogWarn("loading saved accounts failed: %v", err)
	}
	if store == nil {
		store = &vpnclient.AccountStore{}
	}

	s.mu.Lock()
	s.accounts = store
	var auth *engine.Auth
	if acc := store.ActiveAccount(); acc != nil {
		auth = authFromAccount(acc)
		s.auth = auth
		s.email = acc.Email
		s.user = userFromAccount(acc)
	}
	s.mu.Unlock()

	if auth == nil {
		return
	}
	// Entitlement and the location list live server-side, so a restored
	// session would otherwise keep showing "free allowance" to a subscriber
	// until they signed in again.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// A session saved before the app recorded emails has no address to
		// show, and may duplicate an account added since. Name it first so
		// everything below works with the real address.
		s.backfillNamelessAccounts(ctx)

		s.mu.Lock()
		email := s.email
		auth := s.auth
		s.mu.Unlock()
		if auth == nil {
			return
		}
		if auth.Valid(time.Now()) {
			s.loadAccountAndServers(email, auth)
		} else if auth.RefreshToken != "" {
			s.refreshInBackground()
		}
	}()
}

func authFromAccount(acc *vpnclient.Account) *engine.Auth {
	return &engine.Auth{
		AccessToken:  acc.Tokens.AccessToken,
		RefreshToken: acc.Tokens.RefreshToken,
		ExpiresIn:    acc.Tokens.ExpiresIn,
		ObtainedAt:   acc.Tokens.ObtainedAt,
	}
}

func userFromAccount(acc *vpnclient.Account) UserInfo {
	return UserInfo{
		SignedIn:   true,
		Email:      acc.Email,
		UID:        acc.UID,
		Subscribed: acc.Subscribed,
		MaxBytes:   acc.MaxBytes,
	}
}

// humanBytes formats a byte count for the diagnostics report.
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

func (s *Server) refreshInBackground() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	auth, err := s.ensureAuth(ctx)
	if err != nil {
		engine.LogWarn("could not refresh saved session: %v", err)
		return
	}
	s.mu.Lock()
	email := s.email
	s.mu.Unlock()
	s.loadAccountAndServers(email, auth)
}

// ensureAuth returns a valid access token for the active account, refreshing
// it when needed.
func (s *Server) ensureAuth(ctx context.Context) (*engine.Auth, error) {
	s.mu.Lock()
	auth := s.auth
	email := s.email
	s.mu.Unlock()

	if auth == nil {
		return nil, errors.New("not signed in")
	}
	if auth.Valid(time.Now()) {
		return auth, nil
	}
	if auth.RefreshToken == "" {
		return nil, errors.New("session expired; sign in again")
	}
	return s.refreshAccount(ctx, email)
}

// refreshAccount exchanges a stored refresh token for a new access token and
// writes it back onto that account. It deliberately does not touch the active
// pointer: a switch that happens mid-flight must not be undone by a late
// response.
func (s *Server) refreshAccount(ctx context.Context, email string) (*engine.Auth, error) {
	s.mu.Lock()
	var refresh string
	if s.accounts != nil {
		if acc := s.accounts.Find(email); acc != nil {
			refresh = acc.Tokens.RefreshToken
		}
	}
	s.mu.Unlock()

	if refresh == "" {
		return nil, errors.New("session expired; sign in again")
	}

	token, err := vpnclient.FxaRefreshToken(ctx, refresh)
	if err != nil {
		return nil, fmt.Errorf("session expired; sign in again (%w)", err)
	}

	s.mu.Lock()
	var auth *engine.Auth
	if s.accounts != nil {
		if acc := s.accounts.Find(email); acc != nil {
			vpnclient.ApplyToken(acc, token)
			auth = authFromAccount(acc)
			if strings.EqualFold(s.accounts.Active, acc.Email) {
				s.auth = auth
			}
		}
	}
	s.mu.Unlock()

	if auth == nil {
		return nil, errors.New("that account is no longer saved")
	}
	s.saveAccounts()
	return auth, nil
}

// accountStore returns the store, creating an empty one if startup did not.
// Callers must hold s.mu.
func (s *Server) accountStore() *vpnclient.AccountStore {
	if s.accounts == nil {
		s.accounts = &vpnclient.AccountStore{}
	}
	return s.accounts
}

// saveAccounts persists the list. The lock is held across the marshal so a
// concurrent switch cannot tear the JSON.
func (s *Server) saveAccounts() {
	s.mu.Lock()
	var err error
	if s.accounts != nil {
		err = s.accounts.Save()
	}
	s.mu.Unlock()
	if err != nil {
		engine.LogWarn("saving accounts failed: %v", err)
	}
}

// adoptAccountEmail names a nameless account and refreshes the derived state.
func (s *Server) adoptAccountEmail(email string) bool {
	s.mu.Lock()
	if s.accounts == nil || !s.accounts.AdoptEmail(email) {
		s.mu.Unlock()
		return false
	}
	s.applyActiveLocked(s.accounts)
	s.mu.Unlock()
	s.saveAccounts()
	return true
}

// backfillNamelessAccounts recovers the address for accounts saved by a build
// that did not store it. The access token carries the "profile" scope, so this
// needs no password. Naming the account also merges it with any duplicate the
// user has since signed in to.
func (s *Server) backfillNamelessAccounts(ctx context.Context) {
	s.mu.Lock()
	nameless := 0
	if s.accounts != nil {
		for _, acc := range s.accounts.Accounts {
			if acc.Email == "" {
				nameless++
			}
		}
	}
	s.mu.Unlock()

	if nameless == 0 {
		return
	}

	auth, err := s.accountAuth(ctx, "")
	if err != nil {
		engine.LogWarn("could not recover the address of a saved account: %v", err)
		return
	}
	email, err := vpnclient.FetchProfileEmail(ctx, auth.AccessToken)
	if err != nil {
		engine.LogWarn("could not recover the address of a saved account: %v", err)
		return
	}
	if email == "" {
		return
	}
	if s.adoptAccountEmail(email) {
		engine.LogInfo("saved account identified as %s", email)
	}
}

// Handler returns the HTTP mux serving both the API and the embedded UI.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/login", s.handleLogin)
	mux.HandleFunc("/api/verify", s.handleVerify)
	mux.HandleFunc("/api/verify/continue", s.handleVerifyContinue)
	mux.HandleFunc("/api/logout", s.handleLogout)
	mux.HandleFunc("/api/accounts/switch", s.handleSwitchAccount)
	mux.HandleFunc("/api/accounts/remove", s.handleRemoveAccount)
	mux.HandleFunc("/api/accounts/check", s.handleCheckAllowances)
	mux.HandleFunc("/api/connect", s.handleConnect)
	mux.HandleFunc("/api/cancel", s.handleCancel)
	mux.HandleFunc("/api/disconnect", s.handleDisconnect)
	mux.HandleFunc("/api/open-url", s.handleOpenURL)
	mux.HandleFunc("/api/settings", s.handleSettings)
	mux.HandleFunc("/api/locations", s.handleLocations)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/diagnostics", s.handleDiagnostics)
	mux.HandleFunc("/api/show", s.handleShow)
	mux.HandleFunc("/api/ready", s.handleReady)
	mux.HandleFunc("/api/quit", s.handleQuit)
	mux.HandleFunc("/api/elevate", s.handleElevate)
	mux.HandleFunc("/api/window", s.handleWindow)
	mux.HandleFunc("/api/tray-notice", s.handleTrayNotice)

	static, err := fs.Sub(s.webFS, "web")
	if err == nil {
		// Browsers request /favicon.ico unconditionally; serve the app icon so
		// the console stays clean.
		if ico, err := fs.ReadFile(static, "icon.ico"); err == nil {
			mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "image/x-icon")
				w.Header().Set("Cache-Control", "public, max-age=86400")
				_, _ = w.Write(ico)
			})
		}
		mux.Handle("/", http.FileServer(http.FS(static)))
	}

	return noCache(mux)
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// The interface is a local page with no external references at all, so
		// it can be locked down hard. 'unsafe-inline' is needed for the theme
		// script in index.html and for the inline style attributes the views
		// set on a few nodes; scripts are still restricted to the app's own
		// files, so a stray inline script cannot be injected.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self' 'unsafe-inline'; connect-src 'self'; object-src 'none'; "+
				"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func decodeJSON(r *http.Request, v any) error {
	defer io.Copy(io.Discard, io.LimitReader(r.Body, 1<<20))
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(v)
}

type stateResponse struct {
	Status     engine.Status     `json:"status"`
	Settings   store.Settings    `json:"settings"`
	User       UserInfo          `json:"user"`
	Accounts   []accountView     `json:"accounts"`
	Locations  []engine.Location `json:"locations"`
	Version    string            `json:"version"`
	Platform   string            `json:"platform"`
	ProxyOwned bool              `json:"proxyOwned"`
	Elevated   bool              `json:"elevated"`
	// Maximized tells the interface which glyph the window's own
	// maximise button has to draw.
	Maximized bool `json:"maximized"`
	// Rounded tells the interface whether DWM rounded the native window,
	// so it does not draw the Windows 10 hairline fallback on top.
	Rounded bool `json:"rounded"`
}

// Version is injected by main at build time.
var Version = "1.0.0"

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	status := s.eng.Status()

	s.mu.Lock()
	user := s.user
	locs := s.locations
	auth := s.auth
	accounts := s.accountViewsLocked(status)
	s.mu.Unlock()

	user.SignedIn = auth != nil

	// While connected the engine is the authority on allowance, so keep the
	// stored snapshot roughly current for when the tunnel is down.
	s.noteLiveQuota(status)

	writeJSON(w, http.StatusOK, stateResponse{
		Status:     status,
		Settings:   s.settings.Get(),
		User:       user,
		Accounts:   accounts,
		Locations:  locs,
		Version:    Version,
		Platform:   win.OSName(),
		ProxyOwned: s.sysproxy.Applied(),
		Elevated:   win.IsElevated(),
		Maximized:  win.WindowMaximized(s.windowHandle),
		Rounded:    win.SupportsRoundedCorners(),
	})
}

// accountViewsLocked renders the switcher rows. Callers must hold s.mu.
func (s *Server) accountViewsLocked(status engine.Status) []accountView {
	if s.accounts == nil || len(s.accounts.Accounts) == 0 {
		return nil
	}
	ordered := s.accounts.Ordered()
	out := make([]accountView, 0, len(ordered))
	for i := range ordered {
		acc := &ordered[i]
		isActive := strings.EqualFold(acc.Email, s.accounts.Active)
		view := accountView{
			Email:      acc.Email,
			Label:      acc.Label(),
			Active:     isActive,
			Subscribed: acc.Subscribed,
			MaxBytes:   acc.MaxBytes,
			QuotaLeft:  acc.QuotaLeft,
			QuotaMax:   acc.QuotaMax,
		}
		if !acc.QuotaAt.IsZero() {
			at := acc.QuotaAt
			view.QuotaAt = &at
		}
		// The live tunnel reports the active account's allowance more
		// accurately than any stored snapshot.
		if isActive && status.State == engine.StateConnected && status.QuotaMax > 0 {
			view.QuotaLeft = status.QuotaLeft
			view.QuotaMax = status.QuotaMax
		}
		view.Exhausted = view.QuotaMax > 0 && view.QuotaLeft <= 0
		out = append(out, view)
	}
	return out
}

// noteLiveQuota copies the connected account's allowance into the store at
// most once a minute, so the switcher can still show it after disconnecting.
func (s *Server) noteLiveQuota(status engine.Status) {
	if status.State != engine.StateConnected || status.QuotaMax <= 0 {
		return
	}

	s.mu.Lock()
	if s.accounts == nil || time.Since(s.quotaSaved) < time.Minute {
		s.mu.Unlock()
		return
	}
	acc := s.accounts.ActiveAccount()
	if acc == nil {
		s.mu.Unlock()
		return
	}
	changed := acc.QuotaLeft != status.QuotaLeft || acc.QuotaMax != status.QuotaMax
	acc.QuotaLeft = status.QuotaLeft
	acc.QuotaMax = status.QuotaMax
	acc.QuotaAt = time.Now()
	if changed {
		s.quotaSaved = time.Now()
	}
	s.mu.Unlock()

	if changed {
		s.saveAccounts()
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	OK                bool   `json:"ok"`
	NeedsVerification bool   `json:"needsVerification"`
	Method            string `json:"method,omitempty"`
	SessionToken      string `json:"sessionToken,omitempty"`
	Error             string `json:"error,omitempty"`
	// Code names the failure, so the interface can say it in the language
	// the user reads. The message is the fallback for a code it does not
	// know.
	Code string `json:"code,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, errors.New("email and password are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	engine.LogInfo("signing in as %s", req.Email)
	login, err := vpnclient.FxaLogin(ctx, req.Email, req.Password)
	if err != nil {
		engine.LogWarn("sign-in failed: %v", err)
		// The status has to say the sign-in failed. A 200 with an error in
		// the body reads as a success to the interface, which closed the
		// sign-in sheet and showed nothing: the user never learned that
		// their password was wrong.
		code, message := friendlyFxaError(err)
		writeJSON(w, http.StatusUnauthorized, loginResponse{Error: message, Code: code})
		return
	}

	if !login.Verified {
		s.mu.Lock()
		s.pending = login.SessionToken
		s.email = req.Email
		s.mu.Unlock()
		engine.LogInfo("account needs email confirmation (method=%s)", login.VerificationMethod)
		writeJSON(w, http.StatusOK, loginResponse{
			NeedsVerification: true,
			Method:            login.VerificationMethod,
			SessionToken:      login.SessionToken,
		})
		return
	}

	if err := s.finishLogin(ctx, login.SessionToken, req.Email); err != nil {
		engine.LogWarn("finishing the sign-in failed: %v", err)
		writeJSON(w, http.StatusUnauthorized, loginResponse{
			Error: "the sign-in could not be completed; try again",
			Code:  "generic",
		})
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{OK: true})
}

type verifyRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	var req verifyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, errors.New("verification code is required"))
		return
	}

	s.mu.Lock()
	sessionToken := s.pending
	email := s.email
	s.mu.Unlock()
	if sessionToken == "" {
		writeErr(w, http.StatusBadRequest, errors.New("no sign-in is waiting for a code"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	if err := vpnclient.FxaVerifySession(ctx, sessionToken, code); err != nil {
		engine.LogWarn("verification failed: %v", err)
		writeJSON(w, http.StatusUnauthorized, loginResponse{
			Error: "that code was not accepted; check the email and try again",
			Code:  "wrong-code",
		})
		return
	}
	if err := s.finishLogin(ctx, sessionToken, email); err != nil {
		engine.LogWarn("finishing the sign-in failed: %v", err)
		writeJSON(w, http.StatusUnauthorized, loginResponse{
			Error: "the sign-in could not be completed; try again",
			Code:  "generic",
		})
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{OK: true})
}

// handleVerifyContinue is used when Firefox sent a confirmation link instead
// of a code: the user clicks it, then tells the app to continue.
func (s *Server) handleVerifyContinue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	sessionToken := s.pending
	email := s.email
	s.mu.Unlock()
	if sessionToken == "" {
		writeErr(w, http.StatusBadRequest, errors.New("no sign-in is waiting for confirmation"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.finishLogin(ctx, sessionToken, email); err != nil {
		engine.LogWarn("finishing the sign-in failed: %v", err)
		writeJSON(w, http.StatusUnauthorized, loginResponse{
			Error: "the sign-in could not be completed; try again",
			Code:  "generic",
		})
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{OK: true})
}

func (s *Server) finishLogin(ctx context.Context, sessionToken, email string) error {
	token, err := vpnclient.FxaOAuthToken(ctx, sessionToken)
	if err != nil {
		return fmt.Errorf("obtaining access token: %w", err)
	}

	acc := vpnclient.Account{Email: email, AddedAt: time.Now(), LastUsed: time.Now()}
	vpnclient.ApplyToken(&acc, token)

	s.mu.Lock()
	store := s.accountStore()
	store.Upsert(acc)
	store.Active = email
	s.pending = ""
	if saved := store.Find(email); saved != nil {
		s.email = saved.Email
		s.user = userFromAccount(saved)
		s.auth = authFromAccount(saved)
	}
	auth := s.auth
	s.mu.Unlock()
	s.saveAccounts()

	engine.LogInfo("signed in as %s", email)
	go s.loadAccountAndServers(email, auth)
	// If a session carried over from an older build is still sitting there
	// nameless, it is probably this same account; naming it merges the two.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s.backfillNamelessAccounts(ctx)
	}()
	return nil
}

// loadAccountAndServers fetches entitlement and the location list after login
// or an account switch.
func (s *Server) loadAccountAndServers(email string, auth *engine.Auth) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	if ent, err := vpnclient.FetchUserInfo(ctx, vpnclient.GuardianEndpointDefault, auth.AccessToken); err == nil {
		maxBytes := parseBytes(ent.MaxBytes)
		s.mu.Lock()
		// Keep the entitlement on the account itself so the switcher can show
		// the right plan for accounts that are not active.
		if s.accounts != nil {
			if acc := s.accounts.Find(email); acc != nil {
				acc.UID = ent.UID
				acc.Subscribed = ent.Subscribed
				acc.MaxBytes = maxBytes
			}
		}
		if strings.EqualFold(s.email, email) {
			s.user.UID = ent.UID
			s.user.Subscribed = ent.Subscribed
			s.user.MaxBytes = maxBytes
		}
		s.mu.Unlock()
		s.saveAccounts()
	} else if errors.Is(err, vpnclient.ErrTokenInvalid) {
		// The account is not activated for VPN access yet; that is handled at
		// connect time, so this is informational only.
		engine.LogInfo("account is not yet activated for VPN access")
	}

	s.refreshLocations()
}

func (s *Server) refreshLocations() { s.refreshLocationsForced(false) }

// refreshLocationsForced fetches the server list unless a recent fetch already
// succeeded. Startup used to trigger two or three identical requests (New,
// the restored session, and the UI asking for a list that was still empty),
// which showed up in the log as "loaded 34 locations" three times in a row.
func (s *Server) refreshLocationsForced(force bool) {
	s.mu.Lock()
	if !force && len(s.locations) > 0 && time.Since(s.locationsAt) < locationCacheTTL {
		s.mu.Unlock()
		return
	}
	if s.refreshing {
		s.mu.Unlock()
		return
	}
	s.refreshing = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.refreshing = false
		s.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	started := time.Now()
	countries, err := vpnclient.FetchServerList(ctx)
	if err != nil {
		engine.LogWarn("fetching server list failed after %s: %v",
			time.Since(started).Round(time.Millisecond), err)
		return
	}
	locs := engine.BuildLocations(countries)
	// A measurement belongs to an edge, not to a list, and the cache still
	// holds the ones taken a moment ago. Carrying them onto the fresh list
	// keeps the distances on screen: without this the list came back with
	// every latency at zero, because the measurement pass skips the edges it
	// already knows and so had nothing to copy back.
	engine.ApplyLatency(locs, s.latency)
	s.mu.Lock()
	s.countries = countries
	s.locations = locs
	s.locationsAt = time.Now()
	s.mu.Unlock()
	engine.LogInfo("loaded %d locations in %s", len(locs), time.Since(started).Round(time.Millisecond))

	// Measure the edges in the background. The list is already usable; the
	// distances arrive a moment later and are filled in on the next poll.
	go s.refreshLatencies()
}

// refreshLatencies measures the round-trip time to every known edge and
// copies the results onto the served location list.
//
// It runs in the background on purpose: thirty TCP handshakes take a
// moment, and the list must not wait for them.
func (s *Server) refreshLatencies() {
	s.mu.Lock()
	locs := append([]engine.Location(nil), s.locations...)
	s.mu.Unlock()
	if len(locs) == 0 {
		return
	}

	hosts := make([]string, 0, len(locs))
	seen := map[string]bool{}
	for _, l := range locs {
		if l.Host == "" || seen[l.Host] {
			continue
		}
		seen[l.Host] = true
		hosts = append(hosts, l.Host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	measured := engine.MeasureLatencies(ctx, hosts, s.latency)
	if len(measured) == 0 {
		return
	}
	// An edge that answers in less than a millisecond has no time to show,
	// and the list leaves it out. The count says how many edges answered
	// with a time, so the log and the list agree.
	usable := 0
	for _, ms := range measured {
		if ms > 0 {
			usable++
		}
	}
	engine.LogInfo("measured %d of %d edge latencies", usable, len(hosts))
	s.applyLatencies()
}

// applyLatencies copies the cached measurements onto the served list.
func (s *Server) applyLatencies() {
	s.mu.Lock()
	locs := append([]engine.Location(nil), s.locations...)
	s.mu.Unlock()
	if len(locs) == 0 {
		return
	}
	engine.ApplyLatency(locs, s.latency)

	s.mu.Lock()
	// Only replace the list if the country list has not been swapped in
	// the meantime.
	if len(s.locations) == len(locs) {
		s.locations = locs
	}
	s.mu.Unlock()
}

// latencySnapshot returns the current measurements, keyed by edge address.
func (s *Server) latencySnapshot() map[string]int {
	s.mu.Lock()
	locs := append([]engine.Location(nil), s.locations...)
	s.mu.Unlock()
	out := map[string]int{}
	for _, l := range locs {
		if l.Host == "" {
			continue
		}
		if ms, ok := s.latency.Get(l.Host); ok {
			out[l.Host] = ms
		}
	}
	return out
}

func parseBytes(raw string) int64 {
	var v int64
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &v); err != nil {
		return 0
	}
	return v
}

// friendlyFxaError turns a sign-in failure into a code the interface can
// translate and a message for the case where it cannot.
//
// The raw upstream error is never the message: it carries an errno, a
// documentation link and the request payload, which is no use to a user
// who typed their address wrong. The detail stays in the log.
func friendlyFxaError(err error) (code, message string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "errno 102"):
		return "invalid-credentials", "incorrect email or password"
	case strings.Contains(msg, "errno 103"):
		return "account-locked", "your account is locked after too many attempts; try again later"
	case strings.Contains(msg, "errno 125"):
		return "too-many-attempts", "too many sign-in attempts; wait a few minutes and try again"
	case strings.Contains(msg, "errno 201"):
		return "needs-confirmation", "the password is correct but the account needs email confirmation"
	case strings.Contains(msg, "406"):
		return "anti-bot", "Firefox's anti-bot check could not be completed; try again"
	case strings.Contains(msg, "errno 107"), strings.Contains(msg, "Invalid parameter in request body"):
		// The address itself was refused before it was ever an account.
		return "invalid-email", "that does not look like an email address"
	default:
		return "", "the sign-in could not be completed; try again"
	}
}

// handleLogout removes the active account. With more than one account saved
// that is a removal rather than a global sign-out, so the app falls back to the
// most recently used survivor instead of leaving the user signed in as nobody.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	_ = s.eng.Disconnect()
	s.RestoreProxy()

	s.mu.Lock()
	store := s.accountStore()
	label := ""
	if acc := store.ActiveAccount(); acc != nil {
		label = acc.Label()
		store.Remove(acc.Email)
	}
	s.applyActiveLocked(store)
	s.pending = ""
	s.mu.Unlock()
	s.saveAccounts()

	if label != "" {
		engine.LogInfo("removed account %s", label)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// applyActiveLocked points the derived session state at whichever account the
// store now considers active. Callers must hold s.mu.
func (s *Server) applyActiveLocked(store *vpnclient.AccountStore) {
	if acc := store.ActiveAccount(); acc != nil {
		s.auth = authFromAccount(acc)
		s.email = acc.Email
		s.user = userFromAccount(acc)
		return
	}
	s.auth = nil
	s.email = ""
	s.user = UserInfo{}
	s.locations = nil
	s.countries = nil
}

type accountRequest struct {
	Email string `json:"email"`
}

func (s *Server) handleSwitchAccount(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	if err := decodeJSON(r, &req); err != nil || req.Email == "" {
		writeErr(w, http.StatusBadRequest, errors.New("email is required"))
		return
	}
	if err := s.SwitchAccount(r.Context(), req.Email); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRemoveAccount(w http.ResponseWriter, r *http.Request) {
	var req accountRequest
	if err := decodeJSON(r, &req); err != nil || req.Email == "" {
		writeErr(w, http.StatusBadRequest, errors.New("email is required"))
		return
	}
	if err := s.RemoveAccount(req.Email); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCheckAllowances(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	s.CheckAllowances(ctx)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// SwitchAccount makes email active. An open tunnel belongs to the previous
// account, so it is torn down first rather than silently charging the new
// account for a session the old one started.
func (s *Server) SwitchAccount(ctx context.Context, email string) error {
	s.mu.Lock()
	store := s.accountStore()
	acc := store.Find(email)
	if acc == nil {
		s.mu.Unlock()
		return errors.New("that account is not saved")
	}
	if strings.EqualFold(store.Active, acc.Email) {
		s.mu.Unlock()
		return nil
	}
	wasConnected := s.eng.Status().State != engine.StateDisconnected
	s.mu.Unlock()

	if wasConnected {
		engine.LogInfo("switching accounts; closing the tunnel first")
		if err := s.eng.Disconnect(); err != nil {
			engine.LogWarn("disconnect before switching failed: %v", err)
		}
		s.RestoreProxy()
	}

	s.mu.Lock()
	store = s.accountStore()
	acc = store.Find(email)
	if acc == nil {
		s.mu.Unlock()
		return errors.New("that account is no longer saved")
	}
	store.Active = acc.Email
	acc.LastUsed = time.Now()
	s.applyActiveLocked(store)
	auth := s.auth
	label := acc.Label()
	s.mu.Unlock()
	s.saveAccounts()

	engine.LogInfo("switched to %s", label)

	if !auth.Valid(time.Now()) && auth.RefreshToken != "" {
		refreshed, err := s.refreshAccount(ctx, email)
		if err != nil {
			engine.LogWarn("refreshing %s failed: %v", email, err)
		} else {
			auth = refreshed
		}
	}
	go s.loadAccountAndServers(email, auth)
	return nil
}

// RemoveAccount deletes a saved account. Removing the active one disconnects
// and falls back to the most recently used survivor.
func (s *Server) RemoveAccount(email string) error {
	s.mu.Lock()
	store := s.accountStore()
	acc := store.Find(email)
	if acc == nil {
		s.mu.Unlock()
		return errors.New("that account is not saved")
	}
	label := acc.Label()
	wasActive := strings.EqualFold(store.Active, acc.Email)
	s.mu.Unlock()

	if wasActive && s.eng.Status().State != engine.StateDisconnected {
		if err := s.eng.Disconnect(); err != nil {
			engine.LogWarn("disconnect before removing account failed: %v", err)
		}
		s.RestoreProxy()
	}

	s.mu.Lock()
	store.Remove(email)
	s.applyActiveLocked(store)
	s.mu.Unlock()
	s.saveAccounts()

	engine.LogInfo("removed account %s", label)
	return nil
}

// CheckAllowances asks Guardian how much allowance each saved account has
// left. It is one cheap request per account and opens no tunnel, so the
// switcher can show which account still has data — the actual reason someone
// switches accounts.
func (s *Server) CheckAllowances(ctx context.Context) {
	s.mu.Lock()
	var emails []string
	if s.accounts != nil {
		for _, acc := range s.accounts.Accounts {
			if acc.Tokens.AccessToken == "" && acc.Tokens.RefreshToken == "" {
				continue
			}
			// Skip accounts checked recently; the answer cannot have moved
			// much and this runs whenever the switcher opens.
			if !acc.QuotaAt.IsZero() && time.Since(acc.QuotaAt) < time.Minute {
				continue
			}
			emails = append(emails, acc.Email)
		}
	}
	s.mu.Unlock()

	for _, email := range emails {
		if err := s.checkAllowance(ctx, email); err != nil {
			engine.LogWarn("checking allowance for %s failed: %v", email, err)
		}
	}
}

func (s *Server) checkAllowance(ctx context.Context, email string) error {
	auth, err := s.accountAuth(ctx, email)
	if err != nil {
		return err
	}

	pass, err := vpnclient.FetchProxyPass(ctx, vpnclient.GuardianEndpointDefault, auth.AccessToken)
	if err != nil {
		return err
	}
	if pass.QuotaLeft == "" && pass.QuotaMax == "" {
		return nil
	}
	left, err := strconv.ParseInt(pass.QuotaLeft, 10, 64)
	if err != nil {
		return fmt.Errorf("unreadable quota %q", pass.QuotaLeft)
	}
	max, _ := strconv.ParseInt(pass.QuotaMax, 10, 64)

	s.mu.Lock()
	if s.accounts != nil {
		if acc := s.accounts.Find(email); acc != nil {
			acc.QuotaLeft = left
			if max > 0 {
				acc.QuotaMax = max
			}
			acc.QuotaAt = time.Now()
		}
	}
	s.mu.Unlock()
	s.saveAccounts()
	return nil
}

// accountAuth returns a valid auth for any saved account, not just the active
// one, refreshing it if needed.
func (s *Server) accountAuth(ctx context.Context, email string) (*engine.Auth, error) {
	s.mu.Lock()
	var auth *engine.Auth
	if s.accounts != nil {
		if acc := s.accounts.Find(email); acc != nil {
			auth = authFromAccount(acc)
		}
	}
	s.mu.Unlock()

	if auth == nil {
		return nil, errors.New("that account is not saved")
	}
	if auth.Valid(time.Now()) {
		return auth, nil
	}
	return s.refreshAccount(ctx, email)
}

type connectRequest struct {
	CountryCode string `json:"countryCode"`
	CityCode    string `json:"cityCode"`
	Transport   string `json:"transport"`
	// TunMode overrides the saved preference for this one attempt, so the
	// settings switch takes effect on the next connect without a reconnect.
	TunMode *bool `json:"tunMode,omitempty"`
}

// errTunNeedsAdmin reports that a connection attempt was refused because TUN
// mode needs an elevated process.
//
// This is the single most confusing failure in the app: the click produces
// no visible effect because the refusal happens before the engine is
// touched. It is a typed error so every path that can hit it — the button,
// the tray, and the automatic connect at launch — can offer the same two
// actions instead of failing silently.
type errTunNeedsAdmin struct{}

func (errTunNeedsAdmin) Error() string {
	return "TUN mode needs administrator rights. Restart Todnet as administrator, or switch to system proxy mode."
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	var req connectRequest
	_ = decodeJSON(r, &req)

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	status, err := s.connectWith(ctx, req)
	if err != nil {
		if _, ok := err.(errTunNeedsAdmin); ok {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": err.Error(),
				"code":  "tun-needs-admin",
			})
			return
		}
		if errors.Is(err, errNotSignedIn) {
			writeErr(w, http.StatusUnauthorized, err)
			return
		}
		if errors.Is(err, errNoSuchLocation) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeErr(w, http.StatusBadGateway, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

// errNotSignedIn and errNoSuchLocation let the caller map a failure onto the
// right status code without inspecting the message.
var (
	errNotSignedIn    = errors.New("not signed in")
	errNoSuchLocation = errors.New("no VPN server available for that location")
)

// connectWith performs one connection attempt: it resolves the location,
// starts the tunnel and points Windows at the local proxy.
//
// Both the Connect button and the automatic connect at launch go through
// here, so they cannot drift apart.
func (s *Server) connectWith(ctx context.Context, req connectRequest) (engine.Status, error) {
	auth, err := s.ensureAuth(ctx)
	if err != nil {
		return engine.Status{}, fmt.Errorf("%w: %v", errNotSignedIn, err)
	}

	cfg := s.settings.Get()
	s.mu.Lock()
	countries := s.countries
	s.mu.Unlock()

	country := req.CountryCode
	if country == "" {
		country = cfg.CountryCode
	}
	city := req.CityCode
	if city == "" {
		city = cfg.CityCode
	}

	var location *engine.Location
	if len(countries) > 0 {
		picked, err := engine.PickLocationWithLatency(countries, country, city, s.latencySnapshot())
		if err != nil {
			return engine.Status{}, fmt.Errorf("%w: %v", errNoSuchLocation, err)
		}
		location = picked
	}

	transport := cfg.Transport
	if req.Transport != "" {
		transport = req.Transport
	}

	tunMode := req.TunMode != nil && *req.TunMode
	if req.TunMode == nil {
		tunMode = cfg.TunMode
	}
	if tunMode && !win.IsElevated() {
		engine.LogWarn("connect refused: TUN mode needs administrator rights (%s)", win.SystemSummary())
		return engine.Status{}, errTunNeedsAdmin{}
	}

	err = s.eng.Connect(engine.ConnectConfig{
		Auth:           auth,
		Location:       location,
		Transport:      transport,
		SocksPort:      cfg.SocksPort,
		HTTPPort:       cfg.HTTPPort,
		VerifyExit:     cfg.VerifyExit,
		TunMode:        tunMode,
		DirectFallback: cfg.DirectFallback,
		BypassDomains:  cfg.BypassDomains,
		Latencies:      s.latencySnapshot(),
	})
	if err != nil {
		return engine.Status{}, err
	}

	// Point Windows at the local proxy so traffic flows without any per-app
	// configuration. In TUN mode the adapter already captures everything, and
	// a system proxy on top of it would only add a second hop.
	if cfg.SetSystemProxy && !tunMode {
		status := s.eng.Status()
		if status.HTTPAddr != "" {
			host, portStr, splitErr := net.SplitHostPort(status.HTTPAddr)
			if splitErr == nil {
				var port int
				_, _ = fmt.Sscanf(portStr, "%d", &port)
				if applyErr := s.sysproxy.Apply(host, port); applyErr != nil {
					engine.LogWarn("could not set the Windows system proxy: %v", applyErr)
				} else {
					win.BroadcastSettingsChange()
					engine.LogInfo("Windows system proxy set to %s", status.HTTPAddr)
				}
			}
		}
	}

	return s.eng.Status(), nil
}

// MaybeAutoConnect starts a connection at launch when the user asked for one.
//
// The setting used to be written to settings.json and read by nothing, so
// "Connect on launch" did nothing at all. It is read here instead.
//
// Two things are deliberately not done: the attempt waits for the saved
// session to be usable, and a TUN attempt on a process that is not elevated
// is refused rather than attempted, because it could only fail. The client
// is told about that case through the state it already polls.
func (s *Server) MaybeAutoConnect() {
	cfg := s.settings.Get()
	if !cfg.AutoConnect {
		return
	}

	s.mu.Lock()
	signedIn := s.auth != nil
	s.mu.Unlock()
	if !signedIn {
		engine.LogInfo("auto connect requested but no account is signed in; skipping")
		return
	}

	if cfg.TunMode && !win.IsElevated() {
		engine.LogWarn("auto connect requested in TUN mode but the process is not elevated; skipping")
		return
	}

	go func() {
		// Give the saved session a moment to be restored and the location
		// list a moment to arrive, so the first attempt uses the stored
		// location instead of racing the startup work.
		time.Sleep(2 * time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		status, err := s.connectWith(ctx, connectRequest{})
		if err != nil {
			engine.LogWarn("auto connect failed: %v", err)
			return
		}
		engine.LogInfo("auto connect succeeded (%s)", status.Location.CountryName)
	}()
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	s.RestoreProxy()
	if err := s.eng.Disconnect(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": s.eng.Status()})
}

// handleCancel abandons a connection attempt that is still running.
//
// The attempt is aborted on the engine side: the generation counter is
// bumped so the in-flight attempt's own cancellation check trips, and the
// attempt's context is cancelled so the network work stops immediately.
// Without this the "Cancel" button did nothing at all, because the client
// request that started the attempt was still holding the whole handshake.
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	if err := s.eng.Cancel(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": s.eng.Status()})
}

// externalLinkHosts is the allowlist for handleOpenURL. The app opens a
// handful of Mozilla pages in the user's browser; anything else is refused
// so the endpoint cannot be turned into a general-purpose URL opener.
var externalLinkHosts = map[string]bool{
	"accounts.firefox.com":      true,
	"support.mozilla.org":       true,
	"www.mozilla.org":           true,
	"firefox.com":               true,
	"www.firefox.com":           true,
	"support.firefox.com":       true,
	"www.vpn.mozilla.org":       true,
	"vpn.mozilla.org":           true,
	"donate.mozilla.org":        true,
	"www.donate.mozilla.org":    true,
	"blog.mozilla.org":          true,
	"www.blog.mozilla.org":      true,
	"developer.mozilla.org":     true,
	"www.developer.mozilla.org": true,
}

// handleWindow performs a window operation for the interface's own title
// bar: minimise, maximise, restore, toggle-maximize, drag or close.
func (s *Server) handleWindow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	var req struct {
		Action string `json:"action"`
		// Edge is the edge a resize starts from: left, right, top, bottom,
		// top-left, top-right, bottom-left or bottom-right.
		Edge string `json:"edge"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var err error
	if req.Action == "resize" {
		err = win.WindowResize(s.windowHandle, req.Edge)
	} else {
		err = win.WindowAction(s.windowHandle, req.Action)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The state is read after the action, so a toggle or a maximise lets the
	// interface draw the right glyph at once instead of waiting for the next
	// poll of /api/state. Without it the bar showed "maximise" for up to a
	// second after a double click had already maximised the window.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"maximized": win.WindowMaximized(s.windowHandle),
	})
}

// handleTrayNotice takes the text of the balloon the window shows when it
// is closed to the notification area.
//
// The balloon is drawn by Windows, so it cannot be a translated string on
// this side: the interface sends the words it wants, in the language the
// user picked.
func (s *Server) handleTrayNotice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	var req struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.SetTrayNotice(balloonText(req.Title, 120), balloonText(req.Text, 200))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// balloonText makes a string fit the balloon Windows draws: one line, and
// not too long.
func balloonText(s string, max int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		s = strings.TrimSpace(s[:max])
	}
	return s
}

// handleOpenURL opens a Mozilla page in the user's default browser.
//
// The interface is a local page with no outbound navigation of its own, so
// "Create an account" and "Forgot password?" have to leave the app. Doing it
// through the control plane means the link opens in the user's real browser,
// with their real profile, instead of inside the WebView.
func (s *Server) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("that is not a valid link"))
		return
	}
	if parsed.Scheme != "https" {
		writeErr(w, http.StatusBadRequest, errors.New("only https links can be opened"))
		return
	}
	host := strings.ToLower(parsed.Hostname())
	if !externalLinkHosts[host] {
		writeErr(w, http.StatusBadRequest, errors.New("that link is not on the allow list"))
		return
	}
	if err := win.OpenURL(parsed.String()); err != nil {
		engine.LogWarn("opening %s failed: %v", parsed.String(), err)
		writeErr(w, http.StatusBadGateway, errors.New("the link could not be opened in your browser"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.settings.Get())
	case http.MethodPost:
		var next store.Settings
		if err := decodeJSON(r, &next); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		prev := s.settings.Get()
		if err := s.settings.Save(next); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if prev.LaunchAtStartup != next.LaunchAtStartup {
			if err := win.SetLaunchAtStartup(next.LaunchAtStartup); err != nil {
				engine.LogWarn("updating startup entry failed: %v", err)
			}
		}
		engine.SetVerbose(next.VerboseLogs)
		writeJSON(w, http.StatusOK, s.settings.Get())
	default:
		writeErr(w, http.StatusMethodNotAllowed, errors.New("GET or POST required"))
	}
}

func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	// The Remote Settings server list is public, so locations can be browsed
	// before signing in; only connecting requires an account.
	//
	// The list changes rarely, so serve the cache unless the caller explicitly
	// asks to refresh. Re-fetching on every page view meant two identical
	// requests to Mozilla at every startup.
	force := r.URL.Query().Get("refresh") != ""

	s.mu.Lock()
	cached := s.locations
	s.mu.Unlock()

	if force || len(cached) == 0 {
		s.refreshLocationsForced(force)
		s.mu.Lock()
		cached = s.locations
		s.mu.Unlock()
	}

	if len(cached) == 0 {
		writeErr(w, http.StatusBadGateway,
			errors.New("could not load the server list; check your connection and press Refresh"))
		return
	}
	// Serve a copy with the current measurements attached. The cached slice
	// is shared with the rest of the server, so it is never written to.
	served := append([]engine.Location(nil), cached...)
	engine.ApplyLatency(served, s.latency)
	writeJSON(w, http.StatusOK, served)
}

// handleDiagnostics produces one text blob that contains everything needed to
// debug a report: versions, machine, settings, tunnel state, the adapter table
// and the log tail. Asking a user to assemble that by hand never works.
func (s *Server) handleDiagnostics(w http.ResponseWriter, r *http.Request) {
	status := s.eng.Status()

	s.mu.Lock()
	user := s.user
	accounts := append([]accountView(nil), s.accountsForDiagnostics()...)
	s.mu.Unlock()

	cfg := s.settings.Get()
	var b strings.Builder
	fmt.Fprintf(&b, "Todnet %s\n", Version)
	fmt.Fprintf(&b, "generated: %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(&b, "system:    %s\n", win.SystemSummary())
	fmt.Fprintf(&b, "exe:       %s\n", win.ExePath())
	fmt.Fprintf(&b, "pid:       %d\n", os.Getpid())
	fmt.Fprintf(&b, "account:   %s (signed in: %t, %d saved)\n", user.Email, user.SignedIn, len(accounts))
	for _, a := range accounts {
		fmt.Fprintf(&b, "  - %s active=%t quota=%s/%s\n", a.Email, a.Active,
			humanBytes(a.QuotaLeft), humanBytes(a.QuotaMax))
	}

	// The setting and the live session are different things: a mode change
	// applies on the next connection, so a report that printed only the
	// setting disagreed with the session block right below it and sent every
	// reader looking for a bug in the wrong place.
	liveMode := "-"
	if status.State == engine.StateConnected || status.Mode != "" {
		liveMode = modeLabel(status.Mode == "tun")
	}
	fmt.Fprintf(&b, "\nmode:      setting=%s live=%s directFallback=%t systemProxy=%t\n",
		modeLabel(cfg.TunMode), liveMode, cfg.DirectFallback, cfg.SetSystemProxy)
	fmt.Fprintf(&b, "transport: setting=%s live=%s verifyExit=%t\n", cfg.Transport, status.Transport, cfg.VerifyExit)
	fmt.Fprintf(&b, "ports:     socks=%d http=%d\n", cfg.SocksPort, cfg.HTTPPort)
	// The location the attempt used, not the one that is saved. The report
	// says elsewhere that a setting and a live session are different things,
	// and the location was the one line that still printed only the setting
	// while the log right below it named the edge that was used.
	countryCode, cityCode := cfg.CountryCode, cfg.CityCode
	if status.Location != nil {
		if status.Location.CountryCode != "" {
			countryCode = status.Location.CountryCode
		}
		if status.Location.CityCode != "" {
			cityCode = status.Location.CityCode
		}
	}
	fmt.Fprintf(&b, "location:  %s/%s\n", countryCode, cityCode)

	fmt.Fprintf(&b, "\nstate:     %s\n", status.State)
	if status.Error != "" {
		fmt.Fprintf(&b, "error:     %s\n", status.Error)
	}
	if status.Location != nil {
		fmt.Fprintf(&b, "edge:      %s (%s)\n", status.Location.Host, status.Location.CountryName)
	}
	if status.Transport != "" {
		fmt.Fprintf(&b, "session:   %s mode=%s\n", status.Transport, status.Mode)
	}
	if status.ExitIP != "" {
		fmt.Fprintf(&b, "exit:      %s (%s, %d ms)\n", status.ExitIP, status.ExitCountry, status.ExitLatency)
	}
	fmt.Fprintf(&b, "traffic:   up %s, down %s, %d active / %d total connections\n",
		humanBytes(status.BytesUp), humanBytes(status.BytesDown), status.ActiveConns, status.TotalConns)
	if status.QuotaMax > 0 {
		fmt.Fprintf(&b, "quota:     %s of %s left, resets %s\n",
			humanBytes(status.QuotaLeft), humanBytes(status.QuotaMax), status.QuotaReset)
	}

	fmt.Fprintf(&b, "\nadapters:\n%s\n", tun.NetworkSummary())
	// Read the live WinINET state back: in proxy mode this is what decides
	// whether an app is captured at all, and "the setting is on but Windows
	// still has the old server" is a real failure this report should show.
	fmt.Fprintf(&b, "\nsystem proxy: %s\n", win.CurrentProxySummary())

	fmt.Fprintf(&b, "\nlog (last %d lines):\n", diagnosticsLogLines)
	logs := engine.RecentLogs()
	start := 0
	if len(logs) > diagnosticsLogLines {
		start = len(logs) - diagnosticsLogLines
	}
	for _, e := range logs[start:] {
		fmt.Fprintf(&b, "%s %-5s %s\n", e.Time.Format("2006-01-02 15:04:05.000"), e.Level, e.Message)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="todnet-diagnostics.txt"`)
	_, _ = io.WriteString(w, b.String())
}

const diagnosticsLogLines = 400

// modeLabel renders a tunnel mode for the report.
func modeLabel(tun bool) string {
	if tun {
		return "tun"
	}
	return "proxy"
}

// accountsForDiagnostics is a redacted view: addresses and allowance, never
// tokens. Callers must hold s.mu.
func (s *Server) accountsForDiagnostics() []accountView {
	if s.accounts == nil {
		return nil
	}
	out := make([]accountView, 0, len(s.accounts.Accounts))
	for i := range s.accounts.Accounts {
		acc := &s.accounts.Accounts[i]
		out = append(out, accountView{
			Email:     acc.Email,
			Active:    strings.EqualFold(acc.Email, s.accounts.Active),
			QuotaLeft: acc.QuotaLeft,
			QuotaMax:  acc.QuotaMax,
		})
	}
	return out
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	since := int64(0)
	if v := r.URL.Query().Get("since"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &since)
	}

	s.mu.Lock()
	out := make([]logRecord, 0, len(s.logs))
	for _, rec := range s.logs {
		if rec.Seq > since {
			out = append(out, rec)
		}
	}
	seq := s.logSeq
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{"seq": seq, "logs": out})
}

// ShowFunc is invoked by /api/show so a second launch can raise the existing
// window instead of starting a duplicate instance.
var ShowFunc = func() {}

func (s *Server) handleShow(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go ShowFunc()
}

// ReadyFunc is invoked by /api/ready when the interface has drawn its first
// frame. The window is created hidden, so this is what puts it on screen;
// the host keeps its own timeout in case the page never gets here.
var ReadyFunc = func() {}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go ReadyFunc()
}

// QuitFunc is invoked by /api/quit so the host can shut down cleanly.
var QuitFunc = func() {}

// handleElevate restarts the app with an administrator token. TUN mode needs
// one to create the virtual adapter, and asking the user to find "Run as
// administrator" themselves is a worse answer than a button.
func (s *Server) handleElevate(w http.ResponseWriter, r *http.Request) {
	if win.IsElevated() {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "elevated": true})
		return
	}
	if err := win.RelaunchElevated("-wait-instance"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	// Give the new process a moment to start before dropping the single
	// instance lock and the tunnel.
	go func() {
		time.Sleep(2 * time.Second)
		QuitFunc()
	}()
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		time.Sleep(120 * time.Millisecond)
		QuitFunc()
	}()
}

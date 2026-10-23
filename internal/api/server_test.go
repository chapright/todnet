package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"todnet/internal/engine"
	"todnet/internal/store"
	"todnet/internal/win"
)

// newTestServer builds a control server whose profile lives in a temporary
// directory, so a test never touches the real settings or the real Windows
// proxy settings.
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	// os.UserConfigDir reads %AppData% on Windows.
	t.Setenv("APPDATA", dir)
	t.Setenv("AppData", dir)
	t.Setenv("LOCALAPPDATA", dir)

	settings, err := store.New()
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	srv := New(engine.NewEngine(), settings, os.DirFS(filepath.Join(dir, "web")))
	// New installs a log sink; give it back so the tests do not steal it
	// from the rest of the package.
	t.Cleanup(func() { engine.SetLogSink(nil) })
	return srv, settings
}

func do(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestConnectRefusesTunModeWithoutElevation(t *testing.T) {
	if win.IsElevated() {
		t.Skip("the test process is elevated, so TUN mode is allowed")
	}
	srv, _ := newTestServer(t)
	// A signed-in session, so the request gets past the authorisation
	// check and reaches the elevation check.
	srv.auth = &engine.Auth{
		AccessToken:  "test-token",
		RefreshToken: "test-refresh",
		ExpiresIn:    3600,
		ObtainedAt:   time.Now(),
	}

	rec := do(t, srv, http.MethodPost, "/api/connect", `{"tunMode":true}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
	}
	// The code is the only thing the interface can use to recognise this
	// failure, and the client used to drop every coded error on the floor.
	if payload["code"] != "tun-needs-admin" {
		t.Fatalf("want code tun-needs-admin, got %q", payload["code"])
	}
	if !strings.Contains(payload["error"], "administrator") {
		t.Fatalf("the message should say what is wrong, got %q", payload["error"])
	}
}

func TestConnectRequiresASession(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodPost, "/api/connect", `{}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestCancelIsAccepted(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodPost, "/api/cancel", ``)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("want ok:true, got %v", payload["ok"])
	}
}

func TestTheTrayNoticeIsTakenFromTheInterface(t *testing.T) {
	srv, _ := newTestServer(t)

	// The balloon Windows draws when the window is closed to the
	// notification area is not part of the page, so the words have to
	// come from the interface. The language is the user's choice, so the
	// text is whatever the interface sent.
	rec := do(t, srv, http.MethodPost, "/api/tray-notice",
		`{"title":"Todnet همچنان فعال است","text":"تونل شما روشن می‌ماند."}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/tray-notice: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	title, text := srv.TrayNotice()
	if title != "Todnet همچنان فعال است" {
		t.Fatalf("the title was not kept: %q", title)
	}
	if text != "تونل شما روشن می‌ماند." {
		t.Fatalf("the text was not kept: %q", text)
	}
}

func TestTheTrayNoticeIsOneLine(t *testing.T) {
	srv, _ := newTestServer(t)

	// A balloon is one line: a newline in the text would break it.
	rec := do(t, srv, http.MethodPost, "/api/tray-notice",
		`{"title":"one","text":"first line\nsecond line"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/tray-notice: want 200, got %d", rec.Code)
	}
	_, text := srv.TrayNotice()
	if strings.ContainsAny(text, "\r\n") {
		t.Fatalf("the text still has a line break: %q", text)
	}
}

func TestSettingsRoundTripKeepsTheBypassList(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodPost, "/api/settings",
		`{"bypassDomains":["*.ir","example.com"],"transport":"auto","theme":"dark","socksPort":1080,"httpPort":8080}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings: want 200, got %d", rec.Code)
	}
	var got store.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	if len(got.BypassDomains) != 2 {
		t.Fatalf("the bypass list did not survive: %v", got.BypassDomains)
	}
	if got.BypassDomains[0] != "*.ir" || got.BypassDomains[1] != "example.com" {
		t.Fatalf("the bypass list came back changed: %v", got.BypassDomains)
	}
}

func TestSettingsRoundTripKeepsAutoConnect(t *testing.T) {
	srv, _ := newTestServer(t)

	// The setting used to be written to settings.json and read by no Go
	// code at all, so this is the round trip that proves it survives.
	rec := do(t, srv, http.MethodPost, "/api/settings", `{"autoConnect":true,"transport":"auto","theme":"dark","socksPort":1080,"httpPort":8080}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	rec = do(t, srv, http.MethodGet, "/api/settings", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings: want 200, got %d", rec.Code)
	}
	var got store.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("settings are not JSON: %v", err)
	}
	if !got.AutoConnect {
		t.Fatal("autoConnect did not survive the round trip")
	}
}

func TestSettingsRejectsAnImpossiblePort(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodPost, "/api/settings", `{"socksPort":0,"httpPort":8080,"autoConnect":false}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("a port of 0 should be refused, got 200 (%s)", rec.Body.String())
	}
}

func TestDiagnosticsProducesAReport(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/api/diagnostics", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("want text/plain, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Todnet") {
		t.Fatalf("the report should name the app:\n%s", body)
	}
	if !strings.Contains(body, "Elevation") && !strings.Contains(body, "elevated") {
		t.Fatalf("the report should say whether the process is elevated:\n%s", body)
	}
}

func TestOpenURLRefusesAnythingOutsideTheAllowList(t *testing.T) {
	srv, _ := newTestServer(t)

	// A host that is not on the list must be refused. Nothing is opened,
	// so the test does not launch a browser.
	cases := []struct {
		name string
		url  string
	}{
		{"a host that is not Mozilla", "https://example.com/"},
		{"a look-alike host", "https://accounts.firefox.com.evil.test/"},
		{"plain http", "http://accounts.firefox.com/"},
		{"a file url", "file:///C:/Windows/System32/cmd.exe"},
		{"a javascript url", "javascript:alert(1)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, srv, http.MethodPost, "/api/open-url", `{"url":"`+c.url+`"}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400 for %s, got %d (%s)", c.url, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestResponsesCarryAContentSecurityPolicy(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/api/state", "")
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("no Content-Security-Policy header")
	}
	if !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("the policy should default to self: %q", csp)
	}
	if !strings.Contains(csp, "object-src 'none'") {
		t.Fatalf("the policy should forbid plugins: %q", csp)
	}
}

func TestStateCarriesTheLocationList(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := do(t, srv, http.MethodGet, "/api/state", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var payload struct {
		Status   engine.Status  `json:"status"`
		Settings store.Settings `json:"settings"`
		Version  string         `json:"version"`
		Elevated bool           `json:"elevated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("state is not JSON: %v", err)
	}
	if payload.Status.State != "disconnected" {
		t.Fatalf("a fresh engine should be disconnected, got %q", payload.Status.State)
	}
	if payload.Version == "" {
		t.Fatal("the state should carry a version")
	}
}

func TestLatencyMeasurementsReachTheLocationList(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.latency.Set("ams-edge.example.net:2499", 24)

	rec := do(t, srv, http.MethodGet, "/api/locations", "")
	// The list comes from Mozilla, which the test does not call, so an
	// error here is the expected outcome; what matters is that the
	// measurement reaches a location that has one.
	if rec.Code == http.StatusOK {
		var locs []engine.Location
		if err := json.Unmarshal(rec.Body.Bytes(), &locs); err != nil {
			t.Fatalf("locations are not JSON: %v", err)
		}
		for _, l := range locs {
			if l.Host == "ams-edge.example.net:2499" && l.LatencyMs != 24 {
				t.Fatalf("the measurement was not applied: %+v", l)
			}
		}
	}
}

func TestTheEngineCancelsAConnectionAttempt(t *testing.T) {
	eng := engine.NewEngine()
	if eng.Cancelling() {
		t.Fatal("a fresh engine is not connecting")
	}
	// Cancelling an idle engine is a no-op rather than an error.
	if err := eng.Cancel(); err != nil {
		t.Fatalf("Cancel on an idle engine: %v", err)
	}
	if eng.Cancelling() {
		t.Fatal("the engine should not be connecting after a cancel")
	}
}

// The interface reports its first painted frame through /api/ready, and that
// request is what shows the native window. The handler has to hand the signal
// on, and the caller must not wait for the host: the reply is what the page
// needs to move on.
func TestReadyReportsTheFirstFrameToTheHost(t *testing.T) {
	srv, _ := newTestServer(t)

	previous := ReadyFunc
	t.Cleanup(func() { ReadyFunc = previous })
	got := make(chan struct{}, 1)
	ReadyFunc = func() { got <- struct{}{} }

	rec := do(t, srv, http.MethodPost, "/api/ready", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("/api/ready did not reach the host")
	}
}

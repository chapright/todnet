//go:build windows && proxytest

// These tests write to HKCU\...\Internet Settings, which is exactly what a
// malicious proxy hijacker would do. Some antivirus engines quarantine the
// compiled test binary on sight, which would break `go test ./...` for
// everyone, so they are opt-in:
//
//	go test -tags proxytest ./internal/win
//
// Every test restores the original settings before it returns.
package win

import (
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestSystemProxyRoundTrip applies the proxy and then restores it, asserting
// that every value the app touches comes back byte-for-byte. This is the
// difference between "Disconnect works" and "Disconnect silently leaves the
// user's browser pointed at a closed port".
func TestSystemProxyRoundTrip(t *testing.T) {
	before, err := captureProxy()
	if err != nil {
		t.Fatalf("capture before: %v", err)
	}
	if !before.Valid {
		t.Skip("no Internet Settings key to test against")
	}

	// Whatever happens below, put the machine back.
	defer func() {
		p := NewSystemProxy()
		p.snapshot = before
		p.applied = true
		if err := p.restoreLocked(); err != nil {
			t.Fatalf("deferred restore: %v", err)
		}
		clearProxyState()
	}()

	proxy := NewSystemProxy()
	if err := proxy.Apply("127.0.0.1", 18080); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !proxy.Applied() {
		t.Fatal("Applied() = false after a successful Apply")
	}

	// The registry must now point at our loopback proxy.
	mid := readProxy(t)
	if !mid.HadProxyEnable || mid.ProxyEnable != 1 {
		t.Errorf("ProxyEnable = %d (present %v), want 1", mid.ProxyEnable, mid.HadProxyEnable)
	}
	if mid.ProxyServer != "127.0.0.1:18080" {
		t.Errorf("ProxyServer = %q, want 127.0.0.1:18080", mid.ProxyServer)
	}
	if mid.HadAutoConfig {
		t.Errorf("AutoConfigURL still present (%q); PAC would override the proxy", mid.AutoConfigURL)
	}

	// A backup file must exist for crash recovery while the tunnel is up.
	if _, err := os.Stat(proxyStatePath()); err != nil {
		t.Errorf("proxy backup file missing while applied: %v", err)
	}

	if err := proxy.Restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if proxy.Applied() {
		t.Error("Applied() = true after Restore")
	}

	after := readProxy(t)
	assertSame(t, "ProxyEnable", before.HadProxyEnable, before.ProxyEnable, after.HadProxyEnable, after.ProxyEnable)
	assertSameString(t, "ProxyServer", before.HadProxyServer, before.ProxyServer, after.HadProxyServer, after.ProxyServer)
	assertSameString(t, "ProxyOverride", before.HadProxyOverride, before.ProxyOverride, after.HadProxyOverride, after.ProxyOverride)
	assertSameString(t, "AutoConfigURL", before.HadAutoConfig, before.AutoConfigURL, after.HadAutoConfig, after.AutoConfigURL)

	if _, err := os.Stat(proxyStatePath()); !os.IsNotExist(err) {
		t.Errorf("proxy backup file should be gone after a clean restore, stat err = %v", err)
	}
}

// TestRecoverProxyStateFromStaleBackup simulates the app being killed mid-tunnel:
// a backup file exists, the registry holds our proxy, and startup must undo it.
func TestRecoverProxyStateFromStaleBackup(t *testing.T) {
	before, err := captureProxy()
	if err != nil || !before.Valid {
		t.Skip("no Internet Settings key to test against")
	}
	defer func() {
		p := NewSystemProxy()
		p.snapshot = before
		p.applied = true
		_ = p.restoreLocked()
		clearProxyState()
	}()

	// Pretend a crashed run left the proxy enabled and a backup on disk.
	stale := ProxySnapshot{
		Valid:            true,
		ProxyEnable:      0,
		HadProxyEnable:   true,
		ProxyServer:      "127.0.0.1:19999",
		HadProxyServer:   true,
		ProxyOverride:    "<local>",
		HadProxyOverride: true,
	}
	if err := saveProxyState(stale); err != nil {
		t.Fatalf("saveProxyState: %v", err)
	}
	if err := writeRawProxy(t, "127.0.0.1:18081"); err != nil {
		t.Fatalf("priming registry: %v", err)
	}

	restored, err := RecoverProxyState()
	if err != nil {
		t.Fatalf("RecoverProxyState: %v", err)
	}
	if !restored {
		t.Fatal("RecoverProxyState reported nothing to restore")
	}

	got := readProxy(t)
	if got.ProxyServer != "127.0.0.1:19999" {
		t.Errorf("ProxyServer = %q, want the saved 127.0.0.1:19999", got.ProxyServer)
	}
	if got.ProxyEnable != 0 {
		t.Errorf("ProxyEnable = %d, want 0 (the saved disabled state)", got.ProxyEnable)
	}
	if _, err := os.Stat(proxyStatePath()); !os.IsNotExist(err) {
		t.Error("backup file should be consumed by recovery")
	}

	// A second call must be a no-op rather than an error.
	again, err := RecoverProxyState()
	if err != nil {
		t.Fatalf("second RecoverProxyState: %v", err)
	}
	if again {
		t.Error("second RecoverProxyState should report nothing to do")
	}
}

func readProxy(t *testing.T) ProxySnapshot {
	t.Helper()
	snap, err := captureProxy()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	return snap
}

func writeRawProxy(t *testing.T, server string) error {
	t.Helper()
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue("ProxyServer", server); err != nil {
		return err
	}
	return key.SetDWordValue("ProxyEnable", 1)
}

func assertSame(t *testing.T, name string, wantPresent bool, want uint32, gotPresent bool, got uint32) {
	t.Helper()
	if wantPresent != gotPresent || want != got {
		t.Errorf("%s = %d (present %v), want %d (present %v)", name, got, gotPresent, want, wantPresent)
	}
}

func assertSameString(t *testing.T, name string, wantPresent bool, want string, gotPresent bool, got string) {
	t.Helper()
	if wantPresent != gotPresent || want != got {
		t.Errorf("%s = %q (present %v), want %q (present %v)", name, got, gotPresent, want, wantPresent)
	}
}

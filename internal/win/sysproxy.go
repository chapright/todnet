//go:build windows

package win

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"

	"todnet/internal/appdir"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// proxyStateFile records the pre-tunnel proxy settings on disk for as long as
// the tunnel owns them. If the process dies without restoring (crash, power
// loss, killed from Task Manager) the next launch puts the user's original
// settings back instead of leaving their internet pointed at a dead port.
const proxyStateFile = "proxy-restore.json"

var (
	wininet               = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOption = wininet.NewProc("InternetSetOptionW")
)

const (
	internetOptionRefresh         = 37
	internetOptionSettingsChanged = 39
)

// ProxySnapshot captures the pre-existing Internet Settings so they can be
// restored exactly when the tunnel stops. Exported fields are what gets
// persisted to disk for crash recovery.
type ProxySnapshot struct {
	Valid bool `json:"valid"`

	ProxyEnable    uint32 `json:"proxyEnable"`
	HadProxyEnable bool   `json:"hadProxyEnable"`

	ProxyServer    string `json:"proxyServer"`
	HadProxyServer bool   `json:"hadProxyServer"`

	ProxyOverride    string `json:"proxyOverride"`
	HadProxyOverride bool   `json:"hadProxyOverride"`

	AutoConfigURL string `json:"autoConfigURL"`
	HadAutoConfig bool   `json:"hadAutoConfig"`
}

// SystemProxy owns the machine's WinINET proxy configuration while a tunnel
// is up.
type SystemProxy struct {
	mu       sync.Mutex
	snapshot ProxySnapshot
	applied  bool
}

// NewSystemProxy returns a controller for the user's proxy settings.
func NewSystemProxy() *SystemProxy { return &SystemProxy{} }

// Apply points WinINET at the local HTTP proxy. The previous configuration is
// saved first (in memory and on disk) so Restore can put it back verbatim.
func (p *SystemProxy) Apply(host string, port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.applied {
		// Already ours; just update the server string.
		return p.writeProxy(host, port)
	}

	snap, err := captureProxy()
	if err != nil {
		return fmt.Errorf("reading current proxy settings: %w", err)
	}
	p.snapshot = snap

	// Persist before touching the registry: if we die between these two steps
	// the recovery file still describes the original state.
	if err := saveProxyState(snap); err != nil {
		return fmt.Errorf("saving proxy backup: %w", err)
	}

	if err := p.writeProxy(host, port); err != nil {
		// Undo whatever half-applied, so a failed connect never strands the
		// user on a proxy that is not running.
		_ = p.restoreLocked()
		return err
	}
	p.applied = true
	return nil
}

func (p *SystemProxy) writeProxy(host string, port int) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening Internet Settings: %w", err)
	}
	defer key.Close()

	server := fmt.Sprintf("%s:%d", host, port)
	if err := key.SetStringValue("ProxyServer", server); err != nil {
		return fmt.Errorf("setting ProxyServer: %w", err)
	}
	if err := key.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("enabling proxy: %w", err)
	}
	// Keep loopback and local hostnames direct so the app itself and local
	// development servers keep working.
	if err := key.SetStringValue("ProxyOverride", "<local>;127.*;10.*;172.16.*;172.17.*;172.18.*;172.19.*;172.20.*;172.21.*;172.22.*;172.23.*;172.24.*;172.25.*;172.26.*;172.27.*;172.28.*;172.29.*;172.30.*;172.31.*;192.168.*"); err != nil {
		return fmt.Errorf("setting ProxyOverride: %w", err)
	}
	// A PAC script takes precedence over ProxyServer, so it has to go while
	// the tunnel is active.
	if err := key.DeleteValue("AutoConfigURL"); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("clearing AutoConfigURL: %w", err)
	}

	notifyProxyChanged()
	return nil
}

// Restore puts the saved Internet Settings back.
func (p *SystemProxy) Restore() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.restoreLocked()
}

func (p *SystemProxy) restoreLocked() error {
	if !p.snapshot.Valid {
		p.applied = false
		clearProxyState()
		return nil
	}

	key, _, err := registry.CreateKey(registry.CURRENT_USER, internetSettingsKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening Internet Settings: %w", err)
	}
	defer key.Close()

	if p.snapshot.HadProxyEnable {
		_ = key.SetDWordValue("ProxyEnable", p.snapshot.ProxyEnable)
	} else {
		_ = key.DeleteValue("ProxyEnable")
	}
	if p.snapshot.HadProxyServer {
		_ = key.SetStringValue("ProxyServer", p.snapshot.ProxyServer)
	} else {
		_ = key.DeleteValue("ProxyServer")
	}
	if p.snapshot.HadProxyOverride {
		_ = key.SetStringValue("ProxyOverride", p.snapshot.ProxyOverride)
	} else {
		_ = key.DeleteValue("ProxyOverride")
	}
	if p.snapshot.HadAutoConfig {
		_ = key.SetStringValue("AutoConfigURL", p.snapshot.AutoConfigURL)
	} else {
		_ = key.DeleteValue("AutoConfigURL")
	}

	notifyProxyChanged()
	p.applied = false
	clearProxyState()
	return nil
}

// Applied reports whether this controller currently owns the proxy settings.
func (p *SystemProxy) Applied() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.applied
}

// RecoverProxyState is called once at startup. If a previous run left the
// proxy applied (crash or forced kill), the saved settings are restored so the
// user is never left without working internet.
func RecoverProxyState() (bool, error) {
	path := proxyStatePath()
	if path == "" {
		return false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	var snap ProxySnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		// Unreadable backup is worse than useless; drop it.
		clearProxyState()
		return false, err
	}
	if !snap.Valid {
		clearProxyState()
		return false, nil
	}

	p := NewSystemProxy()
	p.snapshot = snap
	p.applied = true
	if err := p.restoreLocked(); err != nil {
		return false, err
	}
	return true, nil
}

func proxyStatePath() string {
	path, err := appdir.Path(proxyStateFile)
	if err != nil {
		return ""
	}
	return path
}

func saveProxyState(snap ProxySnapshot) error {
	path := proxyStatePath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	// Write-then-rename so a crash mid-write cannot leave a truncated file
	// that would be restored as garbage.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func clearProxyState() {
	if path := proxyStatePath(); path != "" {
		_ = os.Remove(path)
	}
}

func captureProxy() (ProxySnapshot, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return ProxySnapshot{Valid: true}, nil
		}
		return ProxySnapshot{}, err
	}
	defer key.Close()

	snap := ProxySnapshot{Valid: true}
	if v, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
		snap.ProxyEnable = uint32(v)
		snap.HadProxyEnable = true
	}
	if v, _, err := key.GetStringValue("ProxyServer"); err == nil {
		snap.ProxyServer = v
		snap.HadProxyServer = true
	}
	if v, _, err := key.GetStringValue("ProxyOverride"); err == nil {
		snap.ProxyOverride = v
		snap.HadProxyOverride = true
	}
	if v, _, err := key.GetStringValue("AutoConfigURL"); err == nil {
		snap.AutoConfigURL = v
		snap.HadAutoConfig = true
	}
	return snap, nil
}

// notifyProxyChanged tells WinINET and running apps to re-read the settings.
// Without this the registry change is invisible until the next sign-in.
func notifyProxyChanged() {
	_, _, _ = procInternetSetOption.Call(0, internetOptionSettingsChanged, 0, 0)
	_, _, _ = procInternetSetOption.Call(0, internetOptionRefresh, 0, 0)
}

// CurrentProxySummary reports the live WinINET proxy configuration. A
// diagnostics report needs the value Windows holds right now, not the value
// this process believes it wrote: "system proxy enabled" is the difference
// between an app being captured and an app going direct.
func CurrentProxySummary() string {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return "no Internet Settings key"
		}
		return "unreadable: " + err.Error()
	}
	defer key.Close()

	var b strings.Builder
	if v, _, err := key.GetIntegerValue("ProxyEnable"); err == nil {
		if v != 0 {
			b.WriteString("enabled")
		} else {
			b.WriteString("disabled")
		}
	} else {
		b.WriteString("disabled (unset)")
	}
	if v, _, err := key.GetStringValue("ProxyServer"); err == nil && v != "" {
		fmt.Fprintf(&b, " server=%s", v)
	} else {
		b.WriteString(" server=-")
	}
	if v, _, err := key.GetStringValue("AutoConfigURL"); err == nil && v != "" {
		// A PAC script wins over ProxyServer, so its presence changes the
		// meaning of everything above it.
		fmt.Fprintf(&b, " pac=%s (overrides server)", v)
	}
	return b.String()
}

// BroadcastSettingsChange notifies top-level windows that Internet Settings
// changed, so apps that cache proxy state pick it up immediately.
func BroadcastSettingsChange() {
	const (
		hwndBroadcast   = 0xffff
		wmSettingChange = 0x001A
		smtoAbortIfHung = 0x0002
	)
	user32 := syscall.NewLazyDLL("user32.dll")
	sendMessageTimeout := user32.NewProc("SendMessageTimeoutW")

	section, _ := syscall.UTF16PtrFromString("Software\\Microsoft\\Windows\\CurrentVersion\\Internet Settings")
	var result uintptr
	_, _, _ = sendMessageTimeout.Call(
		hwndBroadcast,
		wmSettingChange,
		0,
		uintptr(unsafe.Pointer(section)),
		smtoAbortIfHung,
		3000,
		uintptr(unsafe.Pointer(&result)),
	)
}

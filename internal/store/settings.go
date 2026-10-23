package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"todnet/internal/appdir"
)

// The languages the interface ships with. The code is the BCP 47 tag, so it
// can be handed straight to the browser.
const (
	// LanguageEnglish is the default: the interface's first language.
	LanguageEnglish = "en"
	// LanguagePersian is the right-to-left locale. "fa" is the tag Persian
	// is normally written with.
	LanguagePersian = "fa"
)

// Settings is the persisted user configuration.
type Settings struct {
	Theme          string `json:"theme"`          // "system" | "light" | "dark"
	Language       string `json:"language"`       // "en" | "fa"
	CountryCode    string `json:"countryCode"`    // preferred country, empty = first available
	CityCode       string `json:"cityCode"`       // preferred city, empty = first in country
	Transport      string `json:"transport"`      // "auto" | "h2" | "h3"
	VerifyExit     bool   `json:"verifyExit"`     // probe the real exit IP after connecting
	SocksPort      int    `json:"socksPort"`      // local SOCKS5 port
	HTTPPort       int    `json:"httpPort"`       // local HTTP proxy port
	SetSystemProxy bool   `json:"setSystemProxy"` // point Windows at the HTTP proxy
	TunMode        bool   `json:"tunMode"`        // capture all traffic with a virtual adapter
	DirectFallback bool   `json:"directFallback"` // connect directly when the edge refuses a host
	// BypassDomains are the host patterns that skip the tunnel. "*.example.com"
	// is every host under that domain.
	BypassDomains   []string `json:"bypassDomains"`
	LaunchAtStartup bool     `json:"launchAtStartup"`
	StartMinimized  bool     `json:"startMinimized"`
	VerboseLogs     bool     `json:"verboseLogs"`
	AutoConnect     bool     `json:"autoConnect"`
}

// The limits on the bypass list. A list is a convenience, not a
// configuration file: a hundred patterns is already more than anyone
// needs, and an unbounded list would be a way to fill settings.json.
const (
	MaxBypassDomains   = 100
	MaxBypassDomainLen = 200
)

// Defaults returns the settings a fresh install starts with.
func Defaults() Settings {
	return Settings{
		Theme:           "system",
		Language:        LanguageEnglish,
		Transport:       "auto",
		VerifyExit:      true,
		SocksPort:       1080,
		HTTPPort:        8080,
		SetSystemProxy:  true,
		LaunchAtStartup: false,
		StartMinimized:  false,
		VerboseLogs:     false,
		AutoConnect:     false,
	}
}

// Store persists settings as JSON under %APPDATA%\Todnet.
type Store struct {
	mu      sync.Mutex
	path    string
	cur     Settings
	loadErr error
}

// New loads settings from disk, falling back to defaults when the file is
// missing or unreadable.
func New() (*Store, error) {
	base, err := appdir.Dir()
	if err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(base, "settings.json"), cur: Defaults()}
	s.load()
	return s, nil
}

// Dir returns the per-user data directory.
func (s *Store) Dir() string { return filepath.Dir(s.path) }

// LoadError returns the error from the last load, if the settings file
// could not be read.
//
// A file that cannot be parsed falls back to the defaults, which is the
// right thing to do, but it must not be silent: the user would see
// every setting of theirs ignored with no explanation.
func (s *Store) LoadError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		// A missing file is the normal first run, not a problem.
		return
	}
	// A file written by a text editor can start with a byte-order mark.
	// json.Unmarshal does not skip it, and the whole file would be
	// ignored — every setting of the user's silently back to its default.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	var parsed Settings
	if err := json.Unmarshal(data, &parsed); err != nil {
		s.mu.Lock()
		s.loadErr = fmt.Errorf("reading %s: %w", s.path, err)
		s.mu.Unlock()
		return
	}
	// Fill any zero-valued fields a older config may be missing.
	merged := Defaults()
	merged.Theme = parsed.Theme
	merged.Language = parsed.Language
	merged.CountryCode = parsed.CountryCode
	merged.CityCode = parsed.CityCode
	if parsed.Transport != "" {
		merged.Transport = parsed.Transport
	}
	merged.VerifyExit = parsed.VerifyExit
	if parsed.SocksPort > 0 {
		merged.SocksPort = parsed.SocksPort
	}
	if parsed.HTTPPort > 0 {
		merged.HTTPPort = parsed.HTTPPort
	}
	merged.SetSystemProxy = parsed.SetSystemProxy
	merged.TunMode = parsed.TunMode
	merged.DirectFallback = parsed.DirectFallback
	merged.BypassDomains = cleanBypassDomains(parsed.BypassDomains)
	merged.LaunchAtStartup = parsed.LaunchAtStartup
	merged.StartMinimized = parsed.StartMinimized
	merged.VerboseLogs = parsed.VerboseLogs
	merged.AutoConnect = parsed.AutoConnect
	if merged.Theme == "" {
		merged.Theme = "system"
	}
	if merged.Language == "" {
		merged.Language = LanguageEnglish
	}
	if merged.Transport == "" {
		merged.Transport = "auto"
	}
	s.cur = merged
}

// Get returns the current settings.
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Save validates and persists new settings.
func (s *Store) Save(next Settings) error {
	if next.SocksPort < 1 || next.SocksPort > 65535 {
		return errors.New("SOCKS5 port must be between 1 and 65535")
	}
	if next.HTTPPort < 1 || next.HTTPPort > 65535 {
		return errors.New("HTTP proxy port must be between 1 and 65535")
	}
	if next.SocksPort == next.HTTPPort {
		return errors.New("SOCKS5 and HTTP proxy ports must differ")
	}
	switch next.Theme {
	case "system", "light", "dark":
	default:
		return errors.New("theme must be system, light or dark")
	}
	switch next.Language {
	case "", LanguageEnglish, LanguagePersian:
	default:
		return errors.New("language must be en or fa")
	}
	if next.Language == "" {
		next.Language = LanguageEnglish
	}
	next.BypassDomains = cleanBypassDomains(next.BypassDomains)
	switch next.Transport {
	case "auto", "h2", "h3":
	default:
		return errors.New("transport must be auto, h2 or h3")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeAtomic(s.path, next); err != nil {
		return err
	}
	s.cur = next
	return nil
}

// cleanBypassDomains trims the list, drops the blank entries and keeps it
// within the limits. A pattern that is too long or a list that is too
// long is not an error: the entry is dropped, and the rest of the list
// still works.
func cleanBypassDomains(in []string) []string {
	out := make([]string, 0, len(in))
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" || len(p) > MaxBypassDomainLen {
			continue
		}
		out = append(out, p)
		if len(out) == MaxBypassDomains {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func writeAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(path)
		return os.Rename(tmpName, path)
	}
	return nil
}

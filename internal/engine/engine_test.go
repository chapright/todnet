package engine

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"todnet/internal/vpnclient"
)

// directOpener stands in for the VPN controller: it dials the target
// directly, which lets the proxy servers be tested without an upstream edge.
type directOpener struct{}

func (directOpener) OpenTunnel(authority string) (net.Conn, error) {
	return net.DialTimeout("tcp", authority, 5*time.Second)
}

func startEchoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// socksConnect performs a minimal SOCKS5 handshake and returns the live conn.
func socksConnect(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("greeting: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("greeting reply: %v", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("unexpected greeting reply %v", resp)
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split target: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parse port: %v", err)
	}

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, []byte(host)...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("connect request: %v", err)
	}

	// Reply: VER REP RSV ATYP BND.ADDR BND.PORT
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("connect reply: %v", err)
	}
	if head[1] != 0x00 {
		t.Fatalf("CONNECT rejected with reply code %d", head[1])
	}
	switch head[3] {
	case 0x01:
		_, _ = io.ReadFull(conn, make([]byte, 4+2))
	case 0x04:
		_, _ = io.ReadFull(conn, make([]byte, 16+2))
	case 0x03:
		var l [1]byte
		_, _ = io.ReadFull(conn, l[:])
		_, _ = io.ReadFull(conn, make([]byte, int(l[0])+2))
	}
	return conn
}

func TestSocks5CarriesTraffic(t *testing.T) {
	echo := startEchoServer(t)
	stats := &Stats{}
	srv, err := NewSocksServer("127.0.0.1:0", directOpener{}, stats)
	if err != nil {
		t.Fatalf("start socks: %v", err)
	}
	defer srv.Close()

	conn := socksConnect(t, srv.Addr().String(), echo.Addr().String())
	defer conn.Close()

	payload := []byte("hello through the tunnel")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %q want %q", got, payload)
	}

	deadline := time.Now().Add(2 * time.Second)
	for stats.BytesUp.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stats.BytesUp.Load() == 0 {
		t.Fatal("byte counters were not updated")
	}
}

func TestSocks5RejectsUDPAssociate(t *testing.T) {
	srv, err := NewSocksServer("127.0.0.1:0", directOpener{}, &Stats{})
	if err != nil {
		t.Fatalf("start socks: %v", err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_, _ = conn.Write([]byte{0x05, 0x01, 0x00})
	_, _ = io.ReadFull(conn, make([]byte, 2))

	// UDP ASSOCIATE
	_, _ = conn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if head[1] != socksReplyCmdUnsup {
		t.Fatalf("expected command-unsupported reply, got %d", head[1])
	}
}

func TestHTTPConnectCarriesTraffic(t *testing.T) {
	echo := startEchoServer(t)
	srv, err := NewHTTPProxyServer("127.0.0.1:0", directOpener{}, &Stats{})
	if err != nil {
		t.Fatalf("start http proxy: %v", err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr(), echo.Addr())
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("write connect: %v", err)
	}

	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("unexpected CONNECT status: %q", status)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}

	payload := []byte("tls-like bytes")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echo mismatch: got %q want %q", got, payload)
	}
}

func TestHTTPPlainRequest(t *testing.T) {
	origin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "ok")
		_, _ = io.WriteString(w, "origin reached "+r.URL.Path)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = origin.Serve(ln) }()
	defer origin.Close()

	srv, err := NewHTTPProxyServer("127.0.0.1:0", directOpener{}, &Stats{})
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	req := fmt.Sprintf("GET http://%s/hello HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", ln.Addr(), ln.Addr())
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	body, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "origin reached /hello") {
		t.Fatalf("origin response not relayed, got: %s", string(body))
	}
	if !strings.Contains(string(body), "X-Test: ok") {
		t.Fatalf("response headers not relayed, got: %s", string(body))
	}
}

func TestTunnelWriteBufferRoundTrip(t *testing.T) {
	// The ring is smaller than the payload on purpose: writes must block until
	// the reader drains, so the producer runs in its own goroutine.
	b := newTunnelWriteBuffer(16)
	payload := []byte("0123456789abcdefghij")
	writeDone := make(chan error, 1)
	go func() {
		_, err := b.Write(payload)
		writeDone <- err
	}()

	out := make([]byte, 0, len(payload))
	buf := make([]byte, 7)
	for len(out) < len(payload) {
		n, err := b.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	if err := <-writeDone; err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = b.Close()
	if !bytes.Equal(out, payload) {
		t.Fatalf("got %q want %q", out, payload)
	}
}

func TestParseTrace(t *testing.T) {
	body := []byte("fl=1f2\nh=www.cloudflare.com\nip=203.0.113.7\nts=1\nloc=DE\n")
	ip, country := parseTrace(body)
	if ip != "203.0.113.7" || country != "DE" {
		t.Fatalf("got ip=%q country=%q", ip, country)
	}

	jsonBody := []byte(`{"ip":"198.51.100.9","country_code":"NL"}`)
	ip, country = parseTrace(jsonBody)
	if ip != "198.51.100.9" || country != "NL" {
		t.Fatalf("json: got ip=%q country=%q", ip, country)
	}
}

func TestBuildLocationsAndPick(t *testing.T) {
	countries := []vpnclient.Country{
		{
			Name: "Germany", Code: "DE",
			Cities: []vpnclient.City{{
				Name: "Berlin", Code: "ber",
				Servers: []vpnclient.Server{
					{Quarantined: true, Hostname: "bad.example", Port: 443},
					{
						Hostname: "edge.example", Port: 443,
						Protocols: []vpnclient.Protocol{
							{Name: "masque", Host: "m.example", Port: 443},
							{Name: "connect", Host: "edge.example", Port: 443},
						},
					},
				},
			}},
		},
		{
			Name: "Japan", Code: "JP",
			Cities: []vpnclient.City{{
				Name: "Tokyo", Code: "tyo",
				Servers: []vpnclient.Server{{Hostname: "jp.example", Port: 443}},
			}},
		},
	}

	locs := BuildLocations(countries)
	if len(locs) != 2 {
		t.Fatalf("expected 2 locations, got %d", len(locs))
	}
	if locs[0].CountryName != "Germany" {
		t.Fatalf("expected sorted by country, got %s", locs[0].CountryName)
	}
	if locs[0].Host != "edge.example:443" {
		t.Fatalf("quarantined server should be skipped, got %s", locs[0].Host)
	}

	picked, err := PickLocation(countries, "jp", "")
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if picked.CountryCode != "JP" || picked.Host != "jp.example:443" {
		t.Fatalf("unexpected pick: %+v", picked)
	}

	picked, err = PickLocation(countries, "DE", "ber")
	if err != nil {
		t.Fatalf("pick city: %v", err)
	}
	if picked.CityCode != "ber" {
		t.Fatalf("unexpected city: %+v", picked)
	}

	if _, err := PickLocation(countries, "ZZ", ""); err == nil {
		t.Fatal("expected error for unknown country")
	}
}

func TestParseHostPort(t *testing.T) {
	cases := map[string]string{
		"example.com":      "example.com:443",
		"example.com:8443": "example.com:8443",
		"1.2.3.4:80":       "1.2.3.4:80",
	}
	for in, want := range cases {
		got, err := ParseHostPort(in, 443)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestResolveTransport(t *testing.T) {
	mustURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		return u
	}

	cases := []struct {
		name string
		pref string
		url  string
		want bool
	}{
		// Auto prefers TCP: the MASQUE edges answer HTTP/2 on 2499 as well as
		// QUIC, and TCP survives networks that drop UDP. Verified with a TLS
		// probe that negotiates "h2" against the published port.
		{"masque auto", "auto", "https://ysbk106.m1.fastly-masque.net:2499", false},
		{"port 2499 auto", "auto", "https://edge.example.net:2499", false},
		{"legacy connect auto", "auto", "https://edge.example.net:443", false},
		{"forced h3", "h3", "https://edge.example.net:443", true},
		{"forced h2", "h2", "https://ysbk106.m1.fastly-masque.net:2499", false},
	}
	for _, tc := range cases {
		got := resolveTransport(tc.pref, mustURL(tc.url))
		if got != tc.want {
			t.Errorf("%s: got h3=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsTimeout(t *testing.T) {
	if !isTimeout(errors.New("timeout: no recent network activity")) {
		t.Fatal("quic-go timeout string should be detected")
	}
	if isTimeout(errors.New("connection refused")) {
		t.Fatal("non-timeout should not be reported as timeout")
	}
	if isTimeout(nil) {
		t.Fatal("nil is not a timeout")
	}
}

func TestQuotaExhausted(t *testing.T) {
	if isQuotaExhausted(&vpnclient.ProxyPassInfo{QuotaLeft: "0"}) != true {
		t.Fatal("zero quota should be exhausted")
	}
	if isQuotaExhausted(&vpnclient.ProxyPassInfo{QuotaLeft: "1024"}) != false {
		t.Fatal("positive quota should not be exhausted")
	}
	if isQuotaExhausted(&vpnclient.ProxyPassInfo{}) != false {
		t.Fatal("missing quota header should not be exhausted")
	}
}

func TestRenewedPassUpdatesTheLiveAllowance(t *testing.T) {
	e := NewEngine()
	e.mu.Lock()
	e.status = Status{
		State:      StateConnected,
		QuotaLeft:  500,
		QuotaMax:   1000,
		QuotaReset: "2026-11-01T00:00:00Z",
	}
	e.mu.Unlock()

	e.notePassQuota(&vpnclient.ProxyPassInfo{
		QuotaLeft: "250", QuotaMax: "1000", QuotaReset: "2026-12-01T00:00:00Z",
	})
	st := e.Status()
	if st.QuotaLeft != 250 || st.QuotaMax != 1000 || st.QuotaReset != "2026-12-01T00:00:00Z" {
		t.Fatalf("renewed pass not applied: %+v", st)
	}

	// A header the server omitted must not erase a value that is still valid.
	e.notePassQuota(&vpnclient.ProxyPassInfo{QuotaLeft: "200"})
	st = e.Status()
	if st.QuotaLeft != 200 || st.QuotaMax != 1000 || st.QuotaReset != "2026-12-01T00:00:00Z" {
		t.Fatalf("partial pass erased the rest: %+v", st)
	}

	// A renewal that lands after a disconnect must not resurrect a balance.
	e.mu.Lock()
	e.status = Status{State: StateDisconnected}
	e.mu.Unlock()
	e.notePassQuota(&vpnclient.ProxyPassInfo{QuotaLeft: "1", QuotaMax: "1000"})
	if st := e.Status(); st.QuotaLeft != 0 || st.QuotaMax != 0 {
		t.Fatalf("quota updated while disconnected: %+v", st)
	}
}

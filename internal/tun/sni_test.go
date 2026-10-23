//go:build windows

package tun

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// buildClientHello assembles a minimal but structurally valid TLS ClientHello
// carrying serverName, so the parser is exercised against real framing rather
// than a hand-picked byte blob.
func buildClientHello(serverName string) []byte {
	var exts []byte

	if serverName != "" {
		name := []byte(serverName)
		entry := make([]byte, 0, 3+len(name))
		entry = append(entry, 0x00) // host_name
		entry = binary.BigEndian.AppendUint16(entry, uint16(len(name)))
		entry = append(entry, name...)

		list := binary.BigEndian.AppendUint16(nil, uint16(len(entry)))
		list = append(list, entry...)

		exts = binary.BigEndian.AppendUint16(exts, 0x0000) // server_name
		exts = binary.BigEndian.AppendUint16(exts, uint16(len(list)))
		exts = append(exts, list...)
	}

	// supported_versions, to look like a modern client.
	exts = binary.BigEndian.AppendUint16(exts, 0x002b)
	exts = binary.BigEndian.AppendUint16(exts, 3)
	exts = append(exts, 2, 0x03, 0x04)

	var body []byte
	body = append(body, 0x03, 0x03)               // client_version TLS1.2
	body = append(body, make([]byte, 32)...)      // random
	body = append(body, 32)                       // session id length
	body = append(body, make([]byte, 32)...)      // session id
	body = binary.BigEndian.AppendUint16(body, 4) // cipher suites length
	body = append(body, 0x13, 0x01, 0x13, 0x02)   // TLS_AES_128/256_GCM
	body = append(body, 1, 0x00)                  // compression methods
	body = binary.BigEndian.AppendUint16(body, uint16(len(exts)))
	body = append(body, exts...)

	handshake := []byte{0x01}
	handshake = append(handshake, byte(len(body)>>16), byte(len(body)>>8), byte(len(body)))
	handshake = append(handshake, body...)

	record := []byte{0x16, 0x03, 0x01}
	record = binary.BigEndian.AppendUint16(record, uint16(len(handshake)))
	return append(record, handshake...)
}

func TestParseClientHelloServerName(t *testing.T) {
	cases := []struct {
		name string
		sni  string
		want string
		ok   bool
	}{
		{name: "plain host", sni: "api.workbuddy.cn", want: "api.workbuddy.cn", ok: true},
		{name: "single label", sni: "localhost", want: "localhost", ok: true},
		{name: "no sni extension", sni: "", want: "", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseClientHelloServerName(buildClientHello(tc.sni))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestParseClientHelloRejectsGarbage(t *testing.T) {
	if _, ok := parseClientHelloServerName(nil); ok {
		t.Fatal("empty buffer parsed as a ClientHello")
	}
	if _, ok := parseClientHelloServerName([]byte("GET / HTTP/1.1\r\n\r\n")); ok {
		t.Fatal("plain HTTP parsed as a ClientHello")
	}
	// Truncating a valid hello must fail cleanly rather than panic: this is
	// attacker-controlled input on a live socket.
	full := buildClientHello("example.com")
	for _, n := range []int{1, 4, 6, 40, len(full) - 1} {
		if _, ok := parseClientHelloServerName(full[:n]); ok {
			t.Fatalf("truncated hello (%d bytes) parsed successfully", n)
		}
	}
}

// The peek must never lose bytes: whatever it consumed has to come back out of
// the returned connection, or the TLS handshake breaks.
func TestPeekServerNameReplaysEveryByte(t *testing.T) {
	hello := buildClientHello("speed.cloudflare.com")

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = server.Write(hello)
		// Hold the pipe open so the peek stops on its own reasoning, not on
		// an EOF from the writer.
		time.Sleep(200 * time.Millisecond)
	}()

	name, wrapped := peekServerName(client, 150*time.Millisecond)
	if name != "speed.cloudflare.com" {
		t.Fatalf("sni = %q, want speed.cloudflare.com", name)
	}

	got := make([]byte, len(hello))
	_ = wrapped.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(wrapped, got); err != nil {
		t.Fatalf("replaying buffered bytes: %v", err)
	}
	if !bytes.Equal(got, hello) {
		t.Fatalf("replayed bytes differ from what the client sent")
	}
}

// A non-TLS first byte must return immediately instead of waiting out the
// deadline — server-first protocols would otherwise take a 250ms penalty.
func TestPeekServerNameIgnoresPlainTraffic(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := []byte("EHLO mail.example.com\r\n")
	go func() { _, _ = server.Write(payload) }()

	start := time.Now()
	name, wrapped := peekServerName(client, 2*time.Second)
	if name != "" {
		t.Fatalf("plain traffic produced an SNI: %q", name)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("plain traffic waited %s; it should return at once", elapsed)
	}

	got := make([]byte, len(payload))
	_ = wrapped.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.ReadFull(wrapped, got); err != nil {
		t.Fatalf("replaying plain bytes: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("plain bytes were lost by the peek")
	}
}

func TestTLSFirstPort(t *testing.T) {
	for _, p := range []uint16{443, 8443} {
		if !tlsFirstPort(p) {
			t.Fatalf("port %d should be treated as TLS-first", p)
		}
	}
	for _, p := range []uint16{25, 53, 80, 110, 587, 993, 5228} {
		if tlsFirstPort(p) {
			t.Fatalf("port %d should not be peeked", p)
		}
	}
}

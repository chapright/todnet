// Diagnostic: what does an edge speak on TCP? Reports the negotiated ALPN for
// each candidate port, which tells us whether the legacy HTTP/2 CONNECT path
// is still available even though the published port is QUIC-only.
//
//	go run ./tools/tlsprobe <host> [ports...]
package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	host := os.Args[1]
	ports := os.Args[2:]
	if len(ports) == 0 {
		ports = []string{"443", "2499"}
	}
	for _, port := range ports {
		probe(host, port)
	}
}

func probe(host, port string) {
	addr := net.JoinHostPort(host, port)
	start := time.Now()
	raw, err := net.DialTimeout("tcp", addr, 6*time.Second)
	if err != nil {
		fmt.Printf("%-46s TCP connect failed: %v\n", addr, err)
		return
	}
	defer raw.Close()

	// Offer both, so the server's preference is visible.
	conn := tls.Client(raw, &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1", "h3"},
	})
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	if err := conn.Handshake(); err != nil {
		fmt.Printf("%-46s TCP ok, TLS failed: %v\n", addr, err)
		return
	}
	st := conn.ConnectionState()
	fmt.Printf("%-46s TCP+TLS ok in %v  alpn=%q  tls=%s\n",
		addr, time.Since(start).Round(time.Millisecond), st.NegotiatedProtocol, tlsName(st.Version))
}

func tlsName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	}
	return fmt.Sprintf("0x%04x", v)
}

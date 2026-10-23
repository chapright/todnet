// Diagnostic: attempt a QUIC/HTTP-3 handshake against an edge and report what
// happened. Used to tell "UDP is blocked" apart from "our TLS/ALPN is wrong".
//
//	go run ./tools/quicprobe <host:port> [more...]
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/quic-go/quic-go"
)

func main() {
	targets := os.Args[1:]
	if len(targets) == 0 {
		targets = []string{"eze223.m1.fastly-masque.net:2499"}
	}
	for _, target := range targets {
		probe(target)
	}
}

func probe(target string) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		fmt.Printf("%-46s bad target: %v\n", target, err)
		return
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		fmt.Printf("%-46s DNS failed: %v\n", target, err)
		return
	}

	for _, alpn := range []string{"h3", "h3-29"} {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		addr := &net.UDPAddr{IP: ips[0], Port: atoi(port)}
		uc, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
		if err != nil {
			fmt.Printf("%-46s udp socket: %v\n", target, err)
			cancel()
			return
		}
		start := time.Now()
		conn, err := quic.Dial(ctx, uc, addr, &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{alpn},
		}, &quic.Config{InitialPacketSize: 1200, HandshakeIdleTimeout: 7 * time.Second})
		elapsed := time.Since(start).Round(time.Millisecond)
		cancel()

		if err != nil {
			fmt.Printf("%-46s alpn=%-6s FAILED after %v: %v\n", target, alpn, elapsed, err)
			_ = uc.Close()
			continue
		}
		st := conn.ConnectionState()
		fmt.Printf("%-46s alpn=%-6s OK in %v  negotiated=%q\n",
			target, alpn, elapsed, st.TLS.NegotiatedProtocol)
		_ = conn.CloseWithError(0, "")
		_ = uc.Close()
		return
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

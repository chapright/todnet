//go:build windows

package tun

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// The tunnel carries TCP CONNECT streams and nothing else, so every UDP
// datagram that is not DNS has to be refused. Dropping it silently is what
// makes Chromium-based apps fail: they keep a cached Alt-Svc "h3" for a host,
// open a QUIC connection on UDP/443, and wait out the full QUIC handshake
// timer (seconds, not milliseconds) before falling back to TCP. An app with a
// shorter request timeout gives up first and reports "network error".
//
// Answering with ICMP port-unreachable collapses that wait to one round trip:
// Windows fails the UDP socket immediately and the client retries over TCP
// without the user ever noticing. It is also the honest answer — the port
// really is unreachable through this tunnel.
const (
	ipProtoICMP = 1
	ipProtoUDP  = 17

	icmpDestUnreachable   = 3
	icmpPortUnreachable   = 3
	icmpHeaderLen         = 8
	ipv4HeaderLen         = 20
	udpHeaderLen          = 8
	maxICMPErrorsPerBurst = 4
	icmpRefusalInterval   = 400 * time.Millisecond
	icmpRefusalTableCap   = 512
)

// buildPortUnreachable assembles the ICMPv4 message Windows needs to fail the
// client's UDP socket. Per RFC 792 the body carries the offending datagram's
// IP header plus its first eight bytes — exactly the UDP header, which is what
// the receiving stack matches the socket on.
//
//	client     the app that sent the datagram (outer destination, inner source)
//	clientPort the app's source port
//	server     the datagram's destination (outer source, inner destination)
//	serverPort the datagram's destination port
func buildPortUnreachable(client net.IP, clientPort uint16, server net.IP, serverPort uint16) []byte {
	inner := ipv4HeaderLen + udpHeaderLen
	icmpLen := icmpHeaderLen + inner
	pkt := make([]byte, ipv4HeaderLen+icmpLen)

	// Outer IPv4 header: from the "server" back to the client.
	pkt[0] = 0x45 // version 4, IHL 5
	pkt[1] = 0
	binary.BigEndian.PutUint16(pkt[2:], uint16(len(pkt)))
	binary.BigEndian.PutUint16(pkt[4:], 0) // identification
	binary.BigEndian.PutUint16(pkt[6:], 0) // flags / fragment offset
	pkt[8] = 64                            // TTL
	pkt[9] = ipProtoICMP
	binary.BigEndian.PutUint16(pkt[10:], 0)
	copy(pkt[12:16], server.To4())
	copy(pkt[16:20], client.To4())
	binary.BigEndian.PutUint16(pkt[10:], internetChecksum(pkt[:ipv4HeaderLen]))

	icmp := pkt[ipv4HeaderLen:]
	icmp[0] = icmpDestUnreachable
	icmp[1] = icmpPortUnreachable
	// icmp[2:4] is the checksum, filled in last.
	// icmp[4:8] must stay zero for an ICMP error.

	// The quoted datagram: the client's own IP header, as it sent it.
	q := icmp[icmpHeaderLen:]
	q[0] = 0x45
	q[1] = 0
	binary.BigEndian.PutUint16(q[2:], uint16(inner))
	binary.BigEndian.PutUint16(q[4:], 0)
	binary.BigEndian.PutUint16(q[6:], 0)
	q[8] = 64
	q[9] = ipProtoUDP
	binary.BigEndian.PutUint16(q[10:], 0)
	copy(q[12:16], client.To4())
	copy(q[16:20], server.To4())
	binary.BigEndian.PutUint16(q[10:], internetChecksum(q[:ipv4HeaderLen]))

	udp := q[ipv4HeaderLen:]
	binary.BigEndian.PutUint16(udp[0:], clientPort)
	binary.BigEndian.PutUint16(udp[2:], serverPort)
	binary.BigEndian.PutUint16(udp[4:], udpHeaderLen) // length: header only

	binary.BigEndian.PutUint16(icmp[2:], internetChecksum(icmp))
	return pkt
}

// internetChecksum is the RFC 1071 one's-complement sum used by IPv4 and ICMP.
func internetChecksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// udpRefusal tracks how often each client has been told its datagram is
// unreachable. A QUIC client sends a small burst and then waits, so a handful
// of replies per flow is plenty; more than that is just noise on the wire.
type udpRefusal struct {
	mu    sync.Mutex
	seen  map[udpFlow]refusalEntry
	total int64
}

type udpFlow struct {
	client [4]byte
	port   uint16
}

type refusalEntry struct {
	last  time.Time
	count int
}

func newUDPRefusal() *udpRefusal {
	return &udpRefusal{seen: make(map[udpFlow]refusalEntry)}
}

// allow reports whether this datagram should be answered. The first few
// datagrams of a flow always are — that is the burst a QUIC handshake retries
// with — and after that one reply per icmpRefusalInterval is enough to keep the
// socket failed without answering every packet an app sends.
func (r *udpRefusal) allow(client [4]byte, port uint16, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	flow := udpFlow{client, port}
	e, ok := r.seen[flow]
	if ok && now.Sub(e.last) < icmpRefusalInterval && e.count >= maxICMPErrorsPerBurst {
		return false
	}
	if !ok && len(r.seen) >= icmpRefusalTableCap {
		// Nothing better to do than start over: the table only exists to
		// throttle, so forgetting it costs a few extra ICMP packets.
		r.seen = make(map[udpFlow]refusalEntry)
	}
	e.last = now
	e.count++
	r.seen[flow] = e
	r.total++
	return true
}

func (r *udpRefusal) sent() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

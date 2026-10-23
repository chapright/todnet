//go:build windows

package tun

import (
	"context"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/checksum"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
)

const (
	testIPHeaderLen  = header.IPv4MinimumSize
	testTCPHeaderLen = header.TCPMinimumSize

	clientPort = 12345
	serverPort = 80
)

var (
	clientIP = [4]byte{198, 18, 0, 2}
	serverIP = [4]byte{1, 2, 3, 4}
)

// tcpSegment builds a complete IPv4/TCP packet so the stack sees exactly what
// an application would send into the adapter.
func tcpSegment(src [4]byte, srcPort uint16, dst [4]byte, dstPort uint16,
	seq, ack uint32, flags header.TCPFlags, payload []byte) []byte {

	buf := make([]byte, testIPHeaderLen+testTCPHeaderLen+len(payload))

	ip := header.IPv4(buf)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(buf)),
		TTL:         64,
		Protocol:    uint8(header.TCPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4(src),
		DstAddr:     tcpip.AddrFrom4(dst),
	})
	ip.SetChecksum(^ip.CalculateChecksum())

	segment := header.TCP(buf[testIPHeaderLen:])
	segment.Encode(&header.TCPFields{
		SrcPort:    srcPort,
		DstPort:    dstPort,
		SeqNum:     seq,
		AckNum:     ack,
		DataOffset: testTCPHeaderLen,
		Flags:      flags,
		WindowSize: 65535,
	})
	copy(buf[testIPHeaderLen+testTCPHeaderLen:], payload)
	// The transport checksum covers the whole segment — header *and* payload —
	// on top of the pseudo-header. TCP.CalculateChecksum only walks the header,
	// so a segment built with it verifies for a SYN but is silently dropped by
	// the stack as soon as it carries data.
	segment.SetChecksum(0)
	segment.SetChecksum(^checksum.Checksum(buf[testIPHeaderLen:], header.PseudoHeaderChecksum(
		header.TCPProtocolNumber,
		tcpip.AddrFrom4(src),
		tcpip.AddrFrom4(dst),
		uint16(len(buf)-testIPHeaderLen),
	)))
	return buf
}

// segmentChecksum recomputes a segment's checksum the way the stack does, so a
// test can assert the packet it built is one the stack would actually accept.
func segmentChecksum(pkt []byte) uint16 {
	ip := header.IPv4(pkt)
	return checksum.Checksum(pkt[ip.HeaderLength():], header.PseudoHeaderChecksum(
		header.TCPProtocolNumber,
		ip.SourceAddress(),
		ip.DestinationAddress(),
		uint16(len(pkt)-int(ip.HeaderLength())),
	))
}

// fakeDialer records the addresses the stack asks for and hands back a pipe so
// the test can watch the forwarded bytes.
func fakeDialer() (func(context.Context, string, string) (net.Conn, error), <-chan string, <-chan net.Conn) {
	calls := make(chan string, 8)
	peers := make(chan net.Conn, 8)
	dial := func(_ context.Context, network, address string) (net.Conn, error) {
		calls <- network + " " + address
		ours, theirs := net.Pipe()
		peers <- theirs
		return ours, nil
	}
	return dial, calls, peers
}

// emitSink stands in for the adapter: it records the raw packets the stack
// pushes out, which is where ICMP refusals show up.
type emitSink struct {
	mu   sync.Mutex
	pkts [][]byte
}

func (s *emitSink) emit(p []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pkts = append(s.pkts, append([]byte(nil), p...))
	return nil
}

func (s *emitSink) next(t *testing.T, within time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.pkts) > 0 {
			p := s.pkts[0]
			s.pkts = s.pkts[1:]
			s.mu.Unlock()
			return p
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no packet emitted within %s", within)
	return nil
}

func newTestStack(t *testing.T, dial func(context.Context, string, string) (net.Conn, error)) (*netstack, *channel.Endpoint, *emitSink) {
	t.Helper()
	link := channel.New(linkQueue, DefaultMTU, "")
	logf := func(format string, args ...any) { t.Logf(format, args...) }
	dns := newDNSRelay(dial, logf, logf)
	warnThrottled := func(_ string, format string, args ...any) { t.Logf(format, args...) }
	sink := &emitSink{}
	n, err := newNetstack(link, dial, nil, nil, dns, nil, logf, logf, warnThrottled, sink.emit)
	if err != nil {
		t.Fatalf("newNetstack: %v", err)
	}
	t.Cleanup(n.close)
	return n, link, sink
}

// readPacket pulls one packet out of the stack, failing if none arrives.
func readPacket(t *testing.T, link *channel.Endpoint, within time.Duration) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	pkt := link.ReadContext(ctx)
	if pkt == nil {
		t.Fatal("stack produced no packet")
	}
	defer pkt.DecRef()
	view := pkt.ToView()
	defer view.Release()
	out := make([]byte, len(view.AsSlice()))
	copy(out, view.AsSlice())
	return out
}

// A SYN must be answered, and the connection handed to the dialer with the
// original destination — that is the whole forwarding contract.
func TestNetstackForwardsTCPToDialer(t *testing.T) {
	dial, calls, peers := fakeDialer()
	n, link, _ := newTestStack(t, dial)

	n.inject(tcpSegment(clientIP, clientPort, serverIP, serverPort, 1000, 0, header.TCPFlagSyn, nil))

	synAck := readPacket(t, link, 2*time.Second)
	ip := header.IPv4(synAck)
	if ip.Protocol() != uint8(header.TCPProtocolNumber) {
		t.Fatalf("reply protocol = %d, want TCP", ip.Protocol())
	}
	segment := header.TCP(synAck[ip.HeaderLength():])
	if segment.SourcePort() != serverPort || segment.DestinationPort() != clientPort {
		t.Errorf("reply ports = %d->%d, want %d->%d",
			segment.SourcePort(), segment.DestinationPort(), serverPort, clientPort)
	}
	if flags := segment.Flags(); flags&header.TCPFlagSyn == 0 || flags&header.TCPFlagAck == 0 {
		t.Fatalf("reply flags = %v, want SYN|ACK", flags)
	}
	if got := segment.AckNumber(); got != 1001 {
		t.Errorf("SYN-ACK acknowledges %d, want 1001", got)
	}

	// The forwarder only dials once the three-way handshake completes, which
	// is what a real client does immediately.
	serverSeq := segment.SequenceNumber()
	n.inject(tcpSegment(clientIP, clientPort, serverIP, serverPort, 1001, serverSeq+1, header.TCPFlagAck, nil))

	select {
	case got := <-calls:
		if got != "tcp 1.2.3.4:80" {
			t.Errorf("dialer called with %q, want tcp 1.2.3.4:80", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dialer was never called")
	}

	// Bytes the application sends must reach the upstream connection.
	var peer net.Conn
	select {
	case peer = <-peers:
	case <-time.After(time.Second):
		t.Fatal("no upstream connection")
	}
	defer peer.Close()

	dataPkt := tcpSegment(clientIP, clientPort, serverIP, serverPort, 1001, serverSeq+1,
		header.TCPFlagAck|header.TCPFlagPsh, []byte("hello"))
	if sum := segmentChecksum(dataPkt); sum != 0xffff {
		t.Fatalf("data segment checksum verifies as %#x, want 0xffff", sum)
	}
	n.inject(dataPkt)

	// A segment carrying data must be acknowledged, which is also the cheapest
	// proof that the stack accepted it rather than dropping it on the floor.
	ackPkt := readPacket(t, link, 2*time.Second)
	ackIP := header.IPv4(ackPkt)
	ackSeg := header.TCP(ackPkt[ackIP.HeaderLength():])
	if ackSeg.Flags()&header.TCPFlagAck == 0 {
		t.Errorf("reply to the data segment has flags %v, want ACK", ackSeg.Flags())
	}
	if got := ackSeg.AckNumber(); got != 1006 {
		t.Errorf("data segment acknowledged through %d, want 1006", got)
	}

	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	read, err := peer.Read(buf)
	if err != nil {
		t.Fatalf("reading forwarded bytes: %v", err)
	}
	if string(buf[:read]) != "hello" {
		t.Errorf("forwarded %q, want %q", buf[:read], "hello")
	}
}

// IPv6 is not tunnelled. Dropping it silently is deliberate, but it must not
// reach the dialer or crash the stack.
func TestNetstackDropsIPv6(t *testing.T) {
	dial, calls, _ := fakeDialer()
	n, link, _ := newTestStack(t, dial)

	pkt := make([]byte, header.IPv6MinimumSize+testTCPHeaderLen)
	pkt[0] = 0x60 // version 6
	n.inject(pkt)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if out := link.ReadContext(ctx); out != nil {
		out.DecRef()
		t.Fatal("stack emitted a packet for IPv6 input")
	}
	select {
	case got := <-calls:
		t.Fatalf("IPv6 traffic was dialled (%s); it must be dropped", got)
	default:
	}
}

func TestNetstackIgnoresGarbage(t *testing.T) {
	dial, calls, _ := fakeDialer()
	n, link, _ := newTestStack(t, dial)

	// Empty, truncated, and a bogus version nibble.
	n.inject(nil)
	n.inject([]byte{0x45})
	n.inject([]byte{0x00, 0x01, 0x02})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if out := link.ReadContext(ctx); out != nil {
		out.DecRef()
		t.Fatal("stack emitted a packet for garbage input")
	}
	select {
	case got := <-calls:
		t.Fatalf("garbage was dialled (%s)", got)
	default:
	}
}

// UDP that is not DNS has nowhere to go: the edge carries TCP CONNECT streams
// only. It must not be dialled, and it must not be swallowed either — the
// client is told the port is unreachable so it retries over TCP straight away.
func TestNetstackRefusesNonDNSUDP(t *testing.T) {
	dial, calls, _ := fakeDialer()
	n, link, sink := newTestStack(t, dial)

	payload := []byte{0x01, 0x02, 0x03, 0x04}
	buf := make([]byte, testIPHeaderLen+8+len(payload))
	ip := header.IPv4(buf)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(buf)),
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4(clientIP),
		DstAddr:     tcpip.AddrFrom4(serverIP),
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	udp := header.UDP(buf[testIPHeaderLen:])
	udp.Encode(&header.UDPFields{
		SrcPort: clientPort,
		DstPort: 443, // QUIC, not DNS
		Length:  uint16(8 + len(payload)),
	})
	copy(buf[testIPHeaderLen+8:], payload)
	udp.SetChecksum(^udp.CalculateChecksum(header.PseudoHeaderChecksum(
		header.UDPProtocolNumber,
		tcpip.AddrFrom4(clientIP),
		tcpip.AddrFrom4(serverIP),
		uint16(8+len(payload)),
	)))

	n.inject(buf)

	// The refusal goes straight to the adapter, not through the stack's own
	// link endpoint, so nothing should appear there.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if out := link.ReadContext(ctx); out != nil {
		out.DecRef()
		t.Fatal("stack emitted a packet for non-DNS UDP")
	}
	select {
	case got := <-calls:
		t.Fatalf("non-DNS UDP was dialled (%s)", got)
	default:
	}

	pkt := sink.next(t, time.Second)
	icmp := header.ICMPv4(pkt[ipv4HeaderLen:])
	if got := icmp.Type(); got != header.ICMPv4DstUnreachable {
		t.Fatalf("ICMP type = %v, want destination unreachable", got)
	}
	if got := icmp.Code(); got != header.ICMPv4PortUnreachable {
		t.Fatalf("ICMP code = %v, want port unreachable", got)
	}

	outer := header.IPv4(pkt)
	if !outer.IsChecksumValid() {
		t.Error("outer IPv4 checksum is wrong")
	}
	if got, want := outer.SourceAddress(), tcpip.AddrFrom4(serverIP); got != want {
		t.Errorf("ICMP source = %s, want %s (the address the client was talking to)", got, want)
	}
	if got, want := outer.DestinationAddress(), tcpip.AddrFrom4(clientIP); got != want {
		t.Errorf("ICMP destination = %s, want %s (the client)", got, want)
	}
	// A valid ICMP message sums to zero over its whole length.
	if sum := internetChecksum(icmp); sum != 0 {
		t.Errorf("ICMP checksum is wrong (residual sum %#04x)", sum)
	}

	// The quoted datagram is what Windows matches the socket on: it must be
	// the client's own header with its own ports, in its own direction.
	quoted := header.IPv4(icmp.Payload())
	if got, want := quoted.SourceAddress(), tcpip.AddrFrom4(clientIP); got != want {
		t.Errorf("quoted source = %s, want %s", got, want)
	}
	if got, want := quoted.DestinationAddress(), tcpip.AddrFrom4(serverIP); got != want {
		t.Errorf("quoted destination = %s, want %s", got, want)
	}
	quotedUDP := header.UDP(quoted.Payload())
	if got := quotedUDP.SourcePort(); got != clientPort {
		t.Errorf("quoted UDP source port = %d, want %d", got, clientPort)
	}
	if got := quotedUDP.DestinationPort(); got != 443 {
		t.Errorf("quoted UDP destination port = %d, want 443", got)
	}
}

// udpSegment builds an IPv4/UDP packet with a correct checksum.
// dnsUpstreamHost is the dial target the relay uses, derived from the
// configured endpoint so the test does not pin a hostname that can change.
func dnsUpstreamHost() string {
	u, err := url.Parse(dnsUpstreamPrimary)
	if err != nil || u.Host == "" {
		return "?"
	}
	if u.Port() == "" {
		return net.JoinHostPort(u.Hostname(), "443")
	}
	return u.Host
}

func udpSegment(src [4]byte, srcPort uint16, dst [4]byte, dstPort uint16, payload []byte) []byte {
	buf := make([]byte, testIPHeaderLen+8+len(payload))
	ip := header.IPv4(buf)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(buf)),
		TTL:         64,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFrom4(src),
		DstAddr:     tcpip.AddrFrom4(dst),
	})
	ip.SetChecksum(^ip.CalculateChecksum())

	udp := header.UDP(buf[testIPHeaderLen:])
	udp.Encode(&header.UDPFields{
		SrcPort: srcPort,
		DstPort: dstPort,
		Length:  uint16(8 + len(payload)),
	})
	copy(buf[testIPHeaderLen+8:], payload)
	// Same trap as TCP: UDP.CalculateChecksum only walks the 8-byte header,
	// so a packet built with it verifies for an empty datagram and is dropped
	// by the stack as soon as it carries a payload. A real query does, and a
	// dropped query would look exactly like the bug under test.
	udp.SetChecksum(0)
	udp.SetChecksum(^checksum.Checksum(buf[testIPHeaderLen:], header.PseudoHeaderChecksum(
		header.UDPProtocolNumber,
		tcpip.AddrFrom4(src),
		tcpip.AddrFrom4(dst),
		uint16(8+len(payload)),
	)))
	return buf
}

// Every DNS query that enters the tunnel has to be answered, whatever address
// it was sent to.
//
// Answering only lookups aimed at the adapter's own resolver left every app
// with a hardcoded server (8.8.8.8, 1.1.1.1, the router) unable to resolve
// anything: the query came into the tunnel, UDP is not carried, and the reply
// was ICMP port-unreachable. The client saw "no such host" for every name.
func TestNetstackAnswersDNSWhateverTheDestination(t *testing.T) {
	dial, calls, _ := fakeDialer()
	n, link, sink := newTestStack(t, dial)

	// A real query for example.com aimed at a public resolver.
	query := dnsQuery(0x4242, "example.com")
	n.inject(udpSegment([4]byte{198, 18, 0, 1}, 5353, [4]byte{8, 8, 8, 8}, 53, query))

	// The relay runs in the tunnel: it must reach for the DoH endpoint over
	// the same upstream session as everything else.
	select {
	case got := <-calls:
		if got != "tcp "+dnsUpstreamHost() {
			t.Errorf("relay dialled %q, want the DoH endpoint", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the query was not relayed")
	}

	// It must not be refused: an ICMP port-unreachable is what made these
	// lookups fail before.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if out := link.ReadContext(ctx); out != nil {
		out.DecRef()
		t.Fatal("the stack answered a DNS query itself instead of relaying it")
	}
	sink.mu.Lock()
	emitted := len(sink.pkts)
	sink.mu.Unlock()
	if emitted != 0 {
		t.Fatalf("%d ICMP refusal(s) sent for a DNS query, want none", emitted)
	}
}

// A QUIC client retries its handshake in a burst; answering every datagram
// would put more traffic on the wire than the app sent.
func TestUDPRefusalThrottlesRepeats(t *testing.T) {
	r := newUDPRefusal()
	now := time.Now()
	client := [4]byte{198, 18, 0, 2}

	allowed := 0
	for i := 0; i < 20; i++ {
		if r.allow(client, 443, now) {
			allowed++
		}
	}
	if allowed != maxICMPErrorsPerBurst {
		t.Fatalf("first burst allowed %d replies, want %d", allowed, maxICMPErrorsPerBurst)
	}

	if r.allow(client, 443, now.Add(icmpRefusalInterval/2)) {
		t.Fatal("answered again inside the throttle window")
	}
	if !r.allow(client, 443, now.Add(icmpRefusalInterval)) {
		t.Fatal("did not answer once the throttle window elapsed")
	}

	// A different flow has its own budget.
	if !r.allow([4]byte{198, 18, 0, 3}, 443, now) {
		t.Fatal("throttled an unrelated client")
	}
}

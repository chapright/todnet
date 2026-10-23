//go:build windows

package tun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	nicID   = 1
	dnsPort = 53
	// Slightly longer than the session's own CONNECT timeout, so a queued
	// tunnel reports the specific "timed out opening the tunnel" reason
	// instead of a bare context deadline.
	dialTimeout = 40 * time.Second
)

// netstack is the userspace TCP/IP implementation behind the adapter. The
// adapter delivers IP packets here, the stack terminates TCP, and each
// connection is dialled through the upstream CONNECT session.
type netstack struct {
	stack *stack.Stack
	ep    *channel.Endpoint
	dial  func(ctx context.Context, network, address string) (net.Conn, error)
	// bypass reports whether a host should skip the tunnel, and
	// dialDirect opens the connection that leaves without it.
	bypass     func(host string) bool
	dialDirect func(ctx context.Context, network, address string) (net.Conn, error)
	dns        *dnsRelay
	stats      Stats

	logf          func(string, ...any)
	warnf         func(string, ...any)
	warnThrottled func(key, format string, args ...any)
	connSeq       atomic.Uint64

	// UDP the tunnel cannot carry is counted per port so the log can name
	// what is being dropped instead of staying silent about it.
	udpDropped atomic.Int64
	udpMu      sync.Mutex
	udpPorts   map[uint16]int64

	// emit writes a raw IPv4 packet straight to the adapter, bypassing the
	// stack. It is how a refused UDP datagram gets answered with ICMP.
	emit    func([]byte) error
	refusal *udpRefusal
}

func newNetstack(link *channel.Endpoint, dial func(context.Context, string, string) (net.Conn, error),
	bypass func(string) bool, dialDirect func(context.Context, string, string) (net.Conn, error),
	dns *dnsRelay, stats Stats, logf, warnf func(string, ...any),
	warnThrottled func(string, string, ...any), emit func([]byte) error) (*netstack, error) {

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	if err := s.CreateNIC(nicID, link); err != nil {
		return nil, tcpipErr(err)
	}
	// Accept packets addressed to anything, not just the adapter's own
	// address: every destination on the machine arrives here.
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, tcpipErr(err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, tcpipErr(err)
	}

	addr := tcpip.AddrFrom4([4]byte{198, 18, 0, 1})
	if err := s.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: addr, PrefixLen: ipv4Prefix},
	}, stack.AddressProperties{}); err != nil {
		return nil, tcpipErr(err)
	}
	s.SetRouteTable([]tcpip.Route{{
		Destination: header.IPv4EmptySubnet,
		NIC:         nicID,
	}})

	sack := tcpip.TCPSACKEnabled(true)
	_ = s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	moderate := tcpip.TCPModerateReceiveBufferOption(true)
	_ = s.SetTransportProtocolOption(tcp.ProtocolNumber, &moderate)

	n := &netstack{
		stack:      s,
		ep:         link,
		dial:       dial,
		bypass:     bypass,
		dialDirect: dialDirect,
		dns:        dns,
		stats:      stats,
		logf:       logf,
		warnf:      warnf,
		udpPorts:   make(map[uint16]int64),
		emit:       emit,
		refusal:    newUDPRefusal(),
	}
	if warnThrottled != nil {
		n.warnThrottled = warnThrottled
	} else {
		n.warnThrottled = func(_ string, format string, args ...any) { warnf(format, args...) }
	}

	tcpForwarder := tcp.NewForwarder(s, 0, 1024, n.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpForwarder.HandlePacket)

	udpForwarder := udp.NewForwarder(s, n.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpForwarder.HandlePacket)

	return n, nil
}

// inject hands one IP packet from the adapter to the stack.
func (n *netstack) inject(pkt []byte) {
	if len(pkt) == 0 {
		return
	}
	var proto tcpip.NetworkProtocolNumber
	switch pkt[0] >> 4 {
	case 4:
		proto = ipv4.ProtocolNumber
	case 6:
		// IPv6 is not tunnelled; drop it rather than half-handling it.
		return
	default:
		return
	}
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(pkt),
	})
	n.ep.InjectInbound(proto, pb)
	pb.DecRef()
}

func (n *netstack) close() {
	n.stack.Close()
	n.stack.Wait()
}

func (n *netstack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dest := id.LocalAddress
	port := id.LocalPort

	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		r.Complete(true)
		n.logf("tun: could not accept %s:%d from %s: %v", dest, port, id.RemoteAddress, tcpipErr(err))
		return
	}
	r.Complete(false)

	client := gonet.NewTCPConn(&wq, ep)

	go func() {
		defer client.Close()

		if isDNSQuery(port) {
			n.dns.serveTCP(client)
			return
		}

		connID := n.connSeq.Add(1)
		authority := net.JoinHostPort(dest.String(), strconv.Itoa(int(port)))
		src := net.JoinHostPort(id.RemoteAddress.String(), strconv.Itoa(int(id.RemotePort)))
		start := time.Now()

		// Read the TLS ClientHello first: in TUN mode the destination is an
		// IP, and "43.156.86.196:443" says nothing about which app broke.
		// The hostname does, and it costs one packet to get.
		sni := ""
		var stream net.Conn = client
		if tlsFirstPort(port) {
			sni, stream = peekServerName(client, sniPeekTimeout)
		}
		label := authority
		if sni != "" {
			label = sni + " (" + authority + ")"
		}

		// A bypassed host leaves the machine without the tunnel. The name
		// is only known for TLS, so a plain HTTP connection cannot be
		// matched — the address is all the stack has by then.
		if sni != "" && n.bypass != nil && n.bypass(sni) {
			n.logf("tun: #%d %s bypasses the tunnel", connID, label)
			if n.dialDirect == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
			upstream, err := n.dialDirect(ctx, "tcp", authority)
			cancel()
			if err != nil {
				n.warnThrottled("bypass:"+authority+":"+err.Error(),
					"tun: cannot reach %s directly (from %s): %s", label, src, explainDialError(err))
				return
			}
			defer upstream.Close()

			if n.stats != nil {
				n.stats.ConnOpened()
				defer n.stats.ConnClosed()
			}
			n.logf("tun: #%d open %s <- %s directly", connID, label, src)

			res := splice(stream, upstream, n.stats)
			n.logf("tun: #%d close %s after %s (up %d B, down %d B, bypassed)",
				connID, label, time.Since(start).Round(time.Millisecond), res.up, res.down)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
		upstream, err := n.dial(ctx, "tcp", authority)
		cancel()
		if err != nil {
			// One line per distinct destination, with the reason spelled
			// out: a refused destination is a policy answer from the edge,
			// not a broken tunnel, and saying so saves a support round trip.
			n.warnThrottled("dial:"+authority+":"+err.Error(),
				"tun: cannot reach %s (from %s): %s", label, src, explainDialError(err))
			n.logf("tun: #%d %s <- %s failed after %s: %v",
				connID, label, src, time.Since(start).Round(time.Millisecond), err)
			return
		}
		defer upstream.Close()

		if n.stats != nil {
			n.stats.ConnOpened()
			defer n.stats.ConnClosed()
		}
		n.logf("tun: #%d open %s <- %s via %s", connID, label, src, upstream.RemoteAddr())

		res := splice(stream, upstream, n.stats)
		// ttfb separates "the destination never answered" from "the
		// connection was slow": a stream with down=0 and no first byte
		// never got a reply at all.
		ttfb := "never"
		if !res.firstByteAt.IsZero() {
			ttfb = res.firstByteAt.Sub(start).Round(time.Millisecond).String()
		}
		reason := res.closeReason()
		if reason != "" {
			n.logf("tun: #%d close %s after %s (up %d B, down %d B, ttfb %s) %s",
				connID, label, time.Since(start).Round(time.Millisecond), res.up, res.down, ttfb, reason)
		} else {
			n.logf("tun: #%d close %s after %s (up %d B, down %d B, ttfb %s)",
				connID, label, time.Since(start).Round(time.Millisecond), res.up, res.down, ttfb)
		}
	}()
}

// explainDialError turns the edge's HTTP status into something a user can act
// on. "502 Bad Gateway: Destination IP Prohibited" is Fastly's egress policy
// refusing a destination — nothing about the local machine is wrong.
func explainDialError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Destination IP Prohibited"):
		return "the VPN server refuses to route to it (its egress blocks that address)"
	case strings.Contains(msg, "Destination Port Prohibited"):
		return "the VPN server does not carry that port"
	case strings.Contains(msg, "502"), strings.Contains(msg, "503"):
		return "the VPN server could not reach it: " + msg
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"):
		return "timed out opening the tunnel through the VPN server"
	default:
		return msg
	}
}

func (n *netstack) handleUDP(r *udp.ForwarderRequest) {
	id := r.ID()
	if !isDNSQuery(id.LocalPort) {
		// UDP cannot cross the edge, so refuse it rather than ignore it:
		// an ICMP port-unreachable makes the client retry over TCP at
		// once, while a silent drop leaves it waiting out its QUIC
		// handshake timer and looking like a dead network.
		n.noteUDPDrop(id.LocalPort)
		n.refuseUDP(id)
		return
	}

	var wq waiter.Queue
	ep, err := r.CreateEndpoint(&wq)
	if err != nil {
		// Silence here is why a broken relay looked like a broken tunnel:
		// the query simply vanished with nothing in the log.
		n.logf("tun: could not accept DNS query from %s: %v", id.RemoteAddress, tcpipErr(err))
		return
	}
	go func() {
		conn := gonet.NewUDPConn(&wq, ep)
		n.dns.serveUDP(conn)
	}()
}

// noteUDPDrop counts datagrams the tunnel cannot carry. Silently dropping is
// why "some apps just do not work" was impossible to diagnose: QUIC on 443
// looks identical to a broken VPN unless the log says otherwise.
func (n *netstack) noteUDPDrop(port uint16) {
	total := n.udpDropped.Add(1)
	n.udpMu.Lock()
	n.udpPorts[port]++
	summary := n.udpSummaryLocked()
	n.udpMu.Unlock()

	// Throttled, so a QUIC-heavy app cannot flood the log, but the count is
	// cumulative so the number keeps climbing and stays honest.
	n.warnThrottled("udp-drop",
		"tun: refused %d UDP datagram(s) this session — this tunnel carries TCP only (%s). "+
			"Each one is answered with ICMP port-unreachable so the app retries over TCP",
		total, summary)
}

// refuseUDP answers a datagram the tunnel cannot carry with ICMP
// port-unreachable, so the client fails the flow now instead of waiting out
// its own timeout. Errors are swallowed on purpose: if the adapter is busy the
// datagram simply stays dropped, which is the old behaviour.
func (n *netstack) refuseUDP(id stack.TransportEndpointID) {
	if n.emit == nil || n.refusal == nil {
		return
	}
	if id.LocalAddress.Len() != 4 || id.RemoteAddress.Len() != 4 {
		return
	}
	client := id.RemoteAddress.As4()
	server := id.LocalAddress.As4()
	if !n.refusal.allow(client, id.LocalPort, time.Now()) {
		return
	}
	pkt := buildPortUnreachable(
		net.IP(client[:]),
		id.RemotePort,
		net.IP(server[:]),
		id.LocalPort,
	)
	_ = n.emit(pkt)
}

func (n *netstack) udpSummaryLocked() string {
	type entry struct {
		port uint16
		n    int64
	}
	entries := make([]entry, 0, len(n.udpPorts))
	for p, c := range n.udpPorts {
		entries = append(entries, entry{p, c})
	}
	// Largest first, so the summary names what is actually being dropped.
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].n > entries[j-1].n; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	if len(entries) > 4 {
		entries = entries[:4]
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString(", ")
		}
		switch e.port {
		case 443:
			fmt.Fprintf(&b, "udp/%d (QUIC) x%d", e.port, e.n)
		case 53:
			fmt.Fprintf(&b, "udp/%d (DNS) x%d", e.port, e.n)
		default:
			fmt.Fprintf(&b, "udp/%d x%d", e.port, e.n)
		}
	}
	return b.String()
}

// tcpipErr adapts gVisor's error interface to the standard one.
func tcpipErr(err tcpip.Error) error {
	return errors.New(err.String())
}

// isDNSQuery reports whether a flow entering the tunnel is a DNS lookup.
//
// The destination is deliberately ignored. Answering only queries aimed at
// the adapter's own resolver address left every other resolver broken: an app
// with a hardcoded server (8.8.8.8, 1.1.1.1, the router) sends its lookup into
// the tunnel, the tunnel cannot carry UDP, and the datagram was answered with
// ICMP port-unreachable — so that app could not resolve anything at all, while
// the browser (using the adapter's resolver) could. Port 53 is the signal, not
// the address: whatever the client aimed at, the answer comes back from the
// same address because the endpoint is bound to the packet's destination.
func isDNSQuery(port uint16) bool {
	return port == dnsPort
}

/* ---------------- splicing ---------------- */

// spliceResult is what the close log is built from. The per-direction error
// is the difference between "the client hung up" (normal) and "the tunnel
// broke" (worth reporting).
type spliceResult struct {
	up, down    int64
	firstByteAt time.Time
	upErr       error // client -> server
	downErr     error // server -> client
}

// closeReason renders the error that ended the stream, if any, in the same
// vocabulary the rest of the log uses.
func (r spliceResult) closeReason() string {
	err := r.downErr
	if err == nil {
		err = r.upErr
	}
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return ""
	}
	return "reason=" + err.Error()
}

// splice copies both directions until each side is done, forwarding
// half-closes so an HTTP response can finish while the request is still open.
func splice(a, b net.Conn, stats Stats) spliceResult {
	var (
		wg   sync.WaitGroup
		once sync.Once
		res  spliceResult
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		// a <- b is the download direction.
		w := &countWriter{w: a, add: statsAdd(stats, false)}
		w.onFirstByte = func() {
			once.Do(func() { res.firstByteAt = time.Now() })
		}
		n, err := io.Copy(w, b)
		res.down = n
		res.downErr = err
		closeWrite(a)
	}()
	go func() {
		defer wg.Done()
		// b <- a is the upload direction.
		n, err := io.Copy(&countWriter{w: b, add: statsAdd(stats, true)}, a)
		res.up = n
		res.upErr = err
		closeWrite(b)
	}()

	wg.Wait()
	return res
}

func statsAdd(s Stats, up bool) func(int64) {
	if s == nil {
		return nil
	}
	if up {
		return s.AddUp
	}
	return s.AddDown
}

type countWriter struct {
	w           io.Writer
	add         func(int64)
	onFirstByte func()
	seen        bool
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		if c.add != nil {
			c.add(int64(n))
		}
		if !c.seen {
			c.seen = true
			if c.onFirstByte != nil {
				c.onFirstByte()
			}
		}
	}
	return n, err
}

func closeWrite(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// Stats receives live counters from the tunnel.
type Stats interface {
	AddUp(int64)
	AddDown(int64)
	ConnOpened()
	ConnClosed()
}

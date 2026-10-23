//go:build windows

// Package tun implements the virtual-adapter tunnel mode for Windows.
//
// The adapter is a Wintun device: Windows routes packets into it, a userspace
// TCP/IP stack (gVisor netstack) terminates them, and every TCP connection is
// dialled through the same upstream CONNECT session the proxy mode uses. That
// makes TUN mode capture applications that ignore the Windows proxy setting —
// Telegram's MTProto sockets, games, anything with its own network stack.
//
// Limitations, by construction: the edge carries TCP CONNECT streams only, so
// UDP other than DNS (QUIC, voice, torrents) cannot be carried. Those datagrams
// are answered with ICMP port-unreachable rather than dropped, so a client
// retries over TCP immediately instead of waiting out its QUIC timer. IPv6 is
// not tunnelled.
package tun

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"

	"todnet/internal/win"
)

const (
	// DefaultMTU matches the physical link so packets are not fragmented
	// twice on their way to the edge.
	DefaultMTU = 1500

	// The adapter uses the RFC 2544 benchmarking range, which is never
	// routed on the public internet and therefore cannot collide with a real
	// destination.
	ipv4Address = "198.18.0.1"
	ipv4Mask    = "255.254.0.0"
	ipv4Prefix  = 15

	// dnsAddress is the resolver handed to Windows, and it is deliberately
	// NOT ipv4Address.
	//
	// Windows delivers a packet addressed to an interface's *own* address to
	// the local stack instead of transmitting it, so a resolver listening on
	// 198.18.0.1 would never see a query: the datagram is looped back inside
	// Windows, nothing is bound to port 53 there, and the lookup fails on the
	// spot. Windows then quietly falls back to the physical adapter's DNS,
	// which is the one thing TUN mode exists to avoid — names are resolved by
	// the ISP (filtered, poisoned, or simply missing), while the same names
	// resolve correctly at the edge in proxy mode.
	//
	// A separate address inside the same /15 has no local owner, so it is
	// routed out of the adapter like any other destination and reaches the
	// userspace stack.
	dnsAddress = "198.18.0.2"

	linkQueue = 1024
)

// Options configures a tunnel.
type Options struct {
	// Dial opens a TCP connection to address through the VPN. The tunnel
	// calls it for every connection an application makes.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// Bypass reports whether a host should skip the tunnel. The tunnel
	// only has a name for TLS, so a plain HTTP connection cannot be
	// matched.
	Bypass func(host string) bool
	// DialDirect opens a connection that leaves the machine without the
	// tunnel. It has to be pinned to the physical interface, or the
	// tunnel's own adapter would route it straight back in.
	DialDirect func(ctx context.Context, network, address string) (net.Conn, error)
	// ExcludeIPs are addresses that must stay on the physical interface —
	// the VPN edge itself, or the tunnel would loop.
	ExcludeIPs []net.IP
	MTU        int
	Stats      Stats
	Logf       func(string, ...any)
	Warnf      func(string, ...any)
	// WarnThrottled collapses repeated warnings that share a key, so one
	// unreachable destination cannot fill the log.
	WarnThrottled func(key, format string, args ...any)
}

// Tunnel is a running virtual adapter.
type Tunnel struct {
	adapter *wintun.Adapter
	session wintun.Session
	readEvt windows.Handle
	link    *channel.Endpoint
	stack   *netstack
	info    *interfaceInfo
	routes  []route

	logf  func(string, ...any)
	warnf func(string, ...any)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Wintun sessions are single-writer: the stack's own packets and the
	// ICMP refusals are produced on different goroutines, so the send ring
	// needs one owner at a time.
	sendMu     sync.Mutex
	sendClosed bool

	closeOnce    sync.Once
	teardownOnce sync.Once
	closeErr     error
}

// Start creates the adapter, configures routing and starts forwarding.
func Start(opts Options) (*Tunnel, error) {
	if opts.Dial == nil {
		return nil, errors.New("tun: no dialer configured")
	}
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	warnf := opts.Warnf
	if warnf == nil {
		warnf = logf
	}
	mtu := opts.MTU
	if mtu <= 0 {
		mtu = DefaultMTU
	}

	if !win.IsElevated() {
		return nil, errors.New("TUN mode needs administrator rights — restart Todnet as administrator")
	}

	adapter, err := createAdapter()
	if err != nil {
		return nil, err
	}

	t := &Tunnel{
		adapter: adapter,
		logf:    logf,
		warnf:   warnf,
	}
	cleanup := true
	defer func() {
		if cleanup {
			t.teardown()
		}
	}()

	if t.session, err = adapter.StartSession(sessionRingCap); err != nil {
		return nil, fmt.Errorf("starting the adapter session: %w", err)
	}
	t.readEvt = t.session.ReadWaitEvent()
	if version, verr := wintun.RunningVersion(); verr == nil {
		logf("tun: adapter %q up (wintun %d.%d, luid %d)", adapterName,
			version>>16, version&0xffff, adapter.LUID())
	} else {
		logf("tun: adapter %q up (luid %d)", adapterName, adapter.LUID())
	}

	info, err := waitForInterface(adapter.LUID(), adapterName, 8*time.Second)
	if err != nil {
		return nil, err
	}
	t.info = info

	// Pin the edge to the physical gateway before the default route moves,
	// otherwise the tunnel's own packets would be routed into the tunnel.
	gw, gwIf, gwErr := physicalGateway(adapterName)
	if gwErr != nil {
		warnf("tun: could not find the physical default gateway: %v", gwErr)
	} else {
		for _, ip := range opts.ExcludeIPs {
			if ip == nil || ip.To4() == nil {
				continue
			}
			r, rerr := addHostRoute(ip, gw, gwIf)
			if rerr != nil {
				warnf("tun: %v", rerr)
				continue
			}
			t.routes = append(t.routes, r)
			logf("tun: keeping %s on the physical interface (%s)", ip, gw)
		}
	}

	if err := setAddress(adapterName, ipv4Address, ipv4Mask, ipv4Prefix, mtu, dnsAddress); err != nil {
		return nil, err
	}
	// Read the resolver back instead of trusting that netsh took. Windows
	// can keep the previous list, and a stale resolver is invisible until
	// names start failing: every lookup would then be answered by the
	// physical adapter's DNS while the log claims the tunnel owns it.
	t.logf("tun: adapter resolver is %s (answered inside the tunnel)", dnsAddress)
	t.reportResolvers()

	defRoute, err := addDefaultRoute(info.ifIndex)
	if err != nil {
		return nil, err
	}
	t.routes = append(t.routes, defRoute)
	flushDNS()
	logf("tun: default route now points at %q (%s/15, mtu %d)", adapterName, ipv4Address, mtu)

	// Local traffic must never enter the tunnel. The physical interface only
	// carries its own subnet, so a corporate 10.x host, a NAS on 192.168.x or
	// the router's admin page would otherwise be sent to the edge — which
	// answers 502 "Destination IP Prohibited" and looks like a broken tunnel.
	if gwErr == nil {
		bypass, berr := addBypassRoutes(gw, gwIf)
		if berr != nil {
			warnf("tun: %v", berr)
		}
		t.routes = append(t.routes, bypass...)
		if len(bypass) > 0 {
			logf("tun: %d local ranges stay off the tunnel (%s)", len(bypass), localRangesSummary(bypass))
		}
	}

	t.link = channel.New(linkQueue, uint32(mtu), "")
	dns := newDNSRelay(opts.Dial, logf, warnf)
	t.stack, err = newNetstack(t.link, opts.Dial, opts.Bypass, opts.DialDirect, dns, opts.Stats, logf, warnf, opts.WarnThrottled, t.sendToAdapter)
	if err != nil {
		return nil, fmt.Errorf("starting the userspace network stack: %w", err)
	}

	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.wg.Add(2)
	go t.readLoop()
	go t.writeLoop()

	// Prime the resolver's TLS connection now, while nothing is waiting on a
	// lookup, instead of letting the first name a user types pay for it.
	dns.Warm()

	cleanup = false
	return t, nil
}

// Name is the adapter alias shown to the user.
func (t *Tunnel) Name() string { return adapterName }

// reportResolvers states, in the log, which resolver Windows will actually
// use on each adapter — and warns when the tunnel's own resolver did not take.
//
// This is the difference between "DNS should be going through the tunnel" and
// "DNS is going through the tunnel". The failure that produced
// NS_ERROR_UNKNOWN_HOST in the field was exactly this: the adapter existed, the
// routes were right, and every name was still being resolved by the ISP.
func (t *Tunnel) reportResolvers() {
	got := waitForResolver(adapterName, dnsAddress, 2*time.Second)
	switch {
	case len(got) == 0:
		t.warnf("tun: Windows reports no resolver on %q — lookups will fall back to the physical adapter's DNS",
			adapterName)
	case !hasIP(got, dnsAddress):
		t.warnf("tun: Windows kept %s as the resolver on %q instead of %s — DNS is NOT going through the tunnel",
			joinIPs(got), adapterName, dnsAddress)
	default:
		t.logf("tun: Windows confirms %q resolves through %s", adapterName, joinIPs(got))
	}
	// The full table matters because Windows may also still hold the physical
	// adapter's resolver, and multi-homed name resolution can pick either.
	t.logf("tun: resolvers in force: %s", resolverTable())
}

func hasIP(ips []net.IP, want string) bool {
	for _, ip := range ips {
		if ip.String() == want {
			return true
		}
	}
	return false
}

// waitForResolver polls until the adapter reports want, or the deadline
// passes. netsh returns before the change is always visible through
// GetAdaptersAddresses, so a single read can be a false alarm.
func waitForResolver(name, want string, timeout time.Duration) []net.IP {
	deadline := time.Now().Add(timeout)
	for {
		got := dnsServersOf(name)
		if hasIP(got, want) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// IPv4 is the address the adapter was given.
func (t *Tunnel) IPv4() string { return ipv4Address + "/15" }

// readLoop moves packets from the adapter into the stack.
func (t *Tunnel) readLoop() {
	defer t.wg.Done()
	for {
		select {
		case <-t.ctx.Done():
			return
		default:
		}

		pkt, err := t.session.ReceivePacket()
		if err == nil {
			// The ring slot is reused as soon as it is released, and the
			// stack may hold on to the buffer, so copy before releasing.
			data := make([]byte, len(pkt))
			copy(data, pkt)
			t.session.ReleaseReceivePacket(pkt)
			t.stack.inject(data)
			continue
		}
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			if !waitForPacket(t.readEvt, 200*time.Millisecond) {
				return
			}
			continue
		}
		// The adapter went away (or Close was called).
		return
	}
}

// writeLoop moves packets the stack produced back out to the adapter.
func (t *Tunnel) writeLoop() {
	defer t.wg.Done()
	for {
		pkt := t.link.ReadContext(t.ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		t.sendToAdapter(view.AsSlice())
		view.Release()
		pkt.DecRef()
	}
}

// sendToAdapter hands one IP packet to the adapter. Both the stack's write
// loop and the ICMP refusal path go through here so the ring has a single
// writer.
func (t *Tunnel) sendToAdapter(pkt []byte) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if t.sendClosed {
		return errors.New("tun: adapter session is closed")
	}
	buf, err := t.session.AllocateSendPacket(len(pkt))
	if err != nil {
		return err
	}
	copy(buf, pkt)
	t.session.SendPacket(buf)
	return nil
}

// Close removes the routes and the adapter. It is safe to call twice.
func (t *Tunnel) Close() error {
	t.closeOnce.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
		if t.link != nil {
			t.link.Close()
		}
		t.wg.Wait()
		t.teardown()
		t.logf("tun: adapter removed")
	})
	return t.closeErr
}

// teardown is the best-effort cleanup shared by Close and a failed Start.
func (t *Tunnel) teardown() {
	t.teardownOnce.Do(func() {
		for i := len(t.routes) - 1; i >= 0; i-- {
			t.routes[i].remove()
		}
		t.routes = nil
		flushDNS()

		if t.stack != nil {
			t.stack.close()
			t.stack = nil
		}
		// The adapter is destroyed when the last handle closes, which also
		// takes its routes and DNS settings with it.
		t.sendMu.Lock()
		t.sendClosed = true
		t.session.End()
		t.sendMu.Unlock()
		if t.adapter != nil {
			_ = t.adapter.Close()
			t.adapter = nil
		}
	})
}

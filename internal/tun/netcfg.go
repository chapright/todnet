//go:build windows

package tun

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ipUnicastIf is the socket option that pins a socket to one interface.
// The value is the interface index in network byte order.
const ipUnicastIf = 31

// PhysicalDial returns a dialer whose connections leave through the
// physical interface.
//
// The tunnel's adapter holds a default route, so a connection that is
// not pinned comes straight back into the tunnel: a destination that
// was meant to bypass it would be carried by it after all, and the
// direct fallback would wait for a timeout instead of connecting. This is
// what a bypassed destination and the direct fallback use.
func PhysicalDial() func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		dialer := net.Dialer{Timeout: 15 * time.Second}
		if idx, ok := physicalInterfaceIndex(adapterName); ok {
			dialer.Control = bindToInterface(idx)
		}
		return dialer.DialContext(ctx, network, address)
	}
}

// physicalInterfaceIndex returns the IPv4 index of the interface that
// carries the default route, leaving out the tunnel's own adapter.
func physicalInterfaceIndex(exclude string) (uint32, bool) {
	_, idx, err := physicalGateway(exclude)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// bindToInterface pins a socket to one interface.
func bindToInterface(ifIndex uint32) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var err error
		// IP_UNICAST_IF takes the interface index in network byte order.
		value := int(bits.ReverseBytes32(ifIndex))
		c.Control(func(fd uintptr) {
			err = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, ipUnicastIf, value)
		})
		return err
	}
}

func ptrOf(b []byte) unsafe.Pointer {
	if len(b) == 0 {
		return nil
	}
	return unsafe.Pointer(&b[0])
}

// Interface configuration goes through the built-in Windows tools rather than
// hand-rolled iphlpapi structs: MIB_UNICASTIPADDRESS_ROW and
// MIB_IPFORWARD_ROW2 are versioned, pointer-bearing structures where one wrong
// field offset corrupts memory. netsh and route are slower (~100 ms each) but
// they are the same code path an administrator would use by hand.

const (
	ifOperStatusUp = 1
	createNoWindow = 0x08000000
)

// route is one route this tunnel installed and must remove again.
type route struct {
	prefix string // dotted mask form, e.g. "0.0.0.0" / "255.255.255.255"
	dest   string
	gw     string
	ifIdx  uint32
}

// interfaceInfo describes the adapter after Windows created it.
type interfaceInfo struct {
	name    string
	ifIndex uint32
	luid    uint64
}

func runHidden(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
		}
		return text, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return text, nil
}

// findInterface returns the adapter with the given LUID, falling back to the
// alias. The LUID is what the driver reported, so it cannot be confused with
// another Wintun adapter on the same machine.
func findInterface(luid uint64, name string) (*interfaceInfo, error) {
	adapters, err := listAdapters()
	if err != nil {
		return nil, err
	}
	for _, a := range adapters {
		if luid != 0 && a.luid == luid {
			return &interfaceInfo{name: a.name, ifIndex: a.ifIndex, luid: a.luid}, nil
		}
	}
	for _, a := range adapters {
		if a.name == name {
			return &interfaceInfo{name: a.name, ifIndex: a.ifIndex, luid: a.luid}, nil
		}
	}
	return nil, fmt.Errorf("the virtual adapter %q did not appear", name)
}

type adapterInfo struct {
	name      string
	ifIndex   uint32
	luid      uint64
	metric    uint32
	up        bool
	hasV4     bool
	gateway   net.IP
	ipv4Addrs []net.IP
	dns       []net.IP
}

// listAdapters enumerates the IPv4 interfaces with their gateways.
func listAdapters() ([]adapterInfo, error) {
	const (
		afInet         = 2
		gaaFlagInclude = 0x0080 // GAA_FLAG_INCLUDE_GATEWAYS
	)
	var size uint32
	err := windows.GetAdaptersAddresses(afInet, gaaFlagInclude, 0, nil, &size)
	if err != nil && !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
		return nil, fmt.Errorf("enumerating network adapters: %w", err)
	}
	if size == 0 {
		return nil, errors.New("no network adapters reported")
	}

	buf := make([]byte, size)
	first := (*windows.IpAdapterAddresses)(nil)
	for attempt := 0; attempt < 3; attempt++ {
		first = (*windows.IpAdapterAddresses)(ptrOf(buf))
		err = windows.GetAdaptersAddresses(afInet, gaaFlagInclude, 0, first, &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, fmt.Errorf("enumerating network adapters: %w", err)
		}
		buf = make([]byte, size)
	}
	if err != nil {
		return nil, fmt.Errorf("enumerating network adapters: %w", err)
	}

	var out []adapterInfo
	for a := first; a != nil; a = a.Next {
		info := adapterInfo{
			name:    windows.UTF16PtrToString(a.FriendlyName),
			ifIndex: a.IfIndex,
			luid:    a.Luid,
			metric:  a.Ipv4Metric,
			up:      a.OperStatus == ifOperStatusUp,
		}
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			if ip := u.Address.IP(); ip != nil && ip.To4() != nil {
				info.hasV4 = true
				info.ipv4Addrs = append(info.ipv4Addrs, ip)
			}
		}
		for g := a.FirstGatewayAddress; g != nil; g = g.Next {
			if ip := g.Address.IP(); ip != nil && ip.To4() != nil && !ip.IsUnspecified() {
				info.gateway = ip
				break
			}
		}
		// The resolver list is what Windows will actually use. Reading it
		// back is the only way to tell "we set the resolver" from "Windows
		// kept the previous one", which is the difference between a working
		// tunnel and every name being resolved by the ISP.
		for d := a.FirstDnsServerAddress; d != nil; d = d.Next {
			if ip := d.Address.IP(); ip != nil {
				info.dns = append(info.dns, ip)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// physicalGateway finds the interface that currently carries the default
// route, so the edge host can be pinned to it.
func physicalGateway(exclude string) (gw net.IP, ifIndex uint32, err error) {
	adapters, err := listAdapters()
	if err != nil {
		return nil, 0, err
	}
	best := -1
	for i, a := range adapters {
		if a.name == exclude || !a.up || a.gateway == nil {
			continue
		}
		if best < 0 || a.metric < adapters[best].metric {
			best = i
		}
	}
	if best < 0 {
		return nil, 0, errors.New("no interface with a default gateway was found")
	}
	return adapters[best].gateway, adapters[best].ifIndex, nil
}

// setAddress assigns the tunnel address and takes over DNS for the adapter.
func setAddress(name, address, mask string, prefixLen, mtu int, dns string) error {
	if _, err := runHidden("netsh", "interface", "ipv4", "set", "address",
		"name="+name, "static", address, mask); err != nil {
		return fmt.Errorf("assigning %s to %s: %w", address, name, err)
	}
	if _, err := runHidden("netsh", "interface", "ipv4", "set", "subinterface",
		name, fmt.Sprintf("mtu=%d", mtu), "store=active"); err != nil {
		return fmt.Errorf("setting MTU %d: %w", mtu, err)
	}
	// A low interface metric is what makes Windows prefer the tunnel over the
	// physical link.
	if _, err := runHidden("netsh", "interface", "ipv4", "set", "interface",
		"interface="+name, "metric=1"); err != nil {
		return fmt.Errorf("setting interface metric: %w", err)
	}
	if _, err := runHidden("netsh", "interface", "ipv4", "set", "dnsservers",
		"name="+name, "static", dns, "primary", "validate=no"); err != nil {
		return fmt.Errorf("setting DNS server %s: %w", dns, err)
	}
	_ = prefixLen
	return nil
}

// addDefaultRoute sends everything that is not more specific through the
// tunnel. 0.0.0.0 as the next hop means "on link", which is what a
// point-to-point adapter needs.
func addDefaultRoute(ifIndex uint32) (route, error) {
	r := route{prefix: "0.0.0.0", dest: "0.0.0.0", gw: "0.0.0.0", ifIdx: ifIndex}
	_, err := runHidden("route", "add", "0.0.0.0", "mask", "0.0.0.0", "0.0.0.0",
		"metric", "1", "if", fmt.Sprint(ifIndex))
	if err != nil {
		// Older builds reject a 0.0.0.0 next hop from route.exe; netsh takes
		// the same route as on-link.
		if _, nerr := runHidden("netsh", "interface", "ipv4", "add", "route",
			"prefix=0.0.0.0/0", "interface="+fmt.Sprint(ifIndex), "nexthop=0.0.0.0",
			"metric=1", "store=active"); nerr != nil {
			return r, fmt.Errorf("adding the default route: %w", err)
		}
	}
	return r, nil
}

// addHostRoute keeps one address (the VPN edge) outside the tunnel.
func addHostRoute(ip net.IP, gateway net.IP, ifIndex uint32) (route, error) {
	v4 := ip.To4()
	if v4 == nil {
		return route{}, fmt.Errorf("%s is not an IPv4 address", ip)
	}
	r := route{prefix: "255.255.255.255", dest: v4.String(), gw: gateway.String(), ifIdx: ifIndex}
	_, err := runHidden("route", "add", v4.String(), "mask", "255.255.255.255",
		gateway.String(), "metric", "1", "if", fmt.Sprint(ifIndex))
	if err != nil {
		return r, fmt.Errorf("pinning %s to the physical gateway: %w", v4, err)
	}
	return r, nil
}

// localPrefixes are the address ranges that belong to the machine's own
// network, not to the internet: RFC 1918, link-local, CGNAT, multicast and
// broadcast. A VPN must never send these to the exit node — the edge refuses
// them ("Destination IP Prohibited") and the user loses their NAS, printer or
// router admin page while connected.
var localPrefixes = []struct{ dest, mask string }{
	{"10.0.0.0", "255.0.0.0"},
	{"172.16.0.0", "255.240.0.0"},
	{"192.168.0.0", "255.255.0.0"},
	{"169.254.0.0", "255.255.0.0"},
	{"100.64.0.0", "255.192.0.0"},
	{"192.0.0.0", "255.255.255.0"},
	{"224.0.0.0", "240.0.0.0"},
	{"255.255.255.255", "255.255.255.255"},
}

// addBypassRoutes pins every local range to the physical gateway. Failures are
// collected rather than fatal: a missing bypass degrades local access, but the
// tunnel itself still works.
func addBypassRoutes(gateway net.IP, ifIndex uint32) ([]route, error) {
	var (
		added []route
		errs  []string
	)
	for _, p := range localPrefixes {
		r := route{prefix: p.mask, dest: p.dest, gw: gateway.String(), ifIdx: ifIndex}
		_, err := runHidden("route", "add", p.dest, "mask", p.mask,
			gateway.String(), "metric", "1", "if", fmt.Sprint(ifIndex))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: %v", p.dest, p.mask, err))
			continue
		}
		added = append(added, r)
	}
	if len(errs) > 0 {
		return added, fmt.Errorf("bypassing local ranges: %s", strings.Join(errs, "; "))
	}
	return added, nil
}

// localRangesSummary is the short form logged at connect time.
func localRangesSummary(routes []route) string {
	parts := make([]string, 0, len(routes))
	for _, r := range routes {
		if r.dest == "255.255.255.255" {
			parts = append(parts, "broadcast")
			continue
		}
		parts = append(parts, r.dest+"/"+maskToBits(r.prefix))
	}
	return strings.Join(parts, " ")
}

// maskToBits renders a dotted mask as a prefix length.
func maskToBits(mask string) string {
	ip := net.ParseIP(mask).To4()
	if ip == nil {
		return mask
	}
	ones, _ := net.IPMask(ip).Size()
	return fmt.Sprint(ones)
}

func (r route) remove() {
	if r.dest == "" {
		return
	}
	_, _ = runHidden("route", "delete", r.dest, "mask", r.prefix, r.gw, "if", fmt.Sprint(r.ifIdx))
}

// flushDNS clears the resolver cache so stale answers stop pointing at
// addresses that only existed while the tunnel was up.
func flushDNS() {
	_, _ = runHidden("ipconfig", "/flushdns")
}

// NetworkSummary renders the adapter table for a diagnostics report: which
// interface carries the default route, its gateway and metric, whether the
// virtual adapter is present, and — the part that explains most "names do not
// resolve" reports — which resolver Windows has on each adapter.
func NetworkSummary() string {
	adapters, err := listAdapters()
	if err != nil {
		return "adapters: " + err.Error()
	}
	var b strings.Builder
	for _, a := range adapters {
		state := "down"
		if a.up {
			state = "up"
		}
		fmt.Fprintf(&b, "  %-28s idx=%-3d %-4s metric=%-4d", a.name, a.ifIndex, state, a.metric)
		if len(a.ipv4Addrs) > 0 {
			ips := make([]string, 0, len(a.ipv4Addrs))
			for _, ip := range a.ipv4Addrs {
				ips = append(ips, ip.String())
			}
			fmt.Fprintf(&b, " addr=%s", strings.Join(ips, ","))
		}
		if a.gateway != nil {
			fmt.Fprintf(&b, " gw=%s", a.gateway)
		}
		if len(a.dns) > 0 {
			fmt.Fprintf(&b, " dns=%s", joinIPs(a.dns))
		}
		b.WriteString("\n")
	}
	if gw, idx, err := physicalGateway(adapterName); err == nil {
		fmt.Fprintf(&b, "  physical default: gw=%s idx=%d\n", gw, idx)
	} else {
		fmt.Fprintf(&b, "  physical default: %v\n", err)
	}
	return strings.TrimRight(b.String(), "\n")
}

func joinIPs(ips []net.IP) string {
	parts := make([]string, 0, len(ips))
	for _, ip := range ips {
		parts = append(parts, ip.String())
	}
	return strings.Join(parts, ",")
}

// resolverTable is the one-line form logged at connect: which adapter owns
// which resolver, in the order Windows will try them. When a name resolves to
// something unexpected, this line is the first thing to look at.
func resolverTable() string {
	adapters, err := listAdapters()
	if err != nil {
		return "unavailable: " + err.Error()
	}
	var parts []string
	for _, a := range adapters {
		if len(a.dns) == 0 {
			continue
		}
		state := ""
		if a.up {
			state = " up"
		}
		parts = append(parts, fmt.Sprintf("%s(metric=%d%s)=[%s]", a.name, a.metric, state, joinIPs(a.dns)))
	}
	if len(parts) == 0 {
		return "no adapter has a resolver configured"
	}
	return strings.Join(parts, " ")
}

// dnsServersOf reports the resolvers Windows has on one adapter, or nil when
// the adapter is gone.
func dnsServersOf(name string) []net.IP {
	adapters, err := listAdapters()
	if err != nil {
		return nil
	}
	for _, a := range adapters {
		if a.name == name {
			return a.dns
		}
	}
	return nil
}

// waitForInterface polls until Windows has registered the new adapter.
func waitForInterface(luid uint64, name string, timeout time.Duration) (*interfaceInfo, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := findInterface(luid, name)
		if err == nil {
			return info, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

package engine

import "net"

// privateRanges are the address blocks the upstream edge will not route to.
// Asking it to reach one produces a 502 that reads like a VPN failure, when
// the real answer is "that address is on your own network".
//
// Loopback is deliberately absent: a client pointed at the proxy that asks for
// 127.0.0.1 is talking to something local (a dev server, or a test harness),
// and refusing it would break that while saving nothing — the request fails
// harmlessly either way.
var privateRanges = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"169.254.0.0/16",
		"100.64.0.0/10",
		"192.0.0.0/24",
		"198.18.0.0/15",
		"224.0.0.0/4",
		"0.0.0.0/8",
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isPrivateDestination reports whether host names a LAN address the tunnel
// cannot carry. Hostnames are never private: they resolve on the edge.
func isPrivateDestination(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() {
		return false
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return true
	}
	for _, n := range privateRanges {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

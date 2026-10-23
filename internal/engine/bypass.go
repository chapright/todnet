package engine

import (
	"net"
	"strings"
)

// BypassList holds the host patterns that skip the tunnel.
//
// A pattern is either a hostname — "example.com", which matches that
// host and nothing else — or a wildcard suffix, "*.example.com", which
// matches every host under that domain. The wildcard is the useful one:
// a site is rarely on a single host.
//
// Matching is on the hostname only and is case-insensitive: a hostname is
// not case-sensitive, and the port is not part of the name.
type BypassList struct {
	exact    map[string]bool
	suffixes []string
}

// ParseBypassList builds a matcher from the user's patterns. Blank
// patterns are dropped and the rest are lower-cased.
func ParseBypassList(patterns []string) *BypassList {
	b := &BypassList{exact: map[string]bool{}}
	for _, raw := range patterns {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "*.") {
			if suffix := strings.TrimPrefix(p, "*."); suffix != "" {
				b.suffixes = append(b.suffixes, suffix)
			}
			continue
		}
		b.exact[p] = true
	}
	return b
}

// Empty reports whether the list matches nothing.
func (b *BypassList) Empty() bool {
	return b == nil || (len(b.exact) == 0 && len(b.suffixes) == 0)
}

// Matches reports whether a destination should skip the tunnel.
//
// The authority may carry a port ("example.com:443"). A destination
// that is an address can only match an exact pattern: there is no name
// to match a wildcard against.
func (b *BypassList) Matches(authority string) bool {
	if b.Empty() {
		return false
	}
	host := hostOf(authority)
	if host == "" {
		return false
	}
	if b.exact[host] {
		return true
	}
	for _, suffix := range b.suffixes {
		if strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// hostOf returns the hostname of an authority, without the port and
// without the brackets an IPv6 address carries.
func hostOf(authority string) string {
	host := strings.TrimSpace(authority)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(host)
}

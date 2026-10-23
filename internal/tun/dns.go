//go:build windows

package tun

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The upstream edge only carries TCP CONNECT streams, so UDP cannot be
// tunnelled. DNS is the one UDP protocol that must still work, and it is
// answered here instead: the adapter's resolver points at a dedicated address
// inside the tunnel subnet (see dnsAddress in tun.go), every query that
// arrives on port 53 is relayed to a DNS-over-HTTPS endpoint through the same
// upstream session, and the answers are cached.
//
// Everything else on UDP (QUIC, voice calls) is dropped, which makes browsers
// fall back to TCP.
const (
	// The primary is Mozilla's resolver (operated by Cloudflare). It used to
	// be dns.mozilla.org, which no longer resolves: every uncached lookup
	// dialled a non-existent host, failed, and only then fell back — a
	// wasted round trip on every name, visible in the log as "every answer
	// came via the fallback".
	dnsUpstreamPrimary = "https://mozilla.cloudflare-dns.com/dns-query"
	// A different operator on purpose: two hostnames on the same service
	// have no redundancy when that service is the thing that is down.
	dnsUpstreamFallback = "https://dns.google/dns-query"

	dnsMaxResponse = 4096
	dnsCacheMaxTTL = 300 * time.Second
	dnsCacheMinTTL = 10 * time.Second
	// A resolver client gives up long before a full minute: Windows retries
	// after roughly a second and drops the server after a handful. Two DoH
	// attempts have to fit inside that budget, or the answer arrives after
	// the lookup has already been reported as failed.
	dnsLookupTimeout = 6 * time.Second
	// How long a failing endpoint is benched. Long enough that a dead
	// resolver stops costing a wasted dial on every lookup, short enough
	// that a transient outage recovers on its own.
	dnsUpstreamCooldown = 5 * time.Minute
)

type dnsRelay struct {
	client   *http.Client
	upstream []string
	cache    *dnsCache
	logf     func(string, ...any)
	warnf    func(string, ...any)

	// counters make "is the relay on the path at all?" answerable from a log
	// instead of guesswork. A report where queries is zero means Windows is
	// still resolving somewhere else, which is the whole failure mode this
	// relay exists to prevent.
	queries   atomic.Int64
	answered  atomic.Int64
	failed    atomic.Int64
	cacheHits atomic.Int64

	mu        sync.Mutex
	lastName  string
	lastErr   string
	downUntil []time.Time
}

// dnsSnapshot is a consistent-enough read of the counters for one log line.
type dnsSnapshot struct {
	queries, answered, failed, cacheHits int64
}

func (d *dnsRelay) snapshot() dnsSnapshot {
	// The outcomes are read first and queries last, so the totals can never
	// read as "more handled than received". Reading them the other way
	// produced "49 queries, 48 answered, 5 failed" in a report, which looks
	// like a broken counter rather than a torn sample.
	s := dnsSnapshot{
		answered:  d.answered.Load(),
		failed:    d.failed.Load(),
		cacheHits: d.cacheHits.Load(),
	}
	s.queries = d.queries.Load()
	return s
}

func newDNSRelay(dial func(ctx context.Context, network, address string) (net.Conn, error),
	logf, warnf func(string, ...any)) *dnsRelay {

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dial(ctx, "tcp", address)
		},
		TLSHandshakeTimeout:   dnsLookupTimeout,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	r := &dnsRelay{
		client:   &http.Client{Transport: transport, Timeout: dnsLookupTimeout},
		upstream: []string{dnsUpstreamPrimary, dnsUpstreamFallback},
		cache:    newDNSCache(),
		logf:     logf,
		warnf:    warnf,
	}
	r.downUntil = make([]time.Time, len(r.upstream))
	go r.report()
	return r
}

// Warm opens the DoH connection before the first real lookup.
//
// A cold TLS handshake through a tunnel that has just come up can take longer
// than one attempt's share of the budget, and a lookup that misses the budget
// is reported to Windows as a failure even though nothing is wrong. Priming it
// once the stack is running means the first name a user types is answered at
// full speed. It is deliberately not called from the constructor: building a
// relay should not perform I/O.
func (d *dnsRelay) Warm() { go d.warm() }

func (d *dnsRelay) warm() {
	query, err := buildQuery("example.com")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
	defer cancel()
	started := time.Now()
	if _, err := d.post(ctx, d.upstream[0], query); err != nil {
		d.debugf("tun: dns warmup through %s failed after %s: %v",
			d.upstream[0], time.Since(started).Round(time.Millisecond), err)
		return
	}
	d.debugf("tun: dns warmup through %s took %s", d.upstream[0],
		time.Since(started).Round(time.Millisecond))
}

// buildQuery assembles a minimal A query for a name.
func buildQuery(name string) ([]byte, error) {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x5a5a, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name + "."),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return b.Finish()
}


// report summarises DNS activity every 30 seconds. It is deliberately a
// heartbeat rather than one line per query: the log buffer is short, and the
// useful fact is "the relay is being used, and how well", not every name.
func (d *dnsRelay) report() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var prev dnsSnapshot
	for range ticker.C {
		now := d.snapshot()
		if now.queries == prev.queries {
			continue
		}
		d.mu.Lock()
		last, lastErr := d.lastName, d.lastErr
		d.mu.Unlock()

		failed := delta(now.failed, prev.failed)
		line := fmt.Sprintf("tun: dns 30s: %d queries, %d answered (%d from cache), %d failed, last=%s",
			delta(now.queries, prev.queries), delta(now.answered, prev.answered),
			delta(now.cacheHits, prev.cacheHits), failed, last)
		// The per-query warning has usually scrolled out of a pasted report
		// by the time anyone reads it, so the reason rides along here too.
		if failed > 0 && lastErr != "" {
			line += " (" + lastErr + ")"
		}
		d.logf("%s", line)
		prev = now
	}
}

// delta keeps a torn sample from producing a negative count.
func delta(now, prev int64) int64 {
	if now < prev {
		return 0
	}
	return now - prev
}

// handle answers one query and logs what happened. Every field here exists
// because a report without them cannot distinguish "the tunnel resolver was
// never consulted" from "it was consulted and the answer was wrong".
func (d *dnsRelay) handle(ctx context.Context, query []byte, from string) ([]byte, error) {
	n := d.queries.Add(1)
	name, qtype := questionSummary(query)
	d.mu.Lock()
	d.lastName = name + " " + qtype
	d.mu.Unlock()

	started := time.Now()
	resp, cached, upstream, err := d.exchange(ctx, query)
	elapsed := time.Since(started).Round(time.Millisecond)

	if err != nil {
		d.failed.Add(1)
		d.mu.Lock()
		d.lastErr = err.Error()
		d.mu.Unlock()
		d.warnf("tun: dns #%d %s %s <- %s FAILED after %s: %v",
			n, name, qtype, from, elapsed, err)
		return nil, err
	}
	d.answered.Add(1)
	if cached {
		d.cacheHits.Add(1)
	}
	source := upstream
	if cached {
		source = "cache"
	}
	if n == 1 {
		// One unconditional line: proof the relay is on the path. Its absence
		// from a report is itself the diagnosis.
		d.logf("tun: dns relay is answering through the tunnel (first query: %s %s from %s)", name, qtype, from)
	}
	// Per-query detail is verbose-only; the 30 s heartbeat covers the rest.
	d.debugf("tun: dns #%d %s %s <- %s -> %s in %s via %s",
		n, name, qtype, from, summarizeResponse(resp), elapsed, source)
	return resp, nil
}

// debugf is the per-query line. The relay has no access to the engine's
// verbosity flag, so it routes through a hook the engine installs; when that
// hook is absent the line is dropped rather than flooding the buffer.
var dnsDebugf func(string, ...any)

// SetDebugLogger installs the destination for per-query detail. It is called
// once at startup with the engine's debug logger.
func SetDebugLogger(fn func(string, ...any)) { dnsDebugf = fn }

func (d *dnsRelay) debugf(format string, args ...any) {
	if dnsDebugf != nil {
		dnsDebugf(format, args...)
	}
}

// questionSummary names the lookup for the log. A relay line that only says
// "answered a query" cannot tell a working resolver from one that is answering
// the wrong question, so the name and record type are always carried.
func questionSummary(query []byte) (name, qtype string) {
	var p dnsmessage.Parser
	if _, err := p.Start(query); err != nil {
		return "?", "?"
	}
	q, err := p.Question()
	if err != nil {
		return "?", "?"
	}
	return strings.TrimSuffix(q.Name.String(), "."), typeLabel(q.Type)
}

// typeLabel renders a record type the way a person writes it. dnsmessage's
// own String() produces "TypeA" and "RCodeSuccess", which reads like a
// debugging leftover in a log meant for support.
func typeLabel(t dnsmessage.Type) string {
	switch t {
	case dnsmessage.TypeA:
		return "A"
	case dnsmessage.TypeAAAA:
		return "AAAA"
	case dnsmessage.TypeCNAME:
		return "CNAME"
	case dnsmessage.TypeHTTPS:
		return "HTTPS"
	case dnsmessage.TypeTXT:
		return "TXT"
	case dnsmessage.TypeMX:
		return "MX"
	case dnsmessage.TypeNS:
		return "NS"
	case dnsmessage.TypePTR:
		return "PTR"
	case dnsmessage.TypeSOA:
		return "SOA"
	case dnsmessage.TypeSRV:
		return "SRV"
	default:
		return strings.TrimPrefix(t.String(), "Type")
	}
}

// rcodeLabel is the short form of a response code.
func rcodeLabel(c dnsmessage.RCode) string {
	switch c {
	case dnsmessage.RCodeSuccess:
		return "NOERROR"
	case dnsmessage.RCodeFormatError:
		return "FORMERR"
	case dnsmessage.RCodeServerFailure:
		return "SERVFAIL"
	case dnsmessage.RCodeNameError:
		return "NXDOMAIN"
	case dnsmessage.RCodeNotImplemented:
		return "NOTIMP"
	case dnsmessage.RCodeRefused:
		return "REFUSED"
	default:
		return strings.TrimPrefix(c.String(), "RCode")
	}
}

// summarizeResponse renders what came back, so "resolved to the wrong thing"
// and "resolved to nothing" are distinguishable in a single log line.
func summarizeResponse(resp []byte) string {
	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil {
		return fmt.Sprintf("%d B (unparsable)", len(resp))
	}
	rcode := rcodeLabel(h.RCode)
	if err := p.SkipAllQuestions(); err != nil {
		return rcode
	}

	// Addresses first. A CNAME chain fills the answer section with aliases,
	// so reading the first few answers verbatim reported
	// "NOERROR: cname a cname b" for a lookup that had resolved perfectly
	// well — the address was simply the fourth record.
	var addrs, aliases []string
	for len(addrs) < 3 {
		ah, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			break
		}
		switch ah.Type {
		case dnsmessage.TypeA:
			if r, err := p.AResource(); err == nil {
				addrs = append(addrs, net.IP(r.A[:]).String())
				continue
			}
		case dnsmessage.TypeAAAA:
			if r, err := p.AAAAResource(); err == nil {
				addrs = append(addrs, net.IP(r.AAAA[:]).String())
				continue
			}
		case dnsmessage.TypeCNAME:
			if r, err := p.CNAMEResource(); err == nil {
				if len(aliases) < 2 {
					aliases = append(aliases, strings.TrimSuffix(r.CNAME.String(), "."))
				}
				continue
			}
		}
		// Anything else (HTTPS/SVCB, TXT, ...) is not worth decoding:
		// skipping keeps the parser in sync without pretending to explain it.
		if err := p.SkipAnswer(); err != nil {
			break
		}
	}

	switch {
	case len(addrs) > 0:
		return rcode + ": " + strings.Join(addrs, " ")
	case len(aliases) > 0:
		return rcode + ": no address, cname " + strings.Join(aliases, " -> ")
	default:
		return rcode + ", no answers"
	}
}

// exchange answers one DNS query, preferring the cache.
func (d *dnsRelay) exchange(ctx context.Context, query []byte) ([]byte, bool, string, error) {
	key, ok := questionKey(query)
	if ok {
		if cached, ttl := d.cache.get(key); cached != nil {
			out := make([]byte, len(cached))
			copy(out, cached)
			// The ID is per-request; everything else is reusable.
			copy(out[:2], query[:2])
			_ = ttl
			return out, true, "", nil
		}
	}

	var lastErr error
	// Each endpoint gets half the budget, so a stalled primary still leaves
	// room for the fallback inside the resolver's own patience.
	perAttempt := dnsLookupTimeout / time.Duration(len(d.upstream))
	for _, i := range d.candidates() {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttempt)
		resp, err := d.post(attemptCtx, d.upstream[i], query)
		cancel()
		if err == nil {
			d.markUp(i)
			if ok {
				d.cache.put(key, resp, minTTL(resp))
			}
			return resp, false, d.upstream[i], nil
		}
		d.markDown(i, err)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no DNS-over-HTTPS endpoint answered")
	}
	return nil, false, "", lastErr
}

// candidates lists the endpoints to try, in order, skipping any that failed
// recently.
//
// Without this, one dead endpoint costs a wasted dial on every single lookup
// and the log looks like it is working — every answer simply arrives "via"
// the fallback. If they are all benched they are all tried anyway: failing a
// lookup on the strength of bookkeeping would be worse than one wasted attempt.
func (d *dnsRelay) candidates() []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	ready := make([]int, 0, len(d.upstream))
	for i := range d.upstream {
		if !now.Before(d.downUntil[i]) {
			ready = append(ready, i)
		}
	}
	if len(ready) == 0 {
		for i := range d.upstream {
			ready = append(ready, i)
		}
	}
	return ready
}

// markDown benches an endpoint and says why, once per outage.
func (d *dnsRelay) markDown(i int, err error) {
	now := time.Now()
	d.mu.Lock()
	first := !now.Before(d.downUntil[i])
	d.downUntil[i] = now.Add(dnsUpstreamCooldown)
	d.mu.Unlock()
	if first {
		d.warnf("tun: dns endpoint %s is not answering (%v); using the other one for %s",
			d.upstream[i], err, dnsUpstreamCooldown)
	}
}

// markUp returns an endpoint to service after it answered again.
func (d *dnsRelay) markUp(i int) {
	d.mu.Lock()
	d.downUntil[i] = time.Time{}
	d.mu.Unlock()
}

func (d *dnsRelay) post(ctx context.Context, upstream string, query []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("%s returned HTTP %d", upstream, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dnsMaxResponse))
	if err != nil {
		return nil, err
	}
	if len(body) < 12 {
		return nil, errors.New("short DNS response")
	}
	return body, nil
}

// serveUDP answers queries arriving on the tunnel address.
func (d *dnsRelay) serveUDP(conn net.Conn) {
	defer conn.Close()
	// The client is named in every line: with several resolvers configured,
	// "which app asked" is the first thing worth knowing.
	from := addrLabel(conn.RemoteAddr())
	buf := make([]byte, dnsMaxResponse)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		query := append([]byte(nil), buf[:n]...)

		ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
		resp, err := d.handle(ctx, query, from)
		cancel()
		if err != nil {
			// handle already logged the reason, with the name and type.
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(resp); err != nil {
			return
		}
	}
}

// serveTCP handles the length-prefixed DNS-over-TCP form, which Windows uses
// when a response does not fit in a UDP datagram.
func (d *dnsRelay) serveTCP(conn net.Conn) {
	defer conn.Close()
	from := addrLabel(conn.RemoteAddr()) + " (tcp)"
	for {
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		var size uint16
		if err := binary.Read(conn, binary.BigEndian, &size); err != nil {
			return
		}
		if size == 0 || size > dnsMaxResponse {
			return
		}
		query := make([]byte, size)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), dnsLookupTimeout)
		resp, err := d.handle(ctx, query, from)
		cancel()
		if err != nil {
			return
		}
		if len(resp) > 0xffff {
			return
		}
		out := make([]byte, 2+len(resp))
		binary.BigEndian.PutUint16(out[:2], uint16(len(resp)))
		copy(out[2:], resp)
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// addrLabel keeps log lines short when a remote address is unavailable.
func addrLabel(a net.Addr) string {
	if a == nil {
		return "?"
	}
	return a.String()
}

/* ---------------- cache ---------------- */

type dnsCacheEntry struct {
	response []byte
	expires  time.Time
}

type dnsCache struct {
	mu      sync.Mutex
	entries map[string]dnsCacheEntry
}

func newDNSCache() *dnsCache {
	return &dnsCache{entries: make(map[string]dnsCacheEntry)}
}

func (c *dnsCache) get(key string) ([]byte, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, 0
	}
	if time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, 0
	}
	return e.response, time.Until(e.expires)
}

func (c *dnsCache) put(key string, response []byte, ttl uint32) {
	life := time.Duration(ttl) * time.Second
	if life < dnsCacheMinTTL {
		life = dnsCacheMinTTL
	}
	if life > dnsCacheMaxTTL {
		life = dnsCacheMaxTTL
	}
	stored := make([]byte, len(response))
	copy(stored, response)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Keep the map from growing without bound on a long session.
	if len(c.entries) > 2048 {
		now := time.Now()
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) > 2048 {
			c.entries = make(map[string]dnsCacheEntry)
		}
	}
	c.entries[key] = dnsCacheEntry{response: stored, expires: time.Now().Add(life)}
}

func questionKey(query []byte) (string, bool) {
	var p dnsmessage.Parser
	if _, err := p.Start(query); err != nil {
		return "", false
	}
	q, err := p.Question()
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s|%d|%d", q.Name.String(), q.Type, q.Class), true
}

func minTTL(response []byte) uint32 {
	var p dnsmessage.Parser
	if _, err := p.Start(response); err != nil {
		return 0
	}
	if err := p.SkipAllQuestions(); err != nil {
		return 0
	}
	var min uint32
	for {
		h, err := p.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return 0
		}
		if min == 0 || h.TTL < min {
			min = h.TTL
		}
		if err := p.SkipAnswer(); err != nil {
			return 0
		}
	}
	return min
}

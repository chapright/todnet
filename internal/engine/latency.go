package engine

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"
)

// LatencyCache holds the measured round-trip time to each edge.
//
// The locations list used to have no latency at all: the interface had
// code to draw a ping and the signal bars that go with it, but the field
// they read was never sent, so neither ever appeared. The edge is the one
// thing the user can measure, and a list of countries with no distances is
// the weakest surface in the app.
type LatencyCache struct {
	mu     sync.RWMutex
	values map[string]latencyEntry
	ttl    time.Duration
}

type latencyEntry struct {
	ms int
	at time.Time
}

// DefaultLatencyTTL is how long a measurement is considered fresh. Five
// minutes is long enough that a route change is picked up, and short
// enough that a stale number is not shown for long.
const DefaultLatencyTTL = 5 * time.Minute

// LatencyProbeTimeout bounds one measurement. A probe that hangs must not
// hold up the whole list.
const LatencyProbeTimeout = 3 * time.Second

func NewLatencyCache(ttl time.Duration) *LatencyCache {
	if ttl <= 0 {
		ttl = DefaultLatencyTTL
	}
	return &LatencyCache{values: make(map[string]latencyEntry), ttl: ttl}
}

// Get returns the cached measurement for a host, if it is still fresh.
func (c *LatencyCache) Get(host string) (int, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.values[host]
	if !ok {
		return 0, false
	}
	if time.Since(e.at) > c.ttl {
		return 0, false
	}
	return e.ms, true
}

// Set records a measurement.
func (c *LatencyCache) Set(host string, ms int) {
	if c == nil || host == "" {
		return
	}
	c.mu.Lock()
	c.values[host] = latencyEntry{ms: ms, at: time.Now()}
	c.mu.Unlock()
}

// Prune drops the entries that are no longer fresh.
func (c *LatencyCache) Prune() {
	if c == nil {
		return
	}
	c.mu.Lock()
	for host, e := range c.values {
		if time.Since(e.at) > c.ttl {
			delete(c.values, host)
		}
	}
	c.mu.Unlock()
}

// MeasureEdgeLatency times a TCP handshake to an edge.
//
// It is a TCP connect and nothing more: no TLS, no proxy protocol, no
// traffic. The number is the time to reach the edge, which is what the
// user is choosing between. A failed probe returns an error rather than a
// large number, so an unreachable edge does not look like a slow one.
func MeasureEdgeLatency(ctx context.Context, hostPort string) (int, error) {
	dialer := &net.Dialer{Timeout: LatencyProbeTimeout}
	ctx, cancel := context.WithTimeout(ctx, LatencyProbeTimeout)
	defer cancel()

	started := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return 0, err
	}
	ms := int(time.Since(started).Milliseconds())
	_ = conn.Close()
	if ms < 0 {
		ms = 0
	}
	return ms, nil
}

// MeasureLatencies probes a set of edge addresses concurrently and returns
// the measurements.
//
// The probes run in parallel with a small cap on the number at once: thirty
// simultaneous TLS-adjacent handshakes is enough to be noticed by a
// network, and the result is the same either way.
func MeasureLatencies(ctx context.Context, hosts []string, cache *LatencyCache) map[string]int {
	out := make(map[string]int)
	if len(hosts) == 0 {
		return out
	}

	// Only the addresses that are missing or stale are probed.
	var todo []string
	for _, h := range hosts {
		if h == "" {
			continue
		}
		if _, ok := cache.Get(h); ok {
			continue
		}
		todo = append(todo, h)
	}
	if len(todo) == 0 {
		return out
	}

	const parallel = 6
	sem := make(chan struct{}, parallel)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, host := range todo {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			ms, err := MeasureEdgeLatency(ctx, host)
			if err != nil {
				return
			}
			cache.Set(host, ms)
			mu.Lock()
			out[host] = ms
			mu.Unlock()
		}(host)
	}
	wg.Wait()
	return out
}

// ApplyLatency copies the cached measurements onto a location list.
func ApplyLatency(locs []Location, cache *LatencyCache) {
	if cache == nil {
		return
	}
	for i := range locs {
		if ms, ok := cache.Get(locs[i].Host); ok {
			locs[i].LatencyMs = ms
		} else {
			locs[i].LatencyMs = 0
		}
	}
}

// FastestLocation returns the location with the lowest measured latency
// among the candidates, or the first candidate when nothing has been
// measured yet.
//
// This is what makes "Recommended" honest. The label used to claim the
// app chose the fastest available edge while it simply took the first
// entry in an alphabetical list.
func FastestLocation(candidates []Location) *Location {
	if len(candidates) == 0 {
		return nil
	}
	best := -1
	bestMs := 0
	for i := range candidates {
		ms := candidates[i].LatencyMs
		if ms <= 0 {
			continue
		}
		if best == -1 || ms < bestMs {
			best = i
			bestMs = ms
		}
	}
	if best == -1 {
		// Nothing has been measured yet: keep the original order rather
		// than pretending one of them is faster.
		chosen := candidates[0]
		return &chosen
	}
	chosen := candidates[best]
	return &chosen
}

// SortByLatency orders locations by measured latency, with the unmeasured
// ones last.
func SortByLatency(locs []Location) []Location {
	out := append([]Location(nil), locs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].LatencyMs, out[j].LatencyMs
		if a <= 0 {
			return false
		}
		if b <= 0 {
			return true
		}
		return a < b
	})
	return out
}

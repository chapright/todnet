package engine

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestLatencyCacheExpires(t *testing.T) {
	c := NewLatencyCache(50 * time.Millisecond)
	c.Set("edge:2499", 24)

	if ms, ok := c.Get("edge:2499"); !ok || ms != 24 {
		t.Fatalf("want 24ms, got %d (ok=%v)", ms, ok)
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get("edge:2499"); ok {
		t.Fatal("a stale measurement should not be served")
	}
}

func TestMeasureEdgeLatencyTimesARealDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	ms, err := MeasureEdgeLatency(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("MeasureEdgeLatency: %v", err)
	}
	if ms < 0 {
		t.Fatalf("want a non-negative measurement, got %d", ms)
	}
}

func TestMeasureEdgeLatencyFailsOnAClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// An unreachable edge must report a failure rather than a large
	// number: a slow edge and an absent one are different facts.
	if _, err := MeasureEdgeLatency(context.Background(), addr); err == nil {
		t.Fatal("want an error for a closed port")
	}
}

// The fastest edge is the one with the lowest measurement, and the
// unmeasured ones must not be treated as the fastest.
func TestFastestLocationPicksTheClosestEdge(t *testing.T) {
	locs := []Location{
		{CountryCode: "DE", Host: "de:2499", LatencyMs: 41},
		{CountryCode: "NL", Host: "nl:2499", LatencyMs: 24},
		{CountryCode: "US", Host: "us:2499", LatencyMs: 142},
	}
	got := FastestLocation(locs)
	if got == nil || got.CountryCode != "NL" {
		t.Fatalf("want NL, got %+v", got)
	}
}

func TestFastestLocationKeepsTheOrderWhenNothingIsMeasured(t *testing.T) {
	locs := []Location{
		{CountryCode: "AT", Host: "at:2499"},
		{CountryCode: "BE", Host: "be:2499"},
	}
	got := FastestLocation(locs)
	if got == nil || got.CountryCode != "AT" {
		t.Fatalf("want the first entry, got %+v", got)
	}
}

func TestFastestLocationIgnoresUnmeasuredEdges(t *testing.T) {
	locs := []Location{
		{CountryCode: "AT", Host: "at:2499"},
		{CountryCode: "NL", Host: "nl:2499", LatencyMs: 24},
	}
	got := FastestLocation(locs)
	if got == nil || got.CountryCode != "NL" {
		t.Fatalf("want the measured edge, got %+v", got)
	}
}

func TestApplyLatencyCopiesTheMeasurements(t *testing.T) {
	c := NewLatencyCache(time.Minute)
	c.Set("nl:2499", 24)
	locs := []Location{{Host: "nl:2499"}, {Host: "de:2499"}}
	ApplyLatency(locs, c)
	if locs[0].LatencyMs != 24 {
		t.Fatalf("want 24ms, got %d", locs[0].LatencyMs)
	}
	if locs[1].LatencyMs != 0 {
		t.Fatalf("an unmeasured edge should stay at zero, got %d", locs[1].LatencyMs)
	}
}

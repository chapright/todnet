//go:build windows

package tun

import (
	"encoding/binary"
	"testing"
	"time"
)

// The adapter enumeration is read-only, but it is also the part most likely to
// be wrong (Win32 struct layouts), so it is worth exercising on every run.
func TestListAdapters(t *testing.T) {
	adapters, err := listAdapters()
	if err != nil {
		t.Fatalf("listAdapters: %v", err)
	}
	if len(adapters) == 0 {
		t.Fatal("no adapters reported")
	}
	for _, a := range adapters {
		if a.name == "" {
			t.Errorf("adapter with empty name: %+v", a)
		}
		if a.ifIndex == 0 {
			t.Errorf("adapter %q has no interface index", a.name)
		}
	}
}

func TestPhysicalGateway(t *testing.T) {
	gw, idx, err := physicalGateway("Todnet")
	if err != nil {
		// A machine with no default route (offline sandbox) is not a failure.
		t.Skipf("no default gateway available: %v", err)
	}
	if gw.To4() == nil {
		t.Errorf("gateway %v is not IPv4", gw)
	}
	if idx == 0 {
		t.Error("gateway interface index is zero")
	}
}

// A DNS query for example.com, hand-assembled so the test does not depend on
// the network.
func dnsQuery(id uint16, name string) []byte {
	var b []byte
	b = binary.BigEndian.AppendUint16(b, id)
	b = binary.BigEndian.AppendUint16(b, 0x0100) // standard query, recursion desired
	b = binary.BigEndian.AppendUint16(b, 1)      // QDCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)      // ANCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)      // NSCOUNT
	b = binary.BigEndian.AppendUint16(b, 0)      // ARCOUNT
	for _, label := range splitLabels(name) {
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, 1) // A
	b = binary.BigEndian.AppendUint16(b, 1) // IN
	return b
}

func splitLabels(name string) []string {
	var out []string
	start := 0
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			out = append(out, name[start:i])
			start = i + 1
		}
	}
	return append(out, name[start:])
}

func TestQuestionKey(t *testing.T) {
	q := dnsQuery(0x1234, "example.com")
	key, ok := questionKey(q)
	if !ok {
		t.Fatal("questionKey rejected a valid query")
	}
	if want := "example.com.|1|1"; key != want {
		t.Errorf("questionKey = %q, want %q", key, want)
	}

	// The ID must not be part of the key: that is what makes the cache usable.
	q2 := dnsQuery(0xbeef, "example.com")
	key2, ok := questionKey(q2)
	if !ok || key2 != key {
		t.Errorf("key depends on the query ID: %q vs %q", key2, key)
	}
}

func TestCacheReusesAnswerWithNewID(t *testing.T) {
	c := newDNSCache()
	query := dnsQuery(0x1111, "example.com")

	// A minimal answer: header + one A record with a 60 s TTL.
	resp := make([]byte, 12)
	binary.BigEndian.PutUint16(resp[0:], 0x1111)
	binary.BigEndian.PutUint16(resp[2:], 0x8180)
	binary.BigEndian.PutUint16(resp[4:], 1)
	binary.BigEndian.PutUint16(resp[6:], 1)
	// question
	resp = append(resp, dnsQuery(0, "example.com")[12:]...)
	// answer: name pointer, type A, class IN, ttl 60, rdlength 4, 1.2.3.4
	resp = append(resp, 0xc0, 0x0c)
	resp = append(resp, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 1, 2, 3, 4)

	if ttl := minTTL(resp); ttl != 60 {
		t.Fatalf("minTTL = %d, want 60", ttl)
	}

	key, ok := questionKey(query)
	if !ok {
		t.Fatal("questionKey failed")
	}
	c.put(key, resp, 60)

	got, remaining := c.get(key)
	if got == nil {
		t.Fatal("cache miss immediately after put")
	}
	if remaining <= 0 || remaining > time.Minute {
		t.Errorf("unexpected remaining TTL %v", remaining)
	}
	if binary.BigEndian.Uint16(got[:2]) != 0x1111 {
		t.Errorf("cached copy has the wrong ID")
	}
}

func TestCacheExpires(t *testing.T) {
	c := newDNSCache()
	c.put("k", []byte{0, 1, 2, 3}, 0)
	if got, _ := c.get("k"); got == nil {
		t.Fatal("entry with a zero TTL should still be cached briefly")
	}
	// Force expiry.
	c.mu.Lock()
	e := c.entries["k"]
	e.expires = time.Now().Add(-time.Second)
	c.entries["k"] = e
	c.mu.Unlock()
	if got, _ := c.get("k"); got != nil {
		t.Error("expired entry was returned")
	}
}

package engine

import "testing"

// The bypass list decides what skips the tunnel, so the pattern rules
// have to be exactly what the settings screen promises.

func TestBypassMatchesAWildcardSuffix(t *testing.T) {
	b := ParseBypassList([]string{"*.ir"})

	for _, host := range []string{"example.ir", "www.example.ir", "a.b.c.ir"} {
		if !b.Matches(host) {
			t.Fatalf("%s should be bypassed", host)
		}
	}
	// The domain itself is not a host under it.
	for _, host := range []string{"ir", "notir", "example.com", "ir.example.com"} {
		if b.Matches(host) {
			t.Fatalf("%s should not be bypassed", host)
		}
	}
}

func TestBypassMatchesAnExactHost(t *testing.T) {
	b := ParseBypassList([]string{"example.com"})

	if !b.Matches("example.com") {
		t.Fatal("the exact host should be bypassed")
	}
	if b.Matches("www.example.com") {
		t.Fatal("an exact pattern should not match a host under it")
	}
}

func TestBypassIgnoresCaseAndThePort(t *testing.T) {
	b := ParseBypassList([]string{"Example.COM", "*.IR"})

	for _, host := range []string{"example.com", "EXAMPLE.com", "example.com:443", "WWW.Example.IR:80"} {
		if !b.Matches(host) {
			t.Fatalf("%s should be bypassed", host)
		}
	}
}

func TestBypassSkipsBlankPatterns(t *testing.T) {
	b := ParseBypassList([]string{"", "   ", "*.ir"})

	if b.Empty() {
		t.Fatal("the list should not be empty")
	}
	if !b.Matches("example.ir") {
		t.Fatal("the one real pattern should still work")
	}
	if b.Matches("example.com") {
		t.Fatal("nothing else should be bypassed")
	}
}

func TestAnEmptyBypassListMatchesNothing(t *testing.T) {
	b := ParseBypassList(nil)
	if !b.Empty() {
		t.Fatal("the list should be empty")
	}
	if b.Matches("example.com") {
		t.Fatal("an empty list should not match")
	}
	var nilList *BypassList
	if nilList.Matches("example.com") {
		t.Fatal("a nil list should not match")
	}
}

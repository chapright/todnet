//go:build windows

package tun

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// The relay's log lines are the only evidence a report contains, so the
// helpers that build them have to name the lookup and describe the answer.
// A line that says "answered a query" cannot distinguish a working resolver
// from one answering the wrong question.
func TestQuestionSummaryNamesTheLookup(t *testing.T) {
	name, qtype := questionSummary(dnsQuery(0x1234, "workbuddyai.com"))
	if name != "workbuddyai.com" {
		t.Errorf("name = %q, want workbuddyai.com", name)
	}
	if qtype != "A" {
		t.Errorf("qtype = %q, want A", qtype)
	}
}

func TestQuestionSummarySurvivesGarbage(t *testing.T) {
	// A truncated or non-DNS datagram must not panic the relay: it is
	// logged, not fatal.
	if name, qtype := questionSummary([]byte{0x00}); name == "" || qtype == "" {
		t.Errorf("empty labels for garbage input: %q %q", name, qtype)
	}
	if got := summarizeResponse([]byte{0x00, 0x01}); got == "" {
		t.Error("empty summary for garbage response")
	}
}

// The answer values are what make "resolved, but to the wrong thing"
// visible. A summary that only says "NOERROR" hides exactly the failure
// that produced NS_ERROR_UNKNOWN_HOST in the field.
func TestSummarizeResponseShowsTheAddress(t *testing.T) {
	resp := dnsResponse(t, "example.com", [4]byte{93, 184, 216, 34})
	got := summarizeResponse(resp)
	if !strings.Contains(got, "93.184.216.34") {
		t.Errorf("summary %q does not contain the answer address", got)
	}
	if !strings.Contains(got, "NOERROR") {
		t.Errorf("summary %q does not contain the rcode", got)
	}
}

func TestSummarizeResponseReportsEmptyAnswers(t *testing.T) {
	resp := dnsResponse(t, "example.com", [4]byte{})
	if got := summarizeResponse(resp); !strings.Contains(got, "no answers") {
		t.Errorf("summary %q does not report an empty answer section", got)
	}
}

// A CNAME chain puts aliases before the address. Reporting the first few
// answers verbatim printed "NOERROR: cname a cname b" for lookups that had
// resolved fine, which reads like a failure in a support report.
func TestSummarizeResponsePrefersTheAddressOverAliases(t *testing.T) {
	resp := dnsCNAMEChain(t, "www.example.com", "example.com", [4]byte{93, 184, 216, 34})
	got := summarizeResponse(resp)
	if !strings.Contains(got, "93.184.216.34") {
		t.Errorf("summary %q hides the address behind the CNAME chain", got)
	}
	if strings.Contains(got, "no address") {
		t.Errorf("summary %q reports a resolved lookup as unresolved", got)
	}
}

// The warmup query is built by hand; it has to be a query the relay itself
// would accept, or warming silently does nothing.
func TestBuildQueryIsAValidLookup(t *testing.T) {
	q, err := buildQuery("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if name, qtype := questionSummary(q); name != "example.com" || qtype != "A" {
		t.Errorf("warmup query parses as %q %q, want example.com A", name, qtype)
	}
}

// One dead endpoint must not cost a wasted dial on every lookup, and must
// not be permanently dropped either.
func TestCandidatesSkipABenchedEndpoint(t *testing.T) {
	d := &dnsRelay{
		upstream:  []string{"primary", "fallback"},
		downUntil: make([]time.Time, 2),
		warnf:     func(string, ...any) {},
	}
	d.markDown(0, errors.New("no such host"))

	got := d.candidates()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("candidates = %v, want just the working endpoint", got)
	}

	// With everything benched there is nothing to lose by trying again, and
	// failing a lookup on the strength of bookkeeping would be worse.
	d.markDown(1, errors.New("no such host"))
	if got := d.candidates(); len(got) != 2 {
		t.Fatalf("candidates = %v, want both endpoints when all are down", got)
	}

	// A recovered endpoint goes back into rotation; the one still benched
	// stays out.
	d.markUp(0)
	if got := d.candidates(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("candidates = %v, want the recovered endpoint back", got)
	}
}

// A torn sample used to print "49 queries, 48 answered, 5 failed".
func TestDeltaNeverGoesNegative(t *testing.T) {
	if got := delta(3, 5); got != 0 {
		t.Errorf("delta(3, 5) = %d, want 0", got)
	}
	if got := delta(9, 4); got != 5 {
		t.Errorf("delta(9, 4) = %d, want 5", got)
	}
}

// dnsCNAMEChain builds a reply whose first answer is a CNAME and whose second
// is the address, the shape most CDN names resolve to.
func dnsCNAMEChain(t *testing.T, name, target string, addr [4]byte) []byte {
	t.Helper()

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x1234, Response: true})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	qname := dnsmessage.MustNewName(name + ".")
	if err := builder.Question(dnsmessage.Question{
		Name:  qname,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	tname := dnsmessage.MustNewName(target + ".")
	if err := builder.CNAMEResource(dnsmessage.ResourceHeader{
		Name:  qname,
		Type:  dnsmessage.TypeCNAME,
		Class: dnsmessage.ClassINET,
		TTL:   60,
	}, dnsmessage.CNAMEResource{CNAME: tname}); err != nil {
		t.Fatal(err)
	}
	if err := builder.AResource(dnsmessage.ResourceHeader{
		Name:  tname,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
		TTL:   60,
	}, dnsmessage.AResource{A: addr}); err != nil {
		t.Fatal(err)
	}
	msg, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

// dnsResponse builds a reply with one A record, or none when addr is zero.
func dnsResponse(t *testing.T, name string, addr [4]byte) []byte {
	t.Helper()

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 0x1234,
		Response:           true,
		RecursionAvailable: true,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		t.Fatal(err)
	}
	qname := dnsmessage.MustNewName(name + ".")
	if err := builder.Question(dnsmessage.Question{
		Name:  qname,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatal(err)
	}
	if err := builder.StartAnswers(); err != nil {
		t.Fatal(err)
	}
	if addr != ([4]byte{}) {
		if err := builder.AResource(dnsmessage.ResourceHeader{
			Name:  qname,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
			TTL:   60,
		}, dnsmessage.AResource{A: addr}); err != nil {
			t.Fatal(err)
		}
	}
	msg, err := builder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

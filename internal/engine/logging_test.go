package engine

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A burst of identical failures used to produce one log line per attempt, which
// buried the rest of the log: a single blocked destination could push fifty
// copies of the same message. Throttling has to collapse that to one line while
// still reporting how many were folded in.
func TestLogThrottledCollapsesRepeats(t *testing.T) {
	SetVerbose(true)
	t.Cleanup(func() { SetVerbose(false) })

	marker := fmt.Sprintf("throttle-test-%d", time.Now().UnixNano())
	key := "test:" + marker
	FlushThrottled("") // start from a clean slate

	const attempts = 50
	for i := 0; i < attempts; i++ {
		LogThrottled(LevelWarn, key, "%s attempt=%d", marker, i)
	}

	lines := countMatching(marker)
	if lines != 1 {
		t.Fatalf("want 1 log line for %d identical calls, got %d", attempts, lines)
	}

	FlushThrottled("in test")

	summary := findMatching("suppressed")
	want := fmt.Sprintf("suppressed %d repeated log lines in test", attempts-1)
	if !strings.Contains(summary, want) {
		t.Fatalf("flush summary missing %q; got %q", want, summary)
	}

	// The folded-away message must still be recoverable, otherwise throttling
	// hides the very detail you opened the log to read.
	detail := findMatching("suppressed: " + marker)
	if !strings.Contains(detail, "attempt=49") {
		t.Fatalf("flush detail should show the last suppressed message; got %q", detail)
	}
}

// Distinct keys must not suppress each other, or one broken destination would
// silence every other warning for 30 seconds.
func TestLogThrottledKeepsDistinctKeys(t *testing.T) {
	SetVerbose(true)
	t.Cleanup(func() { SetVerbose(false) })

	marker := fmt.Sprintf("distinct-test-%d", time.Now().UnixNano())
	for i := 0; i < 5; i++ {
		LogThrottled(LevelWarn, fmt.Sprintf("%s:%d", marker, i), "%s target=%d", marker, i)
	}

	if lines := countMatching(marker); lines != 5 {
		t.Fatalf("want 5 lines for 5 distinct keys, got %d", lines)
	}
	FlushThrottled("")
}

// FlushThrottled on a quiet log must not emit a "suppressed 0" line.
func TestFlushThrottledSilentWhenNothingSuppressed(t *testing.T) {
	SetVerbose(true)
	t.Cleanup(func() { SetVerbose(false) })

	FlushThrottled("")
	before := countMatching("repeated log lines")

	FlushThrottled("quiet test")
	if after := countMatching("repeated log lines"); after != before {
		t.Fatalf("flush with nothing suppressed emitted %d line(s)", after-before)
	}
}

// countMatching returns how many buffered entries contain substr.
func countMatching(substr string) int {
	n := 0
	for _, e := range RecentLogs() {
		if strings.Contains(e.Message, substr) {
			n++
		}
	}
	return n
}

// findMatching returns the first buffered message containing substr.
func findMatching(substr string) string {
	for _, e := range RecentLogs() {
		if strings.Contains(e.Message, substr) {
			return e.Message
		}
	}
	return ""
}
